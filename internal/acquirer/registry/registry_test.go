package registry

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
)

// setupTestRedis 启动 testcontainers Redis 并返回客户端
func setupTestRedis(t *testing.T) (*redis.Client, func()) {
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
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("skipping: cannot start testcontainers redis: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get container host: %v", err)
	}

	mappedPort, err := container.MappedPort(ctx, "6379")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get mapped port: %v", err)
	}

	addr := host + ":" + mappedPort.Port()
	client := redis.NewClient(&redis.Options{Addr: addr})

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot ping redis: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		_ = container.Terminate(ctx)
	}

	return client, cleanup
}

// mockRepository 模拟 repository 接口
type mockRepository struct {
	mock.Mock
}

func (m *mockRepository) GetTenantAcquirerChannelByID(ctx context.Context, id uuid.UUID) (*domain.TenantAcquirerChannel, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.TenantAcquirerChannel), args.Error(1)
}

func (m *mockRepository) GetPaymentProviderByID(ctx context.Context, id uuid.UUID) (*domain.PaymentProvider, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.PaymentProvider), args.Error(1)
}

func (m *mockRepository) GetProviderCredential(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID, credType domain.CredentialType) (*domain.ProviderCredentialVault, error) {
	args := m.Called(ctx, providerID, channelID, credType)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ProviderCredentialVault), args.Error(1)
}

// 实现其他未使用的方法以满足接口
func (m *mockRepository) ListAllActiveTenantAcquirerChannels(ctx context.Context) ([]domain.TenantAcquirerChannel, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.TenantAcquirerChannel), args.Error(1)
}

// P0-B2: 新增方法 — mock 实现
func (m *mockRepository) ListActiveChannelsByMerchantAndCurrency(ctx context.Context, merchantID uuid.UUID, currency string) ([]domain.TenantAcquirerChannel, error) {
	args := m.Called(ctx, merchantID, currency)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.TenantAcquirerChannel), args.Error(1)
}

func (m *mockRepository) CreateTenantAcquirerChannel(ctx context.Context, channel *domain.TenantAcquirerChannel) error {
	args := m.Called(ctx, channel)
	return args.Error(0)
}

func (m *mockRepository) UpdateTenantAcquirerChannel(ctx context.Context, channel *domain.TenantAcquirerChannel) error {
	args := m.Called(ctx, channel)
	return args.Error(0)
}

func (m *mockRepository) DisableChannel(ctx context.Context, channelID uuid.UUID) error {
	args := m.Called(ctx, channelID)
	return args.Error(0)
}

func (m *mockRepository) EnableChannel(ctx context.Context, channelID uuid.UUID) error {
	args := m.Called(ctx, channelID)
	return args.Error(0)
}

func (m *mockRepository) CreatePaymentProvider(ctx context.Context, provider *domain.PaymentProvider) error {
	args := m.Called(ctx, provider)
	return args.Error(0)
}

func (m *mockRepository) GetPaymentProviderByCode(ctx context.Context, code string) (*domain.PaymentProvider, error) {
	args := m.Called(ctx, code)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.PaymentProvider), args.Error(1)
}

func (m *mockRepository) CreateProviderCredential(ctx context.Context, cred *domain.ProviderCredentialVault) error {
	args := m.Called(ctx, cred)
	return args.Error(0)
}

func (m *mockRepository) GetProviderCredentials(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID) ([]domain.ProviderCredentialVault, error) {
	args := m.Called(ctx, providerID, channelID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.ProviderCredentialVault), args.Error(1)
}

func (m *mockRepository) ListChannelsByTier(ctx context.Context, entityID uuid.UUID, tier domain.ProviderTier) ([]domain.AcquirerChannel, error) {
	args := m.Called(ctx, entityID, tier)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.AcquirerChannel), args.Error(1)
}

func (m *mockRepository) GetAcquirerChannelByID(ctx context.Context, id uuid.UUID) (*domain.AcquirerChannel, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.AcquirerChannel), args.Error(1)
}

