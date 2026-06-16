package issuing

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type IssuingAdapter interface {
	CreateCard(ctx context.Context, req CreateCardRequest) (*CreateCardResult, error)
	FreezeCard(ctx context.Context, req FreezeCardRequest) error
	TerminateCard(ctx context.Context, req TerminateCardRequest) error
	ProcessAuthWebhook(ctx context.Context, req ProcessAuthWebhookRequest) (*CardAuthEvent, error)
}

type IssuingAdapterFactory interface {
	Create(ctx context.Context, providerCode string, credential map[string]string) (IssuingAdapter, error)
	Register(providerCode string, factory func(context.Context, map[string]string) (IssuingAdapter, error))
}

type CreateCardRequest struct {
	MerchantID         uuid.UUID
	ProviderID         uuid.UUID
	PoolCode           string
	Currency           string
	SpendingLimitMinor int64
	IdempotencyKey     string
	PayloadHash        string
	CreatedBy          string
	ProviderConfig     map[string]any
}

type CreateCardResult struct {
	CardID          uuid.UUID
	ProviderCardRef string
	Last4           string
	Status          VirtualCardStatus
	CreatedAt       time.Time
}

type FreezeCardRequest struct {
	CardID       uuid.UUID
	ProviderID   uuid.UUID
	FrozenReason string
}

type TerminateCardRequest struct {
	CardID           uuid.UUID
	ProviderID       uuid.UUID
	TerminatedReason string
}

type ProcessAuthWebhookRequest struct {
	ProviderID uuid.UUID
	RawPayload []byte
	Signature  string
	Timestamp  time.Time
	WebhookID  string
}

type VerifiedWebhookEvent struct {
	EventID     string
	EventType   string
	CardID      uuid.UUID
	Amount      decimal.Decimal
	Currency    string
	ProviderRef string
	Timestamp   time.Time
}
