package fee

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// Handler 费率管理接口。
type Handler struct {
	repo *Repository
	log  *slog.Logger
}

func NewHandler(repo *Repository, log *slog.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

// List GET /admin/api/v3/fees/pricing?merchant_id=
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(r.URL.Query().Get("merchant_id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	rows, err := h.repo.ListPricing(r.Context(), merchantID)
	if err != nil {
		h.log.Error("fee: list pricing failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "PRICING_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"pricing": rows})
}

type upsertPricingRequest struct {
	MerchantID    uuid.UUID  `json:"merchant_id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	Currency      string     `json:"currency"`
	CardBrand     string     `json:"card_brand"`
	ChannelID     *uuid.UUID `json:"channel_id"`
	RateBps       int        `json:"rate_bps"`
	FixedFeeMinor int64      `json:"fixed_fee_minor"`
	MinFeeMinor   int64      `json:"min_fee_minor"`
	MaxFeeMinor   int64      `json:"max_fee_minor"`
	Status        string     `json:"status"`
}

// Upsert POST /admin/api/v3/fees/pricing
func (h *Handler) Upsert(w http.ResponseWriter, r *http.Request) {
	var req upsertPricingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.MerchantID == uuid.Nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "merchant_id is required")
		return
	}
	if req.RateBps < 0 || req.RateBps > 10000 {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_RATE", "rate_bps must be between 0 and 10000")
		return
	}
	if req.TenantID == uuid.Nil {
		req.TenantID = req.MerchantID
	}
	channelID := uuid.Nil
	if req.ChannelID != nil {
		channelID = *req.ChannelID
	}
	id, err := h.repo.UpsertPricing(r.Context(), Pricing{
		TenantID:      req.TenantID,
		MerchantID:    req.MerchantID,
		Currency:      req.Currency,
		CardBrand:     req.CardBrand,
		ChannelID:     channelID,
		RateBps:       req.RateBps,
		FixedFeeMinor: req.FixedFeeMinor,
		MinFeeMinor:   req.MinFeeMinor,
		MaxFeeMinor:   req.MaxFeeMinor,
		Status:        req.Status,
	})
	if err != nil {
		h.log.Error("fee: upsert pricing failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "PRICING_UPSERT_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"id": id})
}
