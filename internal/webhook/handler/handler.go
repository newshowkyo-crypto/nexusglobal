package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/idempotency"
	"github.com/nexusglobal/mamr/internal/webhook/domain"
)

type Handler struct{}

func New() *Handler {
	return &Handler{}
}

type WebhookResponse struct {
	Received       bool   `json:"received"`
	IngressID      string `json:"ingress_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *Handler) HandleMAE(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, "mae")
}

func (h *Handler) HandleBank(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, "bank")
}

func (h *Handler) HandleLP(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, "lp")
}

func (h *Handler) handleWebhook(w http.ResponseWriter, r *http.Request, providerType string) {
	providerID := chi.URLParam(r, "provider_id")
	if providerID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("provider_id required"))
		return
	}

	signature := r.Header.Get("X-Signature")
	providerEventID := r.Header.Get("X-Provider-Event-Id")

	if signature == "" {
		writeError(w, apperr.ErrUnauthorized.WithMessage("missing X-Signature header"))
		return
	}

	payload := make([]byte, r.ContentLength)
	r.Body.Read(payload)

	payloadHash := idempotency.PayloadHash(payload)
	idempotencyKey := domain.ComputeIdempotencyKey(
		providerType, providerID, providerEventID, "webhook", payloadHash, "")

	_ = signature
	_ = payloadHash

	writeJSON(w, http.StatusOK, WebhookResponse{
		Received:       true,
		IngressID:      "ingress-id",
		IdempotencyKey: idempotencyKey,
	})
}

func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
}

func (h *Handler) Replay(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"replayed": true})
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