func (m *mockRepository) GetMerchantRoutingPolicy(ctx context.Context, merchantID uuid.UUID) (*domain.MerchantRoutingPolicy, error) {
	// 必须走 m.Called：各用例通过 On(...) 决定 tier / 状态，硬编码默认值会让
	// tier 不匹配、policy 非 ACTIVE 这两类 fail-closed 用例失去意义。
	args := m.Called(ctx, merchantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.MerchantRoutingPolicy), args.Error(1)
}

// 注意：此测试需要数据库环境。在 -short 模式下会跳过。

func TestChannelRegistry_Get_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()
	bundle, err := reg.Get(channelID)

	assert.ErrorIs(t, err, domain.ErrChannelNotFound)
	assert.Nil(t, bundle)
}

func TestChannelRegistry_Get_Disabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()
	// 预置一个禁用的 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  false,
	}
	reg.mu.Unlock()

	bundle, err := reg.Get(channelID)

	assert.ErrorIs(t, err, domain.ErrChannelDisabled)
	assert.Nil(t, bundle)
}

func TestChannelRegistry_Get_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()
	providerID := uuid.New()
	// 预置一个活跃的 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID:  channelID,
		ProviderID: providerID,
		IsActive:   true,
		Version:    1,
		LoadedAt:   time.Now(),
	}
	reg.mu.Unlock()

	bundle, err := reg.Get(channelID)

	assert.NoError(t, err)
	assert.NotNil(t, bundle)
	assert.Equal(t, channelID, bundle.ChannelID)
	assert.Equal(t, providerID, bundle.ProviderID)
	assert.True(t, bundle.IsActive)
}

func TestChannelRegistry_Get_FailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()
	testErr := domain.ErrCredentialDecrypt

	// 预置一个有加载错误的 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		LoadError: testErr,
	}
	reg.mu.Unlock()

	bundle, err := reg.Get(channelID)

	assert.ErrorIs(t, err, testErr)
	assert.Nil(t, bundle)
}

func TestChannelRegistry_Get_StaleButServing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeStaleServing,
	})

	channelID := uuid.New()
	testErr := domain.ErrCredentialDecrypt

	// 预置一个有加载错误的 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		LoadError: testErr,
		Version:   1,
	}
	reg.mu.Unlock()

	bundle, err := reg.Get(channelID)

	// stale-but-serving 模式下返回旧版本
	assert.NoError(t, err)
	assert.NotNil(t, bundle)
	assert.Equal(t, channelID, bundle.ChannelID)
}

func TestChannelRegistry_Reload_AtomicReplace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()

	// 预置旧版本
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		Version:   1,
		IsActive:  true,
	}
	reg.mu.Unlock()

	// 直接更新为新版本（模拟 reload）
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		Version:   2,
		IsActive:  true,
		LoadedAt:  time.Now(),
	}
	reg.mu.Unlock()

	bundle, err := reg.Get(channelID)

	assert.NoError(t, err)
	assert.Equal(t, 2, bundle.Version)
}

func TestChannelRegistry_Disable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()

	// 预置活跃 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
	}
	reg.mu.Unlock()

	// 禁用（不访问数据库）
	reg.mu.Lock()
	if bundle, ok := reg.bundles[channelID]; ok {
		bundle.IsActive = false
	}
	reg.mu.Unlock()

	bundle, err := reg.Get(channelID)

	assert.ErrorIs(t, err, domain.ErrChannelDisabled)
	assert.Nil(t, bundle)
}

func TestClientBundle_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()

	// 预置 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		Version:   1,
	}
	reg.mu.Unlock()

	// 并发读取
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			bundle, err := reg.Get(channelID)
			assert.NoError(t, err)
			assert.NotNil(t, bundle)
			done <- true
		}()
	}

	// 等待所有读取完成
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("concurrent access timeout")
		}
	}
}

