package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
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

func Test_AdminAPI_FormInconsistency_Rejection(t *testing.T) {
	handler := NewV3ChannelsHandler(nil, nil)
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/channels", map[string]interface{}{
		"entity_id":            uuid.NewString(),
		"provider_tier":        "TIER_1_HIGH_RISK",
		"provider_code":        "generic-acquirer",
		"channel_code":         "high-risk-channel",
		"credential_vault_key": "vault://acquirer/high-risk",
		"strategy_mode":        "DEFAULT",
		"funding_mode":         "FIAT_DIRECT",
		"tenant_id":            uuid.NewString(),
		"merchant_id":          uuid.NewString(),
	})

	handler.CreateChannel(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "CONFIG_INCONSISTENT")
	// G11 (Task 3): configvalidator 使用更严格的错误消息
	assert.Contains(t, rec.Body.String(), "FIAT_DIRECT")
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

	// P0-2: CreateChannel 要求商户已有 ACTIVE merchant_routing_policy
	_, err = pool.Exec(context.Background(),
		`INSERT INTO merchant_routing_policy (merchant_id, provider_tier, status) VALUES ($1, 'TIER_2_COMPLIANT', 'ACTIVE')`,
		entityID)
	require.NoError(t, err)
	// CreateChannel 通过 provider_code 解析 provider（fail-closed）
	_, err = pool.Exec(context.Background(),
		`INSERT INTO payment_provider (id, provider_code, provider_name, provider_type, is_active)
		 VALUES ($1, 'test-provider', 'Test Provider', 'LP', true)`,
		uuid.New())
	require.NoError(t, err)

	handler := NewV3ChannelsHandler(pool, nil)
	body := map[string]interface{}{
		"entity_id":            entityID.String(),
		"provider_tier":        "TIER_2_COMPLIANT",
		"provider_code":        "test-provider",
		"channel_code":         "test-channel",
		"credential_vault_key": "vault://acquirer/test-provider",
		"strategy_mode":        "DEFAULT",
		// P0-2: CreateChannel 必须提供 tenant_id 与 merchant_id
		"tenant_id":   tenantID.String(),
		"merchant_id": entityID.String(),
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
	// P0-3: reload/disable/enable 的 apply 路径要求 runtime binding（fail-closed）
	seedRuntimeChannelBinding(t, pool, channelID, entityID)
	_, err = pool.Exec(context.Background(), `UPDATE tenant_acquirer_channel SET tenant_id=$1 WHERE acquirer_channel_id=$2`, tenantID, channelID)
	require.NoError(t, err)

	handler := NewV3ChannelsHandler(pool, nil)

	// G08: DisableChannel/EnableChannel 现在创建 PENDING 审批请求（不直接执行）
	// 需要注入 AdminIdentity Context
	req, rec := makeRequestWithChiAndIdentity(http.MethodPost,
		"/admin/api/v3/channels/"+channelID.String()+"/disable",
		nil, map[string]string{"id": channelID.String()},
		"operator-1", httpx.AdminRoleOperator)
	handler.DisableChannel(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var disableResp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&disableResp))
	assert.Equal(t, "PENDING", disableResp["status"], "G08: disable must create PENDING approval")

	// 验证通道状态不变（PENDING 不改配置）
	var channelStatus string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM acquirer_channels WHERE id = $1`, channelID).Scan(&channelStatus))
	assert.Equal(t, "ACTIVE", channelStatus, "G08: PENDING must not change config")

	req, rec = makeRequestWithChiAndIdentity(http.MethodPost,
		"/admin/api/v3/channels/"+channelID.String()+"/enable",
		nil, map[string]string{"id": channelID.String()},
		"operator-1", httpx.AdminRoleOperator)
	handler.EnableChannel(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Reload 仍然直接执行（非高危操作）
	req, rec = makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/channels/"+channelID.String()+"/reload", nil, map[string]string{"id": channelID.String()})
	req = req.WithContext(context.WithValue(req.Context(), httpx.AdminContextKey{}, &httpx.AdminIdentity{TenantID: tenantID.String()}))
	handler.ReloadChannel(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "reload_triggered", resp["status"])
}

func TestV3ChannelsHandler_ListChannels_NoCredentialEcho(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	tenantID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Entity V3 Secret', 'Entity V3 Secret LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (
			id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, provider_config, routing_config
		 ) VALUES ($1, $2, 'TIER_2_COMPLIANT', 'test-provider', 'secret-channel', 'vault://top-secret', 'DEFAULT', $3::jsonb, $4::jsonb)`,
		channelID,
		entityID,
		`{"api_key":"plain-api-key","nested":{"webhook_secret":"plain-webhook-secret"},"region":"us"}`,
		`{"kms_key":"plain-kms-key","priority":1}`,
	)
	require.NoError(t, err)

	handler := NewV3ChannelsHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/channels", nil)
	handler.ListChannels(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, "credential_vault_key")
	assert.NotContains(t, body, "vault://top-secret")
	assert.NotContains(t, body, "plain-api-key")
	assert.NotContains(t, body, "plain-webhook-secret")
	assert.NotContains(t, body, "plain-kms-key")
	// A legacy textual reference alone does not establish a stored channel credential.
	assert.Contains(t, body, `"has_credential":false`)
	// P0-5: SanitizeMap 递归脱敏统一使用 <redacted> 标记（JSON 序列化为 \u003c 转义）
	assert.Contains(t, body, `api_key`+"\":\""+`\u003credacted\u003e"`)
	assert.Contains(t, body, `webhook_secret`+"\":\""+`\u003credacted\u003e"`)
	assert.Contains(t, body, `kms_key`+"\":\""+`\u003credacted\u003e"`)
}

