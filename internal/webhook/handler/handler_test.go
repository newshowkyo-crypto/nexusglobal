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
	r.Post("/webhook/mae/{provider_id}", h.HandleMAE)
	r.Post("/webhook/bank/{provider_id}", h.HandleBank)
	r.Post("/webhook/lp/{provider_id}", h.HandleLP)
	r.Get("/admin/webhook/events", h.ListEvents)
	r.Post("/admin/webhook/events/{id}/replay", h.Replay)
	return r
}

func TestHandleMAE_MissingSignature(t *testing.T) {
	h := New(nil, nil, nil)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/webhook/mae/prov_01", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "UNAUTHORIZED", errObj["code"])
}

func TestHandleMAE_Success(t *testing.T) {
	h := New(nil, nil, nil)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/webhook/mae/prov_01", strings.NewReader(`{"event":"captured"}`))
	req.Header.Set("X-Signature", "valid-sig")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp WebhookResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Received)
	assert.NotEmpty(t, resp.IdempotencyKey)
}

func TestHandleBank_Success(t *testing.T) {
	h := New(nil, nil, nil)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/webhook/bank/prov_01", strings.NewReader(`{}`))
	req.Header.Set("X-Signature", "sig")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleLP_Success(t *testing.T) {
	h := New(nil, nil, nil)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/webhook/lp/prov_01", strings.NewReader(`{}`))
	req.Header.Set("X-Signature", "sig")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestListEvents_Success(t *testing.T) {
	h := New(nil, nil, nil)
	r := setupRouter(h)

	req := httptest.NewRequest("GET", "/admin/webhook/events", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReplay_Success(t *testing.T) {
	h := New(nil, nil, nil)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/admin/webhook/events/"+id.String()+"/replay", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]bool
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp["replayed"])
}

func TestReplay_InvalidID(t *testing.T) {
	h := New(nil, nil, nil)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/admin/webhook/events/not-a-uuid/replay", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