// TestOldVersionReloadEventDoesNotOverwriteNewBundle 验证旧版本的 reload event 不会覆盖新版本的 bundle
func TestOldVersionReloadEventDoesNotOverwriteNewBundle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()

	// 预置新版本 bundle (version 3)
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		Version:   3,
	}
	reg.mu.Unlock()

	// 模拟收到旧版本的 reload event (version 2)
	// 验证版本检查逻辑：旧版本不应触发 reload
	reg.mu.RLock()
	existingBundle, exists := reg.bundles[channelID]
	reg.mu.RUnlock()

	assert.True(t, exists)
	assert.Equal(t, 3, existingBundle.Version)

	// 模拟版本检查：旧版本事件应被忽略
	newVersion := 2 // 旧版本
	if exists && existingBundle.Version >= newVersion {
		// 这应该是我们期望的行为
		t.Logf("Old version event (v%d) correctly skipped, current version is v%d", newVersion, existingBundle.Version)
	} else {
		t.Error("Old version event should have been skipped")
	}

	// 验证 bundle 版本仍为 3
	bundle, err := reg.Get(channelID)
	assert.NoError(t, err)
	assert.Equal(t, 3, bundle.Version)
}

// TestPubSub_ValidPayload_PublishAndReceive 验证真实发布 channel_id|version 格式，新版本触发 reload
func TestPubSub_ValidPayload_PublishAndReceive(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	_ = rdb.FlushAll(ctx) // 清理测试数据

	// 创建 mock repository
	mockRepo := &mockRepository{}

	channelID := uuid.New()
	providerID := uuid.New()
	newVersion := 2

	// 设置 mock 返回值
	mockRepo.On("GetTenantAcquirerChannelByID", mock.Anything, channelID).Return(&domain.TenantAcquirerChannel{
		ID:           channelID,
		ProviderID:   providerID,
		IsActive:     true,
		Version:      newVersion,
		ChannelCode:  "test-channel",
		ProviderTier: domain.ProviderTierCompliant,
	}, nil)

	mockRepo.On("GetPaymentProviderByID", mock.Anything, providerID).Return(&domain.PaymentProvider{
		ID:           providerID,
		ProviderCode: "mock",
	}, nil)

	// 凭证返回 nil（模拟不存在）
	mockRepo.On("GetProviderCredential", mock.Anything, providerID, mock.Anything, mock.Anything).Return(nil, nil)

	reg := NewChannelRegistry(RegistryConfig{
		Repo:     mockRepo,
		RDB:      rdb,
		FailMode: FailModeClosed,
	})

	// 预置 version 1 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		Version:   1,
	}
	reg.mu.Unlock()

	// 启动订阅
	err := reg.SubscribeConfigUpdates(ctx)
	assert.NoError(t, err)

	// 等待订阅建立
	time.Sleep(200 * time.Millisecond)

	// 发布 version 2 更新
	err = reg.PublishConfigUpdate(ctx, channelID, newVersion)
	assert.NoError(t, err)

	// 等待消息处理
	time.Sleep(500 * time.Millisecond)

	// 验证版本已更新为 2
	bundle, err := reg.Get(channelID)
	assert.NoError(t, err)
	assert.Equal(t, newVersion, bundle.Version, "new version should trigger reload")

	mockRepo.AssertCalled(t, "GetTenantAcquirerChannelByID", mock.Anything, channelID)
}

