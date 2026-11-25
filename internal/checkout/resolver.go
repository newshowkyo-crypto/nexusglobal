package checkout

import (
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// G10: 三入口统一 Resolver / Page Handler
//
// GET /checkout/v3/{session_id} — Hosted Checkout session resolver
// GET /pay/{token}              — Payment Link resolver
//
// 无效/过期/已用返回稳定页面和状态，不泄露记录存在性。
// 响应不包含 token、session 内部状态等敏感信息。
// ============================================================================

// CheckoutResolverHandler 三入口统一 resolver handler。
type CheckoutResolverHandler struct {
	service        *CheckoutService
	paymentLinkSvc *PaymentLinkService
	attemptRepo    *PaymentAttemptRepository
	statusTmpl     *template.Template // PAY-003: HTML 状态页；nil 表示禁用（回退 JSON）
}

func NewCheckoutResolverHandler(service *CheckoutService, paymentLinkSvc *PaymentLinkService) *CheckoutResolverHandler {
	return &CheckoutResolverHandler{
		service:        service,
		paymentLinkSvc: paymentLinkSvc,
		attemptRepo:    NewPaymentAttemptRepository(service.pool),
	}
}

// SetStatusTmpl 注入 HTML 状态页模板（PAY-003）。nil = 禁用 HTML 输出。
func (h *CheckoutResolverHandler) SetStatusTmpl(tmpl *template.Template) {
	h.statusTmpl = tmpl
}

// ResolveSession GET /checkout/v3/{session_id}
//
// G10: 返回统一 checkout 上下文。
// 无效/过期/已用返回稳定页面和状态，不泄露记录存在性。
//
// P0-2: 在解析 session 前调用 AdmissionCheck，封锁时返回 503。
// 注意：ResolveSession 是消费入口（用户点击 checkout URL 进入），
// 属于新支付动作，必须在封锁时拒绝。
func (h *CheckoutResolverHandler) ResolveSession(w http.ResponseWriter, r *http.Request) {
	// P0-2: 消费入口封锁检查。
	if err := h.service.admissionCheck(r.Context()); err != nil {
		httpx.WriteError(w, apperr.New(failclosed.SystemLockdownCode, http.StatusServiceUnavailable))
		return
	}

	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		h.writeStableError(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	ctx := r.Context()

	session, attempt, err := h.service.ResolveSession(ctx, sessionID)
	if err != nil {
		// G10: 不泄露记录存在性，统一返回稳定错误（浏览器给状态页）
		if wantsHTML(r) && h.renderStatusError(w, r, http.StatusNotFound) {
			return
		}
		h.writeStableError(w, http.StatusNotFound, "SESSION_NOT_AVAILABLE")
		return
	}

	// 构建响应（不包含敏感信息）
	resp := map[string]interface{}{
		"status": "ACTIVE",
	}

	attemptStatus := ""
	if attempt != nil {
		attemptStatus = string(attempt.Status)
		resp["payment_attempt_id"] = attempt.ID.String()
		resp["status"] = attemptStatus
		if attempt.RequiresActionURL != "" {
			resp["requires_action"] = map[string]string{
				"url":  attempt.RequiresActionURL,
				"type": attempt.RequiresActionType,
			}
		}
	}

	// PAY-003: 浏览器（Accept: text/html）返回真实状态页
	if wantsHTML(r) && h.renderStatusPage(w, r, statusPageState("", attemptStatus), http.StatusOK,
		session.AmountMinor, session.Currency, session.MerchantOrderID) {
		return
	}

	// G09: 禁止缓存（防止 token 泄露）
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, resp)
}

// ResolvePaymentLink GET /pay/{token}
//
// G10/G09: 通过 token 解析 Payment Link。
// 无效/过期/已用返回稳定页面和状态，不泄露记录存在性。
// token 不出现在日志、Referer、analytics 中。
//
// P0-2: 在解析 Payment Link 前调用 AdmissionCheck，封锁时返回 503。
// 注意：ResolvePaymentLink 是消费入口（用户点击支付链接进入），
// 属于新支付动作，必须在封锁时拒绝。
func (h *CheckoutResolverHandler) ResolvePaymentLink(w http.ResponseWriter, r *http.Request) {
	// P0-2: 消费入口封锁检查。
	if err := h.service.admissionCheck(r.Context()); err != nil {
		httpx.WriteError(w, apperr.New(failclosed.SystemLockdownCode, http.StatusServiceUnavailable))
		return
	}

	token := chi.URLParam(r, "token")
	if token == "" {
		h.writeStableError(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	ctx := r.Context()

	// G09: ResolveByToken 不泄露记录存在性
	rec, err := h.paymentLinkSvc.ResolveByToken(ctx, token)
	if err != nil {
		// G10: 不泄露记录存在性，统一返回稳定错误（浏览器给状态页）
		if wantsHTML(r) && h.renderStatusError(w, r, http.StatusNotFound) {
			return
		}
		h.writeStableError(w, http.StatusNotFound, "PAYMENT_LINK_NOT_AVAILABLE")
		return
	}

	// 构建响应（不包含 token）
	resp := map[string]interface{}{
		"payment_link_id": rec.ID.String(),
		"status":          rec.Status,
		"amount_minor":    rec.AmountMinor,
		"currency":        rec.Currency,
	}

	// P1 修复：获取关联的 payment_attempt 必须用 entry_ref（= paymentLinkID），
	// 而不是 order_id。Payment Link 创建时 entry_ref = paymentLinkID.String()，
	// order_id 是独立的 checkout_orders.id，两者不同。
	// 旧代码把 PaymentLinkID 误当 order_id，导致关联查询永远找不到 attempt。
	attempt, _ := h.attemptRepo.GetPaymentAttemptByEntryRef(r.Context(), rec.ID.String(), EntryPaymentLink)
	attemptStatus := ""
	if attempt != nil {
		attemptStatus = string(attempt.Status)
		resp["payment_attempt_id"] = attempt.ID.String()
		resp["attempt_status"] = attemptStatus
		if attempt.RequiresActionURL != "" {
			resp["requires_action"] = map[string]string{
				"url":  attempt.RequiresActionURL,
				"type": attempt.RequiresActionType,
			}
		}
	}

	// PAY-003: 浏览器（Accept: text/html）返回真实状态页
	if wantsHTML(r) && h.renderStatusPage(w, r, statusPageState(rec.Status, attemptStatus), http.StatusOK,
		rec.AmountMinor, rec.Currency, rec.MerchantOrderID) {
		return
	}

	// G09: 禁止缓存（防止 token 泄露）
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// G09: Referrer-Policy 禁止发送 Referer
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, http.StatusOK, resp)
}

// ConfirmPaymentLink POST /pay/{token}/confirm
//
// 让用户通过 Payment Link 提交支付方法后真正进入支付生命周期。
// 解析 JSON body: { "payment_method_token": "..." }
// 成功返回 200 + { "payment_attempt_id": "...", "status": "REQUIRES_ACTION" }
// 错误返回稳定错误（不泄露记录存在性）。
//
// P0-2: 在处理请求前调用 AdmissionCheck，封锁时返回 503。
func (h *CheckoutResolverHandler) ConfirmPaymentLink(w http.ResponseWriter, r *http.Request) {
	// P0-2: 消费入口封锁检查。
	if err := h.service.admissionCheck(r.Context()); err != nil {
		httpx.WriteError(w, apperr.New(failclosed.SystemLockdownCode, http.StatusServiceUnavailable))
		return
	}

	token := chi.URLParam(r, "token")
	if token == "" {
		h.writeStableError(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 解析 JSON body
	var body struct {
		PaymentMethodToken string `json:"payment_method_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeStableError(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if body.PaymentMethodToken == "" {
		h.writeStableError(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	ctx := r.Context()

	result, err := h.paymentLinkSvc.ConfirmPayment(ctx, token, body.PaymentMethodToken)
	if err != nil {
		// G10: 不泄露记录存在性，统一返回稳定错误
		h.writeStableError(w, http.StatusNotFound, "PAYMENT_LINK_NOT_AVAILABLE")
		return
	}

	// 成功返回 200 + payment_attempt_id + status
	resp := map[string]interface{}{
		"payment_attempt_id": result.PaymentAttemptID,
		"status":             result.Status,
	}

	// G09: 禁止缓存（防止 token 泄露）
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, http.StatusOK, resp)
}

// writeStableError 写入稳定错误响应（不泄露记录存在性）。
//
// G10: 所有错误使用相同的响应格式和消息，防止信息泄露。
func (h *CheckoutResolverHandler) writeStableError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": code,
	})
}
