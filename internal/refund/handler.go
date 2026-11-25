package refund

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// Handler 商户退款接口（挂在 /merchant 路由组，使用 MerchantAuth）。
type Handler struct {
	svc *Service
	log *slog.Logger
}

func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func merchantFromRequest(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(httpx.GetMerchantID(r.Context()))
	return id, err == nil
}

type createRefundJSON struct {
	PaymentAttemptID uuid.UUID `json:"payment_attempt_id"`
	MerchantRefundID string    `json:"merchant_refund_id"`
	AmountMinor      int64     `json:"amount_minor"`
	Reason           string    `json:"reason"`
	IdempotencyKey   string    `json:"idempotency_key"`
}

// Create POST /merchant/refunds
//
// 幂等键优先取 Idempotency-Key 头，其次取 body.idempotency_key。
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantFromRequest(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	var req createRefundJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	idem := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idem == "" {
		idem = strings.TrimSpace(req.IdempotencyKey)
	}
	if idem == "" {
		httpx.ErrorResponse(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED",
			"Idempotency-Key header or idempotency_key body field is required")
		return
	}

	f, err := h.svc.Create(r.Context(), CreateCmd{
		MerchantID:       merchantID,
		PaymentAttemptID: req.PaymentAttemptID,
		MerchantRefundID: req.MerchantRefundID,
		IdempotencyKey:   idem,
		AmountMinor:      req.AmountMinor,
		Reason:           req.Reason,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrIdempotencyConflict):
			httpx.ErrorResponse(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error())
		case errors.Is(err, ErrAttemptNotFound):
			httpx.ErrorResponse(w, http.StatusNotFound, "ATTEMPT_NOT_FOUND", err.Error())
		case errors.Is(err, ErrNotRefundable):
			httpx.ErrorResponse(w, http.StatusConflict, "NOT_REFUNDABLE", err.Error())
		case errors.Is(err, ErrAmountExceeds):
			httpx.ErrorResponse(w, http.StatusBadRequest, "AMOUNT_EXCEEDS_REFUNDABLE", err.Error())
		default:
			h.log.Error("refund: create failed", "err", err)
			httpx.ErrorResponse(w, http.StatusInternalServerError, "REFUND_CREATE_FAILED", err.Error())
		}
		return
	}
	httpx.SuccessResponse(w, http.StatusAccepted, f)
}

// Get GET /merchant/refunds/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantFromRequest(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_REFUND_ID", "id must be a UUID")
		return
	}
	f, err := h.svc.Get(r.Context(), merchantID, id)
	if err != nil {
		if errors.Is(err, ErrRefundNotFound) {
			httpx.ErrorResponse(w, http.StatusNotFound, "REFUND_NOT_FOUND", err.Error())
			return
		}
		httpx.ErrorResponse(w, http.StatusInternalServerError, "REFUND_GET_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, f)
}

// List GET /merchant/refunds?limit=
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantFromRequest(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	refunds, err := h.svc.List(r.Context(), merchantID, limit)
	if err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "REFUND_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"refunds": refunds})
}