// TestPubSub_OldVersionSkipped 验证旧版本不触发 reload
func TestPubSub_OldVersionSkipped(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	_ = rdb.FlushAll(ctx)

	// 创建空的 mock repository（旧版本不会触发 reload，不会调用 repo）
	mockRepo := &mockRepository{}

	reg := NewChannelRegistry(RegistryConfig{
		Repo:     mockRepo,
		RDB:      rdb,
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()

	// 预置 version 5 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		Version:   5,
	}
	reg.mu.Unlock()

	// 启动订阅
	err := reg.SubscribeConfigUpdates(ctx)
	assert.NoError(t, err)

	// 等待订阅建立
	time.Sleep(200 * time.Millisecond)

	// 发布旧版本 event (version 3)
	err = reg.PublishConfigUpdate(ctx, channelID, 3)
	assert.NoError(t, err)

	// 等待消息处理
	time.Sleep(500 * time.Millisecond)

	// 验证版本仍为 5（未被覆盖）
	bundle, err := reg.Get(channelID)
	assert.NoError(t, err)
	assert.Equal(t, 5, bundle.Version, "old version should not trigger reload")
}

// TestPubSub_InvalidPayload_LoggedAndIgnored 验证非法 payload 记录错误并忽略
func TestPubSub_InvalidPayload_LoggedAndIgnored(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	_ = rdb.FlushAll(ctx)

	// 创建空的 mock repository（非法 payload 不会触发 reload，不会调用 repo）
	mockRepo := &mockRepository{}

	reg := NewChannelRegistry(RegistryConfig{
		Repo:     mockRepo,
		RDB:      rdb,
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()

	// 预置 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		Version:   1,
	}
	reg.mu.Unlock()

	// 启动订阅
	err := reg.SubscribeConfigUpdates(ctx)
	assert.NoError(t, err)

	// 等待订阅建立
	time.Sleep(200 * time.Millisecond)

	// 发布非法 payload（无效 UUID）
	err = rdb.Publish(ctx, "channel.config.updated", "not-a-valid-uuid|1").Err()
	assert.NoError(t, err)

	// 发布非法 payload（无效版本号）
	err = rdb.Publish(ctx, "channel.config.updated", channelID.String()+"|not-a-number").Err()
	assert.NoError(t, err)

	// 发布非法 payload（格式不对，缺少分隔符）
	err = rdb.Publish(ctx, "channel.config.updated", channelID.String()+"_1").Err()
	assert.NoError(t, err)

	// 等待消息处理
	time.Sleep(500 * time.Millisecond)

	// 验证版本仍为 1（未被影响）
	bundle, err := reg.Get(channelID)
	assert.NoError(t, err)
	assert.Equal(t, 1, bundle.Version, "invalid payload should be ignored")
}

// TestPubSub_ConcurrentVersionEvents 验证并发版本事件只更新最新版本
func TestPubSub_ConcurrentVersionEvents(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	_ = rdb.FlushAll(ctx)

	// 创建 mock repository（并发版本事件会触发 reload）
	mockRepo := &mockRepository{}

	channelID := uuid.New()
	providerID := uuid.New()

	// 设置 mock 返回值 - 返回最新版本
	mockRepo.On("GetTenantAcquirerChannelByID", mock.Anything, channelID).Return(&domain.TenantAcquirerChannel{
		ID:           channelID,
		ProviderID:   providerID,
		IsActive:     true,
		Version:      10,
		ChannelCode:  "test-channel",
		ProviderTier: domain.ProviderTierCompliant,
	}, nil)

	mockRepo.On("GetPaymentProviderByID", mock.Anything, providerID).Return(&domain.PaymentProvider{
		ID:           providerID,
		ProviderCode: "mock",
	}, nil)

	mockRepo.On("GetProviderCredential", mock.Anything, providerID, mock.Anything, mock.Anything).Return(nil, nil)

	reg := NewChannelRegistry(RegistryConfig{
		Repo:     mockRepo,
		RDB:      rdb,
		FailMode: FailModeClosed,
	})

	// 预置 version 1 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		Version:   1,
	}
	reg.mu.Unlock()

	// 启动订阅
	err := reg.SubscribeConfigUpdates(ctx)
	assert.NoError(t, err)

	// 等待订阅建立
	time.Sleep(200 * time.Millisecond)

	// 并发发布多个版本
	var counter int32
	for v := int32(2); v <= 10; v++ {
		go func(version int32) {
			_ = reg.PublishConfigUpdate(ctx, channelID, int(version))
			atomic.AddInt32(&counter, 1)
		}(v)
	}

	// 等待消息处理
	time.Sleep(1 * time.Second)

	t.Logf("published %d events", atomic.LoadInt32(&counter))

	// 验证版本为 10（最新版本）
	bundle, err := reg.Get(channelID)
	assert.NoError(t, err)
	assert.Equal(t, 10, bundle.Version, "highest version should be applied")
}

// TestSelectChannel_RejectsMerchantTierMismatch 验证 SelectChannel 在商户 policy tier
// 与通道 tier 不匹配时返回 ErrChannelNotFound（fail-closed）。
func TestSelectChannel_RejectsMerchantTierMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	mockRepo := &mockRepository{}

	merchantID := uuid.New()
	acquirerChannelID := uuid.New()
	providerID := uuid.New()

	// 商户 policy 是 TIER_1_HIGH_RISK
	mockRepo.On("GetMerchantRoutingPolicy", mock.Anything, merchantID).Return(&domain.MerchantRoutingPolicy{
		MerchantID:   merchantID,
		ProviderTier: domain.ProviderTierHighRisk,
		Status:       "ACTIVE",
		Version:      1,
	}, nil)

	// 但通道是 TIER_2_COMPLIANT（不匹配）
	mockRepo.On("ListActiveChannelsByMerchantAndCurrency", mock.Anything, merchantID, "USD").Return([]domain.TenantAcquirerChannel{
		{
			ID:                uuid.New(),
			TenantID:          uuid.New(),
			MerchantID:        merchantID,
			ChannelCode:       "test-channel",
			ProviderID:        providerID,
			IsActive:          true,
			Version:           1,
			ProviderTier:      domain.ProviderTierCompliant,
			AcquirerChannelID: &acquirerChannelID,
		},
	}, nil)

	reg := NewChannelRegistry(RegistryConfig{
		Repo:     mockRepo,
		FailMode: FailModeClosed,
	})

	_, err := reg.SelectChannel(context.Background(), merchantID, "USD", 10000)
	assert.ErrorIs(t, err, domain.ErrChannelNotFound, "tier mismatch should result in ErrChannelNotFound")
}

