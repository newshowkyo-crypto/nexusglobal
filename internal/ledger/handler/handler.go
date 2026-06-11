package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/ledger/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
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
}

type LedgerService interface {
	Post(ctx context.Context, cmd PostCmd) (PostResult, error)
}

type PostCmd struct {
	IdempotencyKey  string
	TransactionType domain.TransactionType
	AggregateType   string
	AggregateID     string
	Description     string
	Entries         []domain.Entry
}

type PostResult struct {
	TransactionID string
	Replayed      bool
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

	account, err := h.queries.GetLedgerAccountByMerchant(r.Context(), db.GetLedgerAccountByMerchantParams{
		MerchantID:  merchantID,
		AccountType: string(domain.AccountTypeSettled),
		Currency:    currency,
	})
	if err != nil {
		writeError(w, apperr.ErrNotFound.WithMessage("account not found"))
		return
	}

	_ = json.NewEncoder(w).Encode(BalanceResponse{
		MerchantID: account.MerchantID.String(),
		Currency:   account.Currency,
	})
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
