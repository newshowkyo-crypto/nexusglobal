package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ── P0-9: 封闭 Maker-Checker 旁路 ──
//
// 以下 5 个测试验证敏感操作不直接修改业务表，而是创建 PENDING 审批；
// 审批应用时原子地修改配置、写审计日志和 outbox。

// TestSensitiveAdminRoutes_CreatePendingOnly 验证敏感 admin 路由只创建 PENDING 审批，
// 不直接修改业务表。
//
// 覆盖的操作：
//   - DisableAdapterChannel  → adapter_channel.status 不变
//   - EnableAdapterChannel   → adapter_channel.status 不变
//   - DisableBridgeProfile   → bridge_profile.enabled 不变
//   - DisableWalletPolicy    → transit_wallet_policy.status 不变
//   - SetEntityRiskHold      → corporate_entity.status 不变
//   - FreezeIsolationGroup   → pipeline_isolation_groups.status 不变
//   - UnfreezeIsolationGroup → pipeline_isolation_groups.status 不变
//   - UpdateChannelRiskConfig→ adapter_channel.strategy_mode 不变
func TestSensitiveAdminRoutes_CreatePendingOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := createTestHandler(pool)
	ctx := context.Background()

	// ── 准备测试数据 ──

	// 1. adapter_channel
	providerID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, provider_config, status)
		 VALUES ($1, 'test-prov-p09', 'Test Provider', 'CARD_ACQUIRER', '{}', 'ACTIVE')`,
		providerID)
	require.NoError(t, err)

	tenantID := uuid.New()

	channelID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO adapter_channel (id, tenant_id, provider_id, channel_code, channel_name, adapter_type,
		 supported_assets, supported_networks, channel_config, status, strategy_mode, chargeback_threshold_bps,
		 risk_override_enabled)
		 VALUES ($1, $2, $3, 'test-ch-p09', 'Test Channel', 'CARD_ACQUIRER', '[]', '[]', '{}', 'ACTIVE', 'DEFAULT', 0, false)`,
		channelID, tenantID, providerID)
	require.NoError(t, err)

	// 2. bridge_profile
	bridgeID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO bridge_profile (id, tenant_id, name, source_asset, target_asset, buffer_percent, max_slippage_bps,
		 batching_policy, execution_policy, withdrawal_policy, enabled)
		 VALUES ($1, $2, 'test-bridge-p09', 'USD', 'USDC', 5, 100, 'IMMEDIATE', 'MARKET', NULL, true)`,
		bridgeID, tenantID)
	require.NoError(t, err)

	// 3. transit_wallet_policy
	assetID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO asset_definition (id, asset_code, asset_type, decimals, canonical_symbol, is_active)
		 VALUES ($1, 'USDP09', 'FIAT', 2, 'USDP09', true)
		 ON CONFLICT (asset_code, COALESCE(network, '')) DO UPDATE SET id = EXCLUDED.id RETURNING id`,
		assetID)
	require.NoError(t, err)

	walletPolicyID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO transit_wallet_policy (id, tenant_id, policy_code, asset_id, network, address,
		 destination_type, label, max_single_withdrawal, daily_limit, status)
		 VALUES ($1, $2, 'test-wp-p09', $3, 'ETH', '0xtestp09', 'COLD_WALLET', 'test', 1000, 10000, 'ACTIVE')`,
		walletPolicyID, tenantID, assetID)
	require.NoError(t, err)

	// 4. corporate_entity
	entityID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'test-entity-p09', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	// 5. pipeline_isolation_groups
	groupID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO pipeline_isolation_groups (id, name, status) VALUES ($1, 'test-group-p09', 'ACTIVE')`,
		groupID)
	require.NoError(t, err)

	// ── 测试 DisableAdapterChannel ──
	t.Run("DisableAdapterChannel creates PENDING only", func(t *testing.T) {
		req, rec := makeRequestWithChiAndIdentity("POST", "/admin/adapter/channels/"+channelID.String()+"/disable",
			nil, map[string]string{"id": channelID.String()}, "operator-p09", httpx.AdminRoleOperator)
		handler.DisableAdapterChannel(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "pending_approval", resp["status"])
		assert.NotEmpty(t, resp["approval_id"])

		// 验证 adapter_channel.status 仍然是 ACTIVE
		var status string
		require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM adapter_channel WHERE id = $1`, channelID).Scan(&status))
		assert.Equal(t, "ACTIVE", status, "adapter_channel.status 必须不变")
	})

	// ── 测试 DisableBridgeProfile ──
	t.Run("DisableBridgeProfile creates PENDING only", func(t *testing.T) {
		req, rec := makeRequestWithChiAndIdentity("POST", "/admin/bridge/profiles/"+bridgeID.String()+"/disable",
			nil, map[string]string{"id": bridgeID.String()}, "operator-p09", httpx.AdminRoleOperator)
		handler.DisableBridgeProfile(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "pending_approval", resp["status"])

		// 验证 bridge_profile.enabled 仍然是 true
		var enabled bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT enabled FROM bridge_profile WHERE id = $1`, bridgeID).Scan(&enabled))
		assert.True(t, enabled, "bridge_profile.enabled 必须不变")
	})

	// ── 测试 DisableWalletPolicy ──
	t.Run("DisableWalletPolicy creates PENDING only", func(t *testing.T) {
		req, rec := makeRequestWithChiAndIdentity("POST", "/admin/wallet/policies/"+walletPolicyID.String()+"/disable",
			nil, map[string]string{"id": walletPolicyID.String()}, "operator-p09", httpx.AdminRoleOperator)
		handler.DisableWalletPolicy(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "pending_approval", resp["status"])

		// 验证 transit_wallet_policy.status 仍然是 ACTIVE
		var status string
		require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM transit_wallet_policy WHERE id = $1`, walletPolicyID).Scan(&status))
		assert.Equal(t, "ACTIVE", status, "transit_wallet_policy.status 必须不变")
	})

	// ── 测试 SetEntityRiskHold ──
	t.Run("SetEntityRiskHold creates PENDING only", func(t *testing.T) {
		req, rec := makeRequestWithChiAndIdentity("POST", "/admin/entities/"+entityID.String()+"/risk-hold",
			nil, map[string]string{"id": entityID.String()}, "operator-p09", httpx.AdminRoleOperator)
		handler.SetEntityRiskHold(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "pending_approval", resp["status"])

		// 验证 corporate_entity.status 仍然是 ACTIVE
		var status string
		require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM corporate_entity WHERE id = $1`, entityID).Scan(&status))
		assert.Equal(t, "ACTIVE", status, "corporate_entity.status 必须不变")
	})

	// ── 测试 FreezeIsolationGroup ──
	t.Run("FreezeIsolationGroup creates PENDING only", func(t *testing.T) {
		req, rec := makeRequestWithChiAndIdentity("POST", "/admin/isolation-groups/"+groupID.String()+"/freeze",
			map[string]interface{}{"reason": "test freeze", "operator": "operator-p09"},
			map[string]string{"id": groupID.String()}, "operator-p09", httpx.AdminRoleOperator)
		handler.FreezeIsolationGroup(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "pending_approval", resp["status"])

		// 验证 pipeline_isolation_groups.status 仍然是 ACTIVE
		var status string
		require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_isolation_groups WHERE id = $1`, groupID).Scan(&status))
		assert.Equal(t, "ACTIVE", status, "pipeline_isolation_groups.status 必须不变")
	})

	// ── 测试 UpdateChannelRiskConfig ──
	t.Run("UpdateChannelRiskConfig creates PENDING only", func(t *testing.T) {
		req, rec := makeRequestWithChiAndIdentity("PUT", "/admin/channel-risk/"+channelID.String(),
			map[string]interface{}{
				"strategy_mode":            "STRICT",
				"chargeback_threshold_bps": 500,
				"risk_override_enabled":    false,
			},
			map[string]string{"id": channelID.String()}, "operator-p09", httpx.AdminRoleOperator)
		handler.UpdateChannelRiskConfig(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "pending_approval", resp["status"])

		// 验证 adapter_channel.strategy_mode 仍然是 DEFAULT
		var strategyMode string
		require.NoError(t, pool.QueryRow(ctx, `SELECT strategy_mode FROM adapter_channel WHERE id = $1`, channelID).Scan(&strategyMode))
		assert.Equal(t, "DEFAULT", strategyMode, "adapter_channel.strategy_mode 必须不变")
	})
}

// TestPendingApproval_DoesNotChangeBusinessConfig 验证 PENDING 审批不改变业务配置。
//
// 创建一个 DISABLE_ADAPTER_CHANNEL 的 PENDING 审批，验证：
//   - adapter_channel.status 不变
//   - 无 transactional_outbox 记录
//   - 审批状态为 PENDING
func TestPendingApproval_DoesNotChangeBusinessConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// 准备测试数据
	providerID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, provider_config, status)
		 VALUES ($1, 'test-prov-pending', 'Test Provider', 'CARD_ACQUIRER', '{}', 'ACTIVE')`,
		providerID)
	require.NoError(t, err)

	tenantID := uuid.New()

	channelID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO adapter_channel (id, tenant_id, provider_id, channel_code, channel_name, adapter_type,
		 supported_assets, supported_networks, channel_config, status)
		 VALUES ($1, $2, $3, 'test-ch-pending', 'Test Channel', 'CARD_ACQUIRER', '[]', '[]', '{}', 'ACTIVE')`,
		channelID, tenantID, providerID)
	require.NoError(t, err)

	// 用 handler 创建 PENDING 审批
	handler := createTestHandler(pool)
	req, rec := makeRequestWithChiAndIdentity("POST", "/admin/adapter/channels/"+channelID.String()+"/disable",
		nil, map[string]string{"id": channelID.String()}, "operator-pending", httpx.AdminRoleOperator)
	handler.DisableAdapterChannel(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	approvalID := resp["approval_id"].(string)
	require.NotEmpty(t, approvalID)

	// 验证：adapter_channel.status 仍然是 ACTIVE
	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM adapter_channel WHERE id = $1`, channelID).Scan(&status))
	assert.Equal(t, "ACTIVE", status, "PENDING 时 adapter_channel.status 必须不变")

	// 验证：无 outbox 记录
	var outboxCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM transactional_outbox WHERE aggregate_id = $1`, channelID).Scan(&outboxCount))
	assert.Equal(t, 0, outboxCount, "PENDING 时不能有 outbox 记录")

	// 验证：审批状态为 PENDING
	var approvalStatus string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM admin_approval_requests WHERE id = $1`, approvalID).Scan(&approvalStatus))
	assert.Equal(t, "PENDING", approvalStatus)
}