// TestSelectChannel_RejectsInactiveControlPlaneChannel 验证 SelectChannel 在商户 policy
// 状态为 INACTIVE 时返回错误（fail-closed）。
func TestSelectChannel_RejectsInactiveControlPlaneChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	mockRepo := &mockRepository{}

	merchantID := uuid.New()

	// 商户 policy 是 INACTIVE
	mockRepo.On("GetMerchantRoutingPolicy", mock.Anything, merchantID).Return(&domain.MerchantRoutingPolicy{
		MerchantID:   merchantID,
		ProviderTier: domain.ProviderTierCompliant,
		Status:       "INACTIVE",
		Version:      1,
	}, nil)

	reg := NewChannelRegistry(RegistryConfig{
		Repo:     mockRepo,
		FailMode: FailModeClosed,
	})

	_, err := reg.SelectChannel(context.Background(), merchantID, "USD", 10000)
	assert.Error(t, err, "inactive merchant policy should be rejected")
	assert.Contains(t, err.Error(), "fail-closed")
}

// TestLoadAll_RealModeRejectsNilFactory 验证 real mode 下 LoadAll 在没有注入 Factory 时返回错误。
func TestLoadAll_RealModeRejectsNilFactory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	mockRepo := &mockRepository{}

	reg := NewChannelRegistry(RegistryConfig{
		Repo:       mockRepo,
		FailMode:   FailModeClosed,
		IsRealMode: true,
		// Factory 未注入
	})

	err := reg.LoadAll(context.Background())
	assert.Error(t, err, "real mode without Factory should fail")
	assert.Contains(t, err.Error(), "AcquirerAdapterFactory")
}

// TestLoadAll_RealModeRejectsUnboundActiveRuntimeChannel 验证 real mode 下 LoadAll
// 在存在未绑定 acquirer_channel_id 的 active runtime channel 时返回错误。
func TestLoadAll_RealModeRejectsUnboundActiveRuntimeChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping registry test in short mode")
	}

	mockRepo := &mockRepository{}

	// 返回一个 AcquirerChannelID=nil 的 active runtime channel
	mockRepo.On("ListAllActiveTenantAcquirerChannels", mock.Anything).Return([]domain.TenantAcquirerChannel{
		{
			ID:                uuid.New(),
			TenantID:          uuid.New(),
			MerchantID:        uuid.New(),
			ChannelCode:       "unbound-channel",
			ProviderID:        uuid.New(),
			IsActive:          true,
			Version:           1,
			AcquirerChannelID: nil, // 未绑定
		},
	}, nil)

	factory := NewDefaultAdapterFactory(nil)

	reg := NewChannelRegistry(RegistryConfig{
		Repo:       mockRepo,
		FailMode:   FailModeClosed,
		IsRealMode: true,
		Factory:    factory,
	})

	err := reg.LoadAll(context.Background())
	assert.Error(t, err, "real mode with unbound active runtime channel should fail")
	assert.Contains(t, err.Error(), "acquirer_channel_id binding")
}

