package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ============================================================================
// Multi-Asset Ledger Domain - 多资产账本业务域
// ============================================================================

// TransactionType 交易类型
type TransactionType string

const (
	TransactionTypeDeposit        TransactionType = "DEPOSIT"        // 入金
	TransactionTypeWithdrawal     TransactionType = "WITHDRAWAL"     // 出金
	TransactionTypeTransfer       TransactionType = "TRANSFER"       // 转账
	TransactionTypeExchange       TransactionType = "ASSET_EXCHANGE" // 资产兑换
	TransactionTypeFee            TransactionType = "FEE"            // 手续费
	TransactionTypeBridgeOut      TransactionType = "BRIDGE_OUT"     // 桥出
	TransactionTypeBridgeIn       TransactionType = "BRIDGE_IN"      // 桥入
	TransactionTypeSettlement     TransactionType = "SETTLEMENT"     // 结算
	TransactionTypeReconciliation TransactionType = "RECONCILIATION" // 对账调整
	// 向后兼容别名
	TxnTypeOrderRefund        TransactionType = "ORDER_REFUND"
	TxnTypeDisputeFee         TransactionType = "DISPUTE_FEE"
	TxnTypeChargeback         TransactionType = "CHARGEBACK"
	TxnTypeChargebackReceived TransactionType = "CHARGEBACK_RECEIVED" // V3: 收到拒付
	TxnTypeChargebackFee      TransactionType = "CHARGEBACK_FEE"      // V3: 拒付手续费
	TxnTypeChargebackReversed TransactionType = "CHARGEBACK_REVERSED" // V3: 拒付撤销
)

// Direction 方向
type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

// TransactionStatus 交易状态
type TransactionStatus string

const (
	TransactionStatusPending          TransactionStatus = "PENDING"
	TransactionStatusProcessing       TransactionStatus = "PROCESSING"
	TransactionStatusConfirmed        TransactionStatus = "CONFIRMED"
	TransactionStatusFailed           TransactionStatus = "FAILED"
	TransactionStatusReversed         TransactionStatus = "REVERSED"
	TransactionStatusRequiresApproval TransactionStatus = "REQUIRES_APPROVAL"
)

// Transaction 交易记录
type Transaction struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID
	OrderID           string
	Type              TransactionType
	SourceAccountID   uuid.UUID
	TargetAccountID   uuid.UUID
	AssetCode         string
	Amount            decimal.Decimal
	FeeAmount         decimal.Decimal
	FeeAssetCode      string
	Status            TransactionStatus
	ExternalRef       string
	BridgeProfileID   *uuid.UUID
	Metadata          map[string]any
	CreatedAt         time.Time
	UpdatedAt         time.Time
	// 向后兼容字段
	IdempotencyKey  string          // 幂等键
	TransactionType TransactionType // 兼容别名 (与 Type 相同)
	AggregateType   string          // 聚合类型
	AggregateID     string          // 聚合ID
	Description     string          // 描述
	Entries         []Entry         // 交易条目（向后兼容）
}

// LedgerEntry 账本条目
type LedgerEntry struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID
	AccountID         uuid.UUID
	TransactionID     uuid.UUID
	TransactionType   TransactionType
	AssetCode         string
	Amount            decimal.Decimal // 正数为增加，负数为减少
	BalanceBefore     decimal.Decimal
	BalanceAfter      decimal.Decimal
	Description       string
	Metadata          map[string]any
	CreatedAt         time.Time
	// 向后兼容字段
	Direction    Direction // 方向 (DEBIT/CREDIT)
	AccountIDStr string    // 账户ID字符串（向后兼容）
}

// Entry 是 LedgerEntry 的别名，保持向后兼容
type Entry = LedgerEntry

// Account 账户（兼容类型）
type Account struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Type      string
	Name      string
	Currency  string
	Balance   decimal.Decimal
	UpdatedAt time.Time
}

// ValidateBalanced 验证账目平衡
// 向后兼容签名：只接收 entries 列表
func ValidateBalanced(entries []LedgerEntry) error {
	if len(entries) == 0 {
		return ErrNoEntries
	}

	debit := decimal.Zero
	credit := decimal.Zero

	for _, entry := range entries {
		// 检查金额是否为零或负数
		if entry.Amount.IsZero() {
			return ErrZeroAmount
		}
		if entry.Amount.LessThan(decimal.Zero) {
			return ErrZeroAmount
		}

		// 检查方向是否有效
		if entry.Direction != DirectionDebit && entry.Direction != DirectionCredit {
			return ErrInvalidDirection
		}

		if entry.Direction == DirectionDebit {
			debit = debit.Add(entry.Amount)
		} else {
			credit = credit.Add(entry.Amount)
		}
	}

	if !debit.Equal(credit) {
		return ErrImbalanced
	}
	return nil
}

