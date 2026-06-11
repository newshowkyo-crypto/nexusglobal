package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type Handler struct{}

func New() *Handler {
	return &Handler{}
}

type CreateAddressRequest struct {
	TargetAsset       string `json:"target_asset"`
	TargetNetwork     string `json:"target_network"`
	AddressFormatType string `json:"address_format_type"`
	TargetAddress     string `json:"target_address"`
	AddressLabel      string `json:"address_label"`
}

type CreateAddressResponse struct {
	ID                  string `json:"id"`
	TargetAddressMasked string `json:"target_address_masked"`
	ScreeningStatus     string `json:"screening_status"`
	WhitelistStatus     string `json:"whitelist_status"`
}

func (h *Handler) CreateAddress(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, CreateAddressResponse{
		ID:              uuid.New().String(),
		ScreeningStatus: "Pending",
		WhitelistStatus: "Pending",
	})
}

type CreatePayoutRequest struct {
	PayoutAddressID string `json:"payout_address_id"`
	Amount          string `json:"amount"`
	FiatCurrency    string `json:"fiat_currency"`
	TargetAsset     string `json:"target_asset"`
	TargetNetwork   string `json:"target_network"`
}

type CreatePayoutResponse struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *Handler) CreatePayoutRequest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, CreatePayoutResponse{
		ID:     uuid.New().String(),
		Status: "Draft",
	})
}

type FreezeResponse struct {
	Status string `json:"status"`
}

func (h *Handler) Freeze(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idStr); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid payout id"))
		return
	}
	writeJSON(w, http.StatusOK, FreezeResponse{Status: "BalanceFrozen"})
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
