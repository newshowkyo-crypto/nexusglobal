package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/configvalidator"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// R23 Item 3: 真实 Approval 行为测试
// 使用脚本化 pgx.Tx 直接调用真实 validateIssuingActivationTx / applyPoolStatus / applyPoolUpdate
// 禁止只测枚举/setter。每个失败场景断言没有执行 UPDATE issuing_providers。
// ============================================================================

// ── Fake pgx.Tx 实现 ──

// scriptedRow 是一次 QueryRow 调用的可编程返回结果。
type scriptedRow struct {
	scanFn func(dest ...any) error
}

// scriptedTx 是 pgx.Tx 的脚本化 fake，按调用顺序返回预编程结果。
type scriptedTx struct {
	queryRowResults []scriptedRow // 按 QueryRow 调用顺序排列的结果
	queryRowIdx     int
	queryRowSQLs    []string // 记录所有 QueryRow 调用的 SQL
	execCalls       []string // 记录所有 Exec 调用的 SQL
	execTag         string   // Exec 返回的 CommandTag 文本（空 = "UPDATE 1"）
	commitCalls     int
	rollbackCalls   int
	commitErr       error
}

func (t *scriptedTx) Commit(ctx context.Context) error {
	t.commitCalls++
	return t.commitErr
}
func (t *scriptedTx) Rollback(ctx context.Context) error {
	t.rollbackCalls++
	return nil
}
func (t *scriptedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	t.queryRowSQLs = append(t.queryRowSQLs, sql)
	if t.queryRowIdx < len(t.queryRowResults) {
		result := t.queryRowResults[t.queryRowIdx]
		t.queryRowIdx++
		return &scriptedRowImpl{scanFn: result.scanFn}
	}
	return &scriptedRowImpl{scanFn: func(dest ...any) error { return pgx.ErrNoRows }}
}
func (t *scriptedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.execCalls = append(t.execCalls, sql)
	tag := t.execTag
	if tag == "" {
		tag = "UPDATE 1"
	}
	return pgconn.NewCommandTag(tag), nil
}
func (t *scriptedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (t *scriptedTx) Begin(ctx context.Context) (pgx.Tx, error) { return t, nil }
func (t *scriptedTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (t *scriptedTx) Conn() *pgx.Conn { return nil }
func (t *scriptedTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (t *scriptedTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { return nil }
func (t *scriptedTx) LargeObjects() pgx.LargeObjects                               { return pgx.LargeObjects{} }

// scriptedRowImpl 实现 pgx.Row
type scriptedRowImpl struct {
	scanFn func(dest ...any) error
}

func (r *scriptedRowImpl) Scan(dest ...any) error {
	return r.scanFn(dest...)
}

// ── 辅助函数 ──

// makeEntityScanFn 创建返回 entity tenant_id 的 Scan 函数
func makeEntityScanFn(tenantID *uuid.UUID) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) > 0 {
			ptr, ok := dest[0].(**uuid.UUID)
			if ok {
				*ptr = tenantID
			}
		}
		return nil
	}
}

// makeEntityScanErr 创建返回错误的 Scan 函数
func makeEntityScanErr() func(dest ...any) error {
	return func(dest ...any) error {
		return pgx.ErrNoRows
	}
}

// makeAdapterScanFn 创建返回 adapter_provider status/adapter_type 的 Scan 函数
func makeAdapterScanFn(status, adapterType string) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) >= 2 {
			if s, ok := dest[0].(*string); ok {
				*s = status
			}
			if a, ok := dest[1].(*string); ok {
				*a = adapterType
			}
		}
		return nil
	}
}

// makeVaultScanFn 创建返回 vault 记录的 Scan 函数
func makeVaultScanFn(
	recordID *uuid.UUID,
	status string,
	credentialType string,
	tenantID *uuid.UUID,
	issuingProviderID *uuid.UUID,
	providerID *uuid.UUID,
	expiresAt *time.Time,
	keyVersion int,
) func(dest ...any) error {
	return func(dest ...any) error {
		// SELECT id, status, credential_type, tenant_id, issuing_provider_id, provider_id, expires_at, key_version
		if len(dest) >= 8 {
			if v, ok := dest[0].(**uuid.UUID); ok {
				*v = recordID
			}
			if v, ok := dest[1].(*string); ok {
				*v = status
			}
			if v, ok := dest[2].(*string); ok {
				*v = credentialType
			}
			if v, ok := dest[3].(**uuid.UUID); ok {
				*v = tenantID
			}
			if v, ok := dest[4].(**uuid.UUID); ok {
				*v = issuingProviderID
			}
			if v, ok := dest[5].(**uuid.UUID); ok {
				*v = providerID
			}
			if v, ok := dest[6].(**time.Time); ok {
				*v = expiresAt
			}
			if v, ok := dest[7].(*int); ok {
				*v = keyVersion
			}
		}
		return nil
	}
}

// makeVaultScanErr 创建返回 vault 查询错误的 Scan 函数
func makeVaultScanErr() func(dest ...any) error {
	return func(dest ...any) error {
		return pgx.ErrNoRows
	}
}

