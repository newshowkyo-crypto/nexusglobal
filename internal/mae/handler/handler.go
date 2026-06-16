package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/mae/domain"
	"github.com/nexusglobal/mamr/internal/mae/metric"
	"github.com/nexusglobal/mamr/internal/mae/service"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type Handler struct {
	svc     *service.Service
	metrics *metric.Aggregator
}

func New(svc *service.Service, metrics *metric.Aggregator) *Handler {
	return &Handler{svc: svc, metrics: metrics}
}

type CreateMAEResponse struct {
	ID        string `json:"id"`
	MAEID     string `json:"mae_id"`
	Status    string `json:"status"`
	Version   int32  `json:"version"`
	CreatedAt string `json:"created_at"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not initialized"))
		return
	}
	var raw struct {
		MAEID                    string   `json:"mae_id"`
		DisplayName              string   `json:"display_name"`
		MAEType                  string   `json:"mae_type"`
		TenantScope              []string `json:"tenant_scope"`
		Jurisdiction             string   `json:"jurisdiction"`
		SupportedCurrencies      []string `json:"supported_currencies"`
		APIBaseURL               string   `json:"api_base_url"`
		APIKey                   string   `json:"api_key"`
		WebhookSecret            string   `json:"webhook_secret"`
		WebhookProxyURL          string   `json:"webhook_proxy_url"`
		RiskLabel                string   `json:"risk_label"`
		SettlementBankAccountID  string   `json:"settlement_bank_account_id"`
		DailyVmaxMinor           int64    `json:"daily_vmax_minor"`
		MonthlyVmaxMinor         int64    `json:"monthly_vmax_minor"`
		CmaxBps                  int32    `json:"cmax_bps"`
		CardTestingLimit         int32    `json:"card_testing_limit"`
		RoutingWeight            int32    `json:"routing_weight"`
		CooldownMinutes          int32    `json:"cooldown_minutes"`
		TestingTrafficBps        int32    `json:"testing_traffic_bps"`
		TestingMinTxnCount       int32    `json:"testing_min_txn_count"`
		TestingMinSuccessRateBps int32    `json:"testing_min_success_rate_bps"`
		TestingMaxP95LatencyMs   int32    `json:"testing_max_p95_latency_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	tenantScopeJSON, _ := json.Marshal(raw.TenantScope)
	currenciesJSON, _ := json.Marshal(raw.SupportedCurrencies)
	settlementID, _ := uuid.Parse(raw.SettlementBankAccountID)

	cmd := service.CreateMAECmd{
		MAEID:                    raw.MAEID,
		DisplayName:              raw.DisplayName,
		MAEType:                  raw.MAEType,
		TenantScope:              tenantScopeJSON,
		Jurisdiction:             raw.Jurisdiction,
		SupportedCurrencies:      currenciesJSON,
		APIBaseURL:               raw.APIBaseURL,
		APIKey:                   raw.APIKey,
		WebhookSecret:            raw.WebhookSecret,
		WebhookProxyURL:          raw.WebhookProxyURL,
		RiskLabel:                raw.RiskLabel,
		SettlementBankAccountID:  settlementID,
		DailyVmaxMinor:           raw.DailyVmaxMinor,
		MonthlyVmaxMinor:         raw.MonthlyVmaxMinor,
		CmaxBps:                  raw.CmaxBps,
		CardTestingLimit:         raw.CardTestingLimit,
		RoutingWeight:            raw.RoutingWeight,
		CooldownMinutes:          raw.CooldownMinutes,
		TestingTrafficBps:        raw.TestingTrafficBps,
		TestingMinTxnCount:       raw.TestingMinTxnCount,
		TestingMinSuccessRateBps: raw.TestingMinSuccessRateBps,
		TestingMaxP95LatencyMs:   raw.TestingMaxP95LatencyMs,
	}

	result, err := h.svc.Create(r.Context(), cmd)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	createdAt := ""
	if result.CreatedAt.Valid {
		createdAt = result.CreatedAt.Time.Format("2006-01-02T15:04:05Z")
	}
	writeJSON(w, http.StatusCreated, CreateMAEResponse{
		ID:        result.ID.String(),
		MAEID:     raw.MAEID,
		Status:    result.Status,
		Version:   result.Version,
		CreatedAt: createdAt,
	})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not initialized"))
		return
	}

	var raw struct {
		DisplayName              string   `json:"display_name"`
		MAEType                  string   `json:"mae_type"`
		TenantScope              []string `json:"tenant_scope"`
		Jurisdiction             string   `json:"jurisdiction"`
		SupportedCurrencies      []string `json:"supported_currencies"`
		APIBaseURL               string   `json:"api_base_url"`
		WebhookProxyURL          string   `json:"webhook_proxy_url"`
		RiskLabel                string   `json:"risk_label"`
		DailyVmaxMinor           int64    `json:"daily_vmax_minor"`
		MonthlyVmaxMinor         int64    `json:"monthly_vmax_minor"`
		CmaxBps                  int32    `json:"cmax_bps"`
		CardTestingLimit         int32    `json:"card_testing_limit"`
		RoutingWeight            int32    `json:"routing_weight"`
		CooldownMinutes          int32    `json:"cooldown_minutes"`
		TestingTrafficBps        int32    `json:"testing_traffic_bps"`
		TestingMinTxnCount       int32    `json:"testing_min_txn_count"`
		TestingMinSuccessRateBps int32    `json:"testing_min_success_rate_bps"`
		TestingMaxP95LatencyMs   int32    `json:"testing_max_p95_latency_ms"`
		Version                  int32    `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	tenantScopeJSON, _ := json.Marshal(raw.TenantScope)
	currenciesJSON, _ := json.Marshal(raw.SupportedCurrencies)

	err := h.svc.Update(r.Context(), service.UpdateMAECmd{
		MAEID:                    maeID,
		DisplayName:              raw.DisplayName,
		MAEType:                  raw.MAEType,
		TenantScope:              tenantScopeJSON,
		Jurisdiction:             raw.Jurisdiction,
		SupportedCurrencies:      currenciesJSON,
		APIBaseURL:               raw.APIBaseURL,
		WebhookProxyURL:          raw.WebhookProxyURL,
		RiskLabel:                raw.RiskLabel,
		DailyVmaxMinor:           raw.DailyVmaxMinor,
		MonthlyVmaxMinor:         raw.MonthlyVmaxMinor,
		CmaxBps:                  raw.CmaxBps,
		CardTestingLimit:         raw.CardTestingLimit,
		RoutingWeight:            raw.RoutingWeight,
		CooldownMinutes:          raw.CooldownMinutes,
		TestingTrafficBps:        raw.TestingTrafficBps,
		TestingMinTxnCount:       raw.TestingMinTxnCount,
		TestingMinSuccessRateBps: raw.TestingMinSuccessRateBps,
		TestingMaxP95LatencyMs:   raw.TestingMaxP95LatencyMs,
		Version:                  raw.Version,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mae_id": maeID, "status": "Updated"})
}

func (h *Handler) RotateSecret(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not initialized"))
		return
	}

	var raw struct {
		APIKey        string `json:"api_key"`
		WebhookSecret string `json:"webhook_secret"`
		Version       int32  `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	err := h.svc.RotateSecret(r.Context(), service.RotateSecretCmd{
		MAEID:         maeID,
		APIKey:        raw.APIKey,
		WebhookSecret: raw.WebhookSecret,
		Version:       raw.Version,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"rotated_at": time.Now().UTC().Format("2006-01-02T15:04:05Z"), "secret_version": "v2"})
}

