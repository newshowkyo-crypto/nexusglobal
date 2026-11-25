package checkout

import (
	"encoding/json"
	"net/http"

	"github.com/nexusglobal/mamr/internal/checkout/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/internal/platform/httpx/middleware"
)

type SessionHandler struct {
	service *CheckoutService
	baseURL string
}

func NewSessionHandler(service *CheckoutService, baseURL string) *SessionHandler {
	return &SessionHandler{service: service, baseURL: baseURL}
}

func (h *SessionHandler) CreateSession(w http.ResponseWriter, r *http.Request) {
	// P0-2: 支付入口封锁检查（HTTP 层）。
	// 封锁时返回 503 + SYSTEM_LOCKDOWN_FAIL_CLOSED，不进入业务逻辑。
	if err := h.service.admissionCheck(r.Context()); err != nil {
		httpx.WriteError(w, apperr.New(failclosed.SystemLockdownCode, http.StatusServiceUnavailable))
		return
	}

	mc, ok := middleware.GetMerchantContext(r.Context())
	if !ok {
		httpx.WriteError(w, apperr.New("MERCHANT_AUTH_REQUIRED", http.StatusUnauthorized))
		return
	}

	var req struct {
		MerchantOrderID string               `json:"merchant_order_id"`
		AmountMinor     int64                `json:"amount_minor"`
		Currency        string               `json:"currency"`
		SuccessURL      string               `json:"success_url"`
		CancelURL       string               `json:"cancel_url"`
		Customer        *domain.CustomerInfo `json:"customer,omitempty"`
		Metadata        map[string]string    `json:"metadata,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, apperr.New("INVALID_REQUEST", http.StatusBadRequest))
		return
	}

	if req.MerchantOrderID == "" || req.AmountMinor <= 0 || req.Currency == "" {
		httpx.WriteError(w, apperr.New("INVALID_ARGUMENT", http.StatusBadRequest))
		return
	}

	session, err := h.service.CreateSession(r.Context(), CreateSessionRequest{
		MerchantOrderID: req.MerchantOrderID,
		MerchantID:      mc.MerchantID,
		AmountMinor:     req.AmountMinor,
		Currency:        req.Currency,
		SuccessURL:      req.SuccessURL,
		CancelURL:       req.CancelURL,
		Customer:        req.Customer,
		Metadata:        req.Metadata,
		BaseURL:         h.baseURL,
	})
	if err != nil {
		if appErr, ok := apperr.AsAPIError(err); ok {
			httpx.WriteError(w, appErr)
		} else {
			httpx.WriteError(w, apperr.ErrInternal)
		}
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"session_id":   session.SessionID,
		"checkout_url": session.CheckoutURL,
	})
}