// ValidateBalancedWithTxn 验证账目平衡（带交易信息）
func ValidateBalancedWithTxn(txn Transaction, entries []LedgerEntry) error {
	return ValidateBalanced(entries)
}

// AccountBalance 账户余额
type AccountBalance struct {
	AccountID         uuid.UUID
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID
	AssetCode         string
	Balance           decimal.Decimal
	Available         decimal.Decimal
	Frozen            decimal.Decimal
	UpdatedAt         time.Time
}

// LedgerError 账本错误
var (
	ErrInsufficientBalance    = errors.New("ledger: insufficient balance")
	ErrAccountNotFound        = errors.New("ledger: account not found")
	ErrAssetNotSupported      = errors.New("ledger: asset not supported")
	ErrNegativeAmount         = errors.New("ledger: negative amount")
	ErrInvalidTransactionType = errors.New("ledger: invalid transaction type")
	ErrTransactionNotFound    = errors.New("ledger: transaction not found")
	ErrRequiresApproval       = errors.New("ledger: transaction requires approval")
	ErrImbalanced             = errors.New("ledger: entries not balanced") // V3: 借贷不平衡
	ErrZeroAmount             = errors.New("ledger: zero amount")          // V3: 金额为零
	ErrInvalidDirection       = errors.New("ledger: invalid direction")    // V3: 无效方向
	ErrNoEntries              = errors.New("ledger: no entries")           // V3: 无条目
)

// AccountType 账户类型
type AccountType string

const (
	AccountTypeSettled               AccountType = "SETTLED"                    // 已结算
	AccountTypeFrozenPayout          AccountType = "FROZEN_PAYOUT"              // 冻结出款
	AccountTypeExternalPayout        AccountType = "EXTERNAL_PAYOUT_SETTLEMENT" // 外部出款结算
	AccountTypePendingSettlement     AccountType = "PENDING_SETTLEMENT"         // 待结算
	AccountTypeReserve               AccountType = "RESERVE"                    // 储备
	AccountTypeFee                   AccountType = "FEE"                        // 手续费
	AccountTypeChargeback            AccountType = "CHARGEBACK"                 // 拒付
	AccountTypeAssetBridgeInTransit  AccountType = "ASSET_BRIDGE_IN_TRANSIT"    // 资产桥中转
	AccountTypeExchangeClearing      AccountType = "EXCHANGE_CLEARING"          // 交易所清算
	AccountTypeOnchainClearing       AccountType = "ONCHAIN_CLEARING"           // 链上清算
	AccountTypeAssetExchangeGainLoss AccountType = "ASSET_EXCHANGE_GAIN_LOSS"   // 资产兑换损益
	AccountTypeMerchantCryptoSettled AccountType = "MERCHANT_CRYPTO_SETTLED"    // 商户加密货币结算
)

// IsDebit 判断是否为借方（减少余额）
func (t TransactionType) IsDebit() bool {
	return t == TransactionTypeWithdrawal ||
		t == TransactionTypeTransfer ||
		t == TransactionTypeBridgeOut ||
		t == TransactionTypeFee
}

// IsCredit 判断是否为贷方（增加余额）
func (t TransactionType) IsCredit() bool {
	return t == TransactionTypeDeposit ||
		t == TransactionTypeBridgeIn ||
		t == TransactionTypeSettlement
}

// IsTerminal 判断是否为终态
func (s TransactionStatus) IsTerminal() bool {
	return s == TransactionStatusConfirmed ||
		s == TransactionStatusFailed ||
		s == TransactionStatusReversed
}

// ============================================================================
// Asset Bridge Domain - 资产桥业务域
// ============================================================================

// BridgeStatus 桥状态
type BridgeStatus string

const (
	BridgeStatusPending          BridgeStatus = "PENDING"
	BridgeStatusProcessing       BridgeStatus = "PROCESSING"
	BridgeStatusAwaitingApproval BridgeStatus = "AWAITING_APPROVAL"
	BridgeStatusExecuting        BridgeStatus = "EXECUTING"
	BridgeStatusCompleted        BridgeStatus = "COMPLETED"
	BridgeStatusFailed           BridgeStatus = "FAILED"
	BridgeStatusReversed         BridgeStatus = "REVERSED"
	BridgeStatusUnknown          BridgeStatus = "UNKNOWN_EXTERNAL_STATE"
)

// BridgeDirection 桥方向
type BridgeDirection string

const (
	BridgeDirectionIn  BridgeDirection = "IN"
	BridgeDirectionOut BridgeDirection = "OUT"
)

// ExecutionPolicy 执行策略
type ExecutionPolicy string

