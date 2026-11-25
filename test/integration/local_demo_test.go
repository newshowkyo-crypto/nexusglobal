package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/acquirer/repository"
	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/pipeline"
)

// setupTestPostgres 启动 testcontainers Postgres 并返回连接池
func setupTestPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}

	ctx := context.Background()
	// OPS-007: 外部共享数据库优先（TEST_DATABASE_DSN），testcontainers 兜底。
	if dsn := os.Getenv("TEST_DATABASE_DSN"); dsn != "" {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("connect external TEST_DATABASE_DSN: %v", err)
		}
		if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO public;`); err != nil {
			t.Fatalf("reset external database: %v", err)
		}
		return pool, func() { pool.Close() }
	}
	req := testcontainers.ContainerRequest{
		Image: "postgres:16-alpine",
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "test",
		},
		ExposedPorts: []string{"5432/tcp"},
		WaitingFor:   wait.ForListeningPort("5432/tcp").WithStartupTimeout(120 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("skipping: cannot start testcontainers postgres: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get container host: %v", err)
	}

	mappedPort, err := container.MappedPort(ctx, "5432")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get mapped port: %v", err)
	}

	dsn := fmt.Sprintf("postgres://test:test@%s:%s/test?sslmode=disable", host, mappedPort.Port())
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot create pool: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot ping postgres: %v", err)
	}

	cleanup := func() {
		pool.Close()
		_ = container.Terminate(ctx)
	}

	return pool, cleanup
}

// seedTierRouting 补齐 tier 模型所需数据（P0-B2 之后选路要求商户 tier policy +
// tenant_acquirer_channel 绑定 acquirer_channels）。
// 返回 acquirer_channels.id。
func seedTierRouting(t *testing.T, pool *pgxpool.Pool, tenantID, merchantID, providerID uuid.UUID, channelCode string) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, status)
		VALUES ($1, $2, 'Demo Merchant', 'Demo Merchant Ltd', 'US', 'ACTIVE')
		ON CONFLICT (id) DO NOTHING
	`, merchantID, tenantID)
	require.NoError(t, err)

	acquirerChannelID := uuid.New()
	err = pool.QueryRow(ctx, `
		INSERT INTO acquirer_channels
		    (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, status)
		VALUES ($1, $2, 'TIER_2_COMPLIANT', 'mock', $3, 'vault-mock', 'ACTIVE')
		ON CONFLICT (entity_id, channel_code) DO UPDATE SET status = 'ACTIVE'
		RETURNING id
	`, acquirerChannelID, merchantID, channelCode).Scan(&acquirerChannelID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO merchant_routing_policy (merchant_id, provider_tier, status)
		VALUES ($1, 'TIER_2_COMPLIANT', 'ACTIVE')
		ON CONFLICT (merchant_id) DO UPDATE SET status = 'ACTIVE'
	`, merchantID)
	require.NoError(t, err)

	return acquirerChannelID
}

// TestLocalDemo_FullFlow 完整的本地演示流程测试
// 测试从 migration -> seed -> registry -> checkout 的完整链路
func TestLocalDemo_FullFlow(t *testing.T) {
	pool, cleanup := setupTestPostgres(t)
	defer cleanup()
	ctx := context.Background()

	// 1. 应用完整迁移（registry 依赖 000028 引入的 acquirer_channels，
	//    只跑 000017 会导致 LoadAll 报表不存在）
	runMigrations(t, pool)
	log.Println("Migrations applied successfully")

	// 2. 执行 seed 逻辑
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	merchantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	// 创建 mock provider
	channelConfig := map[string]interface{}{
		"scenarios": map[string]string{
			"order_success": "success",
			"order_fail":    "failure",
			"order_timeout": "timeout",
		},
		"timeout_ms": 100,
	}
	channelConfigJSON, _ := json.Marshal(channelConfig)

	mockProviderID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO payment_provider (id, provider_code, provider_name, provider_type, is_active, version, created_at, updated_at)
		VALUES ($1, 'mock', 'Mock Provider', 'CARD', true, 1, $2, $2)
		ON CONFLICT (provider_code) DO UPDATE SET
			provider_name = EXCLUDED.provider_name,
			provider_type = EXCLUDED.provider_type,
			is_active = EXCLUDED.is_active,
			updated_at = $2
	`, mockProviderID, time.Now())
	require.NoError(t, err)

	var existingProviderID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM payment_provider WHERE provider_code = 'mock'`).Scan(&existingProviderID)
	require.NoError(t, err)
	log.Printf("Mock provider created: %s", existingProviderID)

	// 创建 mock channel（并补齐 tier policy / acquirer_channels 绑定）
	acquirerChannelID := seedTierRouting(t, pool, tenantID, merchantID, existingProviderID, "mock_default")
	mockChannelID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO tenant_acquirer_channel (
			id, tenant_id, merchant_id, provider_id, channel_code, channel_name,
			payment_method, currencies, channel_config, is_active, version, created_at, updated_at,
			acquirer_channel_id
		)
		VALUES ($1, $2, $3, $4, 'mock_default', 'Mock Default Channel',
			'card', '["USD","EUR","GBP"]', $5, true, 1, $6, $6, $7)
		ON CONFLICT (tenant_id, merchant_id, channel_code) DO UPDATE SET
			channel_name = EXCLUDED.channel_name,
			payment_method = EXCLUDED.payment_method,
			currencies = EXCLUDED.currencies,
			channel_config = EXCLUDED.channel_config,
			is_active = EXCLUDED.is_active,
			acquirer_channel_id = EXCLUDED.acquirer_channel_id,
			updated_at = $6
	`, mockChannelID, tenantID, merchantID, existingProviderID, channelConfigJSON, time.Now(), acquirerChannelID)
	require.NoError(t, err)

	var existingChannelID uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT id FROM tenant_acquirer_channel
		WHERE tenant_id = $1 AND merchant_id = $2 AND channel_code = 'mock_default'
	`, tenantID, merchantID).Scan(&existingChannelID)
	require.NoError(t, err)
	log.Printf("Mock channel created: %s", existingChannelID)

	// 3. 初始化 ChannelRegistry
	acqRepo := repository.New(pool)
	acqReg := registry.NewChannelRegistry(registry.RegistryConfig{
		Repo:      acqRepo,
		Encryptor: nil, // mock 模式不需要加密
		RDB:       nil, // mock 模式不需要 Redis
		Logger:    nil,
		FailMode:  registry.FailModeStaleServing,
	})

	// LoadAll 预热
	err = acqReg.LoadAll(ctx)
	require.NoError(t, err)

	channels := acqReg.ListAll()
	assert.NotEmpty(t, channels, "应该有至少一个 channel")
	log.Printf("Registry loaded with %d channels", len(channels))

	// 验证 mock_default channel 存在
	var found bool
	for _, ch := range channels {
		if ch.ChannelCode == "mock_default" && ch.IsActive {
			found = true
			log.Printf("Found mock_default channel: ID=%s, Active=%v", ch.ChannelID, ch.IsActive)
			break
		}
	}
	assert.True(t, found, "应该找到 mock_default channel")

	// 4. 使用 AcquirerGateway 进行 checkout 测试
	pipelineSvc := pipeline.NewService(nil, time.Now)
	gw := checkout.NewAcquirerGateway(acqReg, pipelineSvc, false) // isReal=false

	// 测试 order_success -> CAPTURED
	result, err := gw.Charge(ctx, checkout.PaymentRequest{
		OrderID:           "order_success",
		MerchantID:        merchantID.String(),
		Amount:            decimal.NewFromFloat(100.00),
		Currency:          "USD",
		AcquirerChannelID: existingChannelID,
	})
	require.NoError(t, err, "order_success 应该成功")
	require.NotNil(t, result, "order_success 应该返回结果")
	assert.Equal(t, "CAPTURED", result.Status, "order_success 应该返回 CAPTURED")
	log.Printf("order_success -> %s (expected: CAPTURED)", result.Status)

	// 测试 order_fail -> ErrPaymentFailed
	_, err = gw.Charge(ctx, checkout.PaymentRequest{
		OrderID:           "order_fail",
		MerchantID:        merchantID.String(),
		Amount:            decimal.NewFromFloat(100.00),
		Currency:          "USD",
		AcquirerChannelID: existingChannelID,
	})
	require.Error(t, err, "order_fail 应该返回错误")
	assert.True(t, errors.Is(err, checkout.ErrPaymentFailed), "order_fail 应该返回 ErrPaymentFailed")
	log.Printf("order_fail -> ErrPaymentFailed (expected)")

	// 测试 order_timeout -> ErrPaymentTimeout
	_, err = gw.Charge(ctx, checkout.PaymentRequest{
		OrderID:           "order_timeout",
		MerchantID:        merchantID.String(),
		Amount:            decimal.NewFromFloat(100.00),
		Currency:          "USD",
		AcquirerChannelID: existingChannelID,
	})
	require.Error(t, err, "order_timeout 应该返回错误")
	assert.True(t, errors.Is(err, checkout.ErrPaymentTimeout), "order_timeout 应该返回 ErrPaymentTimeout")
	log.Printf("order_timeout -> ErrPaymentTimeout (expected)")

	log.Println("All local demo tests passed!")
}

// TestAcquirerGateway_RealModeValidation 测试 real 模式验证
func TestAcquirerGateway_RealModeValidation(t *testing.T) {
	pool, cleanup := setupTestPostgres(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 pipeline 服务
	pipelineSvc := pipeline.NewService(nil, time.Now)

	// 完整迁移：registry.LoadAll 依赖 000028 的 acquirer_channels
	runMigrations(t, pool)

	// 创建 mock channel
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	merchantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	mockProviderID := uuid.New()
	mockChannelID := uuid.New()

	channelConfig := map[string]interface{}{
		"scenarios": map[string]string{
			"order_success": "success",
		},
		"timeout_ms": 100,
	}
	channelConfigJSON, _ := json.Marshal(channelConfig)

	_, err := pool.Exec(ctx, `
		INSERT INTO payment_provider (id, provider_code, provider_name, provider_type, is_active, version, created_at, updated_at)
		VALUES ($1, 'mock', 'Mock Provider', 'CARD', true, 1, $2, $2)
	`, mockProviderID, time.Now())
	require.NoError(t, err)

	// tier policy + acquirer_channels 绑定
	acquirerChannelID := seedTierRouting(t, pool, tenantID, merchantID, mockProviderID, "mock_default")
	_, err = pool.Exec(ctx, `
		INSERT INTO tenant_acquirer_channel (
			id, tenant_id, merchant_id, channel_code, provider_id,
			channel_name, payment_method, currencies, channel_config, is_active, version, created_at, updated_at,
			acquirer_channel_id
		)
		VALUES ($1, $2, $3, 'mock_default', $4,
			'Mock Channel', 'card', '["USD"]', $5, true, 1, $6, $6, $7)
	`, mockChannelID, tenantID, merchantID, mockProviderID, channelConfigJSON, time.Now(), acquirerChannelID)
	require.NoError(t, err)

	// 初始化 registry
	acqRepo := repository.New(pool)
	acqReg := registry.NewChannelRegistry(registry.RegistryConfig{
		Repo:     acqRepo,
		FailMode: registry.FailModeClosed,
	})
	err = acqReg.LoadAll(ctx)
	require.NoError(t, err)

	// 测试 Real 模式拒绝 raw card
	t.Run("RealMode_RejectsRawCard", func(t *testing.T) {
		gw := checkout.NewAcquirerGateway(acqReg, pipelineSvc, true) // isReal=true
		_, err := gw.Charge(ctx, checkout.PaymentRequest{
			OrderID:    "test_order",
			MerchantID: merchantID.String(),
			Amount:     decimal.NewFromFloat(100.00),
			Currency:   "USD",
			CardNumber: "4242424242424242",
		})
		assert.Error(t, err)
		assert.True(t, errors.Is(err, checkout.ErrRawCardNotAllowed))
	})

	// 测试 Real 模式要求 payment_method_token
	t.Run("RealMode_RequiresPaymentMethodToken", func(t *testing.T) {
		gw := checkout.NewAcquirerGateway(acqReg, pipelineSvc, true) // isReal=true
		_, err := gw.Charge(ctx, checkout.PaymentRequest{
			OrderID:    "test_order",
			MerchantID: merchantID.String(),
			Amount:     decimal.NewFromFloat(100.00),
			Currency:   "USD",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "payment_method_token required")
	})

	// 测试 Mock 模式允许 raw card
	t.Run("MockMode_AllowsRawCard", func(t *testing.T) {
		gw := checkout.NewAcquirerGateway(acqReg, pipelineSvc, false) // isReal=false
		result, err := gw.Charge(ctx, checkout.PaymentRequest{
			OrderID:           "order_success",
			MerchantID:        merchantID.String(),
			Amount:            decimal.NewFromFloat(100.00),
			Currency:          "USD",
			CardNumber:        "4242424242424242",
			AcquirerChannelID: mockChannelID,
		})
		assert.NoError(t, err)
		assert.Equal(t, "CAPTURED", result.Status) // 修正：应该是 CAPTURED 不是 success
	})
}
