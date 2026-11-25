package pool

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/internal/pool/domain"
)

// Handler 支付池管理端接口。
type Handler struct {
	repo *Repository
	log  *slog.Logger
}

func NewHandler(repo *Repository, log *slog.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

// ListChannels GET /admin/api/v3/pool/channels?merchant_id=
func (h *Handler) ListChannels(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(r.URL.Query().Get("merchant_id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	channels, err := h.repo.ListAllChannels(r.Context(), merchantID)
	if err != nil {
		h.log.Error("pool: list channels failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "POOL_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"channels": channels})
}

type upsertChannelRequest struct {
	TenantID          uuid.UUID  `json:"tenant_id"`
	MerchantID        uuid.UUID  `json:"merchant_id"`
	AcquirerChannelID uuid.UUID  `json:"acquirer_channel_id"`
	EntityID          *uuid.UUID `json:"entity_id"`
	PSPCode           string     `json:"psp_code"`
	ExternalMID       string     `json:"external_mid"`
	MCC               string     `json:"mcc"`
	BusinessLines     []string   `json:"business_lines"`
	Currencies        []string   `json:"currencies"`
	CardBrands        []string   `json:"card_brands"`
	MinAmountMinor    int64      `json:"min_amount_minor"`
	MaxAmountMinor    int64      `json:"max_amount_minor"`
	DailyLimitMinor   int64      `json:"daily_limit_minor"`
	MonthlyLimitMinor int64      `json:"monthly_limit_minor"`
	CostBps           int        `json:"cost_bps"`
	SettlementDays    int        `json:"settlement_days"`
	Weight            int        `json:"weight"`
	HealthScore       int        `json:"health_score"`
	Status            string     `json:"status"`
}

// UpsertChannel POST /admin/api/v3/pool/channels
func (h *Handler) UpsertChannel(w http.ResponseWriter, r *http.Request) {
	var req upsertChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.MerchantID == uuid.Nil || req.AcquirerChannelID == uuid.Nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST",
			"merchant_id and acquirer_channel_id are required")
		return
	}
	if req.TenantID == uuid.Nil {
		req.TenantID = req.MerchantID
	}
	if req.Status == "" {
		req.Status = "ACTIVE"
	}
	if req.Weight <= 0 {
		req.Weight = 100
	}
	if req.HealthScore <= 0 {
		req.HealthScore = 10000
	}

	channel := domain.Channel{
		TenantID:          req.TenantID,
		MerchantID:        req.MerchantID,
		AcquirerChannelID: req.AcquirerChannelID,
		EntityID:          req.EntityID,
		PSPCode:           req.PSPCode,
		ExternalMID:       req.ExternalMID,
		MCC:               req.MCC,
		BusinessLines:     req.BusinessLines,
		Currencies:        req.Currencies,
		CardBrands:        req.CardBrands,
		MinAmountMinor:    req.MinAmountMinor,
		MaxAmountMinor:    req.MaxAmountMinor,
		DailyLimitMinor:   req.DailyLimitMinor,
		MonthlyLimitMinor: req.MonthlyLimitMinor,
		CostBps:           req.CostBps,
		SettlementDays:    req.SettlementDays,
		Weight:            req.Weight,
		HealthScore:       req.HealthScore,
		Status:            req.Status,
	}
	id, err := h.repo.UpsertChannel(r.Context(), channel)
	if err != nil {
		h.log.Error("pool: upsert channel failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "POOL_UPSERT_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"id": id})
}

// ListPolicies GET /admin/api/v3/pool/policies?merchant_id=
func (h *Handler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(r.URL.Query().Get("merchant_id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	policies, err := h.repo.ListPolicies(r.Context(), merchantID)
	if err != nil {
		h.log.Error("pool: list policies failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "POOL_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"policies": policies})
}

type upsertPolicyRequest struct {
	TenantID          uuid.UUID          `json:"tenant_id"`
	MerchantID        uuid.UUID          `json:"merchant_id"`
	SiteID            string             `json:"site_id"`
	BusinessLine      string             `json:"business_line"`
	AllowedChannelIDs []uuid.UUID        `json:"allowed_channel_ids"`
	Strategy          string             `json:"strategy"`
	Weights           map[string]float64 `json:"weights"`
	Status            string             `json:"status"`
}

// UpsertPolicy POST /admin/api/v3/pool/policies
func (h *Handler) UpsertPolicy(w http.ResponseWriter, r *http.Request) {
	var req upsertPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.MerchantID == uuid.Nil || len(req.AllowedChannelIDs) == 0 {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST",
			"merchant_id and at least one allowed_channel_id are required")
		return
	}
	if req.TenantID == uuid.Nil {
		req.TenantID = req.MerchantID
	}
	id, err := h.repo.UpsertPolicy(r.Context(), domain.Policy{
		TenantID:          req.TenantID,
		MerchantID:        req.MerchantID,
		SiteID:            req.SiteID,
		BusinessLine:      req.BusinessLine,
		AllowedChannelIDs: req.AllowedChannelIDs,
		Strategy:          req.Strategy,
		Weights:           req.Weights,
		Status:            req.Status,
	})
	if err != nil {
		h.log.Error("pool: upsert policy failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "POOL_UPSERT_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"id": id})
}

// ListWatermarks GET /admin/api/v3/pool/watermarks?merchant_id=&date=YYYY-MM-DD
func (h *Handler) ListWatermarks(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(r.URL.Query().Get("merchant_id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	day := r.URL.Query().Get("date")
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	rows, err := h.repo.ListWatermarks(r.Context(), merchantID, day)
	if err != nil {
		h.log.Error("pool: list watermarks failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "POOL_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"date": day, "watermarks": rows})
}