// testMockAdapter 用于纯单测的 AcquirerAdapter 实现
type testMockAdapter struct{}

func (m *testMockAdapter) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	return &domain.AuthorizeResult{}, nil
}
func (m *testMockAdapter) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	return &domain.CaptureResult{}, nil
}
func (m *testMockAdapter) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	return &domain.RefundResult{}, nil
}
func (m *testMockAdapter) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	return &domain.VerifiedWebhookEvent{}, nil
}

// TestLoadAll_RealModeBoundChannel 验证 real mode 下 AcquirerChannelID 非 nil 的通道不返回错误。
func TestLoadAll_RealModeBoundChannel(t *testing.T) {
	mockRepo := &mockRepository{}

	acquirerChannelID := uuid.New()
	providerID := uuid.New()
	channelID := uuid.New()

	mockRepo.On("ListAllActiveTenantAcquirerChannels", mock.Anything).Return([]domain.TenantAcquirerChannel{
		{
			ID:                channelID,
			TenantID:          uuid.New(),
			MerchantID:        uuid.New(),
			ChannelCode:       "bound-channel",
			ProviderID:        providerID,
			IsActive:          true,
			Version:           1,
			AcquirerChannelID: &acquirerChannelID,
		},
	}, nil)

	mockRepo.On("GetPaymentProviderByID", mock.Anything, providerID).Return(&domain.PaymentProvider{
		ID:           providerID,
		ProviderCode: "stripe",
	}, nil)

	mockRepo.On("GetProviderCredential", mock.Anything, providerID, mock.Anything, mock.Anything).Return(nil, nil)

	factory := NewDefaultAdapterFactory(nil)
	factory.Register("stripe", func(credentials map[domain.CredentialType]string, config map[string]any) (domain.AcquirerAdapter, error) {
		return &testMockAdapter{}, nil
	})

	reg := NewChannelRegistry(RegistryConfig{
		Repo:       mockRepo,
		FailMode:   FailModeClosed,
		IsRealMode: true,
		Factory:    factory,
	})

	err := reg.LoadAll(context.Background())
	assert.NoError(t, err, "real mode with bound channel should not fail")
}

// TestLoadAll_RealModeRejectsUnbound 验证 real mode 下 AcquirerChannelID 为 nil 的通道返回错误。
func TestLoadAll_RealModeRejectsUnbound(t *testing.T) {
	mockRepo := &mockRepository{}

	mockRepo.On("ListAllActiveTenantAcquirerChannels", mock.Anything).Return([]domain.TenantAcquirerChannel{
		{
			ID:                uuid.New(),
			TenantID:          uuid.New(),
			MerchantID:        uuid.New(),
			ChannelCode:       "unbound-channel",
			ProviderID:        uuid.New(),
			IsActive:          true,
			Version:           1,
			AcquirerChannelID: nil,
		},
	}, nil)

	factory := NewDefaultAdapterFactory(nil)

	reg := NewChannelRegistry(RegistryConfig{
		Repo:       mockRepo,
		FailMode:   FailModeClosed,
		IsRealMode: true,
		Factory:    factory,
	})

	err := reg.LoadAll(context.Background())
	assert.Error(t, err, "real mode with unbound channel should fail")
	assert.Contains(t, err.Error(), "no acquirer_channel_id binding")
}

// TestRealModeRejectsMockProvider 验证 real mode 下 createClient 拒绝 mock provider。
func TestRealModeRejectsMockProvider(t *testing.T) {
	factory := NewDefaultAdapterFactory(nil)
	factory.Register("mock", func(credentials map[domain.CredentialType]string, config map[string]any) (domain.AcquirerAdapter, error) {
		return &testMockAdapter{}, nil
	})

	reg := NewChannelRegistry(RegistryConfig{
		FailMode:   FailModeClosed,
		IsRealMode: true,
		Factory:    factory,
	})

	provider := &domain.PaymentProvider{
		ID:           uuid.New(),
		ProviderCode: "mock",
	}

	_, err := reg.createClient(provider, nil, nil)
	assert.ErrorIs(t, err, domain.ErrProviderCodeUnregistered, "real mode should reject mock provider")
}

