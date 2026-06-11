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
	r.Post("/merchant/payout-address", h.CreateAddress)
	r.Post("/merchant/payout-request", h.CreatePayoutRequest)
	r.Post("/internal/payout/{id}/freeze", h.Freeze)
	r.Post("/internal/payout/{id}/fx-lock", h.FxLock)
	r.Post("/internal/payout/{id}/submit", h.Submit)
	r.Post("/internal/payout/{id}/reconcile", h.Reconcile)
	return r
}

func TestCreateAddress_Success(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	body := `{"target_asset":"USDT","target_network":"TRON_TRC20","target_address":"TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9"}`
	req := httptest.NewRequest("POST", "/merchant/payout-address", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	var resp CreateAddressResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.TargetAddressMasked)
	assert.Equal(t, "Pending", resp.ScreeningStatus)
}

func TestCreatePayoutRequest_MissingIdempotencyKey(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/merchant/payout-request", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFreeze_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/freeze", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestFreeze_InvalidID(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/internal/payout/not-a-uuid/freeze", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFxLock_ValidID(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/fx-lock", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "FxLocked", resp["status"])
}

func TestSubmit_ValidID(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/submit", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReconcile_ValidID(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/reconcile", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
