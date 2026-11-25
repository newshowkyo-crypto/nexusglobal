package issuing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockIssuingAdapter struct {
	mock.Mock
}

func (m *mockIssuingAdapter) CreateCard(ctx context.Context, req CreateCardRequest) (*CreateCardResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CreateCardResult), args.Error(1)
}

func (m *mockIssuingAdapter) FreezeCard(ctx context.Context, req FreezeCardRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *mockIssuingAdapter) TerminateCard(ctx context.Context, req TerminateCardRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *mockIssuingAdapter) ProcessAuthWebhook(ctx context.Context, req ProcessAuthWebhookRequest) (*CardAuthEvent, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CardAuthEvent), args.Error(1)
}

type mockIssuingAdapterFactory struct {
	mock.Mock
}

func (m *mockIssuingAdapterFactory) Create(ctx context.Context, providerCode string, credential map[string]string) (IssuingAdapter, error) {
	args := m.Called(ctx, providerCode, credential)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(IssuingAdapter), args.Error(1)
}

func (m *mockIssuingAdapterFactory) Register(providerCode string, factory func(context.Context, map[string]string) (IssuingAdapter, error)) {
	m.Called(providerCode, factory)
}

func (m *mockIssuingAdapterFactory) Has(providerCode string) bool {
	return m.Called(providerCode).Bool(0)
}

func (m *mockIssuingAdapterFactory) Validate() error {
	return m.Called().Error(0)
}

func (m *mockIssuingAdapterFactory) RegisteredCodes() []string {
	return m.Called().Get(0).([]string)
}

type mockVaultService struct {
	mock.Mock
}

func (m *mockVaultService) Decrypt(ctx context.Context, req VaultCredentialRequest) (map[string]string, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]string), args.Error(1)
}

type mockCardRepository struct {
	mock.Mock
}

func (m *mockCardRepository) Create(ctx context.Context, card VirtualCard) error {
	args := m.Called(ctx, card)
	return args.Error(0)
}

func (m *mockCardRepository) GetByID(ctx context.Context, cardID uuid.UUID) (*VirtualCard, error) {
	args := m.Called(ctx, cardID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*VirtualCard), args.Error(1)
}

func (m *mockCardRepository) UpdateStatus(ctx context.Context, cardID uuid.UUID, status VirtualCardStatus, reason string) error {
	args := m.Called(ctx, cardID, status, reason)
	return args.Error(0)
}

func (m *mockCardRepository) GetByIdempotencyKey(ctx context.Context, merchantID uuid.UUID, idempotencyKey string) (*VirtualCard, error) {
	args := m.Called(ctx, merchantID, idempotencyKey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*VirtualCard), args.Error(1)
}

type mockProviderRepository struct {
	mock.Mock
}

func (m *mockProviderRepository) ListProviders(ctx context.Context, entityID *uuid.UUID) ([]IssuingProvider, error) {
	args := m.Called(ctx, entityID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]IssuingProvider), args.Error(1)
}

func (m *mockProviderRepository) GetProviderByID(ctx context.Context, providerID uuid.UUID) (*IssuingProvider, error) {
	args := m.Called(ctx, providerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*IssuingProvider), args.Error(1)
}

func (m *mockProviderRepository) ListProvidersByTierAndFundingMode(ctx context.Context, entityID *uuid.UUID, tier IssuingTier, fundingMode IssuingFundingMode) ([]IssuingProvider, error) {
	args := m.Called(ctx, entityID, tier, fundingMode)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]IssuingProvider), args.Error(1)
}

type mockFraudGuard struct {
	mock.Mock
}

func (m *mockFraudGuard) CheckMerchantIssuingAllowed(ctx context.Context, merchantID uuid.UUID) error {
	args := m.Called(ctx, merchantID)
	return args.Error(0)
}

func (m *mockFraudGuard) RecordAuthEvent(ctx context.Context, event CardAuthEvent) error {
	args := m.Called(ctx, event)
	return args.Error(0)
}

func (m *mockFraudGuard) CheckCardDeclineThreshold(ctx context.Context, cardID uuid.UUID) error {
	args := m.Called(ctx, cardID)
	return args.Error(0)
}

func (m *mockFraudGuard) CheckMerchantFraudRate(ctx context.Context, merchantID uuid.UUID) error {
	args := m.Called(ctx, merchantID)
	return args.Error(0)
}

