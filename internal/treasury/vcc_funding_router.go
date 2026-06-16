package treasury

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	issuing "github.com/nexusglobal/mamr/internal/issuing"
)

type ProviderTier = domain.ProviderTier
type FundingMode = issuing.IssuingFundingMode

const (
	ProviderTierHighRisk          ProviderTier = domain.ProviderTierHighRisk
	ProviderTierCompliant         ProviderTier = domain.ProviderTierCompliant
	FundingModeCryptoDesensitized FundingMode  = issuing.FundingModeCryptoDesensitized
	FundingModeFiatDirect         FundingMode  = issuing.FundingModeFiatDirect
)

type VCCFundingRouter interface {
	RouteFunding(ctx context.Context, req FundingRouteRequest) (*FundingRouteResult, error)
}

type FundingRouteRequest struct {
	MerchantID        uuid.UUID
	SourceChannelID   uuid.UUID
	SourceTier        ProviderTier
	TargetProviderID  uuid.UUID
	TargetFundingMode FundingMode
	AmountMinor       int64
	Currency          string
	SourceLedgerTxID  string
}

type FundingRouteResult struct {
	Approved         bool
	RouteType        RouteType
	BridgeBatchID    string
	LPCConversionRef string
	TargetProviderID uuid.UUID
	Provenance       FundingProvenance
	RejectReason     string
}

type RouteType string

const (
	RouteTypeFiatDirect         RouteType = "FIAT_DIRECT"
	RouteTypeCryptoDesensitized RouteType = "CRYPTO_DESENSITIZED"
	RouteTypeRejected           RouteType = "REJECTED"
)

type FundingProvenance struct {
	SourceTier              ProviderTier
	SourceRail              string
	SourceLedgerTxID        string
	CryptoBridgeBatchID     string
	LPCConversionRef        string
	TargetIssuingProviderID uuid.UUID
	Timestamp               time.Time
}

type VCCFundingRepository interface {
	LockMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID) error
	DebitMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64, txID string) error
	CreditPendingCryptoBridgeBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64) error
	WriteOutboxEvent(ctx context.Context, event OutboxEvent) error
	GetMerchantRiskProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error)
	GetIssuingProvider(ctx context.Context, providerID uuid.UUID) (*issuing.IssuingProvider, error)
}

type OutboxEvent struct {
	ID          uuid.UUID
	EventType   string
	Payload     []byte
	Status      string
	RetryCount  int
	CreatedAt   time.Time
	ProcessedAt *time.Time
}

type MerchantRiskProfile struct {
	MerchantID uuid.UUID
	RiskScore  int
	IsBlocked  bool
	SourceTier ProviderTier
}

type vccFundingRouter struct {
	repo           VCCFundingRepository
	maxSlippageBps int
}

func NewVCCFundingRouter(repo VCCFundingRepository, maxSlippageBps int) VCCFundingRouter {
	return &vccFundingRouter{
		repo:           repo,
		maxSlippageBps: maxSlippageBps,
	}
}

var (
	ErrHighRiskFiatDirect         = errors.New("treasury: high-risk acquirer with FIAT_DIRECT funding mode is not allowed")
	ErrUnknownSourceTier          = errors.New("treasury: unknown source tier")
	ErrUnknownFundingMode         = errors.New("treasury: unknown funding mode")
	ErrInvalidAmount              = errors.New("treasury: amount must be positive")
	ErrInsufficientBalance        = errors.New("treasury: insufficient merchant balance")
	ErrMerchantRiskProfileMissing = errors.New("treasury: merchant risk profile missing")
	ErrIssuingProviderMissing     = errors.New("treasury: issuing provider not found")
	ErrMissingProvenanceMetadata  = errors.New("treasury: missing provenance metadata")
)

func (r *vccFundingRouter) RouteFunding(ctx context.Context, req FundingRouteRequest) (*FundingRouteResult, error) {
	if err := validateRouteRequest(req); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: err.Error(),
		}, err
	}

	if err := r.checkHighRiskFiatDirect(req.SourceTier, req.TargetFundingMode); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: err.Error(),
		}, err
	}

	riskProfile, err := r.repo.GetMerchantRiskProfile(ctx, req.MerchantID)
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrMerchantRiskProfileMissing.Error(),
		}, ErrMerchantRiskProfileMissing
	}

	if riskProfile.IsBlocked {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "merchant is blocked",
		}, errors.New("merchant is blocked")
	}

	provider, err := r.repo.GetIssuingProvider(ctx, req.TargetProviderID)
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrIssuingProviderMissing.Error(),
		}, ErrIssuingProviderMissing
	}

	if provider.FundingMode != req.TargetFundingMode {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "funding mode mismatch with provider",
		}, errors.New("funding mode mismatch")
	}

	return r.executeRouting(ctx, req, riskProfile)
}