// makePoolStatusScanFn 创建返回 issuing_providers 行的 Scan 函数（applyPoolStatus 用）
func makePoolStatusScanFn(
	tier, funding, providerCode, poolCode, credKey string,
	providerConfig, velocityConfig []byte,
	version int,
	adapterType string,
	adapterProviderID *string,
	entityID *uuid.UUID,
) func(dest ...any) error {
	return func(dest ...any) error {
		// SELECT issuing_tier, funding_mode, provider_code, pool_code,
		//        credential_vault_key, provider_config::bytea, velocity_config::bytea, version,
		//        adapter_type::text, adapter_provider_id::text, entity_id
		if len(dest) >= 11 {
			if v, ok := dest[0].(*string); ok {
				*v = tier
			}
			if v, ok := dest[1].(*string); ok {
				*v = funding
			}
			if v, ok := dest[2].(*string); ok {
				*v = providerCode
			}
			if v, ok := dest[3].(*string); ok {
				*v = poolCode
			}
			if v, ok := dest[4].(*string); ok {
				*v = credKey
			}
			if v, ok := dest[5].(*[]byte); ok {
				*v = providerConfig
			}
			if v, ok := dest[6].(*[]byte); ok {
				*v = velocityConfig
			}
			if v, ok := dest[7].(*int); ok {
				*v = version
			}
			if v, ok := dest[8].(*string); ok {
				*v = adapterType
			}
			if v, ok := dest[9].(**string); ok {
				*v = adapterProviderID
			}
			if v, ok := dest[10].(**uuid.UUID); ok {
				*v = entityID
			}
		}
		return nil
	}
}

// nexusProviderConfig 是 NEXUS_ISSUING_HTTP_V1 测试用的 provider_config JSON
var nexusProviderConfig = []byte(`{"base_url":"https://api.example.com"}`)

// makePoolUpdateScanFn 创建返回 issuing_providers 行的 Scan 函数（applyPoolUpdate 用）
func makePoolUpdateScanFn(
	tier, funding, providerCode, poolCode, credKey, status string,
	providerConfig, velocityConfig []byte,
	version int,
	adapterType string,
	adapterProviderID *string,
	entityID *uuid.UUID,
) func(dest ...any) error {
	return func(dest ...any) error {
		// SELECT issuing_tier, funding_mode, provider_code, pool_code,
		//        credential_vault_key, provider_config::bytea, velocity_config::bytea, version,
		//        adapter_type::text, adapter_provider_id::text, entity_id, status
		if len(dest) >= 12 {
			if v, ok := dest[0].(*string); ok {
				*v = tier
			}
			if v, ok := dest[1].(*string); ok {
				*v = funding
			}
			if v, ok := dest[2].(*string); ok {
				*v = providerCode
			}
			if v, ok := dest[3].(*string); ok {
				*v = poolCode
			}
			if v, ok := dest[4].(*string); ok {
				*v = credKey
			}
			if v, ok := dest[5].(*[]byte); ok {
				*v = providerConfig
			}
			if v, ok := dest[6].(*[]byte); ok {
				*v = velocityConfig
			}
			if v, ok := dest[7].(*int); ok {
				*v = version
			}
			if v, ok := dest[8].(*string); ok {
				*v = adapterType
			}
			if v, ok := dest[9].(**string); ok {
				*v = adapterProviderID
			}
			if v, ok := dest[10].(**uuid.UUID); ok {
				*v = entityID
			}
			if v, ok := dest[11].(*string); ok {
				*v = status
			}
		}
		return nil
	}
}

// makeUpdateResultScanFn 创建返回 UPDATE RETURNING version 的 Scan 函数
func makeUpdateResultScanFn(newVersion int) func(dest ...any) error {
	return func(dest ...any) error {
		if v, ok := dest[0].(*int); ok {
			*v = newVersion
		}
		return nil
	}
}

// hasUpdateIssuingProviders 检查 QueryRow/Exec SQL 中是否有 UPDATE issuing_providers
func hasUpdateIssuingProviders(tx *scriptedTx) bool {
	for _, sql := range tx.execCalls {
		if strings.Contains(sql, "UPDATE issuing_providers") {
			return true
		}
	}
	for _, sql := range tx.queryRowSQLs {
		if strings.Contains(sql, "UPDATE issuing_providers") {
			return true
		}
	}
	return false
}

// hasInsertAuditLog 检查是否有 INSERT admin_audit_log（或 admin_override_audit_log）
func hasInsertAuditLog(tx *scriptedTx) bool {
	for _, sql := range tx.execCalls {
		if strings.Contains(sql, "INSERT INTO admin_audit_log") || strings.Contains(sql, "INSERT INTO admin_override_audit_log") {
			return true
		}
	}
	return false
}

// hasInsertOutbox 检查是否有 INSERT transactional_outbox
func hasInsertOutbox(tx *scriptedTx) bool {
	for _, sql := range tx.execCalls {
		if strings.Contains(sql, "INSERT INTO transactional_outbox") {
			return true
		}
	}
	return false
}

// assertNoSideEffects 断言失败场景没有执行任何业务写入（UPDATE/INSERT）
func assertNoSideEffects(t *testing.T, tx *scriptedTx, context string) {
	t.Helper()
	assert.False(t, hasUpdateIssuingProviders(tx),
		"%s: UPDATE issuing_providers must NOT be executed", context)
	assert.False(t, hasInsertAuditLog(tx),
		"%s: INSERT admin_audit_log must NOT be executed", context)
	assert.False(t, hasInsertOutbox(tx),
		"%s: INSERT transactional_outbox must NOT be executed", context)
}

// ── validateIssuingActivationTx 行为测试 ──

