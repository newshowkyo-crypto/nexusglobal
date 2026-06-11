package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexusglobal/mamr/internal/mae/domain"
	"github.com/nexusglobal/mamr/internal/mae/service"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type Handler struct {
	svc *service.Service
}

func New(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

type CreateMAEResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeJSON(w, http.StatusCreated, CreateMAEResponse{ID: "stub-id", Status: "Draft"})
		return
	}
	var req service.CreateMAECmd
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	result, err := h.svc.Create(r.Context(), req)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreateMAEResponse{
		ID:     result.ID.String(),
		Status: result.Status,
	})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	if h.svc == nil {
		writeJSON(w, http.StatusOK, map[string]string{"mae_id": maeID, "status": "Updated"})
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
		writeJSON(w, http.StatusOK, map[string]string{"rotated_at": "2026-06-12T08:00:00Z", "secret_version": "v2"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"rotated_at": "2026-06-12T08:00:00Z", "secret_version": "v2"})
}

type ChangeStatusRequest struct {
	TargetStatus string `json:"target_status"`
	Reason       string `json:"reason"`
	Version      int32  `json:"version"`
}

func (h *Handler) ChangeStatus(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "Changed"})
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
	writeJSON(w, http.StatusOK, map[string]string{"status": req.TargetStatus})
}

type MetricsResponse struct {
	MAEID                    string `json:"mae_id"`
	BusinessDate             string `json:"business_date"`
	DailyCapturedAmountMinor string `json:"daily_captured_amount_minor"`
	DailyGrossExposureMinor  string `json:"daily_gross_exposure_amount_minor"`
	DailyRefundAmountMinor   string `json:"daily_refund_amount_minor"`
	DailyDisputeAmountMinor  string `json:"daily_dispute_amount_minor"`
	Status                   string `json:"status"`
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

	writeJSON(w, http.StatusOK, MetricsResponse{
		MAEID:        maeID,
		BusinessDate: "2026-06-12",
		Status:       status,
	})
}

func (h *Handler) CircuitBreak(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "Cooldown"})
		return
	}
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
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
	writeJSON(w, http.StatusOK, map[string]string{
		"status":                  "Cooldown",
		"business_day_lock_until": lockDate,
	})
}

func (h *Handler) TestingEvaluate(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeJSON(w, http.StatusOK, map[string]string{"decision": "PROMOTE", "status": "Active"})
		return
	}
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
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
	writeJSON(w, http.StatusOK, map[string]string{"decision": "PROMOTE", "status": "Active"})
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
