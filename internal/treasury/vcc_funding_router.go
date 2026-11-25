package treasury

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	FinalizeVCCFunding(ctx context.Context, req FinalizeVCCFundingRequest) (*FundingRouteResult, error)
}

// FinalizeVCCFundingRequest 是桥接完成后的最终入金请求。
// 与 FundingRouteRequest 不同，它不触发源资金路由（不扣 settled、不写 VCC_CRYPTO_BRIDGE_REQUESTED），
// 而是将 pending_crypto_bridge 余额转为最终入金（debit pending, credit issuing provider funding balance）。
type FinalizeVCCFundingRequest struct {
	BridgeBatchID    string
	MerchantID       uuid.UUID
	TargetProviderID uuid.UUID
	AmountMinor      int64
	Currency         string
	Provenance       map[string]string
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
	// 原有 pool 版本（保留用于查询类操作和向后兼容）
	LockMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID) error
	DebitMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64, txID string) error
	CreditPendingCryptoBridgeBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64) error
	WriteOutboxEvent(ctx context.Context, event OutboxEvent) error
	GetMerchantRiskProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error)
	GetIssuingProvider(ctx context.Context, providerID uuid.UUID) (*issuing.IssuingProvider, error)

	// 事务版本（P0-A2：原子事务，Lock→Debit→CreditPending→WriteOutbox 共享同一 pgx.Tx）
	BeginTx(ctx context.Context) (pgx.Tx, error)
	LockMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID) error
	DebitMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, txID string) error
	CreditPendingCryptoBridgeBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error
	WriteOutboxEventTx(ctx context.Context, tx pgx.Tx, event OutboxEvent) error

	// P0-A1: Finalize 阶段事务版本（DebitPending→CreditIssuingPool→WriteOutbox 共享同一 pgx.Tx）
	DebitPendingCryptoBridgeBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error
	CreditMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error
	WriteOutboxEventFinalizeTx(ctx context.Context, tx pgx.Tx, event OutboxEvent, idempotencyKey string) error

	// R8: 业务幂等 + issuing provider 资金落点
	CreditIssuingProviderBalanceTx(ctx context.Context, tx pgx.Tx, providerID uuid.UUID, merchantID uuid.UUID, bridgeBatchID string, amountMinor int64, currency string, idempotencyKey string) error
	ClaimFundingRequestTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, sourceLedgerTxID string, targetProviderID uuid.UUID, bridgeBatchID string, amountMinor int64, currency string, routeType string) (claimed bool, err error)
	ClaimFundingFinalizationTx(ctx context.Context, tx pgx.Tx, bridgeBatchID string, targetProviderID uuid.UUID, merchantID uuid.UUID, amountMinor int64, currency string) (claimed bool, err error)
	UpdateFundingRequestStatusTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, sourceLedgerTxID string, targetProviderID uuid.UUID, status string) error

	// P0-1/P0-2: FIAT_DIRECT issuing funding outbox + finalization 状态更新
	WriteOutboxIssuingFundingTx(ctx context.Context, tx pgx.Tx, event OutboxEvent, idempotencyKey string) error
	UpdateFundingFinalizationStatusTx(ctx context.Context, tx pgx.Tx, bridgeBatchID string, targetProviderID uuid.UUID, status string) error
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
	// 稳定 bridge_batch_id：与 crypto 路径相同方式派生
	bridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(req.MerchantID.String()+":"+req.SourceLedgerTxID+":"+req.TargetProviderID.String())).String()

	tx, err := r.repo.BeginTx(ctx)
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to begin tx",
		}, err
	}
	defer tx.Rollback(ctx)

	// 先取得业务幂等所有权
	claimed, err := r.repo.ClaimFundingRequestTx(ctx, tx, req.MerchantID, req.SourceLedgerTxID, req.TargetProviderID, bridgeBatchID, req.AmountMinor, req.Currency, string(RouteTypeFiatDirect))
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to claim funding request",
		}, err
	}
	if !claimed {
		// 已存在 — 此前已处理，幂等成功，不继续 debit/credit
		return &FundingRouteResult{
			Approved:         true,
			RouteType:        RouteTypeFiatDirect,
			BridgeBatchID:    bridgeBatchID,
			TargetProviderID: req.TargetProviderID,
		}, nil
	}

	if err := r.repo.LockMerchantSettledBalanceTx(ctx, tx, req.MerchantID); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to lock balance",
		}, err
	}

	if err := r.repo.DebitMerchantSettledBalanceTx(ctx, tx, req.MerchantID, req.AmountMinor, req.SourceLedgerTxID); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrInsufficientBalance.Error(),
		}, ErrInsufficientBalance
	}

	// Credit issuing provider funding balance（FUNDING_PENDING 状态）
	idempotencyKey := "vcc_funding_fiat:" + bridgeBatchID + ":" + req.TargetProviderID.String()
	if err := r.repo.CreditIssuingProviderBalanceTx(ctx, tx, req.TargetProviderID, req.MerchantID, bridgeBatchID, req.AmountMinor, req.Currency, idempotencyKey); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to credit issuing provider balance",
		}, err
	}

	// 写 VCC_ISSUING_FUNDING_REQUESTED outbox 事件
	fiatPayload, err := json.Marshal(map[string]interface{}{
		"bridge_batch_id":    bridgeBatchID,
		"merchant_id":        req.MerchantID,
		"target_provider_id": req.TargetProviderID,
		"amount_minor":       req.AmountMinor,
		"currency":           req.Currency,
	})
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to marshal outbox payload",
		}, err
	}

	outboxEvent := OutboxEvent{
		ID:         uuid.New(),
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
		Payload:    fiatPayload,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	if err := r.repo.WriteOutboxIssuingFundingTx(ctx, tx, outboxEvent, "vcc_issuing_funding:"+bridgeBatchID+":"+req.TargetProviderID.String()); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to write outbox event",
		}, err
	}

	// 更新 vcc_funding_requests 状态为 COMPLETED
	if err := r.repo.UpdateFundingRequestStatusTx(ctx, tx, req.MerchantID, req.SourceLedgerTxID, req.TargetProviderID, "COMPLETED"); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to update funding request status",
		}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to commit",
		}, err
	}

	provenance := FundingProvenance{
		SourceTier:              req.SourceTier,
		SourceRail:              req.SourceChannelID.String(),
		SourceLedgerTxID:        req.SourceLedgerTxID,
		CryptoBridgeBatchID:     bridgeBatchID,
		TargetIssuingProviderID: req.TargetProviderID,
		Timestamp:               time.Now(),
	}

	return &FundingRouteResult{
		Approved:         true,
		RouteType:        RouteTypeFiatDirect,
		BridgeBatchID:    bridgeBatchID,
		TargetProviderID: req.TargetProviderID,
		Provenance:       provenance,
	}, nil
}

