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

func TestGetBalance_InvalidID(t *testing.T) {
	handler := &Handler{}
	r := chi.NewRouter()
	r.Get("/admin/merchant/{id}/balance", handler.GetBalance)

	req := httptest.NewRequest("GET", "/admin/merchant/not-a-uuid/balance", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "INVALID_ARGUMENT", errObj["code"])
}

func TestGetBalance_ValidID_NilQueries(t *testing.T) {
	handler := &Handler{}
	r := chi.NewRouter()
	r.Get("/admin/merchant/{id}/balance", handler.GetBalance)

	id := uuid.New()
	req := httptest.NewRequest("GET", "/admin/merchant/"+id.String()+"/balance", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
