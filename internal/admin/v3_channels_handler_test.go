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

func TestV3ChannelsHandler_ListChannels(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3ChannelsHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/channels", nil)

	handler.ListChannels(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp, "items")
	assert.Contains(t, resp, "total")
}

func TestV3ChannelsHandler_CreateChannel_Validation(t *testing.T) {
	handler := NewV3ChannelsHandler(nil, nil)

	testCases := []struct {
		name string
		body map[string]interface{}
	}{
		{
			name: "missing provider_code",
			body: map[string]interface{}{
				"entity_id":            uuid.NewString(),
				"provider_tier":        "TIER_2_COMPLIANT",
				"channel_code":         "test-channel",
				"credential_vault_key": "vault-key-123",
			},
		},
		{
			name: "missing credential_vault_key",
			body: map[string]interface{}{
				"entity_id":     uuid.NewString(),
				"provider_tier": "TIER_2_COMPLIANT",
				"provider_code": "test-provider",
				"channel_code":  "test-channel",
			},
		},
		{
			name: "invalid provider_tier",
			body: map[string]interface{}{
				"entity_id":            uuid.NewString(),
				"provider_tier":        "INVALID_TIER",
				"provider_code":        "test-provider",
				"channel_code":         "test-channel",
				"credential_vault_key": "vault-key-123",
			},
		},
		{
			name: "invalid strategy_mode",
			body: map[string]interface{}{
				"entity_id":            uuid.NewString(),
				"provider_tier":        "TIER_2_COMPLIANT",
				"provider_code":        "test-provider",
				"channel_code":         "test-channel",
				"credential_vault_key": "vault-key-123",
				"strategy_mode":        "INVALID_MODE",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, rec := makeRequest(http.MethodPost, "/admin/api/v3/channels", tc.body)

			handler.CreateChannel(rec, req)

			assert.NotEqual(t, http.StatusOK, rec.Code)
		})
	}
}

func TestV3ChannelsHandler_CreateChannel_NoCredentialPlaintext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	tenantID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Entity V3 Acq', 'Entity V3 Acq LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	handler := NewV3ChannelsHandler(pool, nil)
	body := map[string]interface{}{
		"entity_id":            entityID.String(),
		"provider_tier":        "TIER_2_COMPLIANT",
		"provider_code":        "test-provider",
		"channel_code":         "test-channel",
		"credential_vault_key": "vault-key-123",
		"strategy_mode":        "DEFAULT",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/channels", body)

	handler.CreateChannel(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.NotContains(t, resp, "credential_vault_key")
	assert.NotContains(t, resp, "credential")
}

func TestV3ChannelsHandler_EnableDisableReload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	tenantID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Entity V3 Acq Ops', 'Entity V3 Acq Ops LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'test-provider', 'ops-channel', 'vault-key-123', 'DEFAULT')`,
		channelID, entityID)
	require.NoError(t, err)

	handler := NewV3ChannelsHandler(pool, nil)

	req, rec := makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/channels/"+channelID.String()+"/disable", nil, map[string]string{"id": channelID.String()})
	handler.DisableChannel(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	req, rec = makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/channels/"+channelID.String()+"/enable", nil, map[string]string{"id": channelID.String()})
	handler.EnableChannel(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	req, rec = makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/channels/"+channelID.String()+"/reload", nil, map[string]string{"id": channelID.String()})
	handler.ReloadChannel(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "reload_triggered", resp["status"])
}
