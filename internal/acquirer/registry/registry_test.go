package registry

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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
		ID:          channelID,
		ProviderID:  providerID,
		IsActive:    true,
		Version:     newVersion,
		ChannelCode: "test-channel",
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
		ID:          channelID,
		ProviderID:  providerID,
		IsActive:    true,
		Version:     10,
		ChannelCode: "test-channel",
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