type ChangeStatusRequest struct {
	TargetStatus string `json:"target_status"`
	Reason       string `json:"reason"`
	Version      int32  `json:"version"`
}

func (h *Handler) ChangeStatus(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not initialized"))
		return
	}
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	var req ChangeStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	err := h.svc.ChangeStatus(r.Context(), service.ChangeStatusCmd{
		MAEID:        maeID,
		TargetStatus: domain.MAEStatus(req.TargetStatus),
		Reason:       req.Reason,
		Version:      req.Version,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": req.TargetStatus, "version": req.Version})
}

type MetricsResponse struct {
	MAEID                         string `json:"mae_id"`
	BusinessDate                  string `json:"business_date"`
	DailyCapturedAmountMinor      string `json:"daily_captured_amount_minor"`
	DailySettledAmountMinor       string `json:"daily_settled_amount_minor"`
	DailyGrossExposureAmountMinor string `json:"daily_gross_exposure_amount_minor"`
	DailyRefundAmountMinor        string `json:"daily_refund_amount_minor"`
	DailyDisputeAmountMinor       string `json:"daily_dispute_amount_minor"`
	AbnormalRateBps               int32  `json:"abnormal_rate_bps"`
	P95LatencyMs                  int32  `json:"p95_latency_ms"`
	CardTestingCount              int32  `json:"card_testing_count"`
	Status                        string `json:"status"`
}

func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}

	status := "Active"
	if h.svc != nil {
		config, err := h.svc.GetConfig(r.Context(), maeID)
		if err == nil && config != nil {
			status = config.Status
		}
	}

	businessDate := metric.FormatBusinessDate(time.Now())
	var snapshot metric.Snapshot
	if h.metrics != nil {
		snapshot, _ = h.metrics.TakeSnapshot(r.Context(), maeID, businessDate)
	}

	writeJSON(w, http.StatusOK, MetricsResponse{
		MAEID:                         maeID,
		BusinessDate:                  businessDate,
		DailyCapturedAmountMinor:      fmt.Sprintf("%d", snapshot.CapturedMinor),
		DailySettledAmountMinor:       "0",
		DailyGrossExposureAmountMinor: fmt.Sprintf("%d", snapshot.GrossExposureMinor),
		DailyRefundAmountMinor:        fmt.Sprintf("%d", snapshot.RefundMinor),
		DailyDisputeAmountMinor:       fmt.Sprintf("%d", snapshot.DisputeMinor),
		AbnormalRateBps:               snapshot.AbnormalRateBps,
		P95LatencyMs:                  snapshot.P95LatencyMs,
		CardTestingCount:              snapshot.CardTestingCount,
		Status:                        status,
	})
}

