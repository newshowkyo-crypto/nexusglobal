package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type AcquirerAdapter interface {
	Authorize(ctx context.Context, req AuthorizeRequest) (*AuthorizeResult, error)
	Capture(ctx context.Context, req CaptureRequest) (*CaptureResult, error)
	Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
	VerifyWebhook(ctx context.Context, req VerifyWebhookRequest) (*VerifiedWebhookEvent, error)
}

type AuthorizeRequest struct {
	OrderID            string
	MerchantID         uuid.UUID
	ChannelID          uuid.UUID
	Amount             decimal.Decimal
	Currency           string
	PaymentMethodToken string
	BuyerIP            string
	CardBIN            string
	IdempotencyKey     string
}

type AuthorizeResult struct {
	TransactionID    uuid.UUID
	ProviderRef      string
	Status           PaymentStatus
	AuthorizedAmount decimal.Decimal
	ExpiresAt        *time.Time
}

type CaptureRequest struct {
	ChannelID      uuid.UUID
	ProviderRef    string
	Amount         decimal.Decimal
	IdempotencyKey string
}

type CaptureResult struct {
	ProviderRef    string
	Status         PaymentStatus
	CapturedAmount decimal.Decimal
}

type RefundRequest struct {
	ChannelID      uuid.UUID
	ProviderRef    string
	Amount         decimal.Decimal
	Reason         string
	IdempotencyKey string
}

type RefundResult struct {
	ProviderRef    string
	Status         PaymentStatus
	RefundedAmount decimal.Decimal
}

type VerifyWebhookRequest struct {
	ChannelID uuid.UUID
	Payload   []byte
	Signature string
}

type VerifiedWebhookEvent struct {
	EventID     string
	EventType   string
	ProviderRef string
	Payload     []byte
}

type PaymentStatus string

const (
	PaymentStatusAuthorized PaymentStatus = "AUTHORIZED"
	PaymentStatusCaptured   PaymentStatus = "CAPTURED"
	PaymentStatusRefunded   PaymentStatus = "REFUNDED"
	PaymentStatusFailed     PaymentStatus = "FAILED"
	PaymentStatusPending    PaymentStatus = "PENDING"
	PaymentStatusUnknown    PaymentStatus = "UNKNOWN"
)

var (
	ErrProviderNotFound         = errors.New("acquirer: provider not found")
	ErrProviderCodeUnregistered = errors.New("acquirer: provider_code not registered in factory")
	ErrCredentialVaultKeyEmpty  = errors.New("acquirer: credential_vault_key is empty")
	ErrCredentialDecrypt        = errors.New("acquirer: vault decrypt failed")
	ErrProviderTierUnknown      = errors.New("acquirer: provider_tier unknown")
	ErrChannelBurnMode          = errors.New("acquirer: channel in BURN_MODE/MANUAL_REVIEW_LOCKDOWN")
	ErrChannelNotActive         = errors.New("acquirer: channel not active")
	ErrChannelNotFound          = errors.New("acquirer: channel not found")
	ErrChannelDisabled          = errors.New("acquirer: channel disabled")
	ErrWebhookSignatureInvalid  = errors.New("acquirer: webhook signature invalid")
	ErrProviderTimeout          = errors.New("acquirer: provider timeout")
	ErrUnknownExternalState     = errors.New("acquirer: unknown external state")
	ErrInvalidInput             = errors.New("acquirer: invalid input")
	ErrExplicitFailure          = errors.New("acquirer: explicit failure")
)

func IsTimeout(err error) bool {
	return errors.Is(err, ErrProviderTimeout)
}

func IsUnknownState(err error) bool {
	return errors.Is(err, ErrUnknownExternalState)
}

type ProviderType string

const (
	ProviderTypeLP   ProviderType = "LP"
	ProviderTypeBank ProviderType = "BANK"
	ProviderTypeMAE  ProviderType = "MAE"
	ProviderTypeCard ProviderType = "CARD"
)

type CredentialType string

const (
	CredentialTypeAPIKey        CredentialType = "API_KEY"
	CredentialTypeSecret        CredentialType = "SECRET"
	CredentialTypePrivateKey    CredentialType = "PRIVATE_KEY"
	CredentialTypeWebhookSecret CredentialType = "WEBHOOK_SECRET"
	CredentialTypeCertificate   CredentialType = "CERTIFICATE"
)

type ProviderTier string

const (
	ProviderTierHighRisk  ProviderTier = "TIER_1_HIGH_RISK"
	ProviderTierCompliant ProviderTier = "TIER_2_COMPLIANT"
)

func (t ProviderTier) Validate() error {
	switch t {
	case ProviderTierHighRisk, ProviderTierCompliant:
		return nil
	default:
		return ErrProviderTierUnknown
	}
}

type ChannelStrategyMode string

const (
	ChannelStrategyModeDefault  ChannelStrategyMode = "DEFAULT"
	ChannelStrategyModeBurnMode ChannelStrategyMode = "BURN_MODE"
)

