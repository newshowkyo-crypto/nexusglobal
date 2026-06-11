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

type CreateCalendarRequest struct {
	CalendarName string `json:"calendar_name"`
	Jurisdiction string `json:"jurisdiction"`
	Timezone     string `json:"timezone"`
}

func (h *Handler) CreateCalendar(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":     uuid.New().String(),
		"status": "Active",
	})
}

type CreateSweepRuleRequest struct {
	SourceBankAccountID string `json:"source_bank_account_id"`
	TargetBankAccountID string `json:"target_bank_account_id"`
	Currency            string `json:"currency"`
	Rail                string `json:"rail"`
	BeneficiaryType     string `json:"beneficiary_type"`
}

func (h *Handler) CreateSweepRule(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      uuid.New().String(),
		"status":  "Active",
		"version": 0,
	})
}

func (h *Handler) UpdateSweepRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid rule id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "Updated"})
}

type EvaluateRequest struct {
	SweepRuleID string `json:"sweep_rule_id"`
	AsOf        string `json:"as_of"`
}

type EvaluateResponse struct {
	Decision        string `json:"decision"`
	SweepTransferID string `json:"sweep_transfer_id,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

func (h *Handler) Evaluate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, EvaluateResponse{
		Decision: "ACCUMULATE",
		Reason:   "below threshold",
	})
}

func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid transfer id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "Submitted"})
}

func (h *Handler) Reconcile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid transfer id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "Confirmed"})
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