func (h *Handler) CircuitBreak(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not initialized"))
		return
	}

	businessDate := metric.FormatBusinessDate(time.Now())
	var exposure int64
	var cardTesting int32
	if h.metrics != nil {
		exposure, _ = h.metrics.GetExposure(r.Context(), maeID, businessDate)
	}

	err := h.svc.EvaluateCircuitBreaker(r.Context(), h.metrics, service.EvaluateCircuitBreakerCmd{
		MAEID:            maeID,
		BusinessDate:     businessDate,
		CardTestingCount: cardTesting,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}

	config, err := h.svc.GetConfig(r.Context(), maeID)
	if err != nil || config == nil {
		writeError(w, apperr.ErrNotFound.WithMessage("MAE not found"))
		return
	}

	lockDate := ""
	if config.BusinessDayLockUntil.Valid {
		lockDate = config.BusinessDayLockUntil.Time.Format("2006-01-02")
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":                  config.Status,
		"business_day_lock_until": lockDate,
		"gross_exposure_minor":    exposure,
	})
}

func (h *Handler) TestingEvaluate(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not initialized"))
		return
	}

	config, err := h.svc.GetConfig(r.Context(), maeID)
	if err != nil || config == nil {
		writeError(w, apperr.ErrNotFound.WithMessage("MAE not found"))
		return
	}
	if domain.MAEStatus(config.Status) != domain.StatusTesting {
		writeError(w, apperr.ErrIllegalTransition.WithMessage("MAE not in Testing status"))
		return
	}

	result, err := h.svc.EvaluateTesting(r.Context(), service.EvaluateTestingCmd{
		MAEID: maeID,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"decision":      result.Decision,
		"target_status": result.TargetStatus,
		"reason":        result.Reason,
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

func writeAppErr(w http.ResponseWriter, err error) {
	if appErr, ok := apperr.AsAPIError(err); ok {
		writeError(w, appErr)
		return
	}
	writeError(w, apperr.ErrInternal.WithMessage(err.Error()))
}