func (m ChannelStrategyMode) IsBurnMode() bool {
	return m == ChannelStrategyModeBurnMode
}

type PaymentProvider struct {
	ID           uuid.UUID
	ProviderCode string
	ProviderName string
	ProviderType ProviderType
	ConfigJSON   map[string]any
	IsActive     bool
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type AcquirerChannel struct {
	ID                 uuid.UUID
	EntityID           uuid.UUID
	ProviderTier       ProviderTier
	ProviderCode       string
	ChannelCode        string
	CredentialVaultKey string
	StrategyMode       ChannelStrategyMode
	Status             string
	Version            int
	ProviderConfig     map[string]any
	RoutingConfig      map[string]any
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type TenantAcquirerChannel struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	MerchantID       uuid.UUID
	ChannelCode      string
	ChannelName      string
	ProviderID       uuid.UUID
	PaymentMethod    string
	Currencies       []string
	ChannelConfig    map[string]any
	IsActive         bool
	IsolationGroupID *uuid.UUID
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// 从 acquirer_channels 表 JOIN 获取
	ProviderTier      ProviderTier // 通道的 provider_tier
	AcquirerChannelID *uuid.UUID   // 指向 acquirer_channels.id，建立控制面/运行时绑定关系
}

// MerchantRoutingPolicy 是商户的权威路由策略，从数据库读取。
// SelectChannel 必须使用此 policy 的 ProviderTier 精确匹配通道，不得接受 body/header/payload 指定 tier。
type MerchantRoutingPolicy struct {
	MerchantID   uuid.UUID
	ProviderTier ProviderTier
	Status       string // ACTIVE, INACTIVE
	Version      int
}

type ProviderCredentialVault struct {
	ID             uuid.UUID
	ProviderID     uuid.UUID
	ChannelID      *uuid.UUID
	CredentialType CredentialType
	KeyVersion     int
	Ciphertext     []byte
	AADContext     string
	ExpiresAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ClientBundle struct {
	ChannelID        uuid.UUID
	ChannelCode      string
	ProviderID       uuid.UUID
	ProviderCode     string
	MerchantID       uuid.UUID // P0-3: 通道所属商户 ID，用于 AcquirerGateway 校验通道归属
	Client           AcquirerClient
	Config           map[string]any
	IsolationGroupID *uuid.UUID
	Version          int
	IsActive         bool
	LoadedAt         time.Time
	LoadError        error
}

type AcquirerClient interface {
	Authorize(ctx context.Context, req AuthorizeRequest) (*AuthorizeResult, error)
	Capture(ctx context.Context, req CaptureRequest) (*CaptureResult, error)
	Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
	VerifyWebhook(ctx context.Context, req VerifyWebhookRequest) (*VerifiedWebhookEvent, error)
}

type LegacyAuthorizeRequest struct {
	OrderID            string
	MerchantID         uuid.UUID
	Amount             decimal.Decimal
	Currency           string
	CardNumber         string
	CardExpiry         string
	CardCVV            string
	BuyerIP            string
	CardBIN            string
	PaymentMethodToken string
	IdempotencyKey     string
}

type LegacyAuthorizeResponse struct {
	TransactionID    uuid.UUID
	ProviderRef      string
	Status           PaymentStatus
	AuthorizedAmount decimal.Decimal
	ExpiresAt        *time.Time
}

type LegacyCaptureRequest struct {
	ProviderRef    string
	Amount         decimal.Decimal
	IdempotencyKey string
}

type LegacyCaptureResponse struct {
	ProviderRef    string
	Status         PaymentStatus
	CapturedAmount decimal.Decimal
}

type LegacyRefundRequest struct {
	ProviderRef    string
	Amount         decimal.Decimal
	Reason         string
	IdempotencyKey string
}

type LegacyRefundResponse struct {
	ProviderRef    string
	Status         PaymentStatus
	RefundedAmount decimal.Decimal
}

type LegacyQueryRequest struct {
	ProviderRef string
}

type LegacyQueryResponse struct {
	ProviderRef string
	Status      PaymentStatus
	Amount      decimal.Decimal
}

type ExternalResult struct {
	Success           bool
	ProviderRef       string
	Status            PaymentStatus
	Amount            decimal.Decimal
	Err               error
	IsTimeout         bool
	IsExplicitFailure bool
}

type CreateProviderRequest struct {
	ProviderCode string
	Name         string
	ProviderType string
}

type UpdateProviderRequest struct {
	Name     *string
	IsActive *bool
}

type CreateChannelRequest struct {
	TenantID      string
	MerchantID    string
	ProviderID    string
	ChannelCode   string
	ChannelName   string
	PaymentMethod string
	Currencies    []string
	IsActive      bool
}

type UpdateChannelRequest struct {
	ChannelName *string
	Currencies  []string
	IsActive    *bool
}

type ChannelSummary struct {
	ChannelID    uuid.UUID
	ChannelCode  string
	ProviderCode string
	IsActive     bool
	Version      int
}
