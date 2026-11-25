package issuing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// F-20: CreateCard 超时路径测试。
// - (nil, ErrProviderTimeout) 不 panic
// - Unknown-state card ID 不是 merchant ID
// - Repository 失败被返回

// TestCreateCard_TimeoutDoesNotDereferenceNilResult 验证超时返回 (nil, ErrProviderTimeout) 不 panic。
func TestCreateCard_TimeoutDoesNotDereferenceNilResult(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	credential := map[string]string{"api_key": "secret"}

	mockAdapter := new(mockIssuingAdapter)
	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	// F-20: Provider 返回 (nil, ErrProviderTimeout) — 不应 panic
	mockAdapter.On("CreateCard", ctx, mock.Anything).Return(nil, ErrProviderTimeout)
	mockCardRepo.On("Create", ctx, mock.Anything).Return(nil)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-1",
		CreatedBy:          "admin",
	}

	// 不应 panic，应返回 ErrUnknownExternalState
	result, err := service.CreateCard(ctx, req)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrUnknownExternalState)
}

// TestCreateCard_TimeoutCardIDIsNotMerchantID 验证超时时 card ID 不是 merchant ID。
func TestCreateCard_TimeoutCardIDIsNotMerchantID(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	credential := map[string]string{"api_key": "secret"}

	mockAdapter := new(mockIssuingAdapter)
	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	mockAdapter.On("CreateCard", ctx, mock.Anything).Return(nil, ErrProviderTimeout)

	// 捕获写入的 card，验证 ID 不是 merchant ID
	var capturedCard VirtualCard
	mockCardRepo.On("Create", ctx, mock.Anything).Run(func(args mock.Arguments) {
		capturedCard = args.Get(1).(VirtualCard)
	}).Return(nil)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-1",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)
	require.Error(t, err)

	// F-20: card ID 不应是 merchant ID
	assert.NotEqual(t, merchantID, capturedCard.ID, "card ID must not be merchant ID")
	// F-20: 不应编造 provider reference 或 last4
	assert.Empty(t, capturedCard.ProviderCardRef, "must not fabricate provider_card_ref")
	assert.Empty(t, capturedCard.Last4, "must not fabricate last4")
	assert.Equal(t, CardStatusUnknownExternalState, capturedCard.Status)
}

// TestCreateCard_TimeoutRepoFailureIsReturned 验证超时后 repository 写入失败被返回。
func TestCreateCard_TimeoutRepoFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	credential := map[string]string{"api_key": "secret"}

	mockAdapter := new(mockIssuingAdapter)
	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	mockAdapter.On("CreateCard", ctx, mock.Anything).Return(nil, ErrProviderTimeout)

	// F-20: repository 写入失败
	repoErr := errors.New("database write failed")
	mockCardRepo.On("Create", ctx, mock.Anything).Return(repoErr)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-1",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)
	// F-20: repository 写入错误必须被返回
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo write failed")
}

// TestCreateCard_NormalRepoFailureIsReturned 验证正常路径 repository 写入失败被返回。
func TestCreateCard_NormalRepoFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	credential := map[string]string{"api_key": "secret"}

	mockAdapter := new(mockIssuingAdapter)
	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	expectedResult := &CreateCardResult{
		CardID:          uuid.New(),
		ProviderCardRef: "provider-ref-123",
		Last4:           "4321",
		Status:          CardStatusActive,
		CreatedAt:       time.Now(),
	}
	mockAdapter.On("CreateCard", ctx, mock.Anything).Return(expectedResult, nil)

	// F-20: repository 写入失败
	repoErr := errors.New("database write failed")
	mockCardRepo.On("Create", ctx, mock.Anything).Return(repoErr)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-1",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)
	// F-20: repository 写入错误必须被返回
	require.Error(t, err)
	assert.ErrorIs(t, err, repoErr)
}