func TestIssuingTierValidation(t *testing.T) {
	assert.NoError(t, IssuingTierAggregator.Validate())
	assert.NoError(t, IssuingTierDirect.Validate())
	assert.Error(t, IssuingTier("UNKNOWN_TIER").Validate())
	assert.ErrorIs(t, IssuingTier("UNKNOWN_TIER").Validate(), ErrIssuingTierUnknown)
}

func TestFundingModeValidation(t *testing.T) {
	assert.NoError(t, FundingModeCryptoDesensitized.Validate())
	assert.NoError(t, FundingModeFiatDirect.Validate())
	assert.Error(t, IssuingFundingMode("UNKNOWN_MODE").Validate())
	assert.ErrorIs(t, IssuingFundingMode("UNKNOWN_MODE").Validate(), ErrFundingModeUnknown)
}

func TestCardStatusValidation(t *testing.T) {
	assert.NoError(t, CardStatusPending.Validate())
	assert.NoError(t, CardStatusActive.Validate())
	assert.NoError(t, CardStatusFrozen.Validate())
	assert.NoError(t, CardStatusTerminated.Validate())
	assert.NoError(t, CardStatusUnknownExternalState.Validate())
	assert.NoError(t, CardStatusManualReview.Validate())
	assert.Error(t, VirtualCardStatus("UNKNOWN").Validate())
	assert.ErrorIs(t, VirtualCardStatus("UNKNOWN").Validate(), ErrCardStatusUnknown)
}

func TestCreateCardSuccess(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	vaultKey := "vault-key-123"
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
		CredentialVaultKey: vaultKey,
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)
	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	resultID := uuid.New()
	expectedResult := &CreateCardResult{
		CardID:          resultID,
		ProviderCardRef: "provider-ref-123",
		Last4:           "4321",
		Status:          CardStatusActive,
		CreatedAt:       time.Now(),
	}
	mockAdapter.On("CreateCard", ctx, mock.Anything).Return(expectedResult, nil)
	mockCardRepo.On("Create", ctx, mock.Anything).Return(nil)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-success",
		CreatedBy:          "admin",
	}

	result, err := service.CreateCard(ctx, req)

	assert.NoError(t, err)
	assert.Equal(t, expectedResult.CardID, result.CardID)
	assert.Equal(t, expectedResult.Last4, result.Last4)
	assert.Equal(t, CardStatusActive, result.Status)

	mockFactory.AssertExpectations(t)
	mockVault.AssertExpectations(t)
	mockCardRepo.AssertExpectations(t)
	mockFG.AssertExpectations(t)
}

func TestCreateCard_ProviderNotActive(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "INACTIVE",
	}

	mockProviderRepo := new(mockProviderRepository)
	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockFG := new(mockFraudGuard)

	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-test",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderNotActive)
}

func TestCreateCard_CredentialVaultKeyEmpty(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "",
		Status:             "ACTIVE",
	}

	mockProviderRepo := new(mockProviderRepository)
	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockFG := new(mockFraudGuard)

	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-test",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialVaultKeyEmpty)
}

func TestCreateCard_CredentialDecryptFailed(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()
	vaultKey := "vault-key-123"

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: vaultKey,
		Status:             "ACTIVE",
	}

	mockProviderRepo := new(mockProviderRepository)
	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockFG := new(mockFraudGuard)

	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(nil, errors.New("decrypt failed"))

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-test",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialDecryptFailed)
}

func TestCreateCard_MerchantNotAllowed(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	mockProviderRepo := new(mockProviderRepository)
	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockFG := new(mockFraudGuard)

	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(ErrMerchantIssuingNotAllowed)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-test",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrMerchantIssuingNotAllowed)
}

func TestCreateCard_MerchantFraudRateExceeded(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	providerID := uuid.New()

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	mockProviderRepo := new(mockProviderRepository)
	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockFG := new(mockFraudGuard)

	mockCardRepo.On("GetByIdempotencyKey", ctx, merchantID, mock.Anything).Return(nil, ErrCardNotFound)
	mockFG.On("CheckMerchantIssuingAllowed", ctx, merchantID).Return(nil)
	mockFG.On("CheckMerchantFraudRate", ctx, merchantID).Return(ErrMerchantFraudRateExceeded)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := CreateCardRequest{
		MerchantID:         merchantID,
		ProviderID:         providerID,
		PoolCode:           "pool-1",
		Currency:           "USD",
		SpendingLimitMinor: 100000,
		IdempotencyKey:     "idem-key-test",
		CreatedBy:          "admin",
	}

	_, err := service.CreateCard(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrMerchantFraudRateExceeded)
}

