package issuing

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type IssuingTier string

const (
	IssuingTierAggregator IssuingTier = "TIER_1_AGGREGATOR"
	IssuingTierDirect     IssuingTier = "TIER_2_DIRECT"
)

func (t IssuingTier) Validate() error {
	switch t {
	case IssuingTierAggregator, IssuingTierDirect:
		return nil
	default:
		return ErrIssuingTierUnknown
	}
}

type IssuingFundingMode string

const (
	FundingModeCryptoDesensitized IssuingFundingMode = "CRYPTO_DESENSITIZED"
	FundingModeFiatDirect         IssuingFundingMode = "FIAT_DIRECT"
)

func (m IssuingFundingMode) Validate() error {
	switch m {
	case FundingModeCryptoDesensitized, FundingModeFiatDirect:
		return nil
	default:
		return ErrFundingModeUnknown
	}
}

type VirtualCardStatus string

const (
	CardStatusPending              VirtualCardStatus = "PENDING"
	CardStatusActive               VirtualCardStatus = "ACTIVE"
	CardStatusFrozen               VirtualCardStatus = "FROZEN"
	CardStatusTerminated           VirtualCardStatus = "TERMINATED"
	CardStatusUnknownExternalState VirtualCardStatus = "UNKNOWN_EXTERNAL_STATE"
	CardStatusManualReview         VirtualCardStatus = "MANUAL_REVIEW"
)

func (s VirtualCardStatus) Validate() error {
	switch s {
	case CardStatusPending, CardStatusActive, CardStatusFrozen,
		CardStatusTerminated, CardStatusUnknownExternalState, CardStatusManualReview:
		return nil
	default:
		return ErrCardStatusUnknown
	}
}

type IssuingProvider struct {
	ID                 uuid.UUID
	EntityID           *uuid.UUID
	IssuingTier        IssuingTier
	FundingMode        IssuingFundingMode
	ProviderCode       string
	PoolCode           string
	CredentialVaultKey string
	Status             string
	Version            int
	ProviderConfig     map[string]any
	VelocityConfig     map[string]any
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type VirtualCard struct {
	ID                         uuid.UUID
	MerchantID                 uuid.UUID
	ProviderID                 uuid.UUID
	ProviderCardRef            string
	Last4                      string
	Status                     VirtualCardStatus
	Currency                   string
	SpendingLimitMinor         int64
	DeclinedCountSlidingWindow int
	DeclinedWindowStartedAt    *time.Time
	FraudRateBps               int
	LastDeclineReason          string
	IdempotencyKey             string
	PayloadHash                string
	CreatedBy                  string
	FrozenReason               string
	TerminatedReason           string
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type CardAuthEvent struct {
	EventID           string
	CardID            uuid.UUID
	ProviderRef       string
	MerchantID        uuid.UUID
	Amount            decimal.Decimal
	Currency          string
	Status            string
	IsDeclined        bool
	DeclineReason     string
	AuthorizationCode string
	Timestamp         time.Time
}

type ProviderStatus struct {
	ProviderID    uuid.UUID
	IsActive      bool
	FundingMode   IssuingFundingMode
	Version       int
	LastUpdatedAt time.Time
}

var (
	ErrProviderNotFound                  = errors.New("issuing: provider not found")
	ErrProviderNotActive                 = errors.New("issuing: provider not active")
	ErrProviderCodeUnregistered          = errors.New("issuing: provider_code not registered in factory")
	ErrCredentialVaultKeyEmpty           = errors.New("issuing: credential_vault_key is empty")
	ErrCredentialDecryptFailed           = errors.New("issuing: vault decrypt failed")
	ErrIssuingTierUnknown                = errors.New("issuing: issuing_tier unknown")
	ErrFundingModeUnknown                = errors.New("issuing: funding_mode unknown")
	ErrFundingModeMismatch               = errors.New("issuing: funding_mode mismatch with source")
	ErrMerchantFraudRateExceeded         = errors.New("issuing: merchant fraud rate exceeded threshold")
	ErrCardDeclinedSlidingWindowExceeded = errors.New("issuing: card declined sliding window exceeded")
	ErrWebhookSignatureInvalid           = errors.New("issuing: webhook signature invalid")
	ErrProviderTimeout                   = errors.New("issuing: provider timeout")
	ErrUnknownExternalState              = errors.New("issuing: unknown external state")
	ErrCardNotFound                      = errors.New("issuing: card not found")
	ErrCardStatusUnknown                 = errors.New("issuing: card status unknown")
	ErrMerchantIssuingNotAllowed         = errors.New("issuing: merchant not allowed to issue cards")
	ErrDuplicateCard                     = errors.New("issuing: duplicate card (idempotency)")
)

func IsTimeout(err error) bool {
	return errors.Is(err, ErrProviderTimeout)
}

func IsUnknownState(err error) bool {
	return errors.Is(err, ErrUnknownExternalState)
}
