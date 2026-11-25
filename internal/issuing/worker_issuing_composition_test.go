package issuing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── P0-7 最终闭环修复: Worker Issuing 组合根测试 ──
//
// 测试 RegisterProviderToFactory 和 BuildWorkerIssuingRuntime 的组合逻辑。

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }

// mockOwnershipChecker 是测试用的 OwnershipChecker，总是返回 nil（通过）。
type mockOwnershipChecker struct{}

func (m *mockOwnershipChecker) CheckProviderOwnership(ctx context.Context, provider IssuingProvider) error {
	return nil
}

// mockOwnershipCheckerFailing 总是返回错误。
type mockOwnershipCheckerFailing struct{}

func (m *mockOwnershipCheckerFailing) CheckProviderOwnership(ctx context.Context, provider IssuingProvider) error {
	return fmt.Errorf("mock: ownership check failed")
}

// TestWorkerIssuingComposition_RegistersSupportedHTTPProvider 验证 NEXUS_ISSUING_HTTP_V1 注册成功。
func TestWorkerIssuingComposition_RegistersSupportedHTTPProvider(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	adapterProviderID := uuid.New()

	err := RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode:      "nexus-issuing-001",
		AdapterProviderID: ptrUUID(adapterProviderID),
		AdapterType:       AdapterTypeNexusIssuingHTTPV1,
		BaseURL:           "https://api.provider.example.com",
		IsProduction:      true,
	})
	require.NoError(t, err)
	assert.True(t, factory.Has("nexus-issuing-001"), "NEXUS_ISSUING_HTTP_V1 should be registered")
}

// TestWorkerIssuingComposition_RegistersLocalMockOnlyInMockMode 验证 MOCK_LOCAL 在 mock 模式注册成功。
// R18 P0-5: MOCK_LOCAL 现在通过 RegisterProviderToFactory 注册 MockIssuingAdapter 到 Factory。
func TestWorkerIssuingComposition_RegistersLocalMockOnlyInMockMode(t *testing.T) {
	factory := NewHTTPAdapterFactory()

	// mock 模式：注册 MockIssuingAdapter 到 Factory
	err := RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode: "mock-provider",
		AdapterType:  AdapterTypeMockLocal,
		BaseURL:      "http://localhost:8080",
		IsProduction: false,
	})
	require.NoError(t, err)
	assert.True(t, factory.Has("mock-provider"), "MOCK_LOCAL should be registered in mock mode")
}

// TestWorkerIssuingComposition_RejectsUnsupportedProviderInProduction 验证 production 拒绝 UNSUPPORTED。
func TestWorkerIssuingComposition_RejectsUnsupportedProviderInProduction(t *testing.T) {
	factory := NewHTTPAdapterFactory()

	err := RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode: "unsupported-provider",
		AdapterType:  AdapterTypeUnsupported,
		BaseURL:      "https://api.example.com",
		IsProduction: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAdapterTypeUnsupported)
	assert.False(t, factory.Has("unsupported-provider"))
}

// TestWorkerIssuingComposition_RejectsInvalidBaseURL 验证无效 base_url 被拒绝。
func TestWorkerIssuingComposition_RejectsInvalidBaseURL(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	adapterProviderID := uuid.New()

	// 空 base_url
	err := RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode:      "test-provider",
		AdapterProviderID: ptrUUID(adapterProviderID),
		AdapterType:       AdapterTypeNexusIssuingHTTPV1,
		BaseURL:           "",
		IsProduction:      false,
	})
	require.Error(t, err)

	// 无效 URL
	err = RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode:      "test-provider",
		AdapterProviderID: ptrUUID(adapterProviderID),
		AdapterType:       AdapterTypeNexusIssuingHTTPV1,
		BaseURL:           "://invalid",
		IsProduction:      false,
	})
	require.Error(t, err)

	// production 下非 HTTPS
	err = RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode:      "test-provider",
		AdapterProviderID: ptrUUID(adapterProviderID),
		AdapterType:       AdapterTypeNexusIssuingHTTPV1,
		BaseURL:           "http://api.example.com",
		IsProduction:      true,
	})
	require.Error(t, err)
}

