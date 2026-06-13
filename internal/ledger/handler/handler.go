package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/ledger/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/money"
)

type BalanceResponse struct {
	MerchantID             string `json:"merchant_id"`
	Currency               string `json:"currency"`
	PendingBalance         string `json:"pending_balance"`
	SettledBalance         string `json:"settled_balance"`
	ReserveBalance         string `json:"reserve_balance"`
	FrozenPayoutBalance    string `json:"frozen_payout_balance"`
	DisputeHoldBalance     string `json:"dispute_hold_balance"`
	AvailablePayoutBalance string `json:"available_payout_balance"`
	Version                int32  `json:"version"`
	UpdatedAt              string `json:"updated_at"`
}

type Handler struct {
	queries *db.Queries
}

func New(queries *db.Queries) *Handler {
	return &Handler{queries: queries}
}

func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	merchantID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant id"))
		return
	}

	if h.queries == nil {
		writeError(w, apperr.ErrInternal.WithMessage("handler not initialized"))
		return
	}

	currency := r.URL.Query().Get("currency")
	if currency == "" {
		currency = "USD"
	}

	account, err := h.queries.GetMerchantTreasuryLedger(r.Context(), db.GetMerchantTreasuryLedgerParams{
		MerchantID: merchantID,
		Currency:   currency,
	})
	if err != nil {
		writeError(w, apperr.ErrNotFound.WithMessage("treasury ledger not found"))
		return
	}

	pending := money.NumericToDecimal(account.PendingBalance)
	settled := money.NumericToDecimal(account.SettledBalance)
	reserve := money.NumericToDecimal(account.ReserveBalance)
	frozen := money.NumericToDecimal(account.FrozenPayoutBalance)
	dispute := money.NumericToDecimal(account.DisputeHoldBalance)
	available := settled.Sub(reserve).Sub(frozen).Sub(dispute)

	updatedAt := ""
	if account.UpdatedAt.Valid {
		updatedAt = account.UpdatedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	writeJSON(w, http.StatusOK, BalanceResponse{
		MerchantID:             account.MerchantID.String(),
		Currency:               account.Currency,
		PendingBalance:         pending.String(),
		SettledBalance:         settled.String(),
		ReserveBalance:         reserve.String(),
		FrozenPayoutBalance:    frozen.String(),
		DisputeHoldBalance:     dispute.String(),
		AvailablePayoutBalance: available.String(),
		Version:                account.Version,
		UpdatedAt:              updatedAt,
	})
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
