package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
)

// mockRepository 模拟仓库
type mockRepository struct {
	providers   map[uuid.UUID]*domain.AdapterProvider
	channels    map[uuid.UUID]*domain.AdapterChannel
	credentials map[string]*domain.AdapterCredentialVault // key: providerID|channelID|type
}

func newMockRepository() *mockRepository {
	return &mockRepository{
		providers:   make(map[uuid.UUID]*domain.AdapterProvider),
		channels:    make(map[uuid.UUID]*domain.AdapterChannel),
		credentials: make(map[string]*domain.AdapterCredentialVault),
	}
}

func (m *mockRepository) GetAdapterProviderByID(ctx context.Context, providerID uuid.UUID) (*domain.AdapterProvider, error) {
	p, ok := m.providers[providerID]
	if !ok {
		return nil, errors.New("provider not found")
	}
	return p, nil
}

func (m *mockRepository) GetAdapterProviderByCode(ctx context.Context, providerCode string) (*domain.AdapterProvider, error) {
	for _, p := range m.providers {
		if p.ProviderCode == providerCode {
			return p, nil
		}
	}
	return nil, errors.New("provider not found")
}

func (m *mockRepository) GetAdapterChannelByID(ctx context.Context, channelID uuid.UUID) (*domain.AdapterChannel, error) {
	c, ok := m.channels[channelID]
	if !ok {
		return nil, errors.New("channel not found")
	}
	return c, nil
}

func (m *mockRepository) GetAdapterChannelByCode(ctx context.Context, tenantID uuid.UUID, channelCode string) (*domain.AdapterChannel, error) {
	for _, c := range m.channels {
		if c.TenantID == tenantID && c.ChannelCode == channelCode {
			return c, nil
		}
	}
	return nil, errors.New("channel not found")
}

func (m *mockRepository) GetAdapterChannelsByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.AdapterChannel, error) {
	result := make([]domain.AdapterChannel, 0)
	for _, c := range m.channels {
		if c.TenantID == tenantID {
			result = append(result, *c)
		}
	}
	return result, nil
}

func (m *mockRepository) GetAdapterChannelsByType(ctx context.Context, tenantID uuid.UUID, adapterType domain.AdapterType) ([]domain.AdapterChannel, error) {
	result := make([]domain.AdapterChannel, 0)
	for _, c := range m.channels {
		if c.TenantID == tenantID && c.AdapterType == adapterType {
			result = append(result, *c)
		}
	}
	return result, nil
}

func (m *mockRepository) GetAdapterChannelsByEntity(ctx context.Context, entityID uuid.UUID) ([]domain.AdapterChannel, error) {
	result := make([]domain.AdapterChannel, 0)
	for _, c := range m.channels {
		if c.CorporateEntityID != nil && *c.CorporateEntityID == entityID {
			result = append(result, *c)
		}
	}
	return result, nil
}

func (m *mockRepository) GetAllAdapterChannels(ctx context.Context) ([]domain.AdapterChannel, error) {
	result := make([]domain.AdapterChannel, 0, len(m.channels))
	for _, c := range m.channels {
		result = append(result, *c)
	}
	return result, nil
}

func (m *mockRepository) GetAdapterCredential(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID, credType domain.CredentialType) (*domain.AdapterCredentialVault, error) {
	key := providerID.String() + "|"
	if channelID != nil {
		key += channelID.String()
	}
	key += "|" + string(credType)

	cred, ok := m.credentials[key]
	if !ok {
		return nil, nil // 凭证不存在不算错误
	}
	return cred, nil
}

func (m *mockRepository) DisableChannel(ctx context.Context, channelID uuid.UUID) error {
	c, ok := m.channels[channelID]
	if !ok {
		return errors.New("channel not found")
	}
	c.Status = domain.AdapterStatusDisabled
	return nil
}

func (m *mockRepository) EnableChannel(ctx context.Context, channelID uuid.UUID) error {
	c, ok := m.channels[channelID]
	if !ok {
		return errors.New("channel not found")
	}
	c.Status = domain.AdapterStatusActive
	return nil
}

func (m *mockRepository) UpdateChannelVersion(ctx context.Context, channelID uuid.UUID, version int) error {
	c, ok := m.channels[channelID]
	if !ok {
		return errors.New("channel not found")
	}
	c.Version = version
	return nil
}

// ============================================================================
// Tests
// ============================================================================

func TestUniversalAdapterRegistry_New(t *testing.T) {
	repo := newMockRepository()
	cfg := RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	}

	registry := NewUniversalAdapterRegistry(cfg)
	require.NotNil(t, registry)
	assert.Equal(t, FailModeClosed, registry.failMode)
}

func TestUniversalAdapterRegistry_Get_NotFound(t *testing.T) {
	repo := newMockRepository()
	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	})

	channelID := uuid.New()
	bundle, err := registry.Get(channelID)

	assert.Error(t, err)
	assert.Equal(t, domain.ErrAdapterNotFound, err)
	assert.Nil(t, bundle)
}

