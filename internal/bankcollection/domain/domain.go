package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ============================================================================
// Bank Collection Domain - 银行收款业务域
// ============================================================================

// InboundIntentState 入金意图状态
type InboundIntentState string

const (
	IntentStateCreated           InboundIntentState = "CREATED"
	IntentStateWaitingFunds      InboundIntentState = "WAITING_FUNDS"
	IntentStatePartiallyMatched  InboundIntentState = "PARTIALLY_MATCHED"
	IntentStateConfirmed         InboundIntentState = "CONFIRMED"
	IntentStateReconcileMismatch InboundIntentState = "RECONCILE_MISMATCH"
	IntentStateExpired           InboundIntentState = "EXPIRED"
	IntentStateFailed            InboundIntentState = "FAILED"
)

// VirtualAccountType 虚拟账户类型
type VirtualAccountType string

const (
	VirtualAccountTypeDynamic VirtualAccountType = "DYNAMIC" // 动态虚拟账户
	VirtualAccountTypeStatic  VirtualAccountType = "STATIC"  // 静态虚拟账户 + 唯一引用
)

// InboundIntent 入金意图
type InboundIntent struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	CorporateEntityID  *uuid.UUID
	MerchantID         *uuid.UUID
	OrderID            string
	AssetCode          string
	ExpectedAmount     decimal.Decimal
	BuyerRef           string
	PaymentReference   string // 唯一支付引用
	VirtualAccountType VirtualAccountType
	ProviderRef        string
	State              InboundIntentState
	ReceivedAmount     decimal.Decimal
	ConfirmedAmount    decimal.Decimal
	Metadata           map[string]any
	ExpiresAt          time.Time
	RetryCount         int
	LastError          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// VirtualAccountAllocation 虚拟账户分配结果
type VirtualAccountAllocation struct {
	AccountNumber    string
	RoutingNumber    string // ACH
	IBAN             string // SEPA
	SWIFT            string
	BankName         string
	BankAddress      string
	PaymentReference string // 唯一引用
	ExpiresAt        time.Time
}

// InboundWebhookPayload 入金 webhook 载荷
type InboundWebhookPayload struct {
	EventID          string
	EventType        string
	ProviderRef      string
	AccountNumber    string
	PaymentReference string
	Amount           decimal.Decimal
	AssetCode        string
	TransactionRef   string
	Timestamp        time.Time
	RawPayload       []byte
}

// ReconciliationResult 对账结果
type ReconciliationResult struct {
	IntentID          uuid.UUID
	Matched           bool
	MismatchType      string // AMOUNT_MISMATCH, CURRENCY_MISMATCH, REFERENCE_MISMATCH
	ReceivedAmount    decimal.Decimal
	ExpectedAmount    decimal.Decimal
	ReceivedReference string
	ExpectedReference string
}

// BankCollectionError 银行收款错误
var (
	ErrIntentNotFound       = errors.New("bank collection: intent not found")
	ErrIntentStateInvalid   = errors.New("bank collection: invalid intent state")
	ErrSignatureInvalid     = errors.New("bank collection: webhook signature invalid")
	ErrAmountMismatch       = errors.New("bank collection: amount mismatch")
	ErrCurrencyMismatch     = errors.New("bank collection: currency mismatch")
	ErrReferenceMismatch    = errors.New("bank collection: reference mismatch")
	ErrWebhookReplay        = errors.New("bank collection: webhook replay")
	ErrIntentExpired        = errors.New("bank collection: intent expired")
	ErrProviderUnavailable  = errors.New("bank collection: provider unavailable")
	ErrUnknownExternalState = errors.New("bank collection: unknown external state")
	ErrInsufficientFunds    = errors.New("bank collection: insufficient funds")
	ErrReconciliationFailed = errors.New("bank collection: reconciliation failed")
)

// IsTerminalState 判断是否为终态
func (s InboundIntentState) IsTerminalState() bool {
	return s == IntentStateConfirmed ||
		s == IntentStateReconcileMismatch ||
		s == IntentStateExpired ||
		s == IntentStateFailed
}

// CanTransitionTo 判断状态是否可以转换到目标状态
func (s InboundIntentState) CanTransitionTo(target InboundIntentState) bool {
	switch s {
	case IntentStateCreated:
		return target == IntentStateWaitingFunds ||
			target == IntentStateFailed ||
			target == IntentStateExpired
	case IntentStateWaitingFunds:
		return target == IntentStatePartiallyMatched ||
			target == IntentStateConfirmed ||
			target == IntentStateReconcileMismatch ||
			target == IntentStateFailed ||
			target == IntentStateExpired
	case IntentStatePartiallyMatched:
		return target == IntentStateConfirmed ||
			target == IntentStateReconcileMismatch ||
			target == IntentStateFailed ||
			target == IntentStateExpired
	default:
		return false // 终态不可转换
	}
}

// ValidateTransition 验证状态转换是否合法
func (s InboundIntentState) ValidateTransition(target InboundIntentState) error {
	if s == target {
		return nil // 相同状态允许
	}
	if !s.CanTransitionTo(target) {
		return ErrIntentStateInvalid
	}
	return nil
}
