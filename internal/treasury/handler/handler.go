package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/treasury/service"
)

type TreasuryService interface {
	Evaluate(ctx context.Context, cmd service.EvaluateCmd) (service.EvaluateResult, error)
	Submit(ctx context.Context, transferID uuid.UUID) error
	Reconcile(ctx context.Context, transferID uuid.UUID, bankRef string) error
}

type Handler struct {
	svc TreasuryService
}

func New(svc TreasuryService) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) CreateCalendar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CalendarName string `json:"calendar_name"`
		Jurisdiction string `json:"jurisdiction"`
		Timezone     string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.CalendarName == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("calendar_name required"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":     uuid.New().String(),
		"status": "Active",
	})
}

func (h *Handler) CreateSweepRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceBankAccountID string `json:"source_bank_account_id"`
		TargetBankAccountID string `json:"target_bank_account_id"`
		Currency            string `json:"currency"`
		Rail                string `json:"rail"`
		BeneficiaryType     string `json:"beneficiary_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.Currency == "" || req.Rail == "" {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("currency and rail required"))
		return
	}
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
	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "Updated"})
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

	if err := h.svc.Reconcile(r.Context(), transferID, ""); err != nil {
		writeAppErr(w, err)
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

func writeAppErr(w http.ResponseWriter, err error) {
	if appErr, ok := apperr.AsAPIError(err); ok {
		writeError(w, appErr)
		return
	}
	writeError(w, apperr.ErrInternal.WithMessage(err.Error()))
}
