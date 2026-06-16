package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/ledger/fee"
)

// MockFeeRepository is a mock implementation of FeeRepository
type MockFeeRepository struct {
	mock.Mock
}

func (m *MockFeeRepository) CreateAllocations(ctx context.Context, allocations []domain.SettlementFeeAllocation) error {
	args := m.Called(ctx, allocations)
	return args.Error(0)
}

func (m *MockFeeRepository) GetByTransactionID(ctx context.Context, transactionID uuid.UUID) ([]domain.SettlementFeeAllocation, error) {
	args := m.Called(ctx, transactionID)
	return args.Get(0).([]domain.SettlementFeeAllocation), args.Error(1)
}

func (m *MockFeeRepository) GetByMerchantID(ctx context.Context, merchantID uuid.UUID) ([]domain.SettlementFeeAllocation, error) {
	args := m.Called(ctx, merchantID)
	return args.Get(0).([]domain.SettlementFeeAllocation), args.Error(1)
}

func TestFeeService_AllocateTransactionFees(t *testing.T) {
	testCases := []struct {
		name        string
		feeDetails  []fee.FeeDetail
		expectError bool
		mockSetup   func(*MockFeeRepository)
	}{
		{
			name: "allocate LP_FEE successfully",
			feeDetails: []fee.FeeDetail{
				{
					FeeType:    fee.FeeTypeLP,
					Amount:     100,
					SourceData: map[string]interface{}{"provider": "lp_institutional", "quote_id": "quote-123"},
				},
			},
			expectError: false,
			mockSetup: func(m *MockFeeRepository) {
				m.On("CreateAllocations", mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "allocate FX_SLIPPAGE successfully",
			feeDetails: []fee.FeeDetail{
				{
					FeeType:    fee.FeeTypeFXSlippage,
					Amount:     200,
					SourceData: map[string]interface{}{"provider": "exchange_a", "order_id": "order-456", "slippage_bps": int64(50)},
				},
			},
			expectError: false,
			mockSetup: func(m *MockFeeRepository) {
				m.On("CreateAllocations", mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "allocate multiple fees",
			feeDetails: []fee.FeeDetail{
				{
					FeeType:    fee.FeeTypeBank,
					Amount:     100,
					SourceData: map[string]interface{}{"payout_id": "payout-1", "fee_type": "bank_fee"},
				},
				{
					FeeType:    fee.FeeTypeNetwork,
					Amount:     50,
					SourceData: map[string]interface{}{"payout_id": "payout-1", "fee_type": "network_fee", "network": "TRON"},
				},
			},
			expectError: false,
			mockSetup: func(m *MockFeeRepository) {
				m.On("CreateAllocations", mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name:        "empty fee details should not fail",
			feeDetails:  []fee.FeeDetail{},
			expectError: false,
			mockSetup: func(m *MockFeeRepository) {
				m.On("CreateAllocations", mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "repository returns error",
			feeDetails: []fee.FeeDetail{
				{
					FeeType: fee.FeeTypeLP,
					Amount:  100,
				},
			},
			expectError: true,
			mockSetup: func(m *MockFeeRepository) {
				m.On("CreateAllocations", mock.Anything, mock.Anything).Return(assert.AnError)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRepo := new(MockFeeRepository)
			tc.mockSetup(mockRepo)

			service := NewFeeService(mockRepo)

			transactionID := uuid.New()
			merchantID := uuid.New()

			err := service.AllocateTransactionFees(context.Background(), transactionID, merchantID, "USD", tc.feeDetails)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

func TestFeeService_GetTransactionFees(t *testing.T) {
	testCases := []struct {
		name          string
		transactionID uuid.UUID
		expectCount   int
		expectError   bool
		mockSetup     func(*MockFeeRepository)
	}{
		{
			name:          "get fees for existing transaction",
			transactionID: uuid.New(),
			expectCount:   2,
			expectError:   false,
			mockSetup: func(m *MockFeeRepository) {
				m.On("GetByTransactionID", mock.Anything, mock.Anything).Return([]domain.SettlementFeeAllocation{
					{ID: uuid.New(), TransactionID: uuid.New(), FeeType: domain.FeeTypeLP},
					{ID: uuid.New(), TransactionID: uuid.New(), FeeType: domain.FeeTypeGas},
				}, nil)
			},
		},
		{
			name:          "get fees for non-existing transaction",
			transactionID: uuid.New(),
			expectCount:   0,
			expectError:   false,
			mockSetup: func(m *MockFeeRepository) {
				m.On("GetByTransactionID", mock.Anything, mock.Anything).Return([]domain.SettlementFeeAllocation{}, nil)
			},
		},
		{
			name:          "repository returns error",
			transactionID: uuid.New(),
			expectCount:   0,
			expectError:   true,
			mockSetup: func(m *MockFeeRepository) {
				m.On("GetByTransactionID", mock.Anything, mock.Anything).Return([]domain.SettlementFeeAllocation{}, assert.AnError)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRepo := new(MockFeeRepository)
			tc.mockSetup(mockRepo)

			service := NewFeeService(mockRepo)

			fees, err := service.GetTransactionFees(context.Background(), tc.transactionID)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if fees != nil {
					assert.Len(t, fees, tc.expectCount)
				}
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

func TestFeeService_GetMerchantFees(t *testing.T) {
	testCases := []struct {
		name        string
		merchantID  uuid.UUID
		expectCount int
		expectError bool
		mockSetup   func(*MockFeeRepository)
	}{
		{
			name:        "get fees for existing merchant",
			merchantID:  uuid.New(),
			expectCount: 3,
			expectError: false,
			mockSetup: func(m *MockFeeRepository) {
				m.On("GetByMerchantID", mock.Anything, mock.Anything).Return([]domain.SettlementFeeAllocation{
					{ID: uuid.New(), MerchantID: uuid.New(), FeeType: domain.FeeTypeBank},
					{ID: uuid.New(), MerchantID: uuid.New(), FeeType: domain.FeeTypeNetwork},
					{ID: uuid.New(), MerchantID: uuid.New(), FeeType: domain.FeeTypeGas},
				}, nil)
			},
		},
		{
			name:        "get fees for non-existing merchant",
			merchantID:  uuid.New(),
			expectCount: 0,
			expectError: false,
			mockSetup: func(m *MockFeeRepository) {
				m.On("GetByMerchantID", mock.Anything, mock.Anything).Return([]domain.SettlementFeeAllocation{}, nil)
			},
		},
		{
			name:        "repository returns error",
			merchantID:  uuid.New(),
			expectCount: 0,
			expectError: true,
			mockSetup: func(m *MockFeeRepository) {
				m.On("GetByMerchantID", mock.Anything, mock.Anything).Return([]domain.SettlementFeeAllocation{}, assert.AnError)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRepo := new(MockFeeRepository)
			tc.mockSetup(mockRepo)

			service := NewFeeService(mockRepo)

			fees, err := service.GetMerchantFees(context.Background(), tc.merchantID)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if fees != nil {
					assert.Len(t, fees, tc.expectCount)
				}
			}

			mockRepo.AssertExpectations(t)
		})
	}
}