// TestValidateIssuingActivationTx_CrossTenantMismatch 验证跨 tenant 时校验失败。
// 直接调用真实 validateIssuingActivationTx，断言返回错误。
func TestValidateIssuingActivationTx_CrossTenantMismatch(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	entityTenantID := uuid.New()
	pathTenantID := uuid.New() // 不同于 entityTenantID
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + pathTenantID.String() + "/" + recordID.String() + "/api_key/1"

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			// 1. entity tenant_id 查询
			{scanFn: makeEntityScanFn(&entityTenantID)},
			// 2. adapter_provider 查询
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			// 3. vault 查询
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &pathTenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	err := svc.validateIssuingActivationTx(context.Background(), tx, poolUUID, &entityID, adapterProviderID.String(), vaultKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant")
}

// TestValidateIssuingActivationTx_RecordVersionMismatch 验证 record/version 不匹配时校验失败。
func TestValidateIssuingActivationTx_RecordVersionMismatch(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	differentRecordID := uuid.New() // vault 返回的 recordID 与 path 不同

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			{scanFn: makeVaultScanFn(&differentRecordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	err := svc.validateIssuingActivationTx(context.Background(), tx, poolUUID, &entityID, adapterProviderID.String(), vaultKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "record_id")
}

// TestValidateIssuingActivationTx_RevokedVault 验证 REVOKED vault 时校验失败。
func TestValidateIssuingActivationTx_RevokedVault(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			{scanFn: makeVaultScanFn(&recordID, "REVOKED", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	err := svc.validateIssuingActivationTx(context.Background(), tx, poolUUID, &entityID, adapterProviderID.String(), vaultKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ACTIVE")
}

// TestValidateIssuingActivationTx_ExpiredVault 验证过期 vault 时校验失败。
func TestValidateIssuingActivationTx_ExpiredVault(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	expiredTime := time.Now().Add(-1 * time.Hour) // 已过期

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, &expiredTime, 1)},
		},
	}

	err := svc.validateIssuingActivationTx(context.Background(), tx, poolUUID, &entityID, adapterProviderID.String(), vaultKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

// TestValidateIssuingActivationTx_WrongCapability 验证错误 capability（非 CARD_ISSUER）时校验失败。
func TestValidateIssuingActivationTx_WrongCapability(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ACQUIRER")}, // 不是 CARD_ISSUER
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	err := svc.validateIssuingActivationTx(context.Background(), tx, poolUUID, &entityID, adapterProviderID.String(), vaultKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CARD_ISSUER")
}

// TestValidateIssuingActivationTx_VaultNotFound 验证 vault 记录不存在时校验失败。
func TestValidateIssuingActivationTx_VaultNotFound(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			{scanFn: makeVaultScanErr()}, // vault 不存在
		},
	}

	err := svc.validateIssuingActivationTx(context.Background(), tx, poolUUID, &entityID, adapterProviderID.String(), vaultKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault credential not found")
}

// TestValidateIssuingActivationTx_Success 验证合法数据校验成功。
func TestValidateIssuingActivationTx_Success(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	err := svc.validateIssuingActivationTx(context.Background(), tx, poolUUID, &entityID, adapterProviderID.String(), vaultKey)
	require.NoError(t, err)
}

// ── applyPoolStatus 行为测试 ──

// TestApplyPoolStatus_EnableNexusHTTP_Success 验证 NEXUS_ISSUING_HTTP_V1 启用成功。
// 直接调用真实 applyPoolStatus，断言执行了 UPDATE issuing_providers。
func TestApplyPoolStatus_EnableNexusHTTP_Success(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			// 1. SELECT issuing_providers FOR UPDATE
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
			// 2. entity tenant_id (validateIssuingActivationTx)
			{scanFn: makeEntityScanFn(&tenantID)},
			// 3. adapter_provider (validateIssuingActivationTx)
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			// 4. vault (validateIssuingActivationTx)
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
			// 5. UPDATE RETURNING version
			{scanFn: makeUpdateResultScanFn(2)},
		},
	}

	out, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.NoError(t, err)
	assert.Equal(t, 2, out.NewVersion)
	assert.True(t, hasUpdateIssuingProviders(tx), "UPDATE issuing_providers should have been executed")
}

// TestApplyPoolStatus_EnableNexusHTTP_CrossTenant 验证 NEXUS_ISSUING_HTTP_V1 跨 tenant 时失败，
// 且不执行 UPDATE issuing_providers。
func TestApplyPoolStatus_EnableNexusHTTP_CrossTenant(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	entityTenantID := uuid.New()
	pathTenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()

	vaultKey := "vault://" + pathTenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			// 1. SELECT issuing_providers FOR UPDATE
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
			// 2. entity tenant_id (validateIssuingActivationTx) — 返回与 path 不同的 tenant
			{scanFn: makeEntityScanFn(&entityTenantID)},
			// 3. adapter_provider
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			// 4. vault
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &pathTenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	assertNoSideEffects(t, tx, "cross-tenant failure")
}

// TestApplyPoolStatus_EnableNexusHTTP_RevokedVault 验证 REVOKED vault 时失败，
// 且不执行 UPDATE issuing_providers。
func TestApplyPoolStatus_EnableNexusHTTP_RevokedVault(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			{scanFn: makeVaultScanFn(&recordID, "REVOKED", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	assertNoSideEffects(t, tx, "REVOKED vault")
}

// TestApplyPoolStatus_EnableNexusHTTP_ExpiredVault 验证过期 vault 时失败，
// 且不执行 UPDATE issuing_providers。
func TestApplyPoolStatus_EnableNexusHTTP_ExpiredVault(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()
	expiredTime := time.Now().Add(-1 * time.Hour)

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, &expiredTime, 1)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	assertNoSideEffects(t, tx, "expired vault")
}

// TestApplyPoolStatus_EnableNexusHTTP_WrongCapability 验证错误 capability 时失败，
// 且不执行 UPDATE issuing_providers。
func TestApplyPoolStatus_EnableNexusHTTP_WrongCapability(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
			{scanFn: makeEntityScanFn(&tenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ACQUIRER")}, // 错误 capability
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	assertNoSideEffects(t, tx, "wrong capability")
}

// TestApplyPoolStatus_EnableNexusHTTP_VersionMismatch 验证 version 不匹配时失败，
// 且不执行 UPDATE issuing_providers。
func TestApplyPoolStatus_EnableNexusHTTP_VersionMismatch(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(5), // 期望 version=5，但 DB 返回 version=1
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1, // version=1，不匹配 target_version=5
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version conflict")
	assertNoSideEffects(t, tx, "version mismatch")
}

// ── LocalMock 分支测试 ──

// TestApplyPoolStatus_EnableMockLocal_LocalMockSuccess 验证 MOCK_LOCAL 在 LocalMock 模式下启用成功，
// 且不查询真实 ownership（不调用 validateIssuingActivationTx）。
func TestApplyPoolStatus_EnableMockLocal_LocalMockSuccess(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	svc.SetRuntimeMode(configvalidator.RuntimeModeLocalMock)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			// 1. SELECT issuing_providers FOR UPDATE — MOCK_LOCAL 不需要 adapter_provider_id 和 entity_id
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "mock-provider", "mock-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"MOCK_LOCAL", nil, nil,
			)},
			// 注意：MOCK_LOCAL 不调用 validateIssuingActivationTx，所以没有 entity/adapter/vault 查询
			// 2. UPDATE RETURNING version
			{scanFn: makeUpdateResultScanFn(2)},
		},
	}

	out, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.NoError(t, err)
	assert.Equal(t, 2, out.NewVersion)
	assert.True(t, hasUpdateIssuingProviders(tx), "UPDATE issuing_providers should have been executed")
	// 验证只调用了 2 次 QueryRow（SELECT + UPDATE），没有 ownership 查询
	assert.Equal(t, 2, tx.queryRowIdx, "MOCK_LOCAL should not query ownership (no validateIssuingActivationTx)")
}

// TestApplyPoolStatus_EnableMockLocal_ProductionRejects 验证 MOCK_LOCAL 在 Production 模式下被拒绝。
func TestApplyPoolStatus_EnableMockLocal_ProductionRejects(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	// 默认 Production 模式

	tenantID := uuid.New()
	poolUUID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "mock-provider", "mock-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"MOCK_LOCAL", nil, nil,
			)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOCK_LOCAL")

	assertNoSideEffects(t, tx, "MOCK_LOCAL rejected in production")
}