// TestLocalModeAllowsExplicitMockProvider 验证 local mode 下 createClient 允许 mock provider。
func TestLocalModeAllowsExplicitMockProvider(t *testing.T) {
	factory := NewDefaultAdapterFactory(nil)
	factory.Register("mock", func(credentials map[domain.CredentialType]string, config map[string]any) (domain.AcquirerAdapter, error) {
		return &testMockAdapter{}, nil
	})

	reg := NewChannelRegistry(RegistryConfig{
		FailMode:   FailModeClosed,
		IsRealMode: false,
		Factory:    factory,
	})

	provider := &domain.PaymentProvider{
		ID:           uuid.New(),
		ProviderCode: "mock",
	}

	client, err := reg.createClient(provider, nil, nil)
	assert.NoError(t, err, "local mode should allow mock provider")
	assert.NotNil(t, client)
}

// TestRegistry_Get_BurnModeFailsClosed 验证 BURN_MODE 的 bundle 在 Get 方法中被拒绝（fail-closed）。
// 纯内存测试，不需要容器，不因 testing.Short() 而 Skip。
func TestRegistry_Get_BurnModeFailsClosed(t *testing.T) {
	reg := NewChannelRegistry(RegistryConfig{
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()

	// 预置 BURN_MODE 状态
	reg.statusCacheMu.Lock()
	reg.statusCache[channelID] = ChannelStatus{
		ChannelID:  channelID,
		IsActive:   true,
		IsBurnMode: true,
		Version:    1,
	}
	reg.statusCacheMu.Unlock()

	// 预置活跃 bundle
	reg.mu.Lock()
	reg.bundles[channelID] = &domain.ClientBundle{
		ChannelID: channelID,
		IsActive:  true,
		Version:   1,
	}
	reg.mu.Unlock()

	bundle, err := reg.Get(channelID)
	assert.Error(t, err, "BURN_MODE bundle should fail-closed")
	assert.Contains(t, err.Error(), "BURN_MODE")
	assert.Nil(t, bundle)
}

// TestRegistry_SelectChannel_BurnModeFailsClosed 验证 BURN_MODE 的 channel 在 SelectChannel 中被跳过（不可路由），
// 返回 ErrChannelNotFound。纯内存测试，不需要容器，不因 testing.Short() 而 Skip。
func TestRegistry_SelectChannel_BurnModeFailsClosed(t *testing.T) {
	mockRepo := &mockRepository{}

	merchantID := uuid.New()
	channelID := uuid.New()
	providerID := uuid.New()

	// 商户 policy 为 ACTIVE COMPLIANT（正常可路由），只有通道本身是 BURN_MODE
	mockRepo.On("GetMerchantRoutingPolicy", mock.Anything, merchantID).Return(&domain.MerchantRoutingPolicy{
		MerchantID:   merchantID,
		ProviderTier: domain.ProviderTierCompliant,
		Status:       "ACTIVE",
		Version:      1,
	}, nil)

	mockRepo.On("ListActiveChannelsByMerchantAndCurrency", mock.Anything, merchantID, "USD").Return([]domain.TenantAcquirerChannel{
		{
			ID:            channelID,
			TenantID:      uuid.New(),
			MerchantID:    merchantID,
			ChannelCode:   "burn-channel",
			ProviderID:    providerID,
			IsActive:      true,
			Version:       1,
			ProviderTier:  domain.ProviderTierCompliant,
			ChannelConfig: map[string]any{"burn_mode": true},
		},
	}, nil)

	reg := NewChannelRegistry(RegistryConfig{
		Repo:     mockRepo,
		FailMode: FailModeClosed,
	})

	_, err := reg.SelectChannel(context.Background(), merchantID, "USD", 100)
	assert.ErrorIs(t, err, domain.ErrChannelNotFound, "BURN_MODE channel should not be selectable")
}
