package treasury

import (
	"context"
	"testing"

	"github.com/google/uuid"
	issuing "github.com/nexusglobal/mamr/internal/issuing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockVCCFundingRepository struct {
	mock.Mock
}

func (m *mockVCCFundingRepository) LockMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID) error {
	args := m.Called(ctx, merchantID)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) DebitMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64, txID string) error {
	args := m.Called(ctx, merchantID, amountMinor, txID)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) CreditPendingCryptoBridgeBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64) error {
	args := m.Called(ctx, merchantID, amountMinor)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) WriteOutboxEvent(ctx context.Context, event OutboxEvent) error {
	args := m.Called(ctx, event)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) GetMerchantRiskProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error) {
	args := m.Called(ctx, merchantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*MerchantRiskProfile), args.Error(1)
}

func (m *mockVCCFundingRepository) GetIssuingProvider(ctx context.Context, providerID uuid.UUID) (*issuing.IssuingProvider, error) {
	args := m.Called(ctx, providerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*issuing.IssuingProvider), args.Error(1)
}

func TestRouteFunding_HighRiskFiatDirect_Rejected(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierHighRisk,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrHighRiskFiatDirect)
	assert.False(t, result.Approved)
	assert.Equal(t, RouteTypeRejected, result.RouteType)
	assert.Contains(t, result.RejectReason, "high-risk")
}

func TestRouteFunding_HighRiskCryptoDesensitized_Allowed(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  50,
		IsBlocked:  false,
		SourceTier: ProviderTierHighRisk,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("LockMerchantSettledBalance", ctx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalance", ctx, merchantID, int64(100000), "ledger-tx-123").Return(nil)
	mockRepo.On("CreditPendingCryptoBridgeBalance", ctx, merchantID, int64(100000)).Return(nil)
	mockRepo.On("WriteOutboxEvent", ctx, mock.Anything).Return(nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierHighRisk,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeCryptoDesensitized,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, RouteTypeCryptoDesensitized, result.RouteType)
	assert.NotEmpty(t, result.BridgeBatchID)
	assert.Equal(t, ProviderTierHighRisk, result.Provenance.SourceTier)
	assert.Equal(t, sourceChannelID.String(), result.Provenance.SourceRail)
	assert.Equal(t, "ledger-tx-123", result.Provenance.SourceLedgerTxID)

	mockRepo.AssertExpectations(t)
}

func TestRouteFunding_CompliantFiatDirect_Allowed(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  20,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeFiatDirect,
		Status:      "ACTIVE",
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("LockMerchantSettledBalance", ctx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalance", ctx, merchantID, int64(50000), "ledger-tx-456").Return(nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       50000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-456",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, RouteTypeFiatDirect, result.RouteType)
	assert.Equal(t, ProviderTierCompliant, result.Provenance.SourceTier)

	mockRepo.AssertExpectations(t)
}

func TestRouteFunding_InvalidAmount(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       0,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAmount)
	assert.False(t, result.Approved)
}

func TestRouteFunding_UnknownSourceTier(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTier("UNKNOWN_TIER"),
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownSourceTier)
	assert.False(t, result.Approved)
}

func TestRouteFunding_UnknownFundingMode(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingMode("UNKNOWN_MODE"),
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownFundingMode)
	assert.False(t, result.Approved)
}

func TestRouteFunding_MissingProvenanceMetadata(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingProvenanceMetadata)
	assert.False(t, result.Approved)
}

func TestRouteFunding_MerchantBlocked(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  100,
		IsBlocked:  true,
		SourceTier: ProviderTierCompliant,
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "blocked")
}

func TestRouteFunding_FundingModeMismatch(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  50,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "mismatch")
}

func TestRouteFunding_InsufficientBalance(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  50,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeFiatDirect,
		Status:      "ACTIVE",
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("LockMerchantSettledBalance", ctx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalance", ctx, merchantID, int64(100000), "ledger-tx-123").Return(ErrInsufficientBalance)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientBalance)
	assert.False(t, result.Approved)
}