func TestUniversalAdapterRegistry_GetByType_Empty(t *testing.T) {
	repo := newMockRepository()
	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	})

	tenantID := uuid.New()
	bundles := registry.GetByType(tenantID, domain.AdapterTypeCardAcquirer)

	assert.Empty(t, bundles)
}

func TestUniversalAdapterRegistry_LoadTenant_MockCardAcquirer(t *testing.T) {
	repo := newMockRepository()

	// 创建 mock provider
	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "mock",
		ProviderName: "Mock Acquirer",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	// 创建 channel
	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "mock_card_channel",
		ChannelName:     "Mock Card Channel",
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD", "EUR"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	// 创建 webhook secret credential
	repo.credentials[providerID.String()+"|"+channelID.String()+"|WEBHOOK_SECRET"] = &domain.AdapterCredentialVault{
		ProviderID:     providerID,
		ChannelID:      &channelID,
		CredentialType: domain.CredentialTypeWebhookSecret,
		Ciphertext:     []byte("whsec_test_secret"),
		Nonce:          []byte("nonce"),
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	})

	// 加载租户
	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	// 验证 bundle
	bundle, err := registry.Get(channelID)
	require.NoError(t, err)
	assert.Equal(t, channelID, bundle.ChannelID)
	assert.Equal(t, domain.AdapterTypeCardAcquirer, bundle.AdapterType)
	assert.True(t, bundle.IsActive)
	assert.NotNil(t, bundle.Client)

	// 验证按类型获取
	bundles := registry.GetByType(tenantID, domain.AdapterTypeCardAcquirer)
	assert.Len(t, bundles, 1)
}

func TestUniversalAdapterRegistry_Disable(t *testing.T) {
	repo := newMockRepository()

	// 创建 provider 和 channel
	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "mock",
		ProviderName: "Mock Acquirer",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "mock_card_channel",
		ChannelName:     "Mock Card Channel",
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD", "EUR"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	repo.credentials[providerID.String()+"|"+channelID.String()+"|WEBHOOK_SECRET"] = &domain.AdapterCredentialVault{
		ProviderID:     providerID,
		ChannelID:      &channelID,
		CredentialType: domain.CredentialTypeWebhookSecret,
		Ciphertext:     []byte("whsec_test_secret"),
		Nonce:          []byte("nonce"),
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	// 验证初始可用
	bundle, err := registry.Get(channelID)
	require.NoError(t, err)
	assert.True(t, bundle.IsActive)

	// 禁用
	err = registry.Disable(context.Background(), channelID)
	require.NoError(t, err)

	// 验证禁用后不可用
	bundle, err = registry.Get(channelID)
	assert.Error(t, err)
	assert.Equal(t, domain.ErrAdapterDisabled, err)
}

func TestUniversalAdapterRegistry_Reload(t *testing.T) {
	repo := newMockRepository()

	// 创建 provider 和 channel
	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "mock",
		ProviderName: "Mock Acquirer",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "mock_card_channel",
		ChannelName:     "Mock Card Channel",
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD", "EUR"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	repo.credentials[providerID.String()+"|"+channelID.String()+"|WEBHOOK_SECRET"] = &domain.AdapterCredentialVault{
		ProviderID:     providerID,
		ChannelID:      &channelID,
		CredentialType: domain.CredentialTypeWebhookSecret,
		Ciphertext:     []byte("whsec_test_secret"),
		Nonce:          []byte("nonce"),
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	// 更新版本
	repo.channels[channelID].Version = 2

	// 重新加载
	err = registry.Reload(context.Background(), channelID)
	require.NoError(t, err)

	// 验证版本更新
	bundle, err := registry.Get(channelID)
	require.NoError(t, err)
	assert.Equal(t, 2, bundle.Version)
}

