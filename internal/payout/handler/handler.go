package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/pkg/validator"
)

type PayoutService interface {
	Freeze(ctx context.Context, cmd service.FreezeCmd) (service.FreezeResult, error)
}

type Handler struct {
	svc PayoutService
}

func New(svc PayoutService) *Handler {
	return &Handler{svc: svc}
}

type CreateAddressResponse struct {
	ID                  string `json:"id"`
	TargetAddressMasked string `json:"target_address_masked"`
	ScreeningStatus     string `json:"screening_status"`
	WhitelistStatus     string `json:"whitelist_status"`
}

func (h *Handler) CreateAddress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetAsset       string `json:"target_asset"`
		TargetNetwork     string `json:"target_network"`
		AddressFormatType string `json:"address_format_type"`
		TargetAddress     string `json:"target_address"`
		AddressLabel      string `json:"address_label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.TargetAddress == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("target_address required"))
		return
	}

	cleaned := domain.CleanAddress(req.TargetAddress, validator.Network(req.TargetNetwork))
	if !cleaned.IsValid {
		writeError(w, apperr.ErrAddressFormat.WithMessage(cleaned.ValidationError))
		return
	}

	writeJSON(w, http.StatusCreated, CreateAddressResponse{
		ID:                  uuid.New().String(),
		TargetAddressMasked: cleaned.TargetAddressMasked,
		ScreeningStatus:     "Pending",
		WhitelistStatus:     "Pending",
	})
}

type CreatePayoutResponse struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *Handler) CreatePayoutRequest(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("Idempotency-Key header required"))
		return
	}

	var req struct {
		PayoutAddressID string `json:"payout_address_id"`
		Amount          string `json:"amount"`
		FiatCurrency    string `json:"fiat_currency"`
		TargetAsset     string `json:"target_asset"`
		TargetNetwork   string `json:"target_network"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.Amount == "" || req.FiatCurrency == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("amount and fiat_currency required"))
		return
	}

	_, err := decimal.NewFromString(req.Amount)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid amount format"))
		return
	}

	writeJSON(w, http.StatusCreated, CreatePayoutResponse{
		ID:             uuid.New().String(),
		Status:         string(domain.StatusDraft),
		IdempotencyKey: idempotencyKey,
	})
}

func (h *Handler) Freeze(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	payoutID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid payout id"))
		return
	}

	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("payout service not initialized"))
		return
	}

	var req struct {
		MerchantID      string `json:"merchant_id"`
		Amount          string `json:"amount"`
		FiatCurrency    string `json:"fiat_currency"`
		PayoutAddressID string `json:"payout_address_id"`
		TargetAsset     string `json:"target_asset"`
		TargetNetwork   string `json:"target_network"`
	}
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

	addrID, _ := uuid.Parse(req.PayoutAddressID)
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		idempotencyKey = "freeze:" + payoutID.String()
	}

	result, err := h.svc.Freeze(r.Context(), service.FreezeCmd{
		PayoutRequestID: payoutID,
		MerchantID:      merchantID,
		Amount:          amount,
		FiatCurrency:    req.FiatCurrency,
		PayoutAddressID: addrID,
		TargetAsset:     req.TargetAsset,
		TargetNetwork:   req.TargetNetwork,
		IdempotencyKey:  idempotencyKey,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":               result.Status,
		"ledger_freeze_txn_id": result.PayoutRequestID,
	})
}

func (h *Handler) FxLock(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idStr); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid payout id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "FxLocked"})
}

func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idStr); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid payout id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "LPProcessing"})
}

func (h *Handler) Reconcile(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idStr); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid payout id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "Confirmed"})
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

func writeErrorWithRequestID(w http.ResponseWriter, r *http.Request, err *apperr.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.HTTP)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":       err.Code,
			"message":    err.Message,
			"request_id": r.Header.Get("X-Request-ID"),
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