// TestApplyPoolStatus_EnableUnsupported_Rejects 验证 UNSUPPORTED 在任何模式下被拒绝。
func TestApplyPoolStatus_EnableUnsupported_Rejects(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	svc.SetRuntimeMode(configvalidator.RuntimeModeLocalMock)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"UNSUPPORTED", nil, nil,
			)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNSUPPORTED")
	assertNoSideEffects(t, tx, "UNSUPPORTED rejected")
}

// TestApplyPoolStatus_EnableNexusHTTP_StillEnforcesOwnership 验证 NEXUS_ISSUING_HTTP_V1 仍强制 ownership 校验。
func TestApplyPoolStatus_EnableNexusHTTP_StillEnforcesOwnership(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
			// entity tenant_id
			{scanFn: makeEntityScanFn(&tenantID)},
			// adapter_provider — 返回 INACTIVE
			{scanFn: makeAdapterScanFn("INACTIVE", "CARD_ISSUER")},
			// vault
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ACTIVE")
	assertNoSideEffects(t, tx, "ownership validation fails")
}

// ── applyPoolUpdate 行为测试 ──

// TestApplyPoolUpdate_ActiveNexusHTTP_Success 验证 ACTIVE NEXUS_ISSUING_HTTP_V1 更新成功。
func TestApplyPoolUpdate_ActiveNexusHTTP_Success(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionUpdateIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolUpdatePayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			// 1. SELECT issuing_providers FOR UPDATE (12 columns including status)
			{scanFn: makePoolUpdateScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, "ACTIVE", nexusProviderConfig, []byte(`{}`), 1,
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
			// 2. entity tenant_id (validateIssuingActivationTx)
			{scanFn: makeEntityScanFn(&tenantID)},
			// 3. adapter_provider
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			// 4. vault
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1)},
			// 5. UPDATE RETURNING version
			{scanFn: makeUpdateResultScanFn(2)},
		},
	}

	out, err := svc.applyPoolUpdate(context.Background(), tx, rec)
	require.NoError(t, err)
	assert.Equal(t, 2, out.NewVersion)
	assert.True(t, hasUpdateIssuingProviders(tx), "UPDATE issuing_providers should have been executed")
}

