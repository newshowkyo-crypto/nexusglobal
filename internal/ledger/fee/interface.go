package fee

import (
	"context"

	"github.com/google/uuid"
)

// FeeType 费用类型
type FeeType string

const (
	FeeTypeLP         FeeType = "LP_FEE"      // 流动性提供商费用
	FeeTypeFXSlippage FeeType = "FX_SLIPPAGE" // FX 滑点费用
	FeeTypeBank       FeeType = "BANK_FEE"    // 银行费用
	FeeTypeNetwork    FeeType = "NETWORK_FEE" // 网络费用
	FeeTypeGas        FeeType = "GAS_FEE"     // Gas 费用
)

// FeeDetail 费用详情
type FeeDetail struct {
	FeeType    FeeType                // 费用类型
	Amount     int64                  // 费用金额（最小单位）
	SourceData map[string]interface{} // 来源数据
}

// FeeServiceInterface 费用服务接口
// 定义在独立包中，避免循环导入
type FeeServiceInterface interface {
	// AllocateTransactionFees 分配交易费用
	AllocateTransactionFees(ctx context.Context, transactionID, merchantID uuid.UUID, currency string, feeDetails []FeeDetail) error

	// GetTransactionFees 获取交易费用
	GetTransactionFees(ctx context.Context, transactionID uuid.UUID) ([]interface{}, error)

	// GetMerchantFees 获取商户费用
	GetMerchantFees(ctx context.Context, merchantID uuid.UUID) ([]interface{}, error)
}
