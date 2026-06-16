package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/nexusglobal/mamr/internal/walletpolicy/domain"
)

type mockRepository struct {
	mock.Mock
}

func (m *mockRepository) GetPolicy(ctx context.Context, merchantID uuid.UUID, network domain.Network, assetCode string) (*domain.TransitWalletPolicy, error) {
	args := m.Called(ctx, merchantID, network, assetCode)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.TransitWalletPolicy), args.Error(1)
}

func (m *mockRepository) ListAllowlistedAddresses(ctx context.Context, merchantID uuid.UUID, network domain.Network) ([]domain.AllowlistedAddress, error) {
	args := m.Called(ctx, merchantID, network)
	return args.Get(0).([]domain.AllowlistedAddress), args.Error(1)
}

func (m *mockRepository) CheckAllowlisted(ctx context.Context, merchantID uuid.UUID, network domain.Network, address string) (bool, error) {
	args := m.Called(ctx, merchantID, network, address)
	return args.Get(0).(bool), args.Error(1)
}

func (m *mockRepository) RecordWithdrawal(ctx context.Context, req *domain.WithdrawalRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *mockRepository) GetDailyWithdrawalTotal(ctx context.Context, merchantID uuid.UUID, network domain.Network, assetCode string) (int64, error) {
	args := m.Called(ctx, merchantID, network, assetCode)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockRepository) CheckTxHashExists(ctx context.Context, txHash string) (bool, error) {
	args := m.Called(ctx, txHash)
	return args.Get(0).(bool), args.Error(1)
}

