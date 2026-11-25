package webhook

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// Handler 商户回调管理端接口。
type Handler struct {
	repo *Repository
	log  *slog.Logger
}

func NewHandler(repo *Repository, log *slog.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

func parseMerchant(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.URL.Query().Get("merchant_id"))
	return id, err == nil
}

// ListEndpoints GET /admin/api/v3/merchant-webhooks/endpoints?merchant_id=
func (h *Handler) ListEndpoints(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := parseMerchant(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	endpoints, err := h.repo.ListEndpoints(r.Context(), merchantID)
	if err != nil {
		h.log.Error("merchant webhook: list endpoints failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "WEBHOOK_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"endpoints": endpoints})
}

type createEndpointRequest struct {
	MerchantID  uuid.UUID `json:"merchant_id"`
	URL         string    `json:"url"`
	Description string    `json:"description"`
	EventTypes  []string  `json:"event_types"`
}

// CreateEndpoint POST /admin/api/v3/merchant-webhooks/endpoints
// 明文 secret 只在本次响应返回，之后不可获取。
func (h *Handler) CreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var req createEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.MerchantID == uuid.Nil || req.URL == "" {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "merchant_id and url are required")
		return
	}
	parsed, err := url.Parse(req.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_URL", "webhook url must be absolute HTTPS")
		return
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "SECRET_GENERATION_FAILED", err.Error())
		return
	}
	secret := "whsec_" + hex.EncodeToString(buf)

	ep, err := h.repo.CreateEndpoint(r.Context(), req.MerchantID, req.MerchantID,
		req.URL, secret, req.Description, req.EventTypes)
	if err != nil {
		h.log.Error("merchant webhook: create endpoint failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "WEBHOOK_CREATE_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{
		"endpoint": ep,
		"secret":   secret,
		"notice":   "secret 只返回一次，请立即保存；签名算法 HMAC-SHA256(timestamp + \".\" + body)",
	})
}

type statusRequest struct {
	Status string `json:"status"`
}

// UpdateEndpointStatus PATCH /admin/api/v3/merchant-webhooks/endpoints/{id}/status?merchant_id=
func (h *Handler) UpdateEndpointStatus(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := parseMerchant(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	endpointID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_ENDPOINT_ID", "id must be a UUID")
		return
	}
	var req statusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.Status != "ACTIVE" && req.Status != "DISABLED" {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_STATUS", "status must be ACTIVE or DISABLED")
		return
	}
	if err := h.repo.SetEndpointStatus(r.Context(), merchantID, endpointID, req.Status); err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "WEBHOOK_UPDATE_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"id": endpointID, "status": req.Status})
}

// DeleteEndpoint DELETE /admin/api/v3/merchant-webhooks/endpoints/{id}?merchant_id=
func (h *Handler) DeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := parseMerchant(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	endpointID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_ENDPOINT_ID", "id must be a UUID")
		return
	}
	if err := h.repo.DeleteEndpoint(r.Context(), merchantID, endpointID); err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "WEBHOOK_DELETE_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"deleted": endpointID})
}

// ListDeliveries GET /admin/api/v3/merchant-webhooks/deliveries?merchant_id=&limit=
func (h *Handler) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := parseMerchant(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := h.repo.ListDeliveries(r.Context(), merchantID, limit)
	if err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "WEBHOOK_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"deliveries": rows})
}

// ReplayDelivery POST /admin/api/v3/merchant-webhooks/deliveries/{id}/replay?merchant_id=
func (h *Handler) ReplayDelivery(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := parseMerchant(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_DELIVERY_ID", "id must be a UUID")
		return
	}
	ok2, err := h.repo.Replay(r.Context(), merchantID, deliveryID)
	if err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "WEBHOOK_REPLAY_FAILED", err.Error())
		return
	}
	if !ok2 {
		httpx.ErrorResponse(w, http.StatusNotFound, "DELIVERY_NOT_REPLAYABLE",
			"delivery not found or not in DEAD/FAILED state")
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"id": deliveryID, "status": "PENDING"})
}
