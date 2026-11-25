package registry

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
)

// TestCreateCardAcquirerClient_UnknownProviderNoMockFallback 验证 G06 修复：
// 未知 provider_code 不再回退 mock，一律返回错误。
// 旧逻辑在非 real 模式下 fallback 到 mock，导致非 mock VCC 配置接 Mock bridge。
func TestCreateCardAcquirerClient_UnknownProviderNoMockFallback(t *testing.T) {
	repo := newMockRepository()

	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "totally_unknown_provider",
		ProviderName: "Unknown Provider",
		AdapterType:  domain.AdapterTypeCardAcquirer,
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
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	// 非 real 模式（isRealMode=false）：旧逻辑会回退 mock，新逻辑必须拒绝
	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:       repo,
		FailMode:   FailModeClosed,
		IsRealMode: false,
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	// 验证 bundle 有加载错误（不再回退 mock）
	bundle, err := registry.Get(channelID)
	assert.Error(t, err, "unknown provider must fail-closed, not fallback to mock")
	if bundle != nil {
		assert.NotNil(t, bundle.LoadError, "bundle should have LoadError set")
		assert.Contains(t, bundle.LoadError.Error(), "no mock fallback",
			"error message should mention no mock fallback")
	}
}

// TestCreateCardAcquirerClient_UnknownProviderRealMode 验证 real 模式下未知 provider 拒绝。
func TestCreateCardAcquirerClient_UnknownProviderRealMode(t *testing.T) {
	repo := newMockRepository()

	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "unknown_in_production",
		ProviderName: "Unknown",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "ch",
		ChannelName:     "Channel",
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:       repo,
		FailMode:   FailModeClosed,
		IsRealMode: true,
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	_, err = registry.Get(channelID)
	assert.Error(t, err, "unknown provider in real mode must fail-closed")
}

// TestCreateCardAcquirerClient_MockProviderRejectedInRealMode 验证 real 模式下 mock provider 被拒绝。
func TestCreateCardAcquirerClient_MockProviderRejectedInRealMode(t *testing.T) {
	repo := newMockRepository()

	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "mock",
		ProviderName: "Mock",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "mock_ch",
		ChannelName:     "Mock Channel",
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
	}

	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:       repo,
		FailMode:   FailModeClosed,
		IsRealMode: true,
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	_, err = registry.Get(channelID)
	assert.Error(t, err, "mock provider must be rejected in real mode")
}

// TestCreateCardAcquirerClient_KnownProviderAllowed 验证已知 provider 仍然正常工作。
// 使用 mock provider 在非 real 模式下测试（mock 不需要真实凭证）。
func TestCreateCardAcquirerClient_KnownProviderAllowed(t *testing.T) {
	repo := newMockRepository()

	providerID := uuid.New()
	repo.providers[providerID] = &domain.AdapterProvider{
		ID:           providerID,
		ProviderCode: "mock",
		ProviderName: "Mock",
		AdapterType:  domain.AdapterTypeCardAcquirer,
		Status:       domain.AdapterStatusActive,
	}

	tenantID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = &domain.AdapterChannel{
		ID:              channelID,
		TenantID:        tenantID,
		ProviderID:      providerID,
		ChannelCode:     "mock_ch",
		ChannelName:     "Mock Channel",
		AdapterType:     domain.AdapterTypeCardAcquirer,
		SupportedAssets: []string{"USD"},
		Status:          domain.AdapterStatusActive,
		Version:         1,
		ChannelConfig:   map[string]any{},
	}

	// 非 real 模式：mock provider 是显式声明的，应该正常工作
	registry := NewUniversalAdapterRegistry(RegistryConfig{
		Repo:       repo,
		FailMode:   FailModeClosed,
		IsRealMode: false,
	})

	err := registry.LoadTenant(context.Background(), tenantID)
	require.NoError(t, err)

	bundle, err := registry.Get(channelID)
	require.NoError(t, err, "mock provider should work in non-real mode")
	require.NotNil(t, bundle)
	assert.Nil(t, bundle.LoadError)
}