func TestSelectProviderByTierAndFundingMode(t *testing.T) {
	ctx := context.Background()
	providerID := uuid.New()

	provider := IssuingProvider{
		ID:                 providerID,
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "ACTIVE",
	}

	mockProviderRepo := new(mockProviderRepository)
	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory := new(mockIssuingAdapterFactory)
	mockVault := new(mockVaultService)
	mockCardRepo := new(mockCardRepository)
	mockFG := new(mockFraudGuard)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	result, err := service.SelectProviderByTierAndFundingMode(ctx, nil, IssuingTierAggregator, FundingModeCryptoDesensitized)

	assert.NoError(t, err)
	assert.Equal(t, providerID, result.ID)
	assert.Equal(t, IssuingTierAggregator, result.IssuingTier)
	assert.Equal(t, FundingModeCryptoDesensitized, result.FundingMode)
}

func TestProcessAuthWebhook_DeclineThresholdExceeded(t *testing.T) {
	ctx := context.Background()
	cardID := uuid.New()
	providerID := uuid.New()
	vaultKey := "vault-key-123"
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
		CredentialVaultKey: vaultKey,
		Status:             "ACTIVE",
	}

	registry := NewChannelRegistry(mockProviderRepo)
	_ = registry.RegisterProvider(provider)

	mockFactory.On("Create", ctx, "test-provider", credential).Return(mockAdapter, nil)
	mockVault.On("Decrypt", ctx, mock.Anything).Return(credential, nil)

	event := &CardAuthEvent{
		EventID:    "evt-123",
		CardID:     cardID,
		MerchantID: uuid.New(),
		Amount:     decimal.NewFromInt(100),
		Currency:   "USD",
		Status:     "DECLINED",
		IsDeclined: true,
	}
	mockAdapter.On("ProcessAuthWebhook", ctx, mock.Anything).Return(event, nil)
	mockFG.On("RecordAuthEvent", ctx, *event).Return(nil)
	mockFG.On("CheckCardDeclineThreshold", ctx, cardID).Return(ErrCardDeclinedSlidingWindowExceeded)
	mockCardRepo.On("UpdateStatus", ctx, cardID, CardStatusFrozen, "decline threshold exceeded").Return(nil)

	service := NewService(registry, mockFactory, mockFG, mockVault, mockCardRepo, mockProviderRepo)

	req := ProcessAuthWebhookRequest{
		ProviderID: providerID,
		RawPayload: []byte(`{"event": "auth"}`),
		Signature:  "sig-123",
	}

	_, err := service.ProcessAuthWebhook(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrCardDeclinedSlidingWindowExceeded)
}

func TestRegistryRegisterProvider_EmptyVaultKey(t *testing.T) {
	provider := IssuingProvider{
		ID:                 uuid.New(),
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "",
		Status:             "ACTIVE",
	}

	mockRepo := new(mockProviderRepository)
	registry := NewChannelRegistry(mockRepo)

	err := registry.RegisterProvider(provider)

	assert.NoError(t, err)

	retrieved, err := registry.GetProvider(context.Background(), provider.ID)
	assert.NoError(t, err)
	assert.Equal(t, provider.ID, retrieved.ID)
}

func TestRegistrySelectByTierAndFundingMode_InactiveProvider(t *testing.T) {
	ctx := context.Background()
	provider := IssuingProvider{
		ID:                 uuid.New(),
		IssuingTier:        IssuingTierAggregator,
		FundingMode:        FundingModeCryptoDesensitized,
		ProviderCode:       "test-provider",
		PoolCode:           "pool-1",
		CredentialVaultKey: "vault-key",
		Status:             "INACTIVE",
	}

	mockRepo := new(mockProviderRepository)
	registry := NewChannelRegistry(mockRepo)
	_ = registry.RegisterProvider(provider)

	providers, err := registry.SelectByTierAndFundingMode(ctx, nil, IssuingTierAggregator, FundingModeCryptoDesensitized)

	assert.NoError(t, err)
	assert.Empty(t, providers)
}
