package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/idempotency"
	"github.com/nexusglobal/mamr/internal/platform/stream"
	"github.com/nexusglobal/mamr/internal/webhook/domain"
	"github.com/nexusglobal/mamr/internal/webhook/repository/db"
)

type Repository interface {
	InsertIngressEvent(ctx context.Context, params db.InsertIngressEventParams) (*db.WebhookIngressEvent, error)
	GetIngressEventByIdempotencyKey(ctx context.Context, key string) (*db.WebhookIngressEvent, error)
	GetReceiverProxy(ctx context.Context, providerType, providerID string) (*db.WebhookReceiverProxy, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status, errorMessage string) error
}

type Handler struct {
	repo      Repository
	producer  *stream.Producer
	encryptor *crypto.Encryptor
}

func New(repo Repository, producer *stream.Producer, encryptor *crypto.Encryptor) *Handler {
	return &Handler{repo: repo, producer: producer, encryptor: encryptor}
}

type WebhookResponse struct {
	Received       bool   `json:"received"`
	IngressID      string `json:"ingress_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Duplicated     bool   `json:"duplicated,omitempty"`
}

func (h *Handler) HandleMAE(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, "mae", "stream:webhook:mae")
}

func (h *Handler) HandleBank(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, "bank", "stream:webhook:bank")
}

func (h *Handler) HandleLP(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, "lp", "stream:webhook:lp")
}

func (h *Handler) handleWebhook(w http.ResponseWriter, r *http.Request, providerType, streamName string) {
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

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, apperr.ErrInternal.WithMessage("failed to read body"))
		return
	}

	payloadHash := idempotency.PayloadHash(payload)
	idempotencyKey := domain.ComputeIdempotencyKey(
		providerType, providerID, providerEventID, "webhook", payloadHash, "")

	var ingressID string
	var duplicated bool

	if h.repo != nil {
		existing, _ := h.repo.GetIngressEventByIdempotencyKey(r.Context(), idempotencyKey)
		if existing != nil {
			writeJSON(w, http.StatusOK, WebhookResponse{
				Received:       true,
				IngressID:      existing.ID.String(),
				IdempotencyKey: idempotencyKey,
				Duplicated:     true,
			})
			return
		}

		proxy, _ := h.repo.GetReceiverProxy(r.Context(), providerType, providerID)
		if proxy != nil {
			valid := domain.VerifyHMACSHA256(payload, signature, proxy.SecretCiphertext)
			if !valid {
				h.repo.InsertIngressEvent(r.Context(), db.InsertIngressEventParams{
					ProviderType:         providerType,
					ProviderID:           providerID,
					ProviderEventID:      providerEventID,
					EventType:            "webhook",
					IdempotencyKey:       idempotencyKey,
					PayloadHash:          payloadHash,
					RawPayloadCiphertext: payload,
					Status:               string(domain.StatusRejected),
				})
				writeError(w, apperr.ErrUnauthorized.WithMessage("invalid signature"))
				return
			}
		}

		encryptedPayload := payload
		if h.encryptor != nil {
			enc, encErr := h.encryptor.Encrypt(r.Context(), payload, crypto.Meta{
				TenantID:  providerType,
				RecordID:  providerID,
				FieldName: "raw_payload",
			})
			if encErr == nil {
				encryptedPayload = enc
			}
		}

		ingress, insertErr := h.repo.InsertIngressEvent(r.Context(), db.InsertIngressEventParams{
			ProviderType:         providerType,
			ProviderID:           providerID,
			ProviderEventID:      providerEventID,
			EventType:            "webhook",
			IdempotencyKey:       idempotencyKey,
			PayloadHash:          payloadHash,
			RawPayloadCiphertext: encryptedPayload,
			Status:               string(domain.StatusReceived),
		})
		if insertErr != nil {
			writeError(w, apperr.ErrInternal.WithMessage("failed to write ingress"))
			return
		}
		ingressID = ingress.ID.String()

		if h.producer != nil {
			h.producer.Publish(r.Context(), streamName, stream.Event{
				IdempotencyKey: idempotencyKey,
				ProviderType:   providerType,
				ProviderID:     providerID,
				EventType:      "webhook",
				PayloadHash:    payloadHash,
				IngressID:      ingressID,
			})
		}
	} else {
		ingressID = uuid.New().String()
	}

	writeJSON(w, http.StatusOK, WebhookResponse{
		Received:       true,
		IngressID:      ingressID,
		IdempotencyKey: idempotencyKey,
		Duplicated:     duplicated,
	})
}

func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
		return
	}

	providerType := r.URL.Query().Get("provider_type")
	if providerType == "" {
		providerType = "mae"
	}

	writeJSON(w, http.StatusOK, map[string]any{"events": []any{}, "provider_type": providerType})
}

func (h *Handler) Replay(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idStr); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid event id"))
		return
	}

	if h.repo != nil {
		if h.producer != nil {
			h.producer.Publish(r.Context(), "stream:webhook:lp", stream.Event{
				IdempotencyKey: "replay:" + idStr,
				EventType:      "replay",
			})
		}
	}

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