func validateRouteRequest(req FundingRouteRequest) error {
	if req.AmountMinor <= 0 {
		return ErrInvalidAmount
	}

	if err := req.SourceTier.Validate(); err != nil {
		return ErrUnknownSourceTier
	}

	if err := req.TargetFundingMode.Validate(); err != nil {
		return ErrUnknownFundingMode
	}

	if req.SourceLedgerTxID == "" {
		return ErrMissingProvenanceMetadata
	}

	return nil
}

func (r *vccFundingRouter) checkHighRiskFiatDirect(sourceTier ProviderTier, fundingMode FundingMode) error {
	if sourceTier == ProviderTierHighRisk && fundingMode == FundingModeFiatDirect {
		return ErrHighRiskFiatDirect
	}
	return nil
}

func (r *vccFundingRouter) executeRouting(ctx context.Context, req FundingRouteRequest, riskProfile *MerchantRiskProfile) (*FundingRouteResult, error) {
	switch req.TargetFundingMode {
	case FundingModeFiatDirect:
		return r.executeFiatDirect(ctx, req)

	case FundingModeCryptoDesensitized:
		return r.executeCryptoDesensitized(ctx, req)

	default:
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrUnknownFundingMode.Error(),
		}, ErrUnknownFundingMode
	}
}

func (r *vccFundingRouter) executeFiatDirect(ctx context.Context, req FundingRouteRequest) (*FundingRouteResult, error) {
	if err := r.repo.LockMerchantSettledBalance(ctx, req.MerchantID); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to lock balance",
		}, err
	}

	if err := r.repo.DebitMerchantSettledBalance(ctx, req.MerchantID, req.AmountMinor, req.SourceLedgerTxID); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrInsufficientBalance.Error(),
		}, ErrInsufficientBalance
	}

	provenance := FundingProvenance{
		SourceTier:              req.SourceTier,
		SourceRail:              req.SourceChannelID.String(),
		SourceLedgerTxID:        req.SourceLedgerTxID,
		TargetIssuingProviderID: req.TargetProviderID,
		Timestamp:               time.Now(),
	}

	return &FundingRouteResult{
		Approved:         true,
		RouteType:        RouteTypeFiatDirect,
		TargetProviderID: req.TargetProviderID,
		Provenance:       provenance,
	}, nil
}

func (r *vccFundingRouter) executeCryptoDesensitized(ctx context.Context, req FundingRouteRequest) (*FundingRouteResult, error) {
	if err := r.repo.LockMerchantSettledBalance(ctx, req.MerchantID); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to lock balance",
		}, err
	}

	if err := r.repo.DebitMerchantSettledBalance(ctx, req.MerchantID, req.AmountMinor, req.SourceLedgerTxID); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrInsufficientBalance.Error(),
		}, ErrInsufficientBalance
	}

	if err := r.repo.CreditPendingCryptoBridgeBalance(ctx, req.MerchantID, req.AmountMinor); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to credit bridge balance",
		}, err
	}

	bridgeBatchID := uuid.New().String()
	provenance := FundingProvenance{
		SourceTier:              req.SourceTier,
		SourceRail:              req.SourceChannelID.String(),
		SourceLedgerTxID:        req.SourceLedgerTxID,
		CryptoBridgeBatchID:     bridgeBatchID,
		TargetIssuingProviderID: req.TargetProviderID,
		Timestamp:               time.Now(),
	}

	outboxEvent := OutboxEvent{
		ID:         uuid.New(),
		EventType:  "VCC_CRYPTO_BRIDGE_REQUESTED",
		Payload:    []byte(`{"bridge_batch_id": "` + bridgeBatchID + `", "merchant_id": "` + req.MerchantID.String() + `", "amount_minor": ` + string(rune(req.AmountMinor)) + `}`),
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	if err := r.repo.WriteOutboxEvent(ctx, outboxEvent); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to write outbox event",
		}, err
	}

	return &FundingRouteResult{
		Approved:         true,
		RouteType:        RouteTypeCryptoDesensitized,
		BridgeBatchID:    bridgeBatchID,
		TargetProviderID: req.TargetProviderID,
		Provenance:       provenance,
	}, nil
}