const (
	ExecutionPolicyMarket ExecutionPolicy = "MARKET" // 市价单
	ExecutionPolicyLimit  ExecutionPolicy = "LIMIT"  // 限价单
	ExecutionPolicySliced ExecutionPolicy = "SLICED" // 切片执行
)

// BatchingPolicy 批处理策略
type BatchingPolicy string

const (
	BatchingPolicyImmediate  BatchingPolicy = "IMMEDIATE"
	BatchingPolicyAccumulate BatchingPolicy = "ACCUMULATE"
	BatchingPolicyScheduled  BatchingPolicy = "SCHEDULED"
)

// BridgeProfile 资产桥配置文件
type BridgeProfile struct {
	ID                   uuid.UUID
	TenantID             uuid.UUID
	CorporateEntityID    *uuid.UUID
	Name                 string
	Description          string
	SourceAsset          string
	TargetAsset          string
	SourceNetwork        string
	TargetNetwork        string
	BufferPercent        decimal.Decimal // 缓冲百分比
	BatchingPolicy       BatchingPolicy
	BatchSize            int
	BatchFrequency       time.Duration
	FXConversionPolicy   string
	ExecutionPolicy      ExecutionPolicy
	SlippageBps          int // 最大滑点（万分之）
	MaxSlippageBps       int // 20bps = 0.2%
	WithdrawalPolicy     string
	MaxTransactionAmount decimal.Decimal
	MinTransactionAmount decimal.Decimal
	DailyLimit           decimal.Decimal
	AllowlistedWallets   []string
	RequireApproval      bool
	ApprovalThreshold    decimal.Decimal
	Enabled              bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// BridgeOperation 桥操作
type BridgeOperation struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	CorporateEntityID     *uuid.UUID
	ProfileID             uuid.UUID
	OrderID               string
	Direction             BridgeDirection
	SourceAsset           string
	TargetAsset           string
	SourceAmount          decimal.Decimal
	TargetAmount          decimal.Decimal
	Rate                  decimal.Decimal
	Status                BridgeStatus
	SourceTransactionRef  string
	TargetTransactionRef  string
	SlippageBps           int
	ActualSlippageBps     int
	ExecutionPolicy       ExecutionPolicy
	AllowlistedWalletUsed bool
	ApprovalRequired      bool
	ApprovedBy            *uuid.UUID
	ApprovedAt            *time.Time
	Metadata              map[string]any
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// SlippageGuardResult 滑点保护结果
type SlippageGuardResult struct {
	Allowed        bool
	MaxSlippageBps int
	EstimatedBps   int
	EstimatedRate  decimal.Decimal
	Message        string
}

// BridgeError 桥错误
var (
	ErrBridgeProfileNotFound   = errors.New("bridge: profile not found")
	ErrBridgeProfileNotEnabled = errors.New("bridge: profile not enabled")
	ErrSlippageExceeded        = errors.New("bridge: slippage exceeds maximum allowed")
	ErrWalletNotAllowlisted    = errors.New("bridge: wallet not in allowlist")
	ErrAmountBelowMinimum      = errors.New("bridge: amount below minimum")
	ErrAmountAboveMaximum      = errors.New("bridge: amount above maximum")
	ErrDailyLimitExceeded      = errors.New("bridge: daily limit exceeded")
	ErrApprovalRequired        = errors.New("bridge: approval required")
	ErrBridgeStatusInvalid     = errors.New("bridge: invalid status transition")
	ErrUnknownExternalState    = errors.New("bridge: unknown external state")
)

// CanTransitionTo 判断桥状态是否可以转换
func (s BridgeStatus) CanTransitionTo(target BridgeStatus) bool {
	switch s {
	case BridgeStatusPending:
		return target == BridgeStatusProcessing ||
			target == BridgeStatusAwaitingApproval ||
			target == BridgeStatusFailed
	case BridgeStatusProcessing:
		return target == BridgeStatusExecuting ||
			target == BridgeStatusAwaitingApproval ||
			target == BridgeStatusFailed ||
			target == BridgeStatusUnknown
	case BridgeStatusAwaitingApproval:
		return target == BridgeStatusProcessing ||
			target == BridgeStatusFailed
	case BridgeStatusExecuting:
		return target == BridgeStatusCompleted ||
			target == BridgeStatusFailed ||
			target == BridgeStatusUnknown
	default:
		return false // 终态不可转换
	}
}

// CalculateSlippage 计算滑点（基于预期价格和实际价格）
func CalculateSlippage(expectedRate, actualRate decimal.Decimal) int {
	if expectedRate.IsZero() {
		return 0
	}
	// 滑点 = |实际价格 - 预期价格| / 预期价格 * 10000 (bps)
	diff := actualRate.Sub(expectedRate).Abs()
	slippage := diff.Div(expectedRate).Mul(decimal.NewFromInt(10000))
	return int(slippage.Round(0).IntPart())
}
