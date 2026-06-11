package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type Handler struct{}

func New() *Handler {
	return &Handler{}
}

type CreateMAERequest struct {
	MAEID       string `json:"mae_id"`
	DisplayName string `json:"display_name"`
	MAEType     string `json:"mae_type"`
}

type CreateMAEResponse struct {
	ID        string `json:"id"`
	MAEID     string `json:"mae_id"`
	Status    string `json:"status"`
	Version   int32  `json:"version"`
	CreatedAt string `json:"created_at"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, CreateMAEResponse{
		ID:     uuid.New().String(),
		Status: "Draft",
	})
}

type UpdateMAERequest struct {
	DisplayName string `json:"display_name"`
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mae_id": maeID, "status": "Updated"})
}

type RotateSecretRequest struct {
	APIKey        string `json:"api_key"`
	WebhookSecret string `json:"webhook_secret"`
}

func (h *Handler) RotateSecret(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"rotated_at": "2026-06-12T08:00:00Z", "secret_version": "v2"})
}

type ChangeStatusRequest struct {
	TargetStatus string `json:"target_status"`
	Reason       string `json:"reason"`
	Version      int32  `json:"version"`
}

func (h *Handler) ChangeStatus(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "Changed"})
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
	writeJSON(w, http.StatusOK, MetricsResponse{
		MAEID:        maeID,
		BusinessDate: "2026-06-12",
		Status:       "Active",
	})
}

type CircuitBreakRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) CircuitBreak(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "Cooldown"})
}

type TestingEvaluateResponse struct {
	Decision string `json:"decision"`
	Status   string `json:"status"`
}

func (h *Handler) TestingEvaluate(w http.ResponseWriter, r *http.Request) {
	maeID := chi.URLParam(r, "mae_id")
	if maeID == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("mae_id required"))
		return
	}
	writeJSON(w, http.StatusOK, TestingEvaluateResponse{
		Decision: "PROMOTE",
		Status:   "Active",
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