func (r *vccFundingRouter) executeCryptoDesensitized(ctx context.Context, req FundingRouteRequest) (*FundingRouteResult, error) {
	// R8: bridge_batch_id 从稳定业务键派生，避免重试时生成不同 ID 导致重复扣款
	bridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(req.MerchantID.String()+":"+req.SourceLedgerTxID+":"+req.TargetProviderID.String())).String()

	tx, err := r.repo.BeginTx(ctx)
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to begin tx",
		}, err
	}
	defer tx.Rollback(ctx)

	// R8: 先取得业务幂等所有权 — INSERT ON CONFLICT DO NOTHING RETURNING
	claimed, err := r.repo.ClaimFundingRequestTx(ctx, tx, req.MerchantID, req.SourceLedgerTxID, req.TargetProviderID, bridgeBatchID, req.AmountMinor, req.Currency, string(RouteTypeCryptoDesensitized))
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to claim funding request",
		}, err
	}
	if !claimed {
		// 已存在 — 此前已处理，幂等成功，不继续 debit/credit
		return &FundingRouteResult{
			Approved:         true,
			RouteType:        RouteTypeCryptoDesensitized,
			BridgeBatchID:    bridgeBatchID,
			TargetProviderID: req.TargetProviderID,
		}, nil
	}

	if err := r.repo.LockMerchantSettledBalanceTx(ctx, tx, req.MerchantID); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to lock balance",
		}, err
	}

	if err := r.repo.DebitMerchantSettledBalanceTx(ctx, tx, req.MerchantID, req.AmountMinor, req.SourceLedgerTxID); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrInsufficientBalance.Error(),
		}, ErrInsufficientBalance
	}

	if err := r.repo.CreditPendingCryptoBridgeBalanceTx(ctx, tx, req.MerchantID, req.AmountMinor); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to credit bridge balance",
		}, err
	}

	provenance := FundingProvenance{
		SourceTier:              req.SourceTier,
		SourceRail:              req.SourceChannelID.String(),
		SourceLedgerTxID:        req.SourceLedgerTxID,
		CryptoBridgeBatchID:     bridgeBatchID,
		TargetIssuingProviderID: req.TargetProviderID,
		Timestamp:               time.Now(),
	}

	// P0-A4: 用 json.Marshal 构建 payload，禁止手工 JSON 拼接。
	bridgePayload, err := json.Marshal(map[string]interface{}{
		"bridge_batch_id":    bridgeBatchID,
		"merchant_id":        req.MerchantID,
		"amount_minor":       req.AmountMinor,
		"currency":           req.Currency,
		"source_tier":        string(req.SourceTier),
		"target_provider_id": req.TargetProviderID,
	})
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to marshal outbox payload",
		}, err
	}

	outboxEvent := OutboxEvent{
		ID:         uuid.New(),
		EventType:  "VCC_CRYPTO_BRIDGE_REQUESTED",
		Payload:    bridgePayload,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	if err := r.repo.WriteOutboxEventTx(ctx, tx, outboxEvent); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to write outbox event",
		}, err
	}

	// R8: Commit 前更新 vcc_funding_requests 状态为 COMPLETED
	if err := r.repo.UpdateFundingRequestStatusTx(ctx, tx, req.MerchantID, req.SourceLedgerTxID, req.TargetProviderID, "COMPLETED"); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to update funding request status",
		}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to commit",
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

