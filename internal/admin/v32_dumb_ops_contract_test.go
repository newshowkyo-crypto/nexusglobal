package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func Test_ScenarioMacro_HighRisk_Enforcement(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	entityID := uuid.New()
	tenantID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'V32 High Risk Entity', 'V32 High Risk Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	// P0-2: CreateChannel 要求商户已有 ACTIVE merchant_routing_policy（tier 匹配）
	_, err = pool.Exec(ctx,
		`INSERT INTO merchant_routing_policy (merchant_id, provider_tier, status) VALUES ($1, 'TIER_1_HIGH_RISK', 'ACTIVE')`,
		entityID)
	require.NoError(t, err)
	// CreateChannel 通过 provider_code 解析 provider（fail-closed）
	_, err = pool.Exec(ctx,
		`INSERT INTO payment_provider (id, provider_code, provider_name, provider_type, is_active)
		 VALUES ($1, 'generic-acquirer', 'Generic Acquirer', 'LP', true)`,
		uuid.New())
	require.NoError(t, err)

	channelHandler := NewV3ChannelsHandler(pool, nil)
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/channels", map[string]interface{}{
		"entity_id":            entityID.String(),
		"provider_tier":        "TIER_1_HIGH_RISK",
		"provider_code":        "generic-acquirer",
		"channel_code":         "high-risk-macro-channel",
		"credential_vault_key": "vault://acquirer/high-risk-macro",
		"strategy_mode":        "BURN_MODE",
		"funding_mode":         "CRYPTO_DESENSITIZED",
		"tenant_id":            tenantID.String(),
		"merchant_id":          entityID.String(),
		"provider_config":      map[string]interface{}{},
		"routing_config": map[string]interface{}{
			"vcc_funding_mode":       "CRYPTO_DESENSITIZED",
			"manual_review_required": true,
		},
	})
	channelHandler.CreateChannel(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var strategyMode, providerTier, routingFundingMode string
	err = pool.QueryRow(ctx,
		`SELECT provider_tier, strategy_mode, routing_config->>'vcc_funding_mode'
		 FROM acquirer_channels WHERE channel_code = 'high-risk-macro-channel'`,
	).Scan(&providerTier, &strategyMode, &routingFundingMode)
	require.NoError(t, err)
	assert.Equal(t, "TIER_1_HIGH_RISK", providerTier)
	assert.Equal(t, "BURN_MODE", strategyMode)
	assert.Equal(t, "CRYPTO_DESENSITIZED", routingFundingMode)

	issuingHandler := NewV3IssuingPoolsHandler(pool, nil)
	req, rec = makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", map[string]interface{}{
		"entity_id":            entityID.String(),
		"issuing_tier":         "TIER_1_AGGREGATOR",
		"funding_mode":         "CRYPTO_DESENSITIZED",
		"provider_code":        "generic-issuer",
		"pool_code":            "high-risk-macro-pool",
		"credential_vault_key": "vault://issuing/high-risk-macro",
		"provider_config":      map[string]interface{}{},
	})
	issuingHandler.CreatePool(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var declineLimit int
	err = pool.QueryRow(ctx,
		`SELECT COALESCE((velocity_config->>'declined_count_sliding_window')::int, -1)
		 FROM issuing_providers WHERE pool_code = 'high-risk-macro-pool'`,
	).Scan(&declineLimit)
	require.NoError(t, err)
	assert.Equal(t, 5, declineLimit, "high-risk scenario macro must default declined_count_sliding_window to the strict limit")

	var ledgerSubjects []string
	rows, err := pool.Query(ctx, `
		SELECT account_type FROM ledger_account WHERE merchant_id = $1
		UNION
		SELECT key FROM jsonb_object_keys((SELECT provider_config FROM issuing_providers WHERE pool_code = 'high-risk-macro-pool')) AS key
	`, entityID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var subject string
		require.NoError(t, rows.Scan(&subject))
		ledgerSubjects = append(ledgerSubjects, subject)
	}
	assert.Contains(t, ledgerSubjects, "ACQUIRING_SETTLEMENT_BALANCE", "high-risk macro must initialize the debit settlement subject")
	assert.Contains(t, ledgerSubjects, "VCC_AVAILABLE_BALANCE", "high-risk macro must initialize the credit VCC available subject")
}