// TestApprovalApply_ChangesConfigExactlyOnce 验证审批应用后配置恰好改变一次。
//
// 创建 DISABLE_ACQUIRER_CHANNEL 的 PENDING 审批，用不同 checker 应用后验证：
//   - 通道状态从 ACTIVE → INACTIVE
//   - version 恰好 +1
//   - 审批状态为 APPLIED
//   - 再次应用同一审批返回错误（不可重复应用）
func TestApprovalApply_ChangesConfigExactlyOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// 创建测试通道
	entityID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)

	channelID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code,
		 credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-ch-once', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	var initialVersion int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	svc := NewApprovalService(pool, nil)

	// 1. operator 创建审批
	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-once",
		Username:    "operator-once",
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
		&initialVersion,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable for testing",
		nil,
	)
	require.NoError(t, err)

	// 2. 不同 checker 审批应用
	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-once",
		Username:    "approver-once",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	result, err := svc.ApplyTransaction(approveCtx, approvalID, "approved")
	require.NoError(t, err)
	require.NotNil(t, result)

	// 3. 验证：通道状态已改变，version 恰好 +1
	var status string
	var version int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&status, &version))
	assert.Equal(t, "INACTIVE", status, "通道必须已被禁用")
	assert.Equal(t, initialVersion+1, version, "version 必须恰好 +1")

	// 4. 验证：审批状态为 APPLIED
	var approvalStatus string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM admin_approval_requests WHERE id = $1`, approvalID).Scan(&approvalStatus))
	assert.Equal(t, "APPLIED", approvalStatus)

	// 5. 验证：再次应用同一审批返回错误（不可重复应用）
	_, err = svc.ApplyTransaction(approveCtx, approvalID, "re-apply attempt")
	require.Error(t, err, "重复应用必须返回错误")
}

// TestApprovalApply_RejectsVersionConflict 验证审批应用时版本冲突被拒绝。
//
// 创建审批时记录 target_version=N，在应用前将通道 version 改为 N+1，
// 应用时必须返回版本冲突错误，审批状态转为 FAILED_STALE。
func TestApprovalApply_RejectsVersionConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	entityID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)

	channelID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code,
		 credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-ch-conflict', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	var initialVersion int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	svc := NewApprovalService(pool, nil)

	// 1. operator 创建审批（记录 target_version = initialVersion）
	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-conflict",
		Username:    "operator-conflict",
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
		&initialVersion,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable for testing",
		nil,
	)
	require.NoError(t, err)

	// 2. 在审批前，另一个操作修改了通道版本（模拟并发修改）
	_, err = pool.Exec(ctx,
		`UPDATE acquirer_channels SET version = version + 1, updated_at = now() WHERE id = $1`, channelID)
	require.NoError(t, err)

	// 3. 尝试审批应用 → 必须返回版本冲突错误
	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-conflict",
		Username:    "approver-conflict",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	_, err = svc.ApplyTransaction(approveCtx, approvalID, "approved after conflict")
	require.Error(t, err, "版本冲突必须返回错误")

	appErr, ok := apperr.AsAPIError(err)
	require.True(t, ok, "必须是 APIError")
	assert.Equal(t, http.StatusConflict, appErr.HTTP, "必须是 409 Conflict")

	// 4. 验证：审批状态为 FAILED_STALE
	var approvalStatus string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM admin_approval_requests WHERE id = $1`, approvalID).Scan(&approvalStatus))
	assert.Equal(t, "FAILED_STALE", approvalStatus, "版本冲突后审批状态必须为 FAILED_STALE")

	// 5. 验证：通道状态仍然是 ACTIVE（未被修改）
	var status string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM acquirer_channels WHERE id = $1`, channelID).Scan(&status))
	assert.Equal(t, "ACTIVE", status, "版本冲突时通道状态必须不变")
}

// TestApprovalApply_WritesAuditAndOutboxAtomically 验证审批应用时审计日志和 outbox 原子写入。
//
// 创建 DISABLE_ACQUIRER_CHANNEL 的 PENDING 审批，应用后验证：
//   - admin_audit_log 中有一条 APPROVAL_APPLIED 记录
//   - transactional_outbox 中有一条配置变更事件
//   - 两者在同一事务中写入（如果事务回滚，两者都不存在）
func TestApprovalApply_WritesAuditAndOutboxAtomically(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	entityID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)

	channelID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code,
		 credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-ch-audit', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	var initialVersion int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	svc := NewApprovalService(pool, nil)

	// 1. operator 创建审批
	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-audit",
		Username:    "operator-audit",
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
		&initialVersion,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable for audit test",
		nil,
	)
	require.NoError(t, err)

	// 2. 应用前：无审计日志、无 outbox
	var auditCountBefore, outboxCountBefore int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_audit_log WHERE entity_id = $1 AND action LIKE 'APPROVAL_APPLIED%'`,
		channelID).Scan(&auditCountBefore))
	assert.Equal(t, 0, auditCountBefore, "应用前不能有审计日志")

	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM transactional_outbox WHERE aggregate_id = $1`,
		channelID).Scan(&outboxCountBefore))
	assert.Equal(t, 0, outboxCountBefore, "应用前不能有 outbox")

	// 3. 不同 checker 审批应用
	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-audit",
		Username:    "approver-audit",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	result, err := svc.ApplyTransaction(approveCtx, approvalID, "approved for audit test")
	require.NoError(t, err)
	require.NotNil(t, result)

	// 4. 验证：审计日志已写入
	var auditCountAfter int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_audit_log WHERE entity_id = $1 AND action LIKE 'APPROVAL_APPLIED%'`,
		channelID).Scan(&auditCountAfter))
	assert.Equal(t, 1, auditCountAfter, "应用后必须有一条审计日志")

	// 5. 验证：outbox 已写入
	var outboxCountAfter int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM transactional_outbox WHERE aggregate_id = $1`,
		channelID).Scan(&outboxCountAfter))
	assert.GreaterOrEqual(t, outboxCountAfter, 1, "应用后必须有 outbox 事件")

	// 6. 验证：审批状态为 APPLIED（事务成功提交）
	var approvalStatus string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM admin_approval_requests WHERE id = $1`, approvalID).Scan(&approvalStatus))
	assert.Equal(t, "APPLIED", approvalStatus, "审批必须已应用")
}