// TestCreateCard_TimeoutPreservesOriginalAdapterError 验证 timeout 时 Adapter 原始 err 被保留。
// R20 P1-1: errors.Join(ErrUnknownExternalState, ErrProviderTimeout, err) 必须保留原始 adapter err。
func TestCreateCard_TimeoutPreservesOriginalAdapterError(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	credential := map[string]string{"api_key": "secret"}

	mockAdapter := new(mockIssuingAdapter)
	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	// R20 P1-1: 使用 wrapped timeout error（不仅是 ErrProviderTimeout 本身）
	wrappedTimeoutErr := fmt.Errorf("adapter: request deadline exceeded: %w", ErrProviderTimeout)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	mockAdapter.On("CreateCard", ctx, mock.Anything).Return(nil, wrappedTimeoutErr)
	mockCardRepo.On("Create", ctx, mock.Anything).Return(nil)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-1",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)
	require.Error(t, err)

	// R20 P1-1: 必须保留 ErrUnknownExternalState
	assert.ErrorIs(t, err, ErrUnknownExternalState)
	// R20 P1-1: 必须保留 ErrProviderTimeout
	assert.ErrorIs(t, err, ErrProviderTimeout)
	// R20 P1-1: 必须保留 Adapter 原始 wrapped timeout error
	assert.ErrorIs(t, err, wrappedTimeoutErr)
}

// TestCreateCard_TimeoutRepoFailurePreservesAllErrors 验证 timeout + repo 失败时所有错误被保留。
// R20 P1-1: errors.Join(ErrUnknownExternalState, ErrProviderTimeout, err, createErr) 必须保留全部。
func TestCreateCard_TimeoutRepoFailurePreservesAllErrors(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	credential := map[string]string{"api_key": "secret"}

	mockAdapter := new(mockIssuingAdapter)
	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	wrappedTimeoutErr := fmt.Errorf("adapter: connection reset: %w", ErrProviderTimeout)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	mockAdapter.On("CreateCard", ctx, mock.Anything).Return(nil, wrappedTimeoutErr)

	repoErr := errors.New("database write failed")
	mockCardRepo.On("Create", ctx, mock.Anything).Return(repoErr)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-1",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)
	require.Error(t, err)

	// R20 P1-1: 必须保留 ErrUnknownExternalState
	assert.ErrorIs(t, err, ErrUnknownExternalState)
	// R20 P1-1: 必须保留 ErrProviderTimeout
	assert.ErrorIs(t, err, ErrProviderTimeout)
	// R20 P1-1: 必须保留 Adapter 原始 wrapped timeout error
	assert.ErrorIs(t, err, wrappedTimeoutErr)
	// R20 P1-1: 必须保留 repository error
	assert.ErrorIs(t, err, repoErr)
}

// TestCreateCard_EmptyIdempotencyKeyRejected 验证空 IdempotencyKey 被 fail-closed 拒绝。
// R18 P1-2: 调用方必须提供稳定 IdempotencyKey，禁止静默生成随机键。
func TestCreateCard_EmptyIdempotencyKeyRejected(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()

	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	registry := NewChannelRegistry(mockProviderRepo)
	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "", // 空 IdempotencyKey
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IdempotencyKey")
}

// TestCreateCard_DBErrorOnIdempotencyCheckFailClosed 验证 GetByIdempotencyKey 返回非 ErrCardNotFound 错误时 fail-closed。
// R18 P1-2: 数据库故障、超时或未知错误必须立即返回，禁止调用 Provider。
func TestCreateCard_DBErrorOnIdempotencyCheckFailClosed(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()

	mockAdapter := new(mockIssuingAdapter)
	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	// R18 P1-2: DB 返回超时错误（不是 ErrCardNotFound）
	dbTimeoutErr := errors.New("database timeout")
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, dbTimeoutErr)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-1",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)
	require.Error(t, err)
	// 必须返回 fail-closed 错误，包含原始 DB 错误
	assert.Contains(t, err.Error(), "fail-closed")
	assert.ErrorIs(t, err, dbTimeoutErr)

	// Provider 绝不被调用
	mockAdapter.AssertNotCalled(t, "CreateCard")
}

// TestCreateCard_ProviderNilResultFailClosed 验证 Provider 返回 (nil, nil) 时 fail-closed。
// R18 P1-2: Provider 返回 nil result 且 nil error 时不允许继续，返回 ErrUnknownExternalState。
func TestCreateCard_ProviderNilResultFailClosed(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	credential := map[string]string{"api_key": "secret"}

	mockAdapter := new(mockIssuingAdapter)
	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockProviderRepo := new(mockProviderRepository)
	mockFG := new(mockFraudGuard)

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	// R18 P1-2: Provider 返回 (nil, nil) — fail-closed
	mockAdapter.On("CreateCard", ctx, mock.Anything).Return(nil, nil)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-1",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownExternalState)
}
