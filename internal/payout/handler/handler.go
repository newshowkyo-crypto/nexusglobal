package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/internal/platform/idempotency"
)

type PayoutService interface {
	Freeze(ctx context.Context, cmd service.FreezeCmd) (service.FreezeResult, error)
	CreateAddress(ctx context.Context, cmd service.CreateAddressCmd) (service.CreateAddressResult, error)
	CreatePayoutRequest(ctx context.Context, cmd service.CreateRequestCmd) (uuid.UUID, error)
	GetPayoutRequest(ctx context.Context, id uuid.UUID) (*db.PayoutRequest, error)
}

type Handler struct {
	svc PayoutService
}

func New(svc PayoutService) *Handler {
	return &Handler{svc: svc}
}

type CreateAddressResponse struct {
	ID                  string `json:"id"`
	TargetAddressMasked string `json:"target_address_masked"`
	ScreeningStatus     string `json:"screening_status"`
	WhitelistStatus     string `json:"whitelist_status"`
}

func (h *Handler) CreateAddress(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("payout service not initialized"))
		return
	}

	// G1: 从 JWT context 取 merchant_id，禁止从请求体取
	ctxMerchantID := httpx.GetMerchantID(r.Context())
	if ctxMerchantID == "" {
		writeError(w, apperr.ErrUnauthorized.WithMessage("merchant_id not found in token"))
		return
	}
	merchantID, err := uuid.Parse(ctxMerchantID)
	if err != nil {
		writeError(w, apperr.ErrUnauthorized.WithMessage("invalid merchant_id in token"))
		return
	}

	var req struct {
		TargetAsset       string `json:"target_asset"`
		TargetNetwork     string `json:"target_network"`
		AddressFormatType string `json:"address_format_type"`
		TargetAddress     string `json:"target_address"`
		AddressLabel      string `json:"address_label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.TargetAddress == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("target_address required"))
		return
	}

	addrResult, err := h.svc.CreateAddress(r.Context(), service.CreateAddressCmd{
		MerchantID:        merchantID,
		TargetAsset:       req.TargetAsset,
		TargetNetwork:     req.TargetNetwork,
		AddressFormatType: req.AddressFormatType,
		TargetAddress:     req.TargetAddress,
		AddressLabel:      req.AddressLabel,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreateAddressResponse{
		ID:                  addrResult.ID,
		TargetAddressMasked: addrResult.TargetAddressMasked,
		ScreeningStatus:     addrResult.ScreeningStatus,
		WhitelistStatus:     addrResult.WhitelistStatus,
	})
}

type CreatePayoutResponse struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *Handler) CreatePayoutRequest(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("Idempotency-Key header required"))
		return
	}
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("payout service not initialized"))
		return
	}

	// G1: 从 JWT context 取 merchant_id
	ctxMerchantID := httpx.GetMerchantID(r.Context())
	if ctxMerchantID == "" {
		writeError(w, apperr.ErrUnauthorized.WithMessage("merchant_id not found in token"))
		return
	}
	merchantID, err := uuid.Parse(ctxMerchantID)
	if err != nil {
		writeError(w, apperr.ErrUnauthorized.WithMessage("invalid merchant_id in token"))
		return
	}

	// R2-1: 先读取原始 body 计算 payload hash
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("failed to read request body"))
		return
	}
	payloadHash := idempotency.PayloadHash(body)

	var req struct {
		PayoutAddressID string `json:"payout_address_id"`
		Amount          string `json:"amount"`
		FiatCurrency    string `json:"fiat_currency"`
		TargetAsset     string `json:"target_asset"`
		TargetNetwork   string `json:"target_network"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.Amount == "" || req.FiatCurrency == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("amount and fiat_currency required"))
		return
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid amount format"))
		return
	}
	addrID, _ := uuid.Parse(req.PayoutAddressID)

	// G1: 校验地址归属（需要 service 层支持，此处传入 merchantID）
	payoutID, err := h.svc.CreatePayoutRequest(r.Context(), service.CreateRequestCmd{
		MerchantID:      merchantID,
		PayoutAddressID: addrID,
		Amount:          amount,
		FiatCurrency:    req.FiatCurrency,
		TargetAsset:     req.TargetAsset,
		TargetNetwork:   req.TargetNetwork,
		IdempotencyKey:  idempotencyKey,
		PayloadHash:     payloadHash, // R2-1: 传入 payload hash
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreatePayoutResponse{
		ID:             payoutID.String(),
		Status:         string(domain.StatusDraft),
		IdempotencyKey: idempotencyKey,
	})
}

func (h *Handler) Freeze(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	payoutID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid payout id"))
		return
	}
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("payout service not initialized"))
		return
	}
	var req struct {
		MerchantID      string `json:"merchant_id"`
		Amount          string `json:"amount"`
		FiatCurrency    string `json:"fiat_currency"`
		PayoutAddressID string `json:"payout_address_id"`
		TargetAsset     string `json:"target_asset"`
		TargetNetwork   string `json:"target_network"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid amount"))
		return
	}
	addrID, _ := uuid.Parse(req.PayoutAddressID)
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		idempotencyKey = "freeze:" + payoutID.String()
	}
	result, err := h.svc.Freeze(r.Context(), service.FreezeCmd{
		PayoutRequestID: payoutID,
		MerchantID:      merchantID,
		Amount:          amount,
		FiatCurrency:    req.FiatCurrency,
		PayoutAddressID: addrID,
		TargetAsset:     req.TargetAsset,
		TargetNetwork:   req.TargetNetwork,
		IdempotencyKey:  idempotencyKey,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":               result.Status,
		"ledger_freeze_txn_id": result.PayoutRequestID,
	})
}

// SEC-012: 原 /internal/payout/{id}/fx-lock|submit|reconcile 三个假成功端点已删除。
// 它们只读取状态并返回硬编码 FxLocked / LPProcessing / Confirmed，不执行任何动作。
// 真实路径：Freeze 写 Outbox FX_LOCK_REQUESTED → fxlock worker 调 LP 锁价 →
// 写 LP_PAYOUT_REQUESTED → payout worker 调 LP 出金 → reconcile worker /
// LP Webhook 依据 Provider 证据推进终态（RED-02/RED-03）。

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

func writeAppErr(w http.ResponseWriter, err error) {
	if appErr, ok := apperr.AsAPIError(err); ok {
		writeError(w, appErr)
		return
	}
	writeError(w, apperr.ErrInternal.WithMessage(err.Error()))
}
