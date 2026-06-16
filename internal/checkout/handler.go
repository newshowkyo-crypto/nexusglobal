package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
	"github.com/nexusglobal/mamr/internal/token"
)

type MerchantDescriptor struct {
	Name         string `json:"name"`
	LogoURL      string `json:"logo_url"`
	TermsURL     string `json:"terms_url"`
	RefundPolicy string `json:"refund_policy"`
	SupportEmail string `json:"support_email"`
	SupportPhone string `json:"support_phone"`
}

type CheckoutData struct {
	OrderID      string             `json:"order_id"`
	MerchantName string             `json:"merchant_name"`
	Amount       string             `json:"amount"`
	Currency     string             `json:"currency"`
	Descriptor   MerchantDescriptor `json:"descriptor"`
	SuccessURL   string             `json:"success_url"`
	CancelURL    string             `json:"cancel_url"`
}

type TokenValidator interface {
	Create(ctx context.Context, cmd token.CreateTokenCmd) (*token.Token, error)
	Redeem(ctx context.Context, cmd token.RedeemTokenCmd) (*token.Token, error)
}

type OrderProvider interface {
	GetOrderDetails(ctx context.Context, orderID string) (*CheckoutData, error)
}

// RiskGateway 风控网关接口（用于测试 mock）
type RiskGateway interface {
	Check(ctx context.Context, req domain.CheckRequest) domain.CheckResult
	RecordFailure(ctx context.Context, req domain.CheckRequest)
	RecordSuccess(ctx context.Context, req domain.CheckRequest)
}

type Handler struct {
	tokenSvc      TokenValidator
	orderProvider OrderProvider
	paymentGW     PaymentGateway
	riskGateway   RiskGateway
	tmpl          *template.Template
}

func NewHandler(tokenSvc TokenValidator, orderProvider OrderProvider, paymentGW PaymentGateway, riskGateway RiskGateway, tmpl *template.Template) *Handler {
	return &Handler{
		tokenSvc:      tokenSvc,
		orderProvider: orderProvider,
		paymentGW:     paymentGW,
		riskGateway:   riskGateway,
		tmpl:          tmpl,
	}
}

