// Package refund 实现商户退款（A2）。
//
// 流程：
//  1. POST /merchant/refunds 在本地事务内创建 refund_request + REFUND_REQUESTED Outbox。
//  2. Worker 在事务外调用上游退款，成功后在同一事务内：
//     更新 refund → SUCCEEDED、账本 DEBIT SETTLED / CREDIT PROVIDER_RECEIVABLE、
//     投影 settled_balance 减少、attempt 状态推进、写商户回调事件。
//  3. 明确拒绝 → FAILED；超时/未知 → UNKNOWN
//     （24h 窗口内由 Reconciler 以相同幂等键重放自动对账，超窗留给人工）。
package refund

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// 状态。
const (
	StatusPending    = "PENDING"
	StatusProcessing = "PROCESSING"
	StatusSucceeded  = "SUCCEEDED"
	StatusFailed     = "FAILED"
	StatusUnknown    = "UNKNOWN"
)

// 错误。
var (
	ErrAttemptNotFound     = errors.New("refund: payment attempt not found")
	ErrNotRefundable       = errors.New("refund: payment attempt is not refundable")
	ErrAmountExceeds       = errors.New("refund: refund amount exceeds refundable balance")
	ErrIdempotencyConflict = errors.New("refund: idempotency key reused with different payload")
	ErrRefundNotFound      = errors.New("refund: not found")
)

// Refund 退款单。
type Refund struct {
	ID               uuid.UUID `json:"id"`
	TenantID         uuid.UUID `json:"tenant_id"`
	MerchantID       uuid.UUID `json:"merchant_id"`
	PaymentAttemptID uuid.UUID `json:"payment_attempt_id"`
	MerchantRefundID string    `json:"merchant_refund_id"`
	IdempotencyKey   string    `json:"-"`
	AmountMinor      int64     `json:"amount_minor"`
	Currency         string    `json:"currency"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason"`
	ProviderRefundID string    `json:"provider_refund_id"`
	FailureCode      string    `json:"failure_code"`
	FailureReason    string    `json:"failure_reason"`
	Version          int       `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
}

// AttemptInfo 退款所需的上游支付信息。
type AttemptInfo struct {
	ID                 uuid.UUID
	MerchantID         uuid.UUID
	AcquirerChannelID  uuid.UUID
	AmountMinor        int64
	Currency           string
	Status             string
	ProviderRef        string
	ProviderTxID       string
	ProviderIdempotKey string
	Version            int
}