// TestApprovalDisable_AlsoDisablesRuntimeChannel 验证 applyChannelStatus 在更新
// acquirer_channels.status 后也更新了 tenant_acquirer_channel.is_active。
func TestApprovalDisable_AlsoDisablesRuntimeChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	tenantID := uuid.New()
	runtimeChannelID := uuid.New()
	providerID := uuid.New()

	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO payment_provider (id, provider_code, provider_name, provider_type, is_active)
		 VALUES ($1, 'provider-a', 'Provider A', 'LP', true)`,
		providerID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO tenant_acquirer_channel (id, tenant_id, merchant_id, channel_code, provider_id, is_active, acquirer_channel_id)
		 VALUES ($1, $2, $3, 'test-channel', $4, true, $5)`,
		runtimeChannelID, tenantID, entityID, providerID, channelID)
	require.NoError(t, err)

	svc := NewApprovalService(pool, nil)

	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	createCtx = context.WithValue(createCtx, httpx.AdminContextKey{}, operatorIdentity)

	targetIDStr := channelID.String()
	approvalID, err := svc.CreateApprovalRequest(
		createCtx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		nil,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable for runtime sync test",
		nil,
	)
	require.NoError(t, err)

	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	result, err := svc.ApplyTransaction(approveCtx, approvalID, "approved")
	require.NoError(t, err)
	require.NotNil(t, result)

	// 验证 runtime channel is_active 已同步更新为 false
	var runtimeIsActive bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT is_active FROM tenant_acquirer_channel WHERE id = $1`, runtimeChannelID).Scan(&runtimeIsActive))
	assert.False(t, runtimeIsActive, "runtime channel is_active must be synced to false after disable")

	// 验证 result.TargetID 是审批目标（控制面通道 ID）——outbox/audit 的 aggregate
	// 必须与 target_id 一致，审计才能按 target 追溯；runtime 同步仅为副作用
	assert.Equal(t, channelID.String(), result.TargetID, "TargetID must be the control-plane channel ID (approval target)")
}

// TestReloadPublishesRuntimeChannelID 验证 V3 ReloadChannel 发布的是 runtime channel ID 而非 control channel ID。
func TestReloadPublishesRuntimeChannelID(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	tenantID := uuid.New()
	merchantID := uuid.New()
	runtimeChannelID := uuid.New()
	providerID := uuid.New()

	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO payment_provider (id, provider_code, provider_name, provider_type, is_active)
		 VALUES ($1, 'provider-a', 'Provider A', 'LP', true)`,
		providerID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO tenant_acquirer_channel (id, tenant_id, merchant_id, channel_code, provider_id, is_active, acquirer_channel_id)
		 VALUES ($1, $2, $3, 'test-channel', $4, true, $5)`,
		runtimeChannelID, tenantID, merchantID, providerID, channelID)
	require.NoError(t, err)

	handler := NewV3ChannelsHandler(pool, nil)

	req, rec := makeRequestWithChiCtx(http.MethodPost,
		"/admin/api/v3/channels/"+channelID.String()+"/reload",
		nil, map[string]string{"id": channelID.String()})
	req = req.WithContext(context.WithValue(req.Context(), httpx.AdminContextKey{}, &httpx.AdminIdentity{TenantID: tenantID.String()}))
	handler.ReloadChannel(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "reload_triggered", resp["status"])
	// 验证返回的是 runtime channel ID 而非 control channel ID
	assert.Equal(t, runtimeChannelID.String(), resp["runtime_channel_id"], "must publish runtime channel ID")
	assert.NotEqual(t, channelID.String(), resp["runtime_channel_id"], "must not be control channel ID")
}

// TestCreateChannel_RollsBackWhenRuntimeBindingFails 验证 CreateChannel 在 runtime binding 失败时回滚
// （acquirer_channels 记录也被回滚）。
func TestCreateChannel_RollsBackWhenRuntimeBindingFails(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	tenantID := uuid.New()
	merchantID := uuid.New()

	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	handler := NewV3ChannelsHandler(pool, nil)

	// 使用不存在的 provider_code，使 provider_id 查询失败，触发回滚
	body := map[string]interface{}{
		"entity_id":            entityID.String(),
		"provider_tier":        "TIER_2_COMPLIANT",
		"provider_code":        "nonexistent-provider-code",
		"channel_code":         "test-channel-rollback",
		"credential_vault_key": "vault-key-123",
		"strategy_mode":        "DEFAULT",
		"tenant_id":            tenantID.String(),
		"merchant_id":          merchantID.String(),
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/channels", body)
	handler.CreateChannel(rec, req)

	// 应该返回错误（provider 不存在）
	assert.NotEqual(t, http.StatusOK, rec.Code)

	// 验证 acquirer_channels 记录也被回滚（不存在）
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM acquirer_channels WHERE channel_code = 'test-channel-rollback'`).Scan(&count))
	assert.Equal(t, 0, count, "acquirer_channels record must be rolled back when runtime binding fails")
}

// TestRuntimeBinding_IsUnique 验证迁移文件 000045 中有 partial UNIQUE 约束。
func TestRuntimeBinding_IsUnique(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// 验证 partial UNIQUE 索引存在
	var indexExists bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE indexname = 'uq_tenant_acquirer_channel_acquirer_channel_id'
		)`).Scan(&indexExists)
	require.NoError(t, err)
	assert.True(t, indexExists, "partial UNIQUE index on acquirer_channel_id must exist (migration 000045)")

	// 验证索引定义包含 WHERE acquirer_channel_id IS NOT NULL（partial unique）
	var indexDef string
	err = pool.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes
		 WHERE indexname = 'uq_tenant_acquirer_channel_acquirer_channel_id'`).Scan(&indexDef)
	require.NoError(t, err)
	assert.Contains(t, indexDef, "WHERE", "index must be partial (WHERE acquirer_channel_id IS NOT NULL)")
}
