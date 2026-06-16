package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV3IssuingPoolsHandler_ListPools(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3IssuingPoolsHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/issuing/pools", nil)

	handler.ListPools(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp, "items")
	assert.Contains(t, resp, "total")
}

func TestV3IssuingPoolsHandler_CreatePool_Validation(t *testing.T) {
	handler := NewV3IssuingPoolsHandler(nil, nil)

	testCases := []struct {
		name string
		body map[string]interface{}
	}{
		{
			name: "missing provider_code",
			body: map[string]interface{}{
				"issuing_tier":         "TIER_2_DIRECT",
				"funding_mode":         "FIAT_DIRECT",
				"pool_code":            "test-pool",
				"credential_vault_key": "vault-key-123",
			},
		},
		{
			name: "missing credential_vault_key",
			body: map[string]interface{}{
				"issuing_tier":  "TIER_2_DIRECT",
				"funding_mode":  "FIAT_DIRECT",
				"provider_code": "test-provider",
				"pool_code":     "test-pool",
			},
		},
		{
			name: "invalid issuing_tier",
			body: map[string]interface{}{
				"issuing_tier":         "INVALID_TIER",
				"funding_mode":         "FIAT_DIRECT",
				"provider_code":        "test-provider",
				"pool_code":            "test-pool",
				"credential_vault_key": "vault-key-123",
			},
		},
		{
			name: "invalid funding_mode",
			body: map[string]interface{}{
				"issuing_tier":         "TIER_2_DIRECT",
				"funding_mode":         "INVALID_MODE",
				"provider_code":        "test-provider",
				"pool_code":            "test-pool",
				"credential_vault_key": "vault-key-123",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", tc.body)

			handler.CreatePool(rec, req)

			assert.NotEqual(t, http.StatusOK, rec.Code)
		})
	}
}

func TestV3IssuingPoolsHandler_CreatePool_NoCredentialPlaintext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3IssuingPoolsHandler(pool, nil)
	body := map[string]interface{}{
		"issuing_tier":         "TIER_2_DIRECT",
		"funding_mode":         "FIAT_DIRECT",
		"provider_code":        "test-provider",
		"pool_code":            "test-pool",
		"credential_vault_key": "vault-key-123",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", body)

	handler.CreatePool(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.NotContains(t, resp, "credential_vault_key")
	assert.NotContains(t, resp, "credential")
}

func TestV3IssuingPoolsHandler_EnableDisableReload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	poolID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key)
		 VALUES ($1, 'TIER_2_DIRECT', 'FIAT_DIRECT', 'test-provider', 'ops-pool', 'vault-key-123')`,
		poolID)
	require.NoError(t, err)

	handler := NewV3IssuingPoolsHandler(pool, nil)

	req, rec := makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/issuing/pools/"+poolID.String()+"/disable", nil, map[string]string{"id": poolID.String()})
	handler.DisablePool(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	req, rec = makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/issuing/pools/"+poolID.String()+"/enable", nil, map[string]string{"id": poolID.String()})
	handler.EnablePool(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	req, rec = makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/issuing/pools/"+poolID.String()+"/reload", nil, map[string]string{"id": poolID.String()})
	handler.ReloadPool(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "reload_triggered", resp["status"])
}
