package domain

import (
	"time"

	"github.com/google/uuid"
)

// FeeType 费用类型
type FeeType string

const (
	FeeTypeLP         FeeType = "LP_FEE"
	FeeTypeFXSlippage FeeType = "FX_SLIPPAGE"
	FeeTypeGas        FeeType = "GAS_FEE"
	FeeTypeBank       FeeType = "BANK_FEE"
	FeeTypeNetwork    FeeType = "NETWORK_FEE"
)

// FeeAllocationStatus 费用分配状态
type FeeAllocationStatus string

const (
	FeeAllocationStatusAllocated FeeAllocationStatus = "ALLOCATED"
	FeeAllocationStatusPending   FeeAllocationStatus = "PENDING"
	FeeAllocationStatusReviewed  FeeAllocationStatus = "REVIEWED"
)

// SettlementFeeAllocation 结算费用分配
type SettlementFeeAllocation struct {
	ID             uuid.UUID
	TransactionID  uuid.UUID
	MerchantID     uuid.UUID
	FeeType        FeeType
	Amount         int64
	Currency       string
	SourceDataHash string
	Status         FeeAllocationStatus
	CreatedAt      time.Time
}

// FeeAllocationResult 费用分配结果
type FeeAllocationResult struct {
	FeeType FeeType
	Amount  int64
	Hash    string
}

// AllocateFees 分配费用
func AllocateFees(transactionID, merchantID uuid.UUID, currency string, results []FeeAllocationResult) []SettlementFeeAllocation {
	var allocations []SettlementFeeAllocation
	now := time.Now()

	for _, result := range results {
		if result.Amount > 0 {
			allocations = append(allocations, SettlementFeeAllocation{
				ID:             uuid.New(),
				TransactionID:  transactionID,
				MerchantID:     merchantID,
				FeeType:        result.FeeType,
				Amount:         result.Amount,
				Currency:       currency,
				SourceDataHash: result.Hash,
				Status:         FeeAllocationStatusAllocated,
				CreatedAt:      now,
			})
		}
	}

	return allocations
}
