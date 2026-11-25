package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/treasury/repository/db"
	"github.com/nexusglobal/mamr/internal/treasury/service"
)

type TreasuryService interface {
	Evaluate(ctx context.Context, cmd service.EvaluateCmd) (service.EvaluateResult, error)
	Submit(ctx context.Context, transferID uuid.UUID) error
	Reconcile(ctx context.Context, transferID uuid.UUID, bankRef string) (service.ReconcileRequestResult, error)
	ApproveSweep(ctx context.Context, approvalID uuid.UUID) error
}

type Handler struct {
	svc  TreasuryService
	pool *pgxpool.Pool
}

func New(svc TreasuryService, pool *pgxpool.Pool) *Handler {
	return &Handler{svc: svc, pool: pool}
}

func (h *Handler) CreateCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		CalendarName          string          `json:"calendar_name"`
		Jurisdiction          string          `json:"jurisdiction"`
		Timezone              string          `json:"timezone"`
		BusinessDays          json.RawMessage `json:"business_days"`
		Holidays              json.RawMessage `json:"holidays"`
		EmergencyClosureDates json.RawMessage `json:"emergency_closure_dates"`
		CutOffMatrix          json.RawMessage `json:"cut_off_matrix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.CalendarName == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("calendar_name required"))
		return
	}

	// 未提供的 JSON 字段使用表默认值，避免 NULL 违反 NOT NULL 约束
	if len(req.BusinessDays) == 0 {
		req.BusinessDays = json.RawMessage(`[]`)
	}
	if len(req.Holidays) == 0 {
		req.Holidays = json.RawMessage(`[]`)
	}
	if len(req.EmergencyClosureDates) == 0 {
		req.EmergencyClosureDates = json.RawMessage(`[]`)
	}
	if len(req.CutOffMatrix) == 0 {
		req.CutOffMatrix = json.RawMessage(`{}`)
	}

	q := db.New(h.pool)
	calendar, err := q.InsertBankingCalendar(ctx, db.InsertBankingCalendarParams{
		CalendarName:          req.CalendarName,
		Jurisdiction:          req.Jurisdiction,
		Timezone:              req.Timezone,
		BusinessDays:          []byte(req.BusinessDays),
		Holidays:              []byte(req.Holidays),
		EmergencyClosureDates: []byte(req.EmergencyClosureDates),
		CutOffMatrix:          []byte(req.CutOffMatrix),
	})
	if err != nil {
		writeError(w, apperr.ErrInternal.WithMessage("failed to create calendar"))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":     calendar.ID.String(),
		"status": calendar.Status,
	})
}

func (h *Handler) CreateSweepRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		SourceBankAccountID    string          `json:"source_bank_account_id"`
		TargetBankAccountID    string          `json:"target_bank_account_id"`
		Currency               string          `json:"currency"`
		Rail                   string          `json:"rail"`
		BeneficiaryType        string          `json:"beneficiary_type"`
		SameNameVerified       bool            `json:"same_name_verified"`
		TriggerThresholdMinor  int64           `json:"trigger_threshold_minor"`
		SweepRatioBps          int32           `json:"sweep_ratio_bps"`
		MaxSingleSweepMinor    int64           `json:"max_single_sweep_minor"`
		MinResidualMinor       int64           `json:"min_residual_minor"`
		TimeWindowMatrix       json.RawMessage `json:"time_window_matrix"`
		ScheduledSweepTimes    json.RawMessage `json:"scheduled_sweep_times"`
		SweepMode              string          `json:"sweep_mode"`
		MaxDailySweepCount     int32           `json:"max_daily_sweep_count"`
		DelayQueueEnabled      bool            `json:"delay_queue_enabled"`
		RequireFourEyes        bool            `json:"require_four_eyes"`
		ApprovalThresholdMinor int64           `json:"approval_threshold_minor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.Currency == "" || req.Rail == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("currency and rail required"))
		return
	}

	sourceID, err := uuid.Parse(req.SourceBankAccountID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid source_bank_account_id"))
		return
	}

	targetID, err := uuid.Parse(req.TargetBankAccountID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid target_bank_account_id"))
		return
	}

	// 未提供的 JSON / 模式字段使用表默认值，避免 NULL 违反 NOT NULL 约束
	if len(req.TimeWindowMatrix) == 0 {
		req.TimeWindowMatrix = json.RawMessage(`{}`)
	}
	if len(req.ScheduledSweepTimes) == 0 {
		req.ScheduledSweepTimes = json.RawMessage(`[]`)
	}
	if req.SweepMode == "" {
		req.SweepMode = "TIERED_SCHEDULED"
	}

	q := db.New(h.pool)
	rule, err := q.InsertSweepRule(ctx, db.InsertSweepRuleParams{
		SourceBankAccountID:    sourceID,
		TargetBankAccountID:    targetID,
		Currency:               req.Currency,
		Rail:                   req.Rail,
		BeneficiaryType:        req.BeneficiaryType,
		SameNameVerified:       req.SameNameVerified,
		TriggerThresholdMinor:  req.TriggerThresholdMinor,
		SweepRatioBps:          req.SweepRatioBps,
		MaxSingleSweepMinor:    req.MaxSingleSweepMinor,
		MinResidualMinor:       req.MinResidualMinor,
		TimeWindowMatrix:       []byte(req.TimeWindowMatrix),
		ScheduledSweepTimes:    []byte(req.ScheduledSweepTimes),
		SweepMode:              req.SweepMode,
		MaxDailySweepCount:     req.MaxDailySweepCount,
		DelayQueueEnabled:      req.DelayQueueEnabled,
		RequireFourEyes:        req.RequireFourEyes,
		ApprovalThresholdMinor: req.ApprovalThresholdMinor,
	})
	if err != nil {
		writeError(w, apperr.ErrInternal.WithMessage("failed to create sweep rule"))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      rule.ID.String(),
		"status":  rule.Status,
		"version": rule.Version,
	})
}

