package carding

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
)

// Handler HTTP 端点
type Handler struct {
	svc *Service
}

// NewHandler 创建 handler
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// EvaluateRequest 风控评估请求
type EvaluateRequest struct {
	TenantID   string `json:"tenant_id"`
	MerchantID string `json:"merchant_id"`
	AppID      string `json:"app_id"`
	BuyerIP    string `json:"buyer_ip"`
	CardBIN    string `json:"card_bin"` // 只接收 BIN（6-8位），不接收完整卡号
	DeviceID   string `json:"device_id"`
}

// EvaluateResponse 风控评估响应
type EvaluateResponse struct {
	Decision     string `json:"decision"`
	Reason       string `json:"reason"`
	WindowType   string `json:"window_type,omitempty"`
	Threshold    int    `json:"threshold,omitempty"`
	CurrentCount int    `json:"current_count,omitempty"`
	BlockedBy    string `json:"blocked_by,omitempty"`
}

// EventResponse 事件响应
type EventResponse struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id"`
	MerchantID   string `json:"merchant_id"`
	AppID        string `json:"app_id"`
	Decision     string `json:"decision"`
	Reason       string `json:"reason"`
	BuyerIPHash  string `json:"buyer_ip_hash"`
	CardBINHash  string `json:"card_bin_hash,omitempty"`
	DeviceHash   string `json:"device_hash,omitempty"`
	WindowType   string `json:"window_type"`
	ThresholdHit int    `json:"threshold_hit"`
	CurrentCount int    `json:"current_count"`
	CreatedAt    string `json:"created_at"`
}

// Evaluate 风控评估端点
// POST /internal/risk/evaluate
func (h *Handler) Evaluate(w http.ResponseWriter, r *http.Request) {
	var req EvaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid tenant_id"))
		return
	}

	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}

	// 校验 BIN 长度（只接收 6-8 位）
	if req.CardBIN != "" && (len(req.CardBIN) < 6 || len(req.CardBIN) > 8) {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("card_bin must be 6-8 digits"))
		return
	}

	result, err := h.svc.Evaluate(r.Context(), domain.CheckRequest{
		TenantID:   tenantID,
		MerchantID: merchantID,
		AppID:      req.AppID,
		BuyerIP:    req.BuyerIP,
		CardBIN:    req.CardBIN,
		DeviceID:   req.DeviceID,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}

	// 根据决策返回不同 HTTP 状态码
	statusCode := http.StatusOK
	if result.Decision == domain.DecisionBlock {
		statusCode = http.StatusForbidden // 403
	} else if result.Decision == domain.DecisionCooldown {
		statusCode = http.StatusTooManyRequests // 429
	}

	writeJSON(w, statusCode, EvaluateResponse{
		Decision:     string(result.Decision),
		Reason:       result.Reason,
		WindowType:   string(result.WindowType),
		Threshold:    result.Threshold,
		CurrentCount: result.CurrentCount,
		BlockedBy:    string(result.BlockedBy),
	})
}

// ListEvents 查询风控事件列表
// GET /admin/risk/events?tenant_id=xxx&limit=50
func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	tenantIDStr := r.URL.Query().Get("tenant_id")
	if tenantIDStr == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("tenant_id required"))
		return
	}

	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid tenant_id"))
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		var parsed int
		if json.Unmarshal([]byte(l), &parsed) == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	events, err := h.svc.ListEvents(r.Context(), tenantID, limit)
	if err != nil {
		writeAppErr(w, err)
		return
	}

	var resp []EventResponse
	for _, e := range events {
		resp = append(resp, EventResponse{
			ID:           e.ID.String(),
			TenantID:     e.TenantID.String(),
			MerchantID:   e.MerchantID.String(),
			AppID:        e.AppID,
			Decision:     string(e.Decision),
			Reason:       e.Reason,
			BuyerIPHash:  e.BuyerIPHash,
			CardBINHash:  e.CardBINHash,
			DeviceHash:   e.DeviceHash,
			WindowType:   string(e.WindowType),
			ThresholdHit: e.ThresholdHit,
			CurrentCount: e.CurrentCount,
			CreatedAt:    e.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetEvent 查询单个风控事件
// GET /admin/risk/events/{id}
func (h *Handler) GetEvent(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid event id"))
		return
	}

	event, err := h.svc.GetEventByID(r.Context(), id)
	if err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, EventResponse{
		ID:           event.ID.String(),
		TenantID:     event.TenantID.String(),
		MerchantID:   event.MerchantID.String(),
		AppID:        event.AppID,
		Decision:     string(event.Decision),
		Reason:       event.Reason,
		BuyerIPHash:  event.BuyerIPHash,
		CardBINHash:  event.CardBINHash,
		DeviceHash:   event.DeviceHash,
		WindowType:   string(event.WindowType),
		ThresholdHit: event.ThresholdHit,
		CurrentCount: event.CurrentCount,
		CreatedAt:    event.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// SetCooldown 设置商户冷却期
// POST /internal/risk/cooldown/{merchant_id}
func (h *Handler) SetCooldown(w http.ResponseWriter, r *http.Request) {
	merchantID := chi.URLParam(r, "merchant_id")
	if merchantID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("merchant_id required"))
		return
	}

	h.svc.SetCooldown(r.Context(), merchantID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "cooldown_set"})
}

// ClearCooldown 清除商户冷却期
// DELETE /internal/risk/cooldown/{merchant_id}
func (h *Handler) ClearCooldown(w http.ResponseWriter, r *http.Request) {
	merchantID := chi.URLParam(r, "merchant_id")
	if merchantID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("merchant_id required"))
		return
	}

	h.svc.ClearCooldown(r.Context(), merchantID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "cooldown_cleared"})
}

// RecordFailure 记录失败事件
// POST /internal/risk/failure
func (h *Handler) RecordFailure(w http.ResponseWriter, r *http.Request) {
	var req EvaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}

	tenantID, _ := uuid.Parse(req.TenantID)

	h.svc.RecordFailure(r.Context(), domain.CheckRequest{
		TenantID:   tenantID,
		MerchantID: merchantID,
		AppID:      req.AppID,
		BuyerIP:    req.BuyerIP,
		CardBIN:    req.CardBIN,
		DeviceID:   req.DeviceID,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "failure_recorded"})
}

// RecordSuccess 记录成功事件
// POST /internal/risk/success
func (h *Handler) RecordSuccess(w http.ResponseWriter, r *http.Request) {
	var req EvaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}

	tenantID, _ := uuid.Parse(req.TenantID)

	h.svc.RecordSuccess(r.Context(), domain.CheckRequest{
		TenantID:   tenantID,
		MerchantID: merchantID,
		AppID:      req.AppID,
		BuyerIP:    req.BuyerIP,
		CardBIN:    req.CardBIN,
		DeviceID:   req.DeviceID,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "success_recorded"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err *apperr.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.HTTP)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    err.Code,
			"message": err.Message,
		},
	})
}

func writeAppErr(w http.ResponseWriter, err error) {
	if appErr, ok := apperr.AsAPIError(err); ok {
		writeError(w, appErr)
		return
	}
	writeError(w, apperr.ErrInternal.WithMessage(err.Error()))
}
