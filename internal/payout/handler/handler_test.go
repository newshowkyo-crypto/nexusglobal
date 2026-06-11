package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateAddress_Success(t *testing.T) {
	handler := New()
	r := chi.NewRouter()
	r.Post("/merchant/payout-address", handler.CreateAddress)

	req := httptest.NewRequest("POST", "/merchant/payout-address", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	var resp CreateAddressResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.ID)
	assert.Equal(t, "Pending", resp.ScreeningStatus)
}

func TestCreatePayoutRequest_Success(t *testing.T) {
	handler := New()
	r := chi.NewRouter()
	r.Post("/merchant/payout-request", handler.CreatePayoutRequest)

	req := httptest.NewRequest("POST", "/merchant/payout-request", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	var resp CreatePayoutResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Draft", resp.Status)
}

func TestFreeze_InvalidID(t *testing.T) {
	handler := New()
	r := chi.NewRouter()
	r.Post("/internal/payout/{id}/freeze", handler.Freeze)

	req := httptest.NewRequest("POST", "/internal/payout/not-a-uuid/freeze", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "INVALID_ARGUMENT", errObj["code"])
}

func TestFreeze_ValidID(t *testing.T) {
	handler := New()
	r := chi.NewRouter()
	r.Post("/internal/payout/{id}/freeze", handler.Freeze)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/payout/"+id.String()+"/freeze", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp FreezeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "BalanceFrozen", resp.Status)
}
