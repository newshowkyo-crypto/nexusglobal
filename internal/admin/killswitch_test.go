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

func TestKillSwitchHandler_Lockdown_DisablesChannelsAndFreezesGroups(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	tenantID := uuid.New()
	activeChannelID := uuid.New()
	inactiveChannelID := uuid.New()
	groupID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Lockdown Entity', 'Lockdown Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES
		 ($1, $3, 'TIER_2_COMPLIANT', 'provider-a', 'active-channel', 'vault-a', 'DEFAULT', 'ACTIVE'),
		 ($2, $3, 'TIER_2_COMPLIANT', 'provider-b', 'inactive-channel', 'vault-b', 'DEFAULT', 'INACTIVE')`,
		activeChannelID, inactiveChannelID, entityID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO pipeline_isolation_groups (id, name, status)
		 VALUES ($1, 'lockdown-group', 'ACTIVE')`,
		groupID)
	require.NoError(t, err)

	handler := NewKillSwitchHandler(pool, nil)
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/system/lockdown", map[string]interface{}{
		"operator": "admin",
		"reason":   "staging drill",
	})
	handler.Lockdown(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, float64(1), resp["disabled_channels"])
	assert.Equal(t, float64(1), resp["risk_hold_groups"])

	var activeStatus, inactiveStatus, groupStatus, frozenReason, frozenBy string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM acquirer_channels WHERE id = $1`, activeChannelID).Scan(&activeStatus))
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM acquirer_channels WHERE id = $1`, inactiveChannelID).Scan(&inactiveStatus))
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status, frozen_reason, frozen_by FROM pipeline_isolation_groups WHERE id = $1`, groupID).Scan(&groupStatus, &frozenReason, &frozenBy))
	assert.Equal(t, "DISABLED", activeStatus)
	assert.Equal(t, "INACTIVE", inactiveStatus)
	assert.Equal(t, "RISK_HOLD", groupStatus)
	assert.Equal(t, "staging drill", frozenReason)
	assert.Equal(t, "admin", frozenBy)

	var auditCount int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM admin_override_audit_log WHERE override_type = 'SYSTEM_LOCKDOWN'`).Scan(&auditCount))
	assert.Equal(t, 1, auditCount)
}
