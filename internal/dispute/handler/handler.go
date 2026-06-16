package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/dispute/domain"
	"github.com/nexusglobal/mamr/internal/dispute/service"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type Handler struct {
	svc *service.Service
}

func New(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

type RefundRequest struct {
	OrderID           string `json:"order_id"`
	MerchantID        string `json:"merchant_id"`
	Amount            string `json:"amount"`
	Currency          string `json:"currency"`
	Reason            string `json:"reason"`
	ProviderDisputeID string `json:"provider_dispute_id"`
	IdempotencyKey    string `json:"idempotency_key"`
}

type ChargebackRequest struct {
	OrderID           string `json:"order_id"`
	MerchantID        string `json:"merchant_id"`
	Amount            string `json:"amount"`
	Currency          string `json:"currency"`
	Reason            string `json:"reason"`
	ProviderDisputeID string `json:"provider_dispute_id"`
	ChargebackFee     string `json:"chargeback_fee"`
	IdempotencyKey    string `json:"idempotency_key"`
}

type ReverseRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *Handler) RefundConfirmed(w http.ResponseWriter, r *http.Request) {
	var req RefundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}

	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid amount"))
		return
	}

	cmd := domain.CreateDisputeCmd{
		OrderID:           req.OrderID,
		MerchantID:        merchantID,
		Amount:            amount,
		Currency:          req.Currency,
		Reason:            req.Reason,
		ProviderDisputeID: req.ProviderDisputeID,
		IdempotencyKey:    req.IdempotencyKey,
	}

	if err := h.svc.RefundConfirmed(r.Context(), cmd); err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "refund_confirmed"})
}

func (h *Handler) ChargebackReceived(w http.ResponseWriter, r *http.Request) {
	var req ChargebackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}

	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid amount"))
		return
	}

	chargebackFee, _ := decimal.NewFromString(req.ChargebackFee)

	cmd := domain.CreateDisputeCmd{
		OrderID:           req.OrderID,
		MerchantID:        merchantID,
		Amount:            amount,
		Currency:          req.Currency,
		Reason:            req.Reason,
		ProviderDisputeID: req.ProviderDisputeID,
		ChargebackFee:     chargebackFee,
		IdempotencyKey:    req.IdempotencyKey,
	}

	if err := h.svc.ChargebackReceived(r.Context(), cmd); err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "chargeback_received"})
}

func (h *Handler) ChargebackReversed(w http.ResponseWriter, r *http.Request) {
	disputeIDStr := chi.URLParam(r, "dispute_id")
	disputeID, err := uuid.Parse(disputeIDStr)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid dispute_id"))
		return
	}

	var req ReverseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	cmd := domain.ReverseDisputeCmd{
		DisputeID:      disputeID,
		IdempotencyKey: req.IdempotencyKey,
	}

	if err := h.svc.ChargebackReversed(r.Context(), cmd); err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "chargeback_reversed"})
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
