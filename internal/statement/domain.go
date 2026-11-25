// Package statement 生成日终对账单（A2）。
//
// 口径：
//   - PAYMENT：当日捕获的支付（captured_at 落在营业日）；
//   - REFUND：当日成功退款（succeeded_at 落在营业日）；
//   - SETTLEMENT：当日结算批次（settlement_batch.business_date）；
//   - 净额 = 支付金额 - 退款金额；结算金额单独列示，不参与净额。
package statement

import (
	"time"

	"github.com/google/uuid"
)

// Statement 对账单头。
type Statement struct {
	ID                 uuid.UUID `json:"id"`
	TenantID           uuid.UUID `json:"tenant_id"`
	MerchantID         uuid.UUID `json:"merchant_id"`
	Currency           string    `json:"currency"`
	BusinessDate       string    `json:"business_date"`
	Status             string    `json:"status"`
	PaymentCount       int       `json:"payment_count"`
	PaymentAmountMinor int64     `json:"payment_amount_minor"`
	RefundCount        int       `json:"refund_count"`
	RefundAmountMinor  int64     `json:"refund_amount_minor"`
	FeeAmountMinor     int64     `json:"fee_amount_minor"`
	NetAmountMinor     int64     `json:"net_amount_minor"`
	SettledAmountMinor int64     `json:"settled_amount_minor"`
	GeneratedAt        time.Time `json:"generated_at"`
}

// Line 对账单明细。
type Line struct {
	ID              uuid.UUID `json:"id"`
	StatementID     uuid.UUID `json:"statement_id"`
	LineType        string    `json:"line_type"`
	ReferenceID     uuid.UUID `json:"reference_id"`
	MerchantOrderID string    `json:"merchant_order_id"`
	AmountMinor     int64     `json:"amount_minor"`
	Currency        string    `json:"currency"`
	OccurredAt      time.Time `json:"occurred_at"`
	Description     string    `json:"description"`
}

// 明细类型。
const (
	LinePayment    = "PAYMENT"
	LineRefund     = "REFUND"
	LineSettlement = "SETTLEMENT"
)
