package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/admin/banking-calendar", h.CreateCalendar)
	r.Post("/admin/sweep-rule", h.CreateSweepRule)
	r.Patch("/admin/sweep-rule/{id}", h.UpdateSweepRule)
	r.Post("/internal/sweep/evaluate", h.Evaluate)
	r.Post("/internal/sweep/{id}/submit", h.Submit)
	r.Post("/internal/sweep/{id}/reconcile", h.Reconcile)
	return r
}

func TestCreateCalendar_Success(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	body := `{"calendar_name":"US_FED","jurisdiction":"US","timezone":"America/New_York"}`
	req := httptest.NewRequest("POST", "/admin/banking-calendar", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestCreateSweepRule_Success(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	body := `{"currency":"USD","rail":"ACH","beneficiary_type":"SAME_NAME"}`
	req := httptest.NewRequest("POST", "/admin/sweep-rule", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestUpdateSweepRule_InvalidID(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	req := httptest.NewRequest("PATCH", "/admin/sweep-rule/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "INVALID_ARGUMENT", errObj["code"])
}

func TestUpdateSweepRule_Success(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("PATCH", "/admin/sweep-rule/"+id.String(), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEvaluate_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/internal/sweep/evaluate", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp EvaluateResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "ACCUMULATE", resp.Decision)
}

func TestSubmit_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/sweep/"+id.String()+"/submit", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestReconcile_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/sweep/"+id.String()+"/reconcile", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
