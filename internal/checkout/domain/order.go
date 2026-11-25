package domain

import (
	"time"

	"github.com/google/uuid"
)

type CheckoutOrder struct {
	ID              uuid.UUID
	MerchantOrderID string
	MerchantID      uuid.UUID
	AmountMinor     int64
	Currency        string
	Status          OrderStatus
	SuccessURL      string
	CancelURL       string
	Customer        *CustomerInfo
	Metadata        map[string]string
	CheckoutURL     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "PENDING"
	OrderStatusPaid      OrderStatus = "PAID"
	OrderStatusFailed    OrderStatus = "FAILED"
	OrderStatusCancelled OrderStatus = "CANCELLED"
)

type CustomerInfo struct {
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Name        string `json:"name"`
	BillingAddr string `json:"billing_address"`
}

type CheckoutSession struct {
	ID          uuid.UUID
	SessionID   string
	OrderID     uuid.UUID
	CheckoutURL string
	Status      SessionStatus
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

type SessionStatus string

const (
	SessionStatusActive    SessionStatus = "ACTIVE"
	SessionStatusExpired   SessionStatus = "EXPIRED"
	SessionStatusPaid      SessionStatus = "PAID"
	SessionStatusCancelled SessionStatus = "CANCELLED"
)