// F-18: TestWorkerIssuingComposition_RejectsMissingVaultBinding 验证 production 模式缺少 adapter_provider_id 时注册失败。
func TestWorkerIssuingComposition_RejectsMissingVaultBinding(t *testing.T) {
	factory := NewHTTPAdapterFactory()

	// production 模式下 NEXUS_ISSUING_HTTP_V1 要求非空 adapter_provider_id
	err := RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode:      "test-provider",
		AdapterProviderID: nil, // 缺少 adapter_provider_id
		AdapterType:       AdapterTypeNexusIssuingHTTPV1,
		BaseURL:           "https://api.example.com",
		IsProduction:      true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAdapterTypeUnsupported)
	assert.False(t, factory.Has("test-provider"), "provider with missing vault binding must not be registered")
}

// TestWorkerIssuingComposition_RejectsURLWithUserinfo 验证 URL 包含 userinfo 时被拒绝。
func TestWorkerIssuingComposition_RejectsURLWithUserinfo(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	adapterProviderID := uuid.New()

	err := RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode:      "test-provider",
		AdapterProviderID: ptrUUID(adapterProviderID),
		AdapterType:       AdapterTypeNexusIssuingHTTPV1,
		BaseURL:           "https://user:pass@api.example.com",
		IsProduction:      true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "userinfo")
}

// F-14: TestWorkerIssuingComposition_RejectsURLWithEmptyHost 验证 URL 空 host 时被拒绝。
func TestWorkerIssuingComposition_RejectsURLWithEmptyHost(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	adapterProviderID := uuid.New()

	err := RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode:      "test-provider",
		AdapterProviderID: ptrUUID(adapterProviderID),
		AdapterType:       AdapterTypeNexusIssuingHTTPV1,
		BaseURL:           "https://",
		IsProduction:      false,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host")
}

