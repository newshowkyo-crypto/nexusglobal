package split

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// Handler 分账规则管理接口。
type Handler struct {
	repo *Repository
	log  *slog.Logger
}

func NewHandler(repo *Repository, log *slog.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

// List GET /admin/api/v3/splits/rules?merchant_id=
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(r.URL.Query().Get("merchant_id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	rules, err := h.repo.ListRules(r.Context(), merchantID)
	if err != nil {
		h.log.Error("split: list rules failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "SPLIT_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"rules": rules})
}

type upsertRuleRequest struct {
	TenantID   uuid.UUID  `json:"tenant_id"`
	MerchantID uuid.UUID  `json:"merchant_id"`
	Currency   string     `json:"currency"`
	Name       string     `json:"name"`
	Receivers  []Receiver `json:"receivers"`
	Status     string     `json:"status"`
}

// Upsert POST /admin/api/v3/splits/rules
func (h *Handler) Upsert(w http.ResponseWriter, r *http.Request) {
	var req upsertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.MerchantID == uuid.Nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "merchant_id is required")
		return
	}
	if len(req.Receivers) == 0 {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "receivers must not be empty")
		return
	}
	var totalBps int
	for _, rc := range req.Receivers {
		if rc.Account == "" {
			httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "receiver account is required")
			return
		}
		if rc.RateBps < 0 || rc.FixedMinor < 0 {
			httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_RATE", "rate_bps and fixed_minor must be >= 0")
			return
		}
		totalBps += rc.RateBps
	}
	if totalBps > 10000 {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_RATE", "sum of rate_bps must not exceed 10000")
		return
	}
	if req.TenantID == uuid.Nil {
		req.TenantID = req.MerchantID
	}
	id, err := h.repo.UpsertRule(r.Context(), Rule{
		TenantID:   req.TenantID,
		MerchantID: req.MerchantID,
		Currency:   req.Currency,
		Name:       req.Name,
		Receivers:  req.Receivers,
		Status:     req.Status,
	})
	if err != nil {
		h.log.Error("split: upsert rule failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "SPLIT_UPSERT_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"id": id})
}
