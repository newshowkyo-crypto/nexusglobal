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
	GetIngressEvent(ctx context.Context, id uuid.UUID) (*db.WebhookIngressEvent, error)
	GetIngressEventByIdempotencyKey(ctx context.Context, key string) (*db.WebhookIngressEvent, error)
	GetReceiverProxy(ctx context.Context, providerType, providerID string) (*db.WebhookReceiverProxy, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status, errorMessage string) error
	ListIngressEvents(ctx context.Context, providerType string, limit int) ([]db.WebhookIngressEvent, error)
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

	var payloadMap map[string]interface{}
	json.Unmarshal(payload, &payloadMap)
	eventType := "webhook"
	if t, ok := payloadMap["event_type"].(string); ok && t != "" {
		eventType = t
	} else if t, ok := payloadMap["type"].(string); ok && t != "" {
		eventType = t
	}

	idempotencyKey := domain.ComputeIdempotencyKey(
		providerType, providerID, providerEventID, eventType, payloadHash, "")

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
		if proxy == nil {
			writeError(w, apperr.ErrUnauthorized.WithMessage("no receiver proxy configured for provider"))
			return
		}

		// G4: 验签前解密密钥
		secret := proxy.SecretCiphertext
		if h.encryptor != nil && len(proxy.SecretCiphertext) > 0 {
			decrypted, decErr := h.encryptor.Decrypt(r.Context(), proxy.SecretCiphertext, crypto.Meta{
				TenantID:  providerType,
				RecordID:  providerID,
				FieldName: "webhook_secret",
			})
			if decErr != nil {
				writeError(w, apperr.ErrInternal.WithMessage("failed to decrypt webhook secret"))
				return
			}
			secret = decrypted
		}

		var sigValid bool
		switch domain.SignatureAlgorithm(proxy.SignatureAlgo) {
		case domain.AlgoRSASHA256:
			sigValid, _ = domain.VerifyRSA256(payload, signature, secret)
		default:
			sigValid = domain.VerifyHMACSHA256(payload, signature, secret)
		}
		if !sigValid {
			h.repo.InsertIngressEvent(r.Context(), db.InsertIngressEventParams{
				ProviderType:         providerType,
				ProviderID:           providerID,
				ProviderEventID:      providerEventID,
				EventType:            eventType,
				IdempotencyKey:       idempotencyKey,
				PayloadHash:          payloadHash,
				RawPayloadCiphertext: payload,
				Status:               string(domain.StatusRejected),
			})
			writeError(w, apperr.ErrUnauthorized.WithMessage("invalid signature"))
			return
		}

		// G9: 加密失败 fail-closed，不落明文
		encryptedPayload := payload
		if h.encryptor != nil {
			enc, encErr := h.encryptor.Encrypt(r.Context(), payload, crypto.Meta{
				TenantID:  providerType,
				RecordID:  providerID,
				FieldName: "raw_payload",
			})
			if encErr != nil {
				writeError(w, apperr.ErrInternal.WithMessage("failed to encrypt payload"))
				return
			}
			encryptedPayload = enc
		}

		ingress, insertErr := h.repo.InsertIngressEvent(r.Context(), db.InsertIngressEventParams{
			ProviderType:         providerType,
			ProviderID:           providerID,
			ProviderEventID:      providerEventID,
			EventType:            eventType,
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
				EventType:      eventType,
				PayloadHash:    payloadHash,
				IngressID:      ingressID,
			})
		}
	} else {
		// T2-1: repo 为 nil 时必须拒绝，不能跳过验签
		writeError(w, apperr.ErrInternal.WithMessage("webhook receiver not configured"))
		return
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
		writeError(w, apperr.ErrInternal.WithMessage("webhook repository not configured"))
		return
	}

	providerType := r.URL.Query().Get("provider_type")
	if providerType == "" {
		providerType = "mae"
	}

	// G6: 真实查询，不再返回空数组
	events, err := h.repo.ListIngressEvents(r.Context(), providerType, 100)
	if err != nil {
		writeError(w, apperr.ErrInternal.WithMessage("failed to list events"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"events": events, "provider_type": providerType})
}

func (h *Handler) Replay(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	eventID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid event id"))
		return
	}

	if h.repo == nil {
		writeError(w, apperr.ErrInternal.WithMessage("webhook repository not configured"))
		return
	}
	if h.producer == nil {
		writeError(w, apperr.ErrInternal.WithMessage("stream producer not configured"))
		return
	}

	// R2-2: 从 repo 加载原始事件
	event, err := h.repo.GetIngressEvent(r.Context(), eventID)
	if err != nil {
		writeError(w, apperr.ErrInternal.WithMessage("failed to load event"))
		return
	}
	if event == nil {
		writeError(w, apperr.ErrNotFound.WithMessage("event not found"))
		return
	}

	// 确定目标 stream
	streamName := "stream:webhook:" + event.ProviderType

	// 重投到对应 stream
	_, err = h.producer.Publish(r.Context(), streamName, stream.Event{
		IdempotencyKey: event.IdempotencyKey,
		ProviderType:   event.ProviderType,
		ProviderID:     event.ProviderID,
		EventType:      event.EventType,
		PayloadHash:    event.PayloadHash,
		IngressID:      event.ID.String(),
	})
	if err != nil {
		writeError(w, apperr.ErrInternal.WithMessage("failed to replay event"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"replayed": true,
		"event_id": eventID.String(),
		"stream":   streamName,
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