// UpdateSweepRule PATCH 部分更新：只更新请求体中出现的字段。
func (h *Handler) UpdateSweepRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid rule id"))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	// version 用于乐观锁，本身不写回
	version := int32(0)
	if raw, ok := fields["version"]; ok {
		_ = json.Unmarshal(raw, &version)
	}

	sets := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+2)
	add := func(col string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}

	strField := func(key, col string) {
		raw, ok := fields[key]
		if !ok {
			return
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return
		}
		add(col, v)
	}
	uuidField := func(key, col string) {
		raw, ok := fields[key]
		if !ok {
			return
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return
		}
		parsed, perr := uuid.Parse(v)
		if perr != nil {
			return
		}
		add(col, parsed)
	}
	jsonField := func(key, col string) {
		raw, ok := fields[key]
		if !ok {
			return
		}
		add(col, []byte(raw))
	}

	uuidField("source_bank_account_id", "source_bank_account_id")
	uuidField("target_bank_account_id", "target_bank_account_id")
	strField("currency", "currency")
	strField("rail", "rail")
	strField("beneficiary_type", "beneficiary_type")
	strField("sweep_mode", "sweep_mode")
	strField("status", "status")
	jsonField("time_window_matrix", "time_window_matrix")
	jsonField("scheduled_sweep_times", "scheduled_sweep_times")

	for _, f := range []struct {
		key string
		col string
		ptr any
	}{
		{"same_name_verified", "same_name_verified", new(bool)},
		{"trigger_threshold_minor", "trigger_threshold_minor", new(int64)},
		{"sweep_ratio_bps", "sweep_ratio_bps", new(int32)},
		{"max_single_sweep_minor", "max_single_sweep_minor", new(int64)},
		{"min_residual_minor", "min_residual_minor", new(int64)},
		{"max_daily_sweep_count", "max_daily_sweep_count", new(int32)},
		{"delay_queue_enabled", "delay_queue_enabled", new(bool)},
		{"require_four_eyes", "require_four_eyes", new(bool)},
		{"approval_threshold_minor", "approval_threshold_minor", new(int64)},
	} {
		raw, ok := fields[f.key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, f.ptr); err != nil {
			continue
		}
		switch v := f.ptr.(type) {
		case *bool:
			add(f.col, *v)
		case *int64:
			add(f.col, *v)
		case *int32:
			add(f.col, *v)
		}
	}

	if len(sets) == 0 {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("no updatable fields provided"))
		return
	}

	args = append(args, id, version)
	query := fmt.Sprintf(
		`UPDATE sweep_rule SET %s, version = version + 1, updated_at = now()
		 WHERE id = $%d AND version = $%d`,
		strings.Join(sets, ", "), len(args)-1, len(args))

	result, err := h.pool.Exec(ctx, query, args...)
	if err != nil {
		writeError(w, apperr.ErrInternal.WithMessage("failed to update sweep rule"))
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, apperr.ErrVersionConflict.WithMessage("version mismatch, rule may have been modified"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":      idStr,
		"status":  "Updated",
		"version": version + 1,
	})
}

type EvaluateResponse struct {
	Decision        string `json:"decision"`
	SweepTransferID string `json:"sweep_transfer_id,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

func (h *Handler) Evaluate(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeJSON(w, http.StatusOK, EvaluateResponse{Decision: "ACCUMULATE", Reason: "service not available"})
		return
	}

	var req struct {
		SweepRuleID string `json:"sweep_rule_id"`
		AsOf        string `json:"as_of"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	ruleID, err := uuid.Parse(req.SweepRuleID)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid sweep_rule_id"))
		return
	}

	asOf := time.Now().UTC()
	if req.AsOf != "" {
		asOf, err = time.Parse(time.RFC3339, req.AsOf)
		if err != nil {
			writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid as_of format"))
			return
		}
	}

	result, err := h.svc.Evaluate(r.Context(), service.EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        asOf,
	})
	if err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, EvaluateResponse{
		Decision:        string(result.Decision),
		SweepTransferID: result.TransferID,
		Reason:          result.Reason,
	})
}

func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not available"))
		return
	}

	id := chi.URLParam(r, "id")
	transferID, err := uuid.Parse(id)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid transfer id"))
		return
	}

	if err := h.svc.Submit(r.Context(), transferID); err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "Submitted"})
}

// Reconcile 受理对账命令（SEC-013）：返回 202 + command_id。
// 真实终态由 sweepreconcile Worker 查询银行后写入，本接口不返回 Confirmed。
func (h *Handler) Reconcile(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not available"))
		return
	}

	id := chi.URLParam(r, "id")
	transferID, err := uuid.Parse(id)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid transfer id"))
		return
	}

	bankRef := r.URL.Query().Get("bank_ref")

	result, err := h.svc.Reconcile(r.Context(), transferID, bankRef)
	if err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"command_id": result.CommandID,
		"status":     result.Status,
	})
}

func (h *Handler) ApproveSweep(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, apperr.ErrInternal.WithMessage("service not available"))
		return
	}

	id := chi.URLParam(r, "id")
	approvalID, err := uuid.Parse(id)
	if err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid approval id"))
		return
	}

	if err := h.svc.ApproveSweep(r.Context(), approvalID); err != nil {
		writeAppErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "Approved"})
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