// FinalizeVCCFunding 执行桥接完成后的最终入金。
//
// 与 RouteFunding 的关键区别：
//   - 不调用 RouteFunding（避免无限递归）
//   - 不扣 settled_balance（资金已在 RouteFunding 阶段从 settled 转入 pending）
//   - 不写 VCC_CRYPTO_BRIDGE_REQUESTED（桥接已完成，无需再次请求）
//   - debit pending_balance + credit issuing provider funding balance（资金从在途转为发卡池入金）
//   - 写 VCC_ISSUING_FUNDING_REQUESTED outbox 事件（不是 VCC_FUNDING_COMPLETED）
//   - 幂等键：vcc_funding_finalize:<bridge_batch_id>:<target_provider_id>
func (r *vccFundingRouter) FinalizeVCCFunding(ctx context.Context, req FinalizeVCCFundingRequest) (*FundingRouteResult, error) {
	if req.AmountMinor <= 0 {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrInvalidAmount.Error(),
		}, ErrInvalidAmount
	}

	if req.BridgeBatchID == "" {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "missing bridge_batch_id",
		}, errors.New("treasury: finalize VCC funding missing bridge_batch_id")
	}

	if req.MerchantID == uuid.Nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "missing merchant_id",
		}, errors.New("treasury: finalize VCC funding missing merchant_id")
	}

	tx, err := r.repo.BeginTx(ctx)
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to begin tx",
		}, err
	}
	defer tx.Rollback(ctx)

	// R8: 先取得业务幂等所有权 — INSERT ON CONFLICT DO NOTHING RETURNING
	claimed, err := r.repo.ClaimFundingFinalizationTx(ctx, tx, req.BridgeBatchID, req.TargetProviderID, req.MerchantID, req.AmountMinor, req.Currency)
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to claim funding finalization",
		}, err
	}
	if !claimed {
		// 已存在 — 此前已处理，幂等成功，不继续 debit/credit
		return &FundingRouteResult{
			Approved:         true,
			RouteType:        RouteTypeCryptoDesensitized,
			BridgeBatchID:    req.BridgeBatchID,
			TargetProviderID: req.TargetProviderID,
		}, nil
	}

	if err := r.repo.DebitPendingCryptoBridgeBalanceTx(ctx, tx, req.MerchantID, req.AmountMinor); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: ErrInsufficientBalance.Error(),
		}, err
	}

	// R8: credit issuing provider funding balance（不是 merchant settled_balance）
	idempotencyKey := "vcc_funding_finalize:" + req.BridgeBatchID + ":" + req.TargetProviderID.String()
	if err := r.repo.CreditIssuingProviderBalanceTx(ctx, tx, req.TargetProviderID, req.MerchantID, req.BridgeBatchID, req.AmountMinor, req.Currency, idempotencyKey); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to credit issuing provider balance",
		}, err
	}

	finalizePayload, err := json.Marshal(map[string]interface{}{
		"bridge_batch_id":    req.BridgeBatchID,
		"merchant_id":        req.MerchantID,
		"amount_minor":       req.AmountMinor,
		"currency":           req.Currency,
		"target_provider_id": req.TargetProviderID,
	})
	if err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to marshal outbox payload",
		}, err
	}

	outboxEvent := OutboxEvent{
		ID:         uuid.New(),
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
		Payload:    finalizePayload,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	if err := r.repo.WriteOutboxEventFinalizeTx(ctx, tx, outboxEvent, "vcc_issuing_funding:"+req.BridgeBatchID+":"+req.TargetProviderID.String()); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to write outbox event",
		}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return &FundingRouteResult{
			Approved:     false,
			RouteType:    RouteTypeRejected,
			RejectReason: "failed to commit",
		}, err
	}

	provenance := FundingProvenance{
		CryptoBridgeBatchID:     req.BridgeBatchID,
		TargetIssuingProviderID: req.TargetProviderID,
		Timestamp:               time.Now(),
	}

	return &FundingRouteResult{
		Approved:         true,
		RouteType:        RouteTypeCryptoDesensitized,
		BridgeBatchID:    req.BridgeBatchID,
		TargetProviderID: req.TargetProviderID,
		Provenance:       provenance,
	}, nil
}