// TestApplyPoolUpdate_ActiveMockLocal_LocalMockSuccess 验证 ACTIVE MOCK_LOCAL 在 LocalMock 模式下更新成功。
func TestApplyPoolUpdate_ActiveMockLocal_LocalMockSuccess(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	svc.SetRuntimeMode(configvalidator.RuntimeModeLocalMock)

	tenantID := uuid.New()
	poolUUID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionUpdateIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolUpdatePayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			// 1. SELECT issuing_providers FOR UPDATE
			{scanFn: makePoolUpdateScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "mock-provider", "mock-pool",
				vaultKey, "ACTIVE", nexusProviderConfig, []byte(`{}`), 1,
				"MOCK_LOCAL", nil, nil,
			)},
			// MOCK_LOCAL 不调用 validateIssuingActivationTx
			// 2. UPDATE RETURNING version
			{scanFn: makeUpdateResultScanFn(2)},
		},
	}

	out, err := svc.applyPoolUpdate(context.Background(), tx, rec)
	require.NoError(t, err)
	assert.Equal(t, 2, out.NewVersion)
	assert.True(t, hasUpdateIssuingProviders(tx))
	// 验证只调用了 2 次 QueryRow（SELECT + UPDATE），没有 ownership 查询
	assert.Equal(t, 2, tx.queryRowIdx, "MOCK_LOCAL should not query ownership")
}

// TestApplyPoolUpdate_ActiveMockLocal_ProductionRejects 验证 ACTIVE MOCK_LOCAL 在 Production 下被拒绝。
func TestApplyPoolUpdate_ActiveMockLocal_ProductionRejects(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	// 默认 Production

	tenantID := uuid.New()
	poolUUID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionUpdateIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolUpdatePayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolUpdateScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "mock-provider", "mock-pool",
				vaultKey, "ACTIVE", nexusProviderConfig, []byte(`{}`), 1,
				"MOCK_LOCAL", nil, nil,
			)},
		},
	}

	_, err := svc.applyPoolUpdate(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOCK_LOCAL")

	assertNoSideEffects(t, tx, "MOCK_LOCAL rejected in production (pool update)")
}

// TestApplyPoolUpdate_ActiveNexusHTTP_CrossTenant 验证 ACTIVE NEXUS_ISSUING_HTTP_V1 跨 tenant 时失败。
func TestApplyPoolUpdate_ActiveNexusHTTP_CrossTenant(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	entityTenantID := uuid.New()
	pathTenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	adapterProviderIDStr := adapterProviderID.String()

	vaultKey := "vault://" + pathTenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionUpdateIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolUpdatePayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolUpdateScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				vaultKey, "ACTIVE", nexusProviderConfig, []byte(`{}`), 1,
				"NEXUS_ISSUING_HTTP_V1", &adapterProviderIDStr, &entityID,
			)},
			{scanFn: makeEntityScanFn(&entityTenantID)},
			{scanFn: makeAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			{scanFn: makeVaultScanFn(&recordID, "ACTIVE", "API_KEY", &pathTenantID, &poolUUID, &adapterProviderID, nil, 1)},
		},
	}

	_, err := svc.applyPoolUpdate(context.Background(), tx, rec)
	require.Error(t, err)
	assertNoSideEffects(t, tx, "cross-tenant failure (pool update)")
}

// TestApplyPoolUpdate_DisabledStatus_UsesDraftValidation 验证 DISABLED 状态只调用 ValidateDraftProvider。
func TestApplyPoolUpdate_DisabledStatus_UsesDraftValidation(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	poolUUID := uuid.New()

	rec := &ApprovalRecord{
		ActionType:    ActionUpdateIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolUpdatePayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolUpdateScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				"", "DISABLED", []byte(`{}`), []byte(`{}`), 1,
				"UNSUPPORTED", nil, nil,
			)},
			// DISABLED 不调用 validateIssuingActivationTx
			// UPDATE RETURNING version
			{scanFn: makeUpdateResultScanFn(2)},
		},
	}

	out, err := svc.applyPoolUpdate(context.Background(), tx, rec)
	require.NoError(t, err)
	assert.Equal(t, 2, out.NewVersion)
	assert.True(t, hasUpdateIssuingProviders(tx))
}

// TestApplyPoolUpdate_UnknownStatus_Rejects 验证未知 status 时被拒绝。
func TestApplyPoolUpdate_UnknownStatus_Rejects(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	poolUUID := uuid.New()

	rec := &ApprovalRecord{
		ActionType:    ActionUpdateIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolUpdatePayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolUpdateScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "test-provider", "test-pool",
				"", "UNKNOWN_STATUS", []byte(`{}`), []byte(`{}`), 1,
				"UNSUPPORTED", nil, nil,
			)},
		},
	}

	_, err := svc.applyPoolUpdate(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")
	assertNoSideEffects(t, tx, "unknown status")
}

// ── 保留原有枚举/setter 测试 ──

func TestIsValidApprovalStatus_RejectsApproved(t *testing.T) {
	assert.False(t, IsValidApprovalStatus("APPROVED"),
		"APPROVED must NOT be a valid approval status — use APPLIED instead")
}

func TestIsValidApprovalStatus_AcceptsValidStatuses(t *testing.T) {
	validStatuses := []string{
		ApprovalStatusPending,
		ApprovalStatusApplying,
		ApprovalStatusApplied,
		ApprovalStatusRejected,
		ApprovalStatusFailedStale,
	}

	for _, status := range validStatuses {
		assert.True(t, IsValidApprovalStatus(status),
			"%s must be a valid approval status", status)
	}
}

