package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ============================================================================
// Crypto Pay Domain - 加密货币支付业务域
// ============================================================================

// DepositState 存款状态
type DepositState string

const (
	DepositStateCreated           DepositState = "CREATED"
	DepositStateWaitingFunds      DepositState = "WAITING_FUNDS"
	DepositStateConfirming        DepositState = "CONFIRMING"
	DepositStateConfirmed         DepositState = "CONFIRMED"
	DepositStateReconcileMismatch DepositState = "RECONCILE_MISMATCH"
	DepositStateExpired           DepositState = "EXPIRED"
	DepositStateFailed            DepositState = "FAILED"
)

// NetworkType 网络类型
type NetworkType string

const (
	NetworkTypeBitcoin  NetworkType = "BITCOIN"
	NetworkTypeEthereum NetworkType = "ETHEREUM"
	NetworkTypeTron     NetworkType = "TRON"
	NetworkTypePolygon  NetworkType = "POLYGON"
	NetworkTypeSolana   NetworkType = "SOLANA"
)

// DepositIntent 存款意图
type DepositIntent struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	CorporateEntityID     *uuid.UUID
	MerchantID            *uuid.UUID
	OrderID               string
	AssetCode             string
	Network               NetworkType
	ExpectedAmount        decimal.Decimal
	DepositAddress        string
	MemoTag               string // 用于区分存款的 memo/tag
	ConfirmationsRequired int
	ProviderRef           string
	State                 DepositState
	ReceivedAmount        decimal.Decimal
	ConfirmedAmount       decimal.Decimal
	Confirmations         int
	TransactionRef        string
	Metadata              map[string]any
	ExpiresAt             time.Time
	RetryCount            int
	LastError             string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// DepositAddressPool 存款地址池
type DepositAddressPool struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	ChannelID         uuid.UUID
	AssetCode         string
	Network           NetworkType
	Address           string
	Status            string // AVAILABLE, ALLOCATED, USED, DISABLED
	AllocatedIntentID *uuid.UUID
	AllocatedAt       time.Time
	ReleasedAt        time.Time
	UsedAt            time.Time
	Metadata          map[string]any
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ChainTransaction 链上交易
type ChainTransaction struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	IntentID        uuid.UUID
	TransactionHash string
	BlockHash       string
	BlockNumber     int64
	FromAddress     string
	ToAddress       string
	Amount          decimal.Decimal
	AssetCode       string
	Network         NetworkType
	Confirmations   int
	Status          string // PENDING, CONFIRMED, FAILED
	MemoTag         string
	Timestamp       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ListenerConfig 监听器配置
type ListenerConfig struct {
	Network               NetworkType
	RPCURL                string
	IndexerURL            string
	PollingInterval       time.Duration
	ConfirmationThreshold int
	StartBlock            int64
}

// CryptoPayError 加密货币支付错误
var (
	ErrDepositIntentNotFound     = errors.New("crypto pay: deposit intent not found")
	ErrDepositIntentStateInvalid = errors.New("crypto pay: invalid deposit intent state")
	ErrAssetNotSupported         = errors.New("crypto pay: asset not supported")
	ErrNetworkNotSupported       = errors.New("crypto pay: network not supported")
	ErrInsufficientConfirmations = errors.New("crypto pay: insufficient confirmations")
	ErrAmountMismatch            = errors.New("crypto pay: amount mismatch")
	ErrNetworkMismatch           = errors.New("crypto pay: network mismatch")
	ErrMemoMismatch              = errors.New("crypto pay: memo/tag mismatch")
	ErrAddressNotAllowlisted     = errors.New("crypto pay: address not allowlisted")
	ErrAddressPoolEmpty          = errors.New("crypto pay: no available addresses in pool")
	ErrAddressAlreadyAllocated   = errors.New("crypto pay: address already allocated")
	ErrIntentExpired             = errors.New("crypto pay: intent expired")
	ErrUnknownExternalState      = errors.New("crypto pay: unknown external state")
	ErrRPCCallFailed             = errors.New("crypto pay: RPC call failed")
	ErrIndexerCallFailed         = errors.New("crypto pay: indexer call failed")
)

// IsTerminalState 判断是否为终态
func (s DepositState) IsTerminalState() bool {
	return s == DepositStateConfirmed ||
		s == DepositStateReconcileMismatch ||
		s == DepositStateExpired ||
		s == DepositStateFailed
}

// CanTransitionTo 判断状态是否可以转换到目标状态
func (s DepositState) CanTransitionTo(target DepositState) bool {
	switch s {
	case DepositStateCreated:
		return target == DepositStateWaitingFunds ||
			target == DepositStateFailed ||
			target == DepositStateExpired
	case DepositStateWaitingFunds:
		return target == DepositStateConfirming ||
			target == DepositStateConfirmed ||
			target == DepositStateReconcileMismatch ||
			target == DepositStateFailed ||
			target == DepositStateExpired
	case DepositStateConfirming:
		return target == DepositStateConfirmed ||
			target == DepositStateReconcileMismatch ||
			target == DepositStateFailed
	default:
		return false // 终态不可转换
	}
}

// ValidateTransition 验证状态转换是否合法
func (s DepositState) ValidateTransition(target DepositState) error {
	if s == target {
		return nil // 相同状态允许
	}
	if !s.CanTransitionTo(target) {
		return ErrDepositIntentStateInvalid
	}
	return nil
}

// HasEnoughConfirmations 判断确认数是否足够
func (s DepositState) HasEnoughConfirmations(current, required int) bool {
	return current >= required
}
