package query

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

type Handler struct {
	repo *Repository
	log  *slog.Logger
}

func NewHandler(repo *Repository, log *slog.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

func merchantID(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(httpx.GetMerchantID(r.Context()))
	return id, err == nil
}

// GetOrder GET /merchant/orders/{merchant_order_id}
func (h *Handler) GetOrder(w http.ResponseWriter, r *http.Request) {
	merchant, ok := merchantID(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	orderID := chi.URLParam(r, "merchant_order_id")
	order, err := h.repo.OrderByMerchantOrderID(r.Context(), merchant, orderID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.ErrorResponse(w, http.StatusNotFound, "ORDER_NOT_FOUND", "order not found")
			return
		}
		h.log.Error("query: get order failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "ORDER_QUERY_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, order)
}

// GetPayment GET /merchant/payments/{attempt_id}
func (h *Handler) GetPayment(w http.ResponseWriter, r *http.Request) {
	merchant, ok := merchantID(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	attemptID, err := uuid.Parse(chi.URLParam(r, "attempt_id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_ATTEMPT_ID", "attempt_id must be a UUID")
		return
	}
	a, err := h.repo.Attempt(r.Context(), merchant, attemptID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.ErrorResponse(w, http.StatusNotFound, "PAYMENT_NOT_FOUND", "payment not found")
			return
		}
		httpx.ErrorResponse(w, http.StatusInternalServerError, "PAYMENT_QUERY_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, a)
}

// ListTransactions GET /merchant/transactions?status=&limit=&offset=
func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	merchant, ok := merchantID(r)
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
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	rows, err := h.repo.ListAttempts(r.Context(), merchant, r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		h.log.Error("query: list transactions failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "TRANSACTION_QUERY_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{
		"transactions": rows, "limit": limit, "offset": offset,
	})
}