func TestApprovalService_SetRuntimeMode(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	assert.Equal(t, configvalidator.RuntimeModeProduction, svc.runtimeMode,
		"default runtime mode must be Production (fail-closed)")

	svc.SetRuntimeMode(configvalidator.RuntimeModeLocalMock)
	assert.Equal(t, configvalidator.RuntimeModeLocalMock, svc.runtimeMode,
		"SetRuntimeMode(LocalMock) must set mode to LocalMock")

	svc.SetRuntimeMode(configvalidator.RuntimeModeProduction)
	assert.Equal(t, configvalidator.RuntimeModeProduction, svc.runtimeMode,
		"SetRuntimeMode(Production) must set mode to Production")
}

// ── R24 P0-2: RuntimeMode 未知值 fail-closed 测试 ──

// TestRuntimeMode_UnknownValue_RejectedByValidate 验证 RuntimeMode(99) 被 Validate 拒绝。
func TestRuntimeMode_UnknownValue_RejectedByValidate(t *testing.T) {
	unknownMode := configvalidator.RuntimeMode(99)
	err := unknownMode.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown RuntimeMode")
}

// TestRuntimeMode_IsLocalMock 验证 IsLocalMock 方法。
func TestRuntimeMode_IsLocalMock(t *testing.T) {
	assert.True(t, configvalidator.RuntimeModeLocalMock.IsLocalMock())
	assert.False(t, configvalidator.RuntimeModeProduction.IsLocalMock())
	assert.False(t, configvalidator.RuntimeMode(99).IsLocalMock())
}

// TestValidateProviderActivation_UnknownMode_RejectsMockLocal 验证 RuntimeMode(99) + MOCK_LOCAL 被拒绝。
func TestValidateProviderActivation_UnknownMode_RejectsMockLocal(t *testing.T) {
	tenantID := uuid.New()
	recordID := uuid.New()
	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	cfg := &configvalidator.IssuingPoolConfig{
		ProviderCode:       "mock-provider",
		PoolCode:           "mock-pool",
		IssuingTier:        "TIER_1_AGGREGATOR",
		FundingMode:        "CRYPTO_DESENSITIZED",
		CredentialVaultKey: vaultKey,
		AdapterType:        "MOCK_LOCAL",
	}

	err := configvalidator.ValidateIssuingRuntimeConfig(cfg, configvalidator.RuntimeMode(99))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown RuntimeMode")
}

// TestValidateProviderActivation_UnknownMode_RejectsHTTP 验证 RuntimeMode(99) + HTTP Provider 被拒绝。
func TestValidateProviderActivation_UnknownMode_RejectsHTTP(t *testing.T) {
	tenantID := uuid.New()
	recordID := uuid.New()
	adapterProviderID := uuid.New()
	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	cfg := &configvalidator.IssuingPoolConfig{
		ProviderCode:       "nexus-provider",
		PoolCode:           "nexus-pool",
		IssuingTier:        "TIER_1_AGGREGATOR",
		FundingMode:        "CRYPTO_DESENSITIZED",
		CredentialVaultKey: vaultKey,
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  adapterProviderID.String(),
		EntityID:           uuid.New().String(),
		ProviderConfig:     map[string]interface{}{"base_url": "https://api.example.com"},
	}

	err := configvalidator.ValidateIssuingRuntimeConfig(cfg, configvalidator.RuntimeMode(99))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown RuntimeMode")
}

// TestApplyPoolStatus_UnknownMode_NoUpdate 验证 applyPoolStatus 未知模式&模式不执行 UPDATE。
func TestApplyPoolStatus_UnknownMode_NoUpdate(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	svc.SetRuntimeMode(configvalidator.RuntimeMode(99)) // 未知模式

	tenantID := uuid.New()
	poolUUID := uuid.New()
	recordID := uuid.New()
	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionEnableIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolStatusPayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "mock-provider", "mock-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"MOCK_LOCAL", nil, nil,
			)},
		},
	}

	_, err := svc.applyPoolStatus(context.Background(), tx, rec)
	require.Error(t, err)
	// ValidateIssuingRuntimeConfig → ValidateProviderActivation 在 mode.Validate() 处拒绝未知模式
	assert.Contains(t, err.Error(), "unknown RuntimeMode")
	assertNoSideEffects(t, tx, "unknown RuntimeMode (pool status)")
}

// TestApplyPoolUpdate_UnknownMode_NoUpdate 验证 applyPoolUpdate 未知模式不执行 UPDATE。
func TestApplyPoolUpdate_UnknownMode_NoUpdate(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	svc.SetRuntimeMode(configvalidator.RuntimeMode(99)) // 未知模式

	tenantID := uuid.New()
	poolUUID := uuid.New()
	recordID := uuid.New()
	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	rec := &ApprovalRecord{
		ActionType:    ActionUpdateIssuingPool,
		TargetVersion: ptrInt(1),
		ValidatedPayload: mustEncodePayload(t, &PoolUpdatePayload{
			PoolID: poolUUID.String(),
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makePoolUpdateScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "mock-provider", "mock-pool",
				vaultKey, "ACTIVE", nexusProviderConfig, []byte(`{}`), 1,
				"MOCK_LOCAL", nil, nil,
			)},
		},
	}

	_, err := svc.applyPoolUpdate(context.Background(), tx, rec)
	require.Error(t, err)
	// ValidateProviderActivation 在 mode.Validate() 处拒绝未知模式
	assert.Contains(t, err.Error(), "unknown RuntimeMode")
	assertNoSideEffects(t, tx, "unknown RuntimeMode (pool update)")
}

