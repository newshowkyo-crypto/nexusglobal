package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateMerchant_Success(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// Verify table exists
	var tableExists bool
	err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'corporate_entity'
		)
	`).Scan(&tableExists)
	require.NoError(t, err, "failed to check table existence")
	assert.True(t, tableExists, "corporate_entity table should exist")

	handler := NewV3MerchantHandler(pool)
	body := map[string]interface{}{
		"merchant_name":            "Acme Corporation",
		"legal_name":               "Acme Corp Legal Entity",
		"jurisdiction":             "US-CA",
		"settlement_currency":      "USD",
		"webhook_url":              "https://webhook.acme.example.com",
		"webhook_secret_vault_key": "vault://secrets/webhook/acme",
		"mae_config_id":            "mae-acme-001",
		"bank_account_id":          uuid.New().String(),
		"reserve_ratio_bps":        1500,
		"risk_label":               "LOW",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/merchants", body)
	tenantID := uuid.New().String()
	req = req.WithContext(context.WithValue(req.Context(), httpx.AdminContextKey{}, &httpx.AdminIdentity{TenantID: tenantID}))

	handler.CreateMerchant(rec, req)

	t.Logf("Response code: %d, body: %s", rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusCreated, rec.Code, "expected 201 Created, got %d: %s", rec.Code, rec.Body.String())

	var resp MerchantResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.NotEmpty(t, resp.ID)
	assert.Equal(t, "Acme Corporation", resp.MerchantName)
	assert.Equal(t, "Acme Corp Legal Entity", resp.LegalName)
	assert.Equal(t, "US-CA", resp.Jurisdiction)
	assert.Equal(t, "USD", resp.SettlementCurrency)
	assert.Equal(t, 1500, resp.ReserveRatioBps)
	assert.Equal(t, "LOW", resp.RiskLabel)

	var name, legalName, jurisdiction string
	var metadata []byte
	err = pool.QueryRow(context.Background(),
		`SELECT name, legal_name, jurisdiction, metadata FROM corporate_entity WHERE id = $1`,
		resp.ID).Scan(&name, &legalName, &jurisdiction, &metadata)
	require.NoError(t, err)
	assert.Equal(t, "Acme Corporation", name)
	assert.Equal(t, "Acme Corp Legal Entity", legalName)
	assert.Equal(t, "US-CA", jurisdiction)

	var meta map[string]interface{}
	require.NoError(t, json.Unmarshal(metadata, &meta))
	assert.Equal(t, "https://webhook.acme.example.com", meta["webhook_url"])
	assert.Equal(t, "vault://secrets/webhook/acme", meta["webhook_secret_vault_key"])
	assert.Equal(t, "mae-acme-001", meta["mae_config_id"])
	assert.Equal(t, float64(1500), meta["reserve_ratio_bps"])
	assert.Equal(t, "LOW", meta["risk_label"])
	assert.Equal(t, "USD", meta["settlement_currency"])
	var storedTenantID string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT tenant_id::text FROM corporate_entity WHERE id = $1`, resp.ID).Scan(&storedTenantID))
	assert.Equal(t, tenantID, storedTenantID, "created merchant must stay in the authenticated account")
	listReq, listRec := makeRequest(http.MethodGet, "/admin/api/v3/merchants", nil)
	listReq = listReq.WithContext(req.Context())
	handler.ListMerchants(listRec, listReq)
	assert.Equal(t, http.StatusOK, listRec.Code)
	assert.Contains(t, listRec.Body.String(), resp.ID, "new merchant must be visible in the same account")
}

func TestCreateMerchant_MissingRequiredFields(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3MerchantHandler(pool)

	testCases := []struct {
		name string
		body map[string]interface{}
	}{
		{
			name: "missing merchant_name",
			body: map[string]interface{}{
				"legal_name":   "Acme Corp Legal Entity",
				"jurisdiction": "US-CA",
			},
		},
		{
			name: "missing legal_name",
			body: map[string]interface{}{
				"merchant_name": "Acme Corporation",
				"jurisdiction":  "US-CA",
			},
		},
		{
			name: "missing jurisdiction",
			body: map[string]interface{}{
				"merchant_name": "Acme Corporation",
				"legal_name":    "Acme Corp Legal Entity",
			},
		},
		{
			name: "all fields missing",
			body: map[string]interface{}{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, rec := makeRequest(http.MethodPost, "/admin/api/v3/merchants", tc.body)
			handler.CreateMerchant(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "expected 400, got %d: %s", rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "INVALID_ARGUMENT", "error should contain INVALID_ARGUMENT")
		})
	}
}

func TestCreateMerchant_InvalidVaultKey(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3MerchantHandler(pool)
	body := map[string]interface{}{
		"merchant_name":            "Acme Corporation",
		"legal_name":               "Acme Corp Legal Entity",
		"jurisdiction":             "US-CA",
		"webhook_secret_vault_key": "plaintext-secret-key",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/merchants", body)

	handler.CreateMerchant(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "expected 400, got %d: %s", rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "vault://", "error should mention vault://")
	assert.Contains(t, rec.Body.String(), "INVALID_ARGUMENT", "error should contain INVALID_ARGUMENT")
}

func TestCreateMerchant_ValidVaultKey(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3MerchantHandler(pool)
	body := map[string]interface{}{
		"merchant_name":            "Valid Corp",
		"legal_name":               "Valid Corp Legal",
		"jurisdiction":             "SG",
		"webhook_secret_vault_key": "vault://secrets/test",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/merchants", body)

	handler.CreateMerchant(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code, "expected 201, got %d: %s", rec.Code, rec.Body.String())
}

func TestListMerchants(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3MerchantHandler(pool)

	merchant1 := map[string]interface{}{
		"merchant_name": "Merchant One",
		"legal_name":    "Merchant One Legal",
		"jurisdiction":  "US-NY",
	}
	req1, rec1 := makeRequest(http.MethodPost, "/admin/api/v3/merchants", merchant1)
	handler.CreateMerchant(rec1, req1)
	require.Equal(t, http.StatusCreated, rec1.Code, "setup merchant 1 failed: %s", rec1.Body.String())

	merchant2 := map[string]interface{}{
		"merchant_name":       "Merchant Two",
		"legal_name":          "Merchant Two Legal",
		"jurisdiction":        "GB-LND",
		"settlement_currency": "GBP",
	}
	req2, rec2 := makeRequest(http.MethodPost, "/admin/api/v3/merchants", merchant2)
	handler.CreateMerchant(rec2, req2)
	require.Equal(t, http.StatusCreated, rec2.Code, "setup merchant 2 failed: %s", rec2.Body.String())

	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/merchants", nil)
	handler.ListMerchants(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "expected 200, got %d: %s", rec.Code, rec.Body.String())

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	items, ok := resp["items"].([]interface{})
	require.True(t, ok, "response should contain items array")
	assert.GreaterOrEqual(t, len(items), 2, "should have at least 2 merchants")

	total, ok := resp["total"].(float64)
	require.True(t, ok, "response should contain total")
	assert.GreaterOrEqual(t, int(total), 2, "total should be at least 2")

	foundOne := false
	foundTwo := false
	for _, item := range items {
		i := item.(map[string]interface{})
		if i["merchant_name"] == "Merchant One" {
			foundOne = true
			assert.Equal(t, "US-NY", i["jurisdiction"])
		}
		if i["merchant_name"] == "Merchant Two" {
			foundTwo = true
			assert.Equal(t, "GB-LND", i["jurisdiction"])
		}
	}
	assert.True(t, foundOne, "should find Merchant One in list")
	assert.True(t, foundTwo, "should find Merchant Two in list")
}
