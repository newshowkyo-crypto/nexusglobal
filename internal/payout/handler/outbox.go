package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type OutboxHandler struct {
	pool *pgxpool.Pool
}

func NewOutboxHandler(pool *pgxpool.Pool) *OutboxHandler {
	return &OutboxHandler{pool: pool}
}

func (h *OutboxHandler) List(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "20"
	}
	_ = status
	writeJSON(w, http.StatusOK, map[string]any{
		"events": []any{},
		"limit":  limit,
	})
}

func (h *OutboxHandler) Replay(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idStr); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid outbox id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"replayed": true})
}