func TestValidateWithdrawal_AddressNotAllowlisted(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := New(repo)

	merchantID := uuid.New()
	policy := &domain.TransitWalletPolicy{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		Network:          domain.NetworkPolygon,
		AssetCode:        "USDC",
		AllowListEnabled: true,
	}

	repo.On("GetPolicy", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(policy, nil)
	repo.On("CheckAllowlisted", ctx, merchantID, domain.NetworkPolygon, "0xNotAllowlisted").Return(false, nil)

	err := svc.ValidateWithdrawal(ctx, WithdrawalValidationCmd{
		MerchantID:  merchantID,
		Network:     domain.NetworkPolygon,
		AssetCode:   "USDC",
		Address:     "0xNotAllowlisted",
		AmountMinor: 100,
	})

	assert.ErrorIs(t, err, domain.ErrAddressNotAllowlisted)
}

func TestValidateWithdrawal_WrongNetwork(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := New(repo)

	merchantID := uuid.New()
	policy := &domain.TransitWalletPolicy{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		Network:          domain.NetworkEthereum,
		AssetCode:        "USDC",
		AllowListEnabled: false,
	}

	repo.On("GetPolicy", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(policy, nil)

	err := svc.ValidateWithdrawal(ctx, WithdrawalValidationCmd{
		MerchantID:  merchantID,
		Network:     domain.NetworkPolygon,
		AssetCode:   "USDC",
		Address:     "0xValidAddress",
		AmountMinor: 100,
	})

	assert.ErrorIs(t, err, domain.ErrWrongNetwork)
}

func TestValidateWithdrawal_ExceedsSingleLimit(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := New(repo)

	merchantID := uuid.New()
	policy := &domain.TransitWalletPolicy{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		Network:          domain.NetworkPolygon,
		AssetCode:        "USDC",
		SingleLimitMinor: 1000,
		AllowListEnabled: false,
	}

	repo.On("GetPolicy", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(policy, nil)

	err := svc.ValidateWithdrawal(ctx, WithdrawalValidationCmd{
		MerchantID:  merchantID,
		Network:     domain.NetworkPolygon,
		AssetCode:   "USDC",
		Address:     "0xValidAddress",
		AmountMinor: 2000,
	})

	assert.ErrorIs(t, err, domain.ErrExceedsSingleLimit)
}

func TestValidateWithdrawal_ExceedsDailyLimit(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := New(repo)

	merchantID := uuid.New()
	policy := &domain.TransitWalletPolicy{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		Network:          domain.NetworkPolygon,
		AssetCode:        "USDC",
		SingleLimitMinor: 10000,
		DailyLimitMinor:  1000,
		AllowListEnabled: false,
	}

	repo.On("GetPolicy", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(policy, nil)
	repo.On("GetDailyWithdrawalTotal", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(int64(800), nil)

	err := svc.ValidateWithdrawal(ctx, WithdrawalValidationCmd{
		MerchantID:  merchantID,
		Network:     domain.NetworkPolygon,
		AssetCode:   "USDC",
		Address:     "0xValidAddress",
		AmountMinor: 300,
	})

	assert.ErrorIs(t, err, domain.ErrExceedsDailyLimit)
}

func TestValidateWithdrawal_DuplicateTxHash(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := New(repo)

	merchantID := uuid.New()
	policy := &domain.TransitWalletPolicy{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		Network:          domain.NetworkPolygon,
		AssetCode:        "USDC",
		SingleLimitMinor: 1000,
		DailyLimitMinor:  10000,
		AllowListEnabled: false,
	}

	repo.On("GetPolicy", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(policy, nil)
	repo.On("GetDailyWithdrawalTotal", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(int64(0), nil)
	repo.On("CheckTxHashExists", ctx, "0xExistingTxHash").Return(true, nil)

	err := svc.ValidateWithdrawal(ctx, WithdrawalValidationCmd{
		MerchantID:  merchantID,
		Network:     domain.NetworkPolygon,
		AssetCode:   "USDC",
		Address:     "0xValidAddress",
		AmountMinor: 100,
		TxHash:      "0xExistingTxHash",
	})

	assert.ErrorIs(t, err, domain.ErrDuplicateTxHash)
}

func TestValidateWithdrawal_Success(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := New(repo)

	merchantID := uuid.New()
	policy := &domain.TransitWalletPolicy{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		Network:          domain.NetworkPolygon,
		AssetCode:        "USDC",
		SingleLimitMinor: 1000,
		DailyLimitMinor:  10000,
		AllowListEnabled: false,
	}

	repo.On("GetPolicy", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(policy, nil)
	repo.On("GetDailyWithdrawalTotal", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(int64(0), nil)
	repo.On("CheckTxHashExists", ctx, "0xNewTxHash").Return(false, nil)

	err := svc.ValidateWithdrawal(ctx, WithdrawalValidationCmd{
		MerchantID:  merchantID,
		Network:     domain.NetworkPolygon,
		AssetCode:   "USDC",
		Address:     "0xValidAddress",
		AmountMinor: 100,
		TxHash:      "0xNewTxHash",
	})

	assert.NoError(t, err)
}

func TestFinalizeWithdrawal_InsufficientConfirmations(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := New(repo)

	merchantID := uuid.New()
	policy := &domain.TransitWalletPolicy{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		Network:          domain.NetworkPolygon,
		AssetCode:        "USDC",
		MinConfirmations: 10,
	}

	repo.On("GetPolicy", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(policy, nil)

	err := svc.FinalizeWithdrawal(ctx, WithdrawalValidationCmd{
		MerchantID:    merchantID,
		Network:       domain.NetworkPolygon,
		AssetCode:     "USDC",
		Address:       "0xValidAddress",
		AmountMinor:   100,
		TxHash:        "0xNewTxHash",
		Confirmations: 5,
	})

	assert.ErrorIs(t, err, domain.ErrInsufficientConfirmations)
}

func TestFinalizeWithdrawal_Success(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := New(repo)

	merchantID := uuid.New()
	policy := &domain.TransitWalletPolicy{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		Network:          domain.NetworkPolygon,
		AssetCode:        "USDC",
		MinConfirmations: 5,
	}

	repo.On("GetPolicy", ctx, merchantID, domain.NetworkPolygon, "USDC").Return(policy, nil)
	repo.On("CheckTxHashExists", ctx, "0xNewTxHash").Return(false, nil)
	repo.On("RecordWithdrawal", ctx, mock.Anything).Return(nil)

	err := svc.FinalizeWithdrawal(ctx, WithdrawalValidationCmd{
		MerchantID:    merchantID,
		Network:       domain.NetworkPolygon,
		AssetCode:     "USDC",
		Address:       "0xValidAddress",
		AmountMinor:   100,
		TxHash:        "0xNewTxHash",
		Confirmations: 10,
	})

	assert.NoError(t, err)
}
