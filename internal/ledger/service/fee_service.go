package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/ledger/fee"
)

// FeeService 费用摊销服务
// 实现 fee.FeeServiceInterface 接口
type FeeService struct {
	repo FeeRepository
}

// FeeRepository 费用仓储接口
type FeeRepository interface {
	CreateAllocations(ctx context.Context, allocations []domain.SettlementFeeAllocation) error
	GetByTransactionID(ctx context.Context, transactionID uuid.UUID) ([]domain.SettlementFeeAllocation, error)
	GetByMerchantID(ctx context.Context, merchantID uuid.UUID) ([]domain.SettlementFeeAllocation, error)
}

// NewFeeService 创建费用服务
func NewFeeService(repo FeeRepository) *FeeService {
	return &FeeService{repo: repo}
}

// AllocateTransactionFees 分配交易费用
// 实现 fee.FeeServiceInterface 接口
func (s *FeeService) AllocateTransactionFees(ctx context.Context, transactionID, merchantID uuid.UUID, currency string, feeDetails []fee.FeeDetail) error {
	var results []domain.FeeAllocationResult

	for _, detail := range feeDetails {
		hash := s.generateSourceHash(detail.SourceData)

		results = append(results, domain.FeeAllocationResult{
			FeeType: domain.FeeType(detail.FeeType),
			Amount:  detail.Amount,
			Hash:    hash,
		})
	}

	allocations := domain.AllocateFees(transactionID, merchantID, currency, results)
	return s.repo.CreateAllocations(ctx, allocations)
}

// GetTransactionFees 获取交易费用
// 实现 fee.FeeServiceInterface 接口
func (s *FeeService) GetTransactionFees(ctx context.Context, transactionID uuid.UUID) ([]interface{}, error) {
	results, err := s.repo.GetByTransactionID(ctx, transactionID)
	if err != nil {
		return nil, err
	}
	// 转换为 []interface{}
	interfaces := make([]interface{}, len(results))
	for i, r := range results {
		interfaces[i] = r
	}
	return interfaces, nil
}

// GetMerchantFees 获取商户费用
// 实现 fee.FeeServiceInterface 接口
func (s *FeeService) GetMerchantFees(ctx context.Context, merchantID uuid.UUID) ([]interface{}, error) {
	results, err := s.repo.GetByMerchantID(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	// 转换为 []interface{}
	interfaces := make([]interface{}, len(results))
	for i, r := range results {
		interfaces[i] = r
	}
	return interfaces, nil
}

// generateSourceHash 生成源数据哈希
func (s *FeeService) generateSourceHash(sourceData map[string]interface{}) string {
	data, _ := json.Marshal(sourceData)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