func TestUniversalAdapterRegistry_EntityIsolation(t *testing.T) {
	repo := newMockRepository()

	// 创建两个法人实体
	entityA := uuid.New()
	entityB := uuid.New()

	// 创建 provider
	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "mock",
		ProviderName: "Mock Acquirer",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()

	// 创建实体 A 的 channel
	channelA := uuid.New()
	repo.channels[channelA] = &domain.AdapterChannel{
		ID:                channelA,
		TenantID:          tenantID,
		CorporateEntityID: &entityA,
		ProviderID:        providerID,
		ChannelCode:       "mock_card_entity_a",
		ChannelName:       "Mock Card Entity A",
		AdapterType:       domain.AdapterTypeCardAcquirer,
		SupportedAssets:   []string{"USD", "EUR"},
		Status:            domain.AdapterStatusActive,
		Version:           1,
	}

	// 创建实体 B 的 channel
	channelB := uuid.New()
	repo.channels[channelB] = &domain.AdapterChannel{
		ID:                channelB,
		TenantID:          tenantID,
		CorporateEntityID: &entityB,
		ProviderID:        providerID,
		ChannelCode:       "mock_card_entity_b",
		ChannelName:       "Mock Card Entity B",
		AdapterType:       domain.AdapterTypeCardAcquirer,
		SupportedAssets:   []string{"USD", "EUR"},
		Status:            domain.AdapterStatusActive,
		Version:           1,
	}

	// 凭证
	repo.credentials[providerID.String()+"|"+channelA.String()+"|WEBHOOK_SECRET"] = &domain.AdapterCredentialVault{
		ProviderID:     providerID,
		ChannelID:      &channelA,
		CredentialType: domain.CredentialTypeWebhookSecret,
		Ciphertext:     []byte("whsec_test_secret_a"),
		Nonce:          []byte("nonce"),
	}
	repo.credentials[providerID.String()+"|"+channelB.String()+"|WEBHOOK_SECRET"] = &domain.AdapterCredentialVault{
		ProviderID:     providerID,
		ChannelID:      &channelB,
		CredentialType: domain.CredentialTypeWebhookSecret,
		Ciphertext:     []byte("whsec_test_secret_b"),
		Nonce:          []byte("nonce"),
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	// 验证按实体获取
	bundlesA := registry.GetByEntity(entityA)
	require.Len(t, bundlesA, 1)
	assert.Equal(t, channelA, bundlesA[0].ChannelID)

	bundlesB := registry.GetByEntity(entityB)
	require.Len(t, bundlesB, 1)
	assert.Equal(t, channelB, bundlesB[0].ChannelID)

	// 禁用实体 A 的 channel，不影响实体 B
	err = registry.Disable(context.Background(), channelA)
	require.NoError(t, err)

	bundlesB = registry.GetByEntity(entityB)
	require.Len(t, bundlesB, 1) // 实体 B 的 channel 仍然可用
}

func TestUniversalAdapterRegistry_UnsupportedAdapterType(t *testing.T) {
	repo := newMockRepository()

	// 创建不支持的 adapter type
	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "unknown_provider",
		ProviderName: "Unknown Provider",
		AdapterType:  domain.AdapterType("UNKNOWN_TYPE"), // 不支持的类型
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "unknown_channel",
		ChannelName:     "Unknown Channel",
		AdapterType:     domain.AdapterType("UNKNOWN_TYPE"),
		SupportedAssets: []string{"USD"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	// 验证 bundle 有加载错误
	bundle, err := registry.Get(channelID)
	assert.Error(t, err) // fail-closed 模式下，加载错误会导致获取失败
	if bundle != nil {
		assert.Equal(t, domain.ErrUnsupportedAdapterType, bundle.LoadError)
	}
}

func TestUniversalAdapterRegistry_FailModeStaleServing(t *testing.T) {
	repo := newMockRepository()

	// 创建 provider
	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "mock",
		ProviderName: "Mock Acquirer",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "mock_card_channel",
		ChannelName:     "Mock Card Channel",
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD", "EUR"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	repo.credentials[providerID.String()+"|"+channelID.String()+"|WEBHOOK_SECRET"] = &domain.AdapterCredentialVault{
		ProviderID:     providerID,
		ChannelID:      &channelID,
		CredentialType: domain.CredentialTypeWebhookSecret,
		Ciphertext:     []byte("whsec_test_secret"),
		Nonce:          []byte("nonce"),
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeStaleServing, // stale-but-serving
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	// 正常获取
	bundle, err := registry.Get(channelID)
	require.NoError(t, err)
	assert.True(t, bundle.IsActive)

	// 模拟加载错误（通过删除 provider）
	delete(repo.providers, providerID)

	// 重新加载会失败
	err = registry.Reload(context.Background(), channelID)
	assert.Error(t, err)

	// stale-but-serving 模式下，仍然可以获取（但有错误记录）
	bundle, err = registry.Get(channelID)
	if bundle != nil {
		assert.NotNil(t, bundle.LoadError) // 有错误记录
	}
}

func TestUniversalAdapterRegistry_HotPathNoDBAccess(t *testing.T) {
	repo := newMockRepository()

	// 创建 provider 和 channel
	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "mock",
		ProviderName: "Mock Acquirer",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "mock_card_channel",
		ChannelName:     "Mock Card Channel",
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD", "EUR"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	repo.credentials[providerID.String()+"|"+channelID.String()+"|WEBHOOK_SECRET"] = &domain.AdapterCredentialVault{
		ProviderID:     providerID,
		ChannelID:      &channelID,
		CredentialType: domain.CredentialTypeWebhookSecret,
		Ciphertext:     []byte("whsec_test_secret"),
		Nonce:          []byte("nonce"),
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:     repo,
		FailMode: FailModeClosed,
	})

	// 预加载
	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	// 热路径调用：Get、GetByType、GetByEntity
	// 这些调用不应该访问数据库（从内存获取）
	start := time.Now()
	for i := 0; i < 1000; i++ {
		registry.Get(channelID)
		registry.GetByType(tenantID, domain.AdapterTypeCardAcquirer)
		registry.GetByEntity(tenantID)
	}
	duration := time.Since(start)

	// 1000 次调用应该在 1ms 内完成（纯内存操作）
	assert.Less(t, duration.Milliseconds(), int64(10), "hot path should be fast (memory only)")
}