// ── ApplyTransaction 入口测试 ──

// TestApplyTransaction_EnableIssuingPool_MockLocalInProduction_Fails 验证通过 ApplyTransaction 入口
// 执行 ENABLE_ISSUING_POOL 时，MOCK_LOCAL 在 Production 模式下被拒绝。
// 此测试覆盖完整 ApplyTransaction 流程：身份校验 → SELECT FOR UPDATE → APPLYING → SAVEPOINT →
// applyByActionType 失败 → handleApplyFailure（ROLLBACK TO SAVEPOINT → FAILED_STALE → Commit）。
// 断言不执行 UPDATE issuing_providers、INSERT admin_audit_log、INSERT transactional_outbox。
func TestApplyTransaction_EnableIssuingPool_MockLocalInProduction_Fails(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	// 默认 Production 模式

	poolUUID := uuid.New()
	tenantID := uuid.New()
	recordID := uuid.New()
	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	// 构造 validated_payload JSON
	payloadJSON, _ := json.Marshal(map[string]interface{}{
		"pool_id": poolUUID.String(),
	})

	poolIDStr := poolUUID.String()
	targetVersion := 1

	// scriptedTx 同时作为 txBeginner 和 pgx.Tx
	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			// 1. SELECT approval FOR UPDATE — PENDING, ENABLE_ISSUING_POOL
			{scanFn: makeApprovalScanFn(
				uuid.New().String(),     // id
				nil,                     // tenant_id (nil = skip tenant check)
				ActionEnableIssuingPool, // action_type
				"issuing_pool",          // target_type
				&poolIDStr,              // target_id
				&targetVersion,          // target_version
				payloadJSON,             // validated_payload
				nil,                     // before_snapshot
				"requester-1",           // requester (different from checker)
				"admin",                 // requester_role
				"test reason",           // reason
				ApprovalStatusPending,   // status
			)},
			// 2. SELECT issuing_providers FOR UPDATE — MOCK_LOCAL
			{scanFn: makePoolStatusScanFn(
				"TIER_1_AGGREGATOR", "CRYPTO_DESENSITIZED", "mock-provider", "mock-pool",
				vaultKey, nexusProviderConfig, []byte(`{}`), 1,
				"MOCK_LOCAL", nil, nil,
			)},
		},
	}

	svc.setTxBeginnerForTest(tx)

	ctx := context.Background()
	identity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	ctx = context.WithValue(ctx, httpx.AdminContextKey{}, identity)

	_, err := svc.ApplyTransaction(ctx, uuid.New().String(), "approved")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOCK_LOCAL")

	// 验证没有执行任何业务写入
	assertNoSideEffects(t, tx, "ApplyTransaction MOCK_LOCAL in production")

	// 验证失败路径：ROLLBACK TO SAVEPOINT 和 FAILED_STALE 都被执行
	assert.True(t, containsExec(tx, "ROLLBACK TO SAVEPOINT approval_apply"),
		"failure path must ROLLBACK TO SAVEPOINT")
	assert.True(t, containsExec(tx, "FAILED_STALE"),
		"failure path must mark approval as FAILED_STALE")
	// 验证 Commit 被调用（handleApplyFailure 中的 Commit）
	assert.Equal(t, 1, tx.commitCalls, "handleApplyFailure must Commit")
}

// makeApprovalScanFn 创建返回 admin_approval_requests 行的 Scan 函数。
// 用于 ApplyTransaction 入口测试，覆盖 selectApprovalForUpdate 的 18 列 Scan。
func makeApprovalScanFn(
	id string,
	tenantID *string,
	actionType string,
	targetType string,
	targetID *string,
	targetVersion *int,
	validatedPayload []byte,
	beforeSnapshot []byte,
	requester string,
	requesterRole string,
	reason string,
	status string,
) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) >= 18 {
			// 1. id (string)
			if v, ok := dest[0].(*string); ok {
				*v = id
			}
			// 2. tenant_id (*string)
			if v, ok := dest[1].(**string); ok {
				*v = tenantID
			}
			// 3. action_type (string)
			if v, ok := dest[2].(*string); ok {
				*v = actionType
			}
			// 4. target_type (string)
			if v, ok := dest[3].(*string); ok {
				*v = targetType
			}
			// 5. target_id (*string)
			if v, ok := dest[4].(**string); ok {
				*v = targetID
			}
			// 6. target_version (*int)
			if v, ok := dest[5].(**int); ok {
				*v = targetVersion
			}
			// 7. validated_payload ([]byte)
			if v, ok := dest[6].(*[]byte); ok {
				*v = validatedPayload
			}
			// 8. before_snapshot ([]byte)
			if v, ok := dest[7].(*[]byte); ok {
				*v = beforeSnapshot
			}
			// 9. requester (string)
			if v, ok := dest[8].(*string); ok {
				*v = requester
			}
			// 10. requester_role (string)
			if v, ok := dest[9].(*string); ok {
				*v = requesterRole
			}
			// 11. reason (string)
			if v, ok := dest[10].(*string); ok {
				*v = reason
			}
			// 12. status (string)
			if v, ok := dest[11].(*string); ok {
				*v = status
			}
			// 13. checker (*string) — nil for PENDING approval
			// 14. review_reason (*string) — nil
			// 15. created_at (time.Time)
			if v, ok := dest[14].(*time.Time); ok {
				*v = time.Now()
			}
			// 16. reviewed_at (*time.Time) — nil
			// 17. applied_at (*time.Time) — nil
			// 18. request_id (*string) — nil
		}
		return nil
	}
}