// F-14: TestWorkerIssuingComposition_RejectsURLWithFragment 验证 URL 包含 fragment 时被拒绝。
func TestWorkerIssuingComposition_RejectsURLWithFragment(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	adapterProviderID := uuid.New()

	err := RegisterProviderToFactory(factory, RegisterProviderConfig{
		ProviderCode:      "test-provider",
		AdapterProviderID: ptrUUID(adapterProviderID),
		AdapterType:       AdapterTypeNexusIssuingHTTPV1,
		BaseURL:           "https://api.example.com#fragment",
		IsProduction:      false,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fragment")
}

// TestWorkerIssuingComposition_BuildRuntimeRegistersProviders 验证 BuildWorkerIssuingRuntime 注册 providers。
// F-11: main.go 和测试使用同一组合函数。
func TestWorkerIssuingComposition_BuildRuntimeRegistersProviders(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	adapterProviderID := uuid.New()
	providerID := uuid.New()
	tenantID := uuid.New()
	recordID := uuid.New()

	providers := []IssuingProvider{
		{
			ID:                 providerID,
			ProviderCode:       "nexus-issuing-001",
			AdapterProviderID:  ptrUUID(adapterProviderID),
			AdapterType:        AdapterTypeNexusIssuingHTTPV1,
			Status:             "ACTIVE",
			ProviderConfig:     map[string]any{"base_url": "https://api.example.com"},
			CredentialVaultKey: "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1",
			IssuingTier:        IssuingTierAggregator,
			FundingMode:        FundingModeCryptoDesensitized,
			PoolCode:           "test-pool-001",
			EntityID:           ptrUUID(uuid.New()),
		},
	}

	count, err := BuildWorkerIssuingRuntime(factory, providers, RuntimeModeProduction, &mockOwnershipChecker{})
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.True(t, factory.Has("nexus-issuing-001"))
}

// TestWorkerIssuingComposition_BuildRuntimeRejectsMissingBindingInProduction 验证 production 拒绝缺少 binding。
func TestWorkerIssuingComposition_BuildRuntimeRejectsMissingBindingInProduction(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	providerID := uuid.New()

	providers := []IssuingProvider{
		{
			ID:                providerID,
			ProviderCode:      "test-provider",
			AdapterProviderID: nil, // 缺少 binding
			AdapterType:       AdapterTypeNexusIssuingHTTPV1,
			Status:            "ACTIVE",
			ProviderConfig:    map[string]any{"base_url": "https://api.example.com"},
		},
	}

	_, err := BuildWorkerIssuingRuntime(factory, providers, RuntimeModeProduction, &mockOwnershipChecker{})
	require.Error(t, err)
	assert.False(t, factory.Has("test-provider"))
}

// R21 P0-2: Production 模式下 ownershipChecker 为 nil 必须报错。
func TestBuildWorkerIssuingRuntime_ProductionNilOwnershipCheckerErrors(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	providers := []IssuingProvider{
		{
			ID:                 uuid.New(),
			ProviderCode:       "test-provider",
			AdapterProviderID:  ptrUUID(uuid.New()),
			AdapterType:        AdapterTypeNexusIssuingHTTPV1,
			Status:             "ACTIVE",
			ProviderConfig:     map[string]any{"base_url": "https://api.example.com"},
			CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
			IssuingTier:        IssuingTierAggregator,
			FundingMode:        FundingModeCryptoDesensitized,
			PoolCode:           "test-pool",
			EntityID:           ptrUUID(uuid.New()),
		},
	}

	_, err := BuildWorkerIssuingRuntime(factory, providers, RuntimeModeProduction, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ownershipChecker")
}

// R21 P0-2: Production 模式下 ownershipChecker 检查失败必须报错。
func TestBuildWorkerIssuingRuntime_ProductionOwnershipCheckFails(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	providers := []IssuingProvider{
		{
			ID:                 uuid.New(),
			ProviderCode:       "test-provider",
			AdapterProviderID:  ptrUUID(uuid.New()),
			AdapterType:        AdapterTypeNexusIssuingHTTPV1,
			Status:             "ACTIVE",
			ProviderConfig:     map[string]any{"base_url": "https://api.example.com"},
			CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
			IssuingTier:        IssuingTierAggregator,
			FundingMode:        FundingModeCryptoDesensitized,
			PoolCode:           "test-pool",
			EntityID:           ptrUUID(uuid.New()),
		},
	}

	_, err := BuildWorkerIssuingRuntime(factory, providers, RuntimeModeProduction, &mockOwnershipCheckerFailing{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ownership check failed")
}

// TestAdapterType_Validate 验证 AdapterType.Validate。
func TestAdapterType_Validate(t *testing.T) {
	assert.NoError(t, AdapterTypeNexusIssuingHTTPV1.Validate())
	assert.NoError(t, AdapterTypeMockLocal.Validate())
	assert.NoError(t, AdapterTypeUnsupported.Validate())
	assert.Error(t, AdapterType("UNKNOWN").Validate())
}

// TestAdapterType_IsProductionAllowed 验证 IsProductionAllowed。
func TestAdapterType_IsProductionAllowed(t *testing.T) {
	assert.True(t, AdapterTypeNexusIssuingHTTPV1.IsProductionAllowed())
	assert.False(t, AdapterTypeMockLocal.IsProductionAllowed())
	assert.False(t, AdapterTypeUnsupported.IsProductionAllowed())
}

// TestMockIssuingAdapter_FreeCard 验证 MockIssuingAdapter 冻结卡片。
// F-12: local/mock 模式注册的 mock adapter 必须可用。
func TestMockIssuingAdapter_FreeCard(t *testing.T) {
	adapter := NewMockIssuingAdapter()
	req := FreezeCardRequest{
		ProviderCardRef: "test-ref",
		IdempotencyKey:  "test-key",
	}

	err := adapter.FreezeCard(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), adapter.FreezeCount())
	assert.Equal(t, "test-ref", adapter.lastReq.ProviderCardRef)
	assert.Equal(t, "test-key", adapter.lastReq.IdempotencyKey)
}

// TestMockIssuingAdapter_IdempotentByProviderRequestKey 验证相同 IdempotencyKey 重放不增加 freezeCount。
// R18 P0-5: MockIssuingAdapter 必须按 provider request key 幂等。
func TestMockIssuingAdapter_IdempotentByProviderRequestKey(t *testing.T) {
	adapter := NewMockIssuingAdapter()
	req := FreezeCardRequest{
		ProviderCardRef: "test-ref",
		IdempotencyKey:  "same-key",
	}

	// 第一次冻结
	err := adapter.FreezeCard(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), adapter.FreezeCount())

	// 相同 IdempotencyKey 重放：不增加 freezeCount
	err = adapter.FreezeCard(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), adapter.FreezeCount(), "replay with same IdempotencyKey must not increment freezeCount")

	// 不同 IdempotencyKey：增加 freezeCount
	req2 := FreezeCardRequest{
		ProviderCardRef: "test-ref-2",
		IdempotencyKey:  "different-key",
	}
	err = adapter.FreezeCard(context.Background(), req2)
	require.NoError(t, err)
	assert.Equal(t, int32(2), adapter.FreezeCount(), "different IdempotencyKey must increment freezeCount")
}

// TestBuildWorkerIssuingRuntime_RegistersMockLocal 验证 BuildWorkerIssuingRuntime 注册 MOCK_LOCAL provider。
// R18 P0-5: MOCK_LOCAL 通过 RegisterProviderToFactory 注册到 Factory。
func TestBuildWorkerIssuingRuntime_RegistersMockLocal(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	providerID := uuid.New()
	tenantID := uuid.New()
	recordID := uuid.New()

	providers := []IssuingProvider{
		{
			ID:                 providerID,
			ProviderCode:       "mock-issuing-001",
			AdapterType:        AdapterTypeMockLocal,
			Status:             "ACTIVE",
			ProviderConfig:     map[string]any{},
			CredentialVaultKey: "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1",
			IssuingTier:        IssuingTierAggregator,
			FundingMode:        FundingModeCryptoDesensitized,
			PoolCode:           "test-pool-mock",
		},
	}

	count, err := BuildWorkerIssuingRuntime(factory, providers, RuntimeModeLocalMock, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "MOCK_LOCAL provider should be registered and counted")
	assert.True(t, factory.Has("mock-issuing-001"), "MOCK_LOCAL should be registered in factory")
}

// TestBuildWorkerIssuingRuntime_RegistersBothHTTPAndMock 验证 BuildWorkerIssuingRuntime 同时注册 HTTP 和 MOCK_LOCAL。
// R18 P0-5: 统计 NEXUS_ISSUING_HTTP_V1 和 MOCK_LOCAL。
func TestBuildWorkerIssuingRuntime_RegistersBothHTTPAndMock(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	httpProviderID := uuid.New()
	mockProviderID := uuid.New()
	adapterProviderID := uuid.New()
	tenantID := uuid.New()
	recordID1 := uuid.New()
	recordID2 := uuid.New()

	providers := []IssuingProvider{
		{
			ID:                 httpProviderID,
			ProviderCode:       "nexus-issuing-001",
			AdapterProviderID:  ptrUUID(adapterProviderID),
			AdapterType:        AdapterTypeNexusIssuingHTTPV1,
			Status:             "ACTIVE",
			ProviderConfig:     map[string]any{"base_url": "https://api.example.com"},
			CredentialVaultKey: "vault://" + tenantID.String() + "/" + recordID1.String() + "/api_key/1",
			IssuingTier:        IssuingTierAggregator,
			FundingMode:        FundingModeCryptoDesensitized,
			PoolCode:           "test-pool-http",
		},
		{
			ID:                 mockProviderID,
			ProviderCode:       "mock-issuing-001",
			AdapterType:        AdapterTypeMockLocal,
			Status:             "ACTIVE",
			ProviderConfig:     map[string]any{},
			CredentialVaultKey: "vault://" + tenantID.String() + "/" + recordID2.String() + "/api_key/1",
			IssuingTier:        IssuingTierAggregator,
			FundingMode:        FundingModeCryptoDesensitized,
			PoolCode:           "test-pool-mock",
		},
	}

	count, err := BuildWorkerIssuingRuntime(factory, providers, RuntimeModeLocalMock, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "both HTTP and MOCK_LOCAL should be registered and counted")
	assert.True(t, factory.Has("nexus-issuing-001"))
	assert.True(t, factory.Has("mock-issuing-001"))
}

// ============================================================================
// R22 Item 7: dbOwnershipChecker 构造和接口实现测试
// ============================================================================

// TestNewDBOwnershipChecker_ReturnsImplementation 验证 NewDBOwnershipChecker(nil) 返回非 nil
// 且实现 OwnershipChecker 接口。
func TestNewDBOwnershipChecker_ReturnsImplementation(t *testing.T) {
	checker := NewDBOwnershipChecker(nil)

	assert.NotNil(t, checker, "NewDBOwnershipChecker(nil) must return non-nil")

	// 验证返回值实现了 OwnershipChecker 接口
	var _ OwnershipChecker = checker
}

// ============================================================================
// R23 Item 4: 真实 dbOwnershipChecker 行为测试
// 使用 fake transaction 直接调用 CheckProviderOwnership
// 覆盖：合法成功并 Commit；缺 Vault、跨 tenant、错误 capability、错误 record/version 时 Rollback
// ============================================================================

// fakeOwnershipDB 实现 ownershipDB 接口，返回可编程的 fake 事务
type fakeOwnershipDB struct {
	tx       pgx.Tx
	txOpts   pgx.TxOptions // R24 P1-1: 记录 BeginTx 的 TxOptions
	hasBegin bool          // R24 P1-1: 是否调用了 BeginTx
}

func (f *fakeOwnershipDB) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	f.txOpts = opts
	f.hasBegin = true
	return f.tx, nil
}

// ownershipScriptedTx 是 CheckProviderOwnership 专用的脚本化 fake 事务
type ownershipScriptedTx struct {
	queryRowResults []func(dest ...any) error // 按 QueryRow 调用顺序排列
	queryRowIdx     int
	commitCalls     int
	rollbackCalls   int
}

func (t *ownershipScriptedTx) Commit(ctx context.Context) error {
	t.commitCalls++
	return nil
}
func (t *ownershipScriptedTx) Rollback(ctx context.Context) error {
	t.rollbackCalls++
	return nil
}
func (t *ownershipScriptedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if t.queryRowIdx < len(t.queryRowResults) {
		fn := t.queryRowResults[t.queryRowIdx]
		t.queryRowIdx++
		return &ownershipScriptedRow{scanFn: fn}
	}
	return &ownershipScriptedRow{scanFn: func(dest ...any) error { return pgx.ErrNoRows }}
}
func (t *ownershipScriptedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (t *ownershipScriptedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (t *ownershipScriptedTx) Begin(ctx context.Context) (pgx.Tx, error) { return t, nil }
func (t *ownershipScriptedTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (t *ownershipScriptedTx) Conn() *pgx.Conn { return nil }
func (t *ownershipScriptedTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (t *ownershipScriptedTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return nil
}
func (t *ownershipScriptedTx) LargeObjects() pgx.LargeObjects { return pgx.LargeObjects{} }

type ownershipScriptedRow struct {
	scanFn func(dest ...any) error
}

func (r *ownershipScriptedRow) Scan(dest ...any) error {
	return r.scanFn(dest...)
}

// newOwnershipCheckerWithFakeDB 创建使用 fake DB 的 dbOwnershipChecker
func newOwnershipCheckerWithFakeDB(tx pgx.Tx) (*dbOwnershipChecker, *fakeOwnershipDB) {
	fakeDB := &fakeOwnershipDB{tx: tx}
	return &dbOwnershipChecker{pool: fakeDB}, fakeDB
}

// makeOwnershipEntityScan 创建返回 entity tenant_id 的 Scan 函数
func makeOwnershipEntityScan(tenantID *uuid.UUID) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) > 0 {
			if ptr, ok := dest[0].(**uuid.UUID); ok {
				*ptr = tenantID
			}
		}
		return nil
	}
}

// makeOwnershipEntityScanErr 创建返回 entity 查询错误的 Scan 函数
func makeOwnershipEntityScanErr() func(dest ...any) error {
	return func(dest ...any) error {
		return pgx.ErrNoRows
	}
}

// makeOwnershipAdapterScan 创建返回 adapter_provider status/adapter_type 的 Scan 函数
func makeOwnershipAdapterScan(status, adapterType string) func(dest ...any) error {
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

// makeOwnershipVaultScan 创建返回 vault 记录的 Scan 函数
func makeOwnershipVaultScan(
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

// makeOwnershipVaultScanErr 创建返回 vault 查询错误的 Scan 函数
func makeOwnershipVaultScanErr() func(dest ...any) error {
	return func(dest ...any) error {
		return pgx.ErrNoRows
	}
}

// TestCheckProviderOwnership_Success 验证合法数据校验成功并 Commit。
// 直接调用真实 CheckProviderOwnership，断言 Commit 被调用。
func TestCheckProviderOwnership_Success(t *testing.T) {
	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	provider := IssuingProvider{
		ID:                 poolUUID,
		EntityID:           &entityID,
		AdapterProviderID:  &adapterProviderID,
		CredentialVaultKey: vaultKey,
		ProviderCode:       "test-provider",
	}

	tx := &ownershipScriptedTx{
		queryRowResults: []func(dest ...any) error{
			// 1. entity tenant_id
			makeOwnershipEntityScan(&tenantID),
			// 2. adapter_provider status/adapter_type
			makeOwnershipAdapterScan("ACTIVE", "CARD_ISSUER"),
			// 3. vault record
			makeOwnershipVaultScan(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1),
		},
	}

	checker, fakeDB := newOwnershipCheckerWithFakeDB(tx)
	err := checker.CheckProviderOwnership(context.Background(), provider)
	require.NoError(t, err)
	assert.Equal(t, 1, tx.commitCalls, "Commit should be called on success")
	// R24 P1-1: 验证 BeginTx 使用 RepeatableRead + ReadOnly
	assert.True(t, fakeDB.hasBegin, "BeginTx should be called")
	assert.Equal(t, pgx.RepeatableRead, fakeDB.txOpts.IsoLevel, "IsoLevel must be RepeatableRead")
	assert.Equal(t, pgx.ReadOnly, fakeDB.txOpts.AccessMode, "AccessMode must be ReadOnly")
	// 注意：CheckProviderOwnership 使用 defer tx.Rollback(ctx)，即使成功也会调用 Rollback
	// 在真实 pgx 中，Commit 后的 Rollback 是 no-op；fake 中 Rollback 也会被调用但不影响正确性
}

// TestCheckProviderOwnership_VaultNotFound 验证 vault 记录不存在时失败并 Rollback。
func TestCheckProviderOwnership_VaultNotFound(t *testing.T) {
	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	provider := IssuingProvider{
		ID:                 poolUUID,
		EntityID:           &entityID,
		AdapterProviderID:  &adapterProviderID,
		CredentialVaultKey: vaultKey,
		ProviderCode:       "test-provider",
	}

	tx := &ownershipScriptedTx{
		queryRowResults: []func(dest ...any) error{
			makeOwnershipEntityScan(&tenantID),
			makeOwnershipAdapterScan("ACTIVE", "CARD_ISSUER"),
			makeOwnershipVaultScanErr(), // vault 不存在
		},
	}

	checker, fakeDB := newOwnershipCheckerWithFakeDB(tx)
	err := checker.CheckProviderOwnership(context.Background(), provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault credential not found")
	assert.Equal(t, 0, tx.commitCalls, "Commit should NOT be called on failure")
	assert.True(t, tx.rollbackCalls >= 1, "Rollback should be called on failure")
	assert.True(t, fakeDB.hasBegin, "BeginTx should be called")
	assert.Equal(t, pgx.RepeatableRead, fakeDB.txOpts.IsoLevel, "IsoLevel must be RepeatableRead")
	assert.Equal(t, pgx.ReadOnly, fakeDB.txOpts.AccessMode, "AccessMode must be ReadOnly")
}

// TestCheckProviderOwnership_CrossTenant 验证跨 tenant 时失败并 Rollback。
func TestCheckProviderOwnership_CrossTenant(t *testing.T) {
	entityTenantID := uuid.New()
	pathTenantID := uuid.New() // 不同于 entityTenantID
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + pathTenantID.String() + "/" + recordID.String() + "/api_key/1"

	provider := IssuingProvider{
		ID:                 poolUUID,
		EntityID:           &entityID,
		AdapterProviderID:  &adapterProviderID,
		CredentialVaultKey: vaultKey,
		ProviderCode:       "test-provider",
	}

	tx := &ownershipScriptedTx{
		queryRowResults: []func(dest ...any) error{
			makeOwnershipEntityScan(&entityTenantID),
			makeOwnershipAdapterScan("ACTIVE", "CARD_ISSUER"),
			makeOwnershipVaultScan(&recordID, "ACTIVE", "API_KEY", &pathTenantID, &poolUUID, &adapterProviderID, nil, 1),
		},
	}

	checker, fakeDB := newOwnershipCheckerWithFakeDB(tx)
	err := checker.CheckProviderOwnership(context.Background(), provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant")
	assert.Equal(t, 0, tx.commitCalls, "Commit should NOT be called on cross-tenant failure")
	assert.True(t, tx.rollbackCalls >= 1, "Rollback should be called on failure")
	assert.True(t, fakeDB.hasBegin, "BeginTx should be called")
	assert.Equal(t, pgx.RepeatableRead, fakeDB.txOpts.IsoLevel, "IsoLevel must be RepeatableRead")
	assert.Equal(t, pgx.ReadOnly, fakeDB.txOpts.AccessMode, "AccessMode must be ReadOnly")
}

// TestCheckProviderOwnership_WrongCapability 验证错误 capability 时失败并 Rollback。
func TestCheckProviderOwnership_WrongCapability(t *testing.T) {
	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	provider := IssuingProvider{
		ID:                 poolUUID,
		EntityID:           &entityID,
		AdapterProviderID:  &adapterProviderID,
		CredentialVaultKey: vaultKey,
		ProviderCode:       "test-provider",
	}

	tx := &ownershipScriptedTx{
		queryRowResults: []func(dest ...any) error{
			makeOwnershipEntityScan(&tenantID),
			makeOwnershipAdapterScan("ACTIVE", "CARD_ACQUIRER"), // 错误 capability
			makeOwnershipVaultScan(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1),
		},
	}

	checker, fakeDB := newOwnershipCheckerWithFakeDB(tx)
	err := checker.CheckProviderOwnership(context.Background(), provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CARD_ISSUER")
	assert.Equal(t, 0, tx.commitCalls, "Commit should NOT be called on wrong capability")
	assert.True(t, tx.rollbackCalls >= 1, "Rollback should be called on failure")
	assert.True(t, fakeDB.hasBegin, "BeginTx should be called")
	assert.Equal(t, pgx.RepeatableRead, fakeDB.txOpts.IsoLevel, "IsoLevel must be RepeatableRead")
	assert.Equal(t, pgx.ReadOnly, fakeDB.txOpts.AccessMode, "AccessMode must be ReadOnly")
}

// TestCheckProviderOwnership_RecordVersionMismatch 验证 record/version 不匹配时失败并 Rollback。
func TestCheckProviderOwnership_RecordVersionMismatch(t *testing.T) {
	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	differentRecordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	provider := IssuingProvider{
		ID:                 poolUUID,
		EntityID:           &entityID,
		AdapterProviderID:  &adapterProviderID,
		CredentialVaultKey: vaultKey,
		ProviderCode:       "test-provider",
	}

	tx := &ownershipScriptedTx{
		queryRowResults: []func(dest ...any) error{
			makeOwnershipEntityScan(&tenantID),
			makeOwnershipAdapterScan("ACTIVE", "CARD_ISSUER"),
			makeOwnershipVaultScan(&differentRecordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, nil, 1),
		},
	}

	checker, fakeDB := newOwnershipCheckerWithFakeDB(tx)
	err := checker.CheckProviderOwnership(context.Background(), provider)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "record_id")
	assert.Equal(t, 0, tx.commitCalls, "Commit should NOT be called on record mismatch")
	assert.True(t, tx.rollbackCalls >= 1, "Rollback should be called on failure")
	assert.True(t, fakeDB.hasBegin, "BeginTx should be called")
	assert.Equal(t, pgx.RepeatableRead, fakeDB.txOpts.IsoLevel, "IsoLevel must be RepeatableRead")
	assert.Equal(t, pgx.ReadOnly, fakeDB.txOpts.AccessMode, "AccessMode must be ReadOnly")
}

// TestCheckProviderOwnership_EntityNotFound 验证 entity 不存在时失败并 Rollback。
func TestCheckProviderOwnership_EntityNotFound(t *testing.T) {
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	tenantID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	provider := IssuingProvider{
		ID:                 poolUUID,
		EntityID:           &entityID,
		AdapterProviderID:  &adapterProviderID,
		CredentialVaultKey: vaultKey,
		ProviderCode:       "test-provider",
	}

	tx := &ownershipScriptedTx{
		queryRowResults: []func(dest ...any) error{
			makeOwnershipEntityScanErr(), // entity 不存在
		},
	}

	checker, fakeDB := newOwnershipCheckerWithFakeDB(tx)
	err := checker.CheckProviderOwnership(context.Background(), provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entity not found")
	assert.Equal(t, 0, tx.commitCalls, "Commit should NOT be called when entity not found")
	assert.True(t, tx.rollbackCalls >= 1, "Rollback should be called on failure")
	assert.True(t, fakeDB.hasBegin, "BeginTx should be called")
	assert.Equal(t, pgx.RepeatableRead, fakeDB.txOpts.IsoLevel, "IsoLevel must be RepeatableRead")
	assert.Equal(t, pgx.ReadOnly, fakeDB.txOpts.AccessMode, "AccessMode must be ReadOnly")
}

// TestCheckProviderOwnership_NoEntityID 验证缺少 entity_id 时失败。
func TestCheckProviderOwnership_NoEntityID(t *testing.T) {
	poolUUID := uuid.New()
	adapterProviderID := uuid.New()
	tenantID := uuid.New()
	recordID := uuid.New()

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	provider := IssuingProvider{
		ID:                 poolUUID,
		EntityID:           nil, // 缺少 entity_id
		AdapterProviderID:  &adapterProviderID,
		CredentialVaultKey: vaultKey,
		ProviderCode:       "test-provider",
	}

	tx := &ownershipScriptedTx{}

	checker, fakeDB := newOwnershipCheckerWithFakeDB(tx)
	err := checker.CheckProviderOwnership(context.Background(), provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entity_id")
	assert.Equal(t, 0, tx.commitCalls, "Commit should NOT be called when entity_id is nil")
	assert.True(t, tx.rollbackCalls >= 1, "Rollback should be called on failure")
	assert.True(t, fakeDB.hasBegin, "BeginTx should be called")
	assert.Equal(t, pgx.RepeatableRead, fakeDB.txOpts.IsoLevel, "IsoLevel must be RepeatableRead")
	assert.Equal(t, pgx.ReadOnly, fakeDB.txOpts.AccessMode, "AccessMode must be ReadOnly")
}

// TestCheckProviderOwnership_ExpiredVault 验证过期 vault 时失败并 Rollback。
func TestCheckProviderOwnership_ExpiredVault(t *testing.T) {
	tenantID := uuid.New()
	poolUUID := uuid.New()
	entityID := uuid.New()
	adapterProviderID := uuid.New()
	recordID := uuid.New()
	expiredTime := time.Now().Add(-1 * time.Hour)

	vaultKey := "vault://" + tenantID.String() + "/" + recordID.String() + "/api_key/1"

	provider := IssuingProvider{
		ID:                 poolUUID,
		EntityID:           &entityID,
		AdapterProviderID:  &adapterProviderID,
		CredentialVaultKey: vaultKey,
		ProviderCode:       "test-provider",
	}

	tx := &ownershipScriptedTx{
		queryRowResults: []func(dest ...any) error{
			makeOwnershipEntityScan(&tenantID),
			makeOwnershipAdapterScan("ACTIVE", "CARD_ISSUER"),
			makeOwnershipVaultScan(&recordID, "ACTIVE", "API_KEY", &tenantID, &poolUUID, &adapterProviderID, &expiredTime, 1),
		},
	}

	checker, fakeDB := newOwnershipCheckerWithFakeDB(tx)
	err := checker.CheckProviderOwnership(context.Background(), provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
	assert.Equal(t, 0, tx.commitCalls, "Commit should NOT be called on expired vault")
	assert.True(t, tx.rollbackCalls >= 1, "Rollback should be called on failure")
	assert.True(t, fakeDB.hasBegin, "BeginTx should be called")
	assert.Equal(t, pgx.RepeatableRead, fakeDB.txOpts.IsoLevel, "IsoLevel must be RepeatableRead")
	assert.Equal(t, pgx.ReadOnly, fakeDB.txOpts.AccessMode, "AccessMode must be ReadOnly")
}
