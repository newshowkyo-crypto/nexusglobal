package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type DisputeStatus string

const (
	DisputeStatusOpen        DisputeStatus = "OPEN"
	DisputeStatusUnderReview DisputeStatus = "UNDER_REVIEW"
	DisputeStatusWon         DisputeStatus = "WON"
	DisputeStatusLost        DisputeStatus = "LOST"
	DisputeStatusReversed    DisputeStatus = "REVERSED"
)

type DisputeType string

const (
	DisputeTypeChargeback DisputeType = "CHARGEBACK"
	DisputeTypeRefund     DisputeType = "REFUND"
)

type PaymentDispute struct {
	ID                uuid.UUID       `json:"id"`
	OrderID           string          `json:"order_id"`
	MerchantID        uuid.UUID       `json:"merchant_id"`
	DisputeType       DisputeType     `json:"dispute_type"`
	Status            DisputeStatus   `json:"status"`
	Amount            decimal.Decimal `json:"amount"`
	Currency          string          `json:"currency"`
	Reason            string          `json:"reason"`
	ProviderDisputeID string          `json:"provider_dispute_id"`
	ChargebackFee     decimal.Decimal `json:"chargeback_fee"`
	IdempotencyKey    string          `json:"idempotency_key"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type PlatformDisputeReserveLedger struct {
	ID               uuid.UUID       `json:"id"`
	PaymentDisputeID uuid.UUID       `json:"payment_dispute_id"`
	MerchantID       uuid.UUID       `json:"merchant_id"`
	Amount           decimal.Decimal `json:"amount"`
	Currency         string          `json:"currency"`
	Direction        string          `json:"direction"` // CREDIT or DEBIT
	Description      string          `json:"description"`
	CreatedAt        time.Time       `json:"created_at"`
}

type MerchantNegativeBalance struct {
	ID         uuid.UUID       `json:"id"`
	MerchantID uuid.UUID       `json:"merchant_id"`
	Currency   string          `json:"currency"`
	Amount     decimal.Decimal `json:"amount"`
	DisputeID  *uuid.UUID      `json:"dispute_id"`
	Reason     string          `json:"reason"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

var (
	ErrDisputeNotFound      = errors.New("dispute: not found")
	ErrDisputeAlreadyExists = errors.New("dispute: already exists")
	ErrInvalidDisputeStatus = errors.New("dispute: invalid status transition")
	ErrInsufficientSettled  = errors.New("dispute: insufficient settled balance")
)

type CreateDisputeCmd struct {
	OrderID           string
	MerchantID        uuid.UUID
	DisputeType       DisputeType
	Amount            decimal.Decimal
	Currency          string
	Reason            string
	ProviderDisputeID string
	ChargebackFee     decimal.Decimal
	IdempotencyKey    string
}

type ReverseDisputeCmd struct {
	DisputeID      uuid.UUID
	IdempotencyKey string
}