func Test_MakerChecker_Pending_Isolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	rdb, redisCleanup := setupContractRedis(t)
	defer redisCleanup()
	pubsub := rdb.PSubscribe(ctx, "*")
	defer pubsub.Close()
	_, err := pubsub.ReceiveTimeout(ctx, 5*time.Second)
	require.NoError(t, err)

	merchantID := uuid.New()
	tenantID := uuid.New()
	profileID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'V32 Merchant', 'V32 Merchant LLC', 'US', 'US-DE', 'ACTIVE')`,
		merchantID, tenantID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		`INSERT INTO merchant_risk_profiles (
			id, merchant_id, rolling_reserve_rate_bps, dispute_reserve_rate_bps, reserve_release_days, status
		 ) VALUES ($1, $2, 1000, 500, 30, 'ACTIVE')`,
		profileID, merchantID)
	require.NoError(t, err)

	handler := NewApprovalHandler(pool, rdb)
	req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/approvals", map[string]interface{}{
		"action_type": "UPDATE_MERCHANT_RESERVE",
		"requester":   "operator_a",
		"reason":      "request to lower reserve from 10 percent to 1 percent",
		"requested_payload": map[string]interface{}{
			"merchant_id":                   merchantID.String(),
			"from_rolling_reserve_rate_bps": 1000,
			"to_rolling_reserve_rate_bps":   100,
			"from_dispute_reserve_rate_bps": 500,
			"to_dispute_reserve_rate_bps":   100,
			"from_reserve_release_days":     30,
			"to_reserve_release_days":       7,
		},
	}, httpx.AdminRoleOperator)
	handler.Create(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var profileRollingBps, profileDisputeBps, profileReleaseDays int
	err = pool.QueryRow(ctx,
		`SELECT rolling_reserve_rate_bps, dispute_reserve_rate_bps, reserve_release_days
		 FROM merchant_risk_profiles WHERE id = $1`, profileID,
	).Scan(&profileRollingBps, &profileDisputeBps, &profileReleaseDays)
	require.NoError(t, err)
	assert.Equal(t, 1000, profileRollingBps)
	assert.Equal(t, 500, profileDisputeBps)
	assert.Equal(t, 30, profileReleaseDays)

	var pendingCount int
	var payloadBytes []byte
	err = pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(max(requested_payload::text), '{}')
		 FROM admin_approval_requests
		 WHERE action_type = 'UPDATE_MERCHANT_RESERVE' AND status = 'PENDING'`,
	).Scan(&pendingCount, &payloadBytes)
	require.NoError(t, err)
	assert.Equal(t, 1, pendingCount)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(payloadBytes, &payload))
	assert.Equal(t, merchantID.String(), payload["merchant_id"])
	assert.Equal(t, float64(100), payload["to_rolling_reserve_rate_bps"])

	var auditCount int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_override_audit_log
		 WHERE entity_type = 'MERCHANT_RISK_PROFILE' OR override_type = 'UPDATE_MERCHANT_RESERVE'`,
	).Scan(&auditCount)
	require.NoError(t, err)
	assert.Equal(t, 0, auditCount, "pending maker-checker requests must not create effective override audit entries")

	_, err = pubsub.ReceiveTimeout(ctx, 200*time.Millisecond)
	assert.Error(t, err, "pending maker-checker requests must not broadcast redis reload before approval")
}

func setupContractRedis(t *testing.T) (*redis.Client, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	ctx := context.Background()
	// OPS-007: 外部共享 Redis 优先（TEST_REDIS_ADDR），testcontainers 兜底。
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		require.NoError(t, rdb.Ping(ctx).Err(), "ping external TEST_REDIS_ADDR")
		return rdb, func() { _ = rdb.Close() }
	}

	req := testcontainers.ContainerRequest{
		Image:        "redis:7",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{Addr: host + ":" + port.Port()})
	require.NoError(t, client.Ping(ctx).Err())

	return client, func() {
		_ = client.Close()
		_ = container.Terminate(ctx)
	}
}
