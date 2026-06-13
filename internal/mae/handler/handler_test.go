package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/admin/mae", h.Create)
	r.Patch("/admin/mae/{mae_id}", h.Update)
	r.Post("/admin/mae/{mae_id}/rotate-secret", h.RotateSecret)
	r.Post("/admin/mae/{mae_id}/status", h.ChangeStatus)
	r.Get("/admin/mae/{mae_id}/metrics", h.GetMetrics)
	r.Post("/internal/mae/{mae_id}/circuit-break", h.CircuitBreak)
	r.Post("/internal/mae/{mae_id}/testing-evaluate", h.TestingEvaluate)
	return r
}

func newTestHandler() *Handler {
	return New(nil, nil)
}

func TestCreate_Success(t *testing.T) {
	h := newTestHandler()
	r := setupRouter(h)

	body := `{"mae_id":"mae_01","display_name":"Test MAE","mae_type":"CARD_GATEWAY"}`
	req := httptest.NewRequest("POST", "/admin/mae", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestUpdate_Success(t *testing.T) {
	h := newTestHandler()
	r := setupRouter(h)

	req := httptest.NewRequest("PATCH", "/admin/mae/mae_01", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRotateSecret_Success(t *testing.T) {
	h := newTestHandler()
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/admin/mae/mae_01/rotate-secret", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestChangeStatus_Success(t *testing.T) {
	h := newTestHandler()
	r := setupRouter(h)

	body := `{"target_status":"Suspended","reason":"test"}`
	req := httptest.NewRequest("POST", "/admin/mae/mae_01/status", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetMetrics_Success(t *testing.T) {
	h := newTestHandler()
	r := setupRouter(h)

	req := httptest.NewRequest("GET", "/admin/mae/mae_01/metrics", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCircuitBreak_NoService(t *testing.T) {
	h := newTestHandler()
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/internal/mae/mae_01/circuit-break", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestTestingEvaluate_NoService(t *testing.T) {
	h := newTestHandler()
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/internal/mae/mae_01/testing-evaluate", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestGetMetrics_Response(t *testing.T) {
	h := newTestHandler()
	r := setupRouter(h)

	req := httptest.NewRequest("GET", "/admin/mae/mae_01/metrics", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp MetricsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "mae_01", resp.MAEID)
	assert.Equal(t, "Active", resp.Status)
}