// containsExec 检查 Exec 调用中是否包含指定 SQL 片段。
func containsExec(tx *scriptedTx, substr string) bool {
	for _, sql := range tx.execCalls {
		if strings.Contains(sql, substr) {
			return true
		}
	}
	return false
}

// ── 辅助函数 ──

func ptrInt(v int) *int { return &v }

func mustEncodePayload(t *testing.T, payload interface{}) map[string]interface{} {
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	var m map[string]interface{}
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)
	return m
}

// ── applyMerchantReserve 测试 ──

// makeReserveTreasuryScanFn 创建返回 merchant_treasury_ledger 聚合行的 Scan 函数：
// (COUNT(*), COALESCE(MIN(version), 0))。
func makeReserveTreasuryScanFn(rowCount int64, version int) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) >= 2 {
			if c, ok := dest[0].(*int64); ok {
				*c = rowCount
			}
			if v, ok := dest[1].(*int); ok {
				*v = version
			}
		}
		return nil
	}
}

// TestApplyMerchantReserve_Success 验证成功路径：按 merchant_id 更新全部币种行并返回新版本。
func TestApplyMerchantReserve_Success(t *testing.T) {
	svc := NewApprovalService(nil, nil)
	merchantID := uuid.New()

	rec := &ApprovalRecord{
		ActionType: ActionUpdateMerchantReserve,
		ValidatedPayload: mustEncodePayload(t, &MerchantReservePayload{
			MerchantID:      merchantID.String(),
			ReserveRatioBps: 1500,
		}),
	}

	tx := &scriptedTx{
		execTag: "UPDATE 2",
		queryRowResults: []scriptedRow{
			{scanFn: makeReserveTreasuryScanFn(2, 3)}, // 2 个币种行，最小 version=3
		},
	}

	out, err := svc.applyMerchantReserve(context.Background(), tx, rec)
	require.NoError(t, err)
	assert.Equal(t, 4, out.NewVersion, "new version = min(version) + 1")
	assert.Equal(t, merchantID, out.TargetUUID)
	assert.Equal(t, "merchant_reserve.updated", out.EventType)
	assert.Equal(t, "config:merchant:reload", out.ReloadTopic)
	assert.Equal(t, 1, len(tx.execCalls), "should execute exactly one UPDATE")
	assert.Contains(t, tx.execCalls[0], "reserve_ratio_bps")
	assert.Contains(t, tx.execCalls[0], "merchant_id = $2", "must key on merchant_id, not proxy id")
}

// TestApplyMerchantReserve_NotFound 验证商户无金库行时报 NotFound 且无写入。
func TestApplyMerchantReserve_NotFound(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	rec := &ApprovalRecord{
		ActionType: ActionUpdateMerchantReserve,
		ValidatedPayload: mustEncodePayload(t, &MerchantReservePayload{
			MerchantID:      uuid.New().String(),
			ReserveRatioBps: 1500,
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makeReserveTreasuryScanFn(0, 0)}, // 无金库行
		},
	}

	_, err := svc.applyMerchantReserve(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assertNoSideEffects(t, tx, "merchant treasury row not found")
}

// TestApplyMerchantReserve_VersionConflict 验证显式 target_version 不匹配时拒绝且无写入。
func TestApplyMerchantReserve_VersionConflict(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	rec := &ApprovalRecord{
		ActionType:    ActionUpdateMerchantReserve,
		TargetVersion: ptrInt(5), // 期望 version=5，但 DB 返回最小 version=1
		ValidatedPayload: mustEncodePayload(t, &MerchantReservePayload{
			MerchantID:      uuid.New().String(),
			ReserveRatioBps: 1500,
		}),
	}

	tx := &scriptedTx{
		queryRowResults: []scriptedRow{
			{scanFn: makeReserveTreasuryScanFn(1, 1)},
		},
	}

	_, err := svc.applyMerchantReserve(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version conflict")
	assertNoSideEffects(t, tx, "version mismatch")
}

// TestApplyMerchantReserve_InvalidRatio 验证 ratio 越界直接拒绝，不触碰数据库。
func TestApplyMerchantReserve_InvalidRatio(t *testing.T) {
	svc := NewApprovalService(nil, nil)

	rec := &ApprovalRecord{
		ActionType: ActionUpdateMerchantReserve,
		ValidatedPayload: mustEncodePayload(t, &MerchantReservePayload{
			MerchantID:      uuid.New().String(),
			ReserveRatioBps: 15000, // 越界（> 10000）
		}),
	}

	tx := &scriptedTx{}

	_, err := svc.applyMerchantReserve(context.Background(), tx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserve_ratio_bps")
	assert.Equal(t, 0, tx.queryRowIdx, "must not query database")
	assert.Equal(t, 0, len(tx.execCalls), "must not execute UPDATE")
}