func (h *Handler) ServeCheckoutPage(w http.ResponseWriter, r *http.Request) {
	tokenValue := r.URL.Query().Get("token")
	if tokenValue == "" {
		http.Error(w, "Missing token", http.StatusBadRequest)
		return
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}

	tkn, err := h.tokenSvc.Redeem(r.Context(), token.RedeemTokenCmd{
		Value:  tokenValue,
		Origin: origin,
	})
	if err != nil {
		switch err {
		case token.ErrTokenNotFound:
			http.Error(w, "Invalid token", http.StatusNotFound)
		case token.ErrTokenExpired:
			http.Error(w, "Token expired", http.StatusGone)
		case token.ErrTokenUsed:
			http.Error(w, "Token already used", http.StatusConflict)
		case token.ErrOriginMismatch:
			http.Error(w, "Origin not allowed", http.StatusForbidden)
		default:
			http.Error(w, "Internal error", http.StatusInternalServerError)
		}
		return
	}

	checkoutData := &CheckoutData{
		OrderID:      tkn.OrderID,
		MerchantName: "Merchant",
		Amount:       "0.00",
		Currency:     "USD",
		Descriptor: MerchantDescriptor{
			Name:         "NexusGlobal Merchant",
			TermsURL:     "/terms",
			RefundPolicy: "Refunds available within 30 days",
			SupportEmail: "support@nexusglobal.com",
		},
	}

	if h.orderProvider != nil {
		details, err := h.orderProvider.GetOrderDetails(r.Context(), tkn.OrderID)
		if err == nil && details != nil {
			checkoutData = details
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if err := h.tmpl.Execute(w, checkoutData); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func (h *Handler) HandleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID    string `json:"order_id"`
		Amount     string `json:"amount"`
		Currency   string `json:"currency"`
		MerchantID string `json:"merchant_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	if req.OrderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing order_id"})
		return
	}

	tkn, err := h.tokenSvc.Create(r.Context(), token.CreateTokenCmd{
		OrderID:    req.OrderID,
		MerchantID: req.MerchantID,
		MaxUses:    1,
		TTL:        15 * time.Minute,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create token"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":      tkn.Value,
		"expires_at": tkn.ExpiresAt.Format(time.RFC3339),
	})
}

func (h *Handler) HandleValidateToken(w http.ResponseWriter, r *http.Request) {
	tokenValue := r.URL.Query().Get("token")
	if tokenValue == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing token"})
		return
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}

	tkn, err := h.tokenSvc.Redeem(r.Context(), token.RedeemTokenCmd{
		Value:  tokenValue,
		Origin: origin,
	})
	if err != nil {
		switch err {
		case token.ErrTokenNotFound:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Invalid token"})
		case token.ErrTokenExpired:
			writeJSON(w, http.StatusGone, map[string]string{"error": "Token expired"})
		case token.ErrTokenUsed:
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Token already used"})
		case token.ErrOriginMismatch:
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Origin not allowed"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal error"})
		}
		return
	}

	checkoutData := &CheckoutData{
		OrderID:      tkn.OrderID,
		MerchantName: "Merchant",
		Amount:       "0.00",
		Currency:     "USD",
	}

	if h.orderProvider != nil {
		details, err := h.orderProvider.GetOrderDetails(r.Context(), tkn.OrderID)
		if err == nil && details != nil {
			checkoutData = details
		}
	}

	writeJSON(w, http.StatusOK, checkoutData)
}

func (h *Handler) HandleCharge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token              string `json:"token"`
		CardNumber         string `json:"card_number"`
		CardExpiry         string `json:"card_expiry"`
		CardCVV            string `json:"card_cvv"`
		PaymentMethodToken string `json:"payment_method_token"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing token"})
		return
	}

	// R3-1: token 校验
	tkn, err := h.tokenSvc.Redeem(r.Context(), token.RedeemTokenCmd{
		Value: req.Token,
	})
	if err != nil {
		switch err {
		case token.ErrTokenNotFound:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Invalid token"})
		case token.ErrTokenExpired:
			writeJSON(w, http.StatusGone, map[string]string{"error": "Token expired"})
		case token.ErrTokenUsed:
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Token already used"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal error"})
		}
		return
	}

	// R3-1: 获取订单详情
	amount := decimal.NewFromInt(0)
	currency := "USD"
	merchantID := tkn.MerchantID

	if h.orderProvider != nil {
		details, err := h.orderProvider.GetOrderDetails(r.Context(), tkn.OrderID)
		if err == nil && details != nil {
			// 解析金额
			if parsedAmount, parseErr := decimal.NewFromString(details.Amount); parseErr == nil {
				amount = parsedAmount
			}
			currency = details.Currency
		}
	}

	// 风控检查：在调用 acquirer 前检查
	if h.riskGateway != nil {
		// 提取买家 IP
		buyerIP := extractClientIP(r)

		// 提取 BIN（卡号前6-8位）
		cardBIN := ""
		if len(req.CardNumber) >= 6 {
			if len(req.CardNumber) >= 8 {
				cardBIN = req.CardNumber[:8]
			} else {
				cardBIN = req.CardNumber[:6]
			}
		}

		// 解析 merchantID 为 UUID
		merchantUUID, _ := uuid.Parse(merchantID)

		riskResult := h.riskGateway.Check(r.Context(), domain.CheckRequest{
			MerchantID: merchantUUID,
			AppID:      merchantID, // 使用 merchantID 作为 appID
			BuyerIP:    buyerIP,
			CardBIN:    cardBIN,
		})

		// 风控拦截：返回 429 或 403
		if riskResult.Decision == domain.DecisionBlock {
			h.riskGateway.RecordFailure(r.Context(), domain.CheckRequest{
				MerchantID: merchantUUID,
				AppID:      merchantID,
				BuyerIP:    buyerIP,
				CardBIN:    cardBIN,
			})
			writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
				"error":       "risk_blocked",
				"reason":      riskResult.Reason,
				"blocked_by":  string(riskResult.BlockedBy),
				"retry_after": 60,
			})
			return
		}

		if riskResult.Decision == domain.DecisionCooldown {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":       "merchant_in_cooldown",
				"reason":      riskResult.Reason,
				"retry_after": 60,
			})
			return
		}
	}

	// R3-1: 调用支付网关扣款（不持久化卡号）
	if h.paymentGW == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Payment gateway not configured"})
		return
	}

	paymentResult, err := h.paymentGW.Charge(r.Context(), PaymentRequest{
		OrderID:            tkn.OrderID,
		MerchantID:         merchantID,
		Amount:             amount,
		Currency:           currency,
		CardNumber:         req.CardNumber, // 仅传给网关，不落库
		CardExpiry:         req.CardExpiry,
		CardCVV:            req.CardCVV,
		PaymentMethodToken: req.PaymentMethodToken,
	})
	if err != nil {
		// 记录失败到风控
		if h.riskGateway != nil {
			buyerIP := extractClientIP(r)
			cardBIN := ""
			if len(req.CardNumber) >= 6 {
				if len(req.CardNumber) >= 8 {
					cardBIN = req.CardNumber[:8]
				} else {
					cardBIN = req.CardNumber[:6]
				}
			}
			merchantUUID, _ := uuid.Parse(merchantID)
			h.riskGateway.RecordFailure(r.Context(), domain.CheckRequest{
				MerchantID: merchantUUID,
				AppID:      merchantID,
				BuyerIP:    buyerIP,
				CardBIN:    cardBIN,
			})
		}

		// 使用 errors.Is 判断错误类型
		switch {
		case errors.Is(err, ErrPaymentFailed):
			writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "Payment failed"})
		case errors.Is(err, ErrPaymentTimeout):
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "Payment timeout"})
		case errors.Is(err, ErrRawCardNotAllowed):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Raw card not allowed in real mode, use payment_method_token"})
		case errors.Is(err, ErrChannelNotFound):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Channel not found"})
		case errors.Is(err, ErrChannelDisabled):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Channel disabled"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Payment error"})
		}
		return
	}

	// 记录成功到风控（用于计算失败率）
	if h.riskGateway != nil {
		buyerIP := extractClientIP(r)
		cardBIN := ""
		if len(req.CardNumber) >= 6 {
			if len(req.CardNumber) >= 8 {
				cardBIN = req.CardNumber[:8]
			} else {
				cardBIN = req.CardNumber[:6]
			}
		}
		merchantUUID, _ := uuid.Parse(merchantID)
		h.riskGateway.RecordSuccess(r.Context(), domain.CheckRequest{
			MerchantID: merchantUUID,
			AppID:      merchantID,
			BuyerIP:    buyerIP,
			CardBIN:    cardBIN,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":        true,
		"order_id":       tkn.OrderID,
		"transaction_id": paymentResult.TransactionID,
		"status":         paymentResult.Status,
		"redirect_url":   "/checkout/success",
	})
}

// extractClientIP 从请求中提取客户端 IP
func extractClientIP(r *http.Request) string {
	// 优先检查 X-Forwarded-For
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For 可能包含多个 IP，取第一个
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}
	// 其次检查 X-Real-IP
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	// 最后使用 RemoteAddr
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	// 移除 IPv6 地址的方括号
	if strings.HasPrefix(ip, "[") && strings.HasSuffix(ip, "]") {
		ip = ip[1 : len(ip)-1]
	}
	return ip
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type ValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	return e.Message
}

func NewValidationError(code, message string) *ValidationError {
	return &ValidationError{Code: code, Message: message}
}

func IsValidationError(err error) bool {
	_, ok := err.(*ValidationError)
	return ok
}

func ToAPIError(err error) *apperr.Error {
	if ve, ok := err.(*ValidationError); ok {
		return apperr.New(ve.Code, http.StatusBadRequest)
	}
	return apperr.ErrInternal
}
