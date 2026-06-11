package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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

func TestCreateAddress_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	body := `{"target_asset":"USDT","target_network":"TRON_TRC20","target_address":"TAddr"}`
	req := httptest.NewRequest("POST", "/merchant/payout-address", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestCreatePayoutRequest_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	body := `{"amount":"100.00","fiat_currency":"USD"}`
	req := httptest.NewRequest("POST", "/merchant/payout-request", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "key-001")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestCreatePayoutRequest_MissingIdempotencyKey(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	req := httptest.NewRequest("POST", "/merchant/payout-request", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFreeze_InvalidID(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	req := httptest.NewRequest("POST", "/internal/payout/not-a-uuid/freeze", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFreeze_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/freeze", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestFxLock_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/fx-lock", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestSubmit_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/submit", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestReconcile_NilService(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/reconcile", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestFreeze_InvalidBody(t *testing.T) {
	h := New(nil)
	r := setupRouter(h)
	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/freeze", strings.NewReader(`{`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
