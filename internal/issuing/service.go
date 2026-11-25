package issuing

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	registry     ProviderRegistry
	factory      IssuingAdapterFactory
	fraudGuard   FraudGuard
	vaultService VaultService
	cardRepo     CardRepository
	providerRepo ProviderRepository
	ledger       *LedgerPoster
}

// VaultService 是凭证解密服务接口。
// P0-7 第二轮：Decrypt 接收 VaultCredentialRequest，严格校验归属关系。
type VaultService interface {
	Decrypt(ctx context.Context, req VaultCredentialRequest) (map[string]string, error)
}

type CardRepository interface {
	Create(ctx context.Context, card VirtualCard) error
	GetByID(ctx context.Context, cardID uuid.UUID) (*VirtualCard, error)
	UpdateStatus(ctx context.Context, cardID uuid.UUID, status VirtualCardStatus, reason string) error
	GetByIdempotencyKey(ctx context.Context, merchantID uuid.UUID, idempotencyKey string) (*VirtualCard, error)
}

type ServiceConfig struct {
	MaxDeclines    int
	WindowDuration time.Duration
	MaxFraudBps    int
}

func NewService(registry ProviderRegistry, factory IssuingAdapterFactory, fraudGuard FraudGuard,
	vaultService VaultService, cardRepo CardRepository, providerRepo ProviderRepository) *Service {
	return &Service{
		registry:     registry,
		factory:      factory,
		fraudGuard:   fraudGuard,
		vaultService: vaultService,
		cardRepo:     cardRepo,
		providerRepo: providerRepo,
	}
}

func (s *Service) CreateCard(ctx context.Context, req CreateCardRequest) (*CreateCardResult, error) {
	// R18 P1-2: 调用方必须提供稳定 IdempotencyKey，禁止静默生成随机键
	if req.IdempotencyKey == "" {
		return nil, fmt.Errorf("issuing: CreateCard requires non-empty IdempotencyKey (fail-closed, caller must provide stable key)")
	}

	existingCard, err := s.cardRepo.GetByIdempotencyKey(ctx, req.MerchantID, req.IdempotencyKey)
	if err == nil && existingCard != nil {
		// R19 P1-1: 已存在 UNKNOWN_EXTERNAL_STATE 卡片的幂等重放不得作为普通成功返回，
		// 应继续返回 ErrUnknownExternalState。
		if existingCard.Status == CardStatusUnknownExternalState {
			return nil, ErrUnknownExternalState
		}
		return &CreateCardResult{
			CardID:          existingCard.ID,
			ProviderCardRef: existingCard.ProviderCardRef,
			Last4:           existingCard.Last4,
			Status:          existingCard.Status,
			CreatedAt:       existingCard.CreatedAt,
		}, nil
	}
	// R18 P1-2: 只有明确 ErrCardNotFound 才允许继续。
	// 数据库故障、超时或未知错误必须立即返回，禁止调用 Provider。
	if err != nil && !errors.Is(err, ErrCardNotFound) {
		return nil, fmt.Errorf("issuing: idempotency check failed (fail-closed, not calling provider): %w", err)
	}

	if err := s.fraudGuard.CheckMerchantIssuingAllowed(ctx, req.MerchantID); err != nil {
		return nil, err
	}

	if err := s.fraudGuard.CheckMerchantFraudRate(ctx, req.MerchantID); err != nil {
		return nil, err
	}

	provider, err := s.registry.GetProvider(ctx, req.ProviderID)
	if err != nil {
		return nil, err
	}

	if provider.Status != "ACTIVE" {
		return nil, ErrProviderNotActive
	}

	if provider.CredentialVaultKey == "" {
		return nil, ErrCredentialVaultKeyEmpty
	}

	if err := provider.IssuingTier.Validate(); err != nil {
		return nil, err
	}

	if err := provider.FundingMode.Validate(); err != nil {
		return nil, err
	}

	credential, err := s.vaultService.Decrypt(ctx, VaultCredentialRequest{
		VaultKey:                  provider.CredentialVaultKey,
		ExpectedIssuingProviderID: provider.ID,
		ExpectedAdapterProviderID: resolveAdapterProviderID(provider.AdapterProviderID),
		ExpectedTenantID:          resolveTenantID(provider.TenantID),
		ExpectedCredentialType:    CredentialTypeAPIKey,
	})
	if err != nil {
		return nil, ErrCredentialDecryptFailed
	}

	adapter, err := s.factory.Create(ctx, provider.ProviderCode, credential)
	if err != nil {
		return nil, ErrProviderCodeUnregistered
	}

	result, err := adapter.CreateCard(ctx, req)
	if err != nil {
		if IsTimeout(err) {
			// F-20: 不解引用可能 nil 的 result；不用 merchant ID 作 card ID；
			// 不忽略 repository 写入错误；不编造 provider reference 或 last4。
			// R19 P1-1: 必须保留原始 ErrProviderTimeout，errors.Is 可识别。
			// R20 P1-1: 必须同时保留 Adapter 原始 err，errors.Is 可识别。
			unknownCard := VirtualCard{
				ID:                         uuid.New(), // 生成真实 card ID，不用 merchant ID
				MerchantID:                 req.MerchantID,
				ProviderID:                 req.ProviderID,
				ProviderCardRef:            "", // 不编造 provider reference
				Last4:                      "", // 不编造 last4
				Status:                     CardStatusUnknownExternalState,
				Currency:                   req.Currency,
				SpendingLimitMinor:         req.SpendingLimitMinor,
				DeclinedCountSlidingWindow: 0,
				FraudRateBps:               0,
				IdempotencyKey:             req.IdempotencyKey,
				PayloadHash:                req.PayloadHash,
				CreatedBy:                  req.CreatedBy,
				CreatedAt:                  time.Now(),
				UpdatedAt:                  time.Now(),
			}
			if createErr := s.cardRepo.Create(ctx, unknownCard); createErr != nil {
				// R20 P1-1: 同时保留 ErrUnknownExternalState、ErrProviderTimeout、Adapter 原始 err、repository error
				// errors.Is 必须能识别全部
				return nil, fmt.Errorf("issuing: create card timeout and repo write failed: %w",
					errors.Join(ErrUnknownExternalState, ErrProviderTimeout, err, createErr))
			}
			// R20 P1-1: timeout 且状态记录成功时返回 errors.Join(ErrUnknownExternalState, ErrProviderTimeout, 原始 adapter err)
			return nil, errors.Join(ErrUnknownExternalState, ErrProviderTimeout, err)
		}
		return nil, err
	}

	// R18 P1-2: Provider 返回 err=nil、result=nil 时必须 Fail-Closed，禁止空指针
	if result == nil {
		return nil, fmt.Errorf("issuing: provider returned nil result without error (fail-closed): %w", ErrUnknownExternalState)
	}

	card := VirtualCard{
		ID:                         result.CardID,
		MerchantID:                 req.MerchantID,
		ProviderID:                 req.ProviderID,
		ProviderCardRef:            result.ProviderCardRef,
		Last4:                      result.Last4,
		Status:                     result.Status,
		Currency:                   req.Currency,
		SpendingLimitMinor:         req.SpendingLimitMinor,
		DeclinedCountSlidingWindow: 0,
		FraudRateBps:               0,
		IdempotencyKey:             req.IdempotencyKey,
		PayloadHash:                req.PayloadHash,
		CreatedBy:                  req.CreatedBy,
		CreatedAt:                  result.CreatedAt,
		UpdatedAt:                  result.CreatedAt,
	}

	if err := s.cardRepo.Create(ctx, card); err != nil {
		// R18 P1-2: Provider 成功但本地写库失败时，返回 unknown external state，保留 repository 错误
		return nil, fmt.Errorf("issuing: provider create succeeded but local repo write failed: %w", errors.Join(ErrUnknownExternalState, err))
	}

	return result, nil
}

// FreezeCard 已废弃：P0-7 第二轮清理旧旁路。
// 旧 FreezeCard 直接调用 Provider adapter，绕过 card_provider_freeze_syncs 状态机，
// 无法保证幂等和 fencing。生产路径必须使用 ProviderCardFreezeService.SyncProviderFreeze。
// 禁止用 card 创建时的 IdempotencyKey 作为冻结幂等键。
func (s *Service) FreezeCard(ctx context.Context, cardID uuid.UUID, reason string) error {
	return fmt.Errorf("issuing: Service.FreezeCard is deprecated, use ProviderCardFreezeService.SyncProviderFreeze instead (bypasses sync state machine)")
}

// TerminateCard 已废弃：P0-7 第二轮清理旧旁路。
// TerminateCard 直接调用 Provider adapter，绕过同步状态机。
// 如需 Provider 侧 terminate 同步，应独立实现专用同步服务（类似 ProviderCardFreezeService）。
func (s *Service) TerminateCard(ctx context.Context, cardID uuid.UUID, reason string) error {
	return fmt.Errorf("issuing: Service.TerminateCard is deprecated, requires dedicated sync state machine (UNSUPPORTED_CAPABILITY)")
}

// SetLedgerPoster 注入发卡账本过账器（P7）；未注入时不做账本记账。
func (s *Service) SetLedgerPoster(poster *LedgerPoster) { s.ledger = poster }

func (s *Service) ProcessAuthWebhook(ctx context.Context, req ProcessAuthWebhookRequest) (*CardAuthEvent, error) {
	provider, err := s.registry.GetProvider(ctx, req.ProviderID)
	if err != nil {
		return nil, err
	}

	if provider.Status != "ACTIVE" {
		return nil, ErrProviderNotActive
	}

	credential, err := s.vaultService.Decrypt(ctx, VaultCredentialRequest{
		VaultKey:                  provider.CredentialVaultKey,
		ExpectedIssuingProviderID: provider.ID,
		ExpectedAdapterProviderID: resolveAdapterProviderID(provider.AdapterProviderID),
		ExpectedTenantID:          resolveTenantID(provider.TenantID),
		ExpectedCredentialType:    CredentialTypeAPIKey,
	})
	if err != nil {
		return nil, ErrCredentialDecryptFailed
	}

	adapter, err := s.factory.Create(ctx, provider.ProviderCode, credential)
	if err != nil {
		return nil, ErrProviderCodeUnregistered
	}

	event, err := adapter.ProcessAuthWebhook(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := s.fraudGuard.RecordAuthEvent(ctx, *event); err != nil {
		return nil, err
	}

	if event.IsDeclined {
		if err := s.fraudGuard.CheckCardDeclineThreshold(ctx, event.CardID); err != nil {
			_ = s.cardRepo.UpdateStatus(ctx, event.CardID, CardStatusFrozen, "decline threshold exceeded")
			return event, err
		}
		return event, nil
	}

	// P7: 授权成功即从商户可出金余额划拨到发卡池，清算/撤销各自过账。
	if s.ledger != nil && event.Amount.IsPositive() && event.Currency != "" {
		authRef := event.ProviderRef
		if authRef == "" {
			authRef = event.EventID
		}
		if err := s.ledger.AuthorizeHold(ctx, event.MerchantID, event.Currency,
			MinorUnits(event.Amount), authRef); err != nil {
			// 记账失败必须暴露给调用方（fail-closed），避免额度与资金脱节
			return event, err
		}
	}

	return event, nil
}

func (s *Service) SelectProviderByTierAndFundingMode(ctx context.Context, entityID *uuid.UUID, tier IssuingTier, fundingMode IssuingFundingMode) (*IssuingProvider, error) {
	providers, err := s.registry.SelectByTierAndFundingMode(ctx, entityID, tier, fundingMode)
	if err != nil {
		return nil, err
	}

	if len(providers) == 0 {
		return nil, ErrProviderNotFound
	}

	return &providers[0], nil
}

func generateIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// resolveTenantID 将 *uuid.UUID 转换为 uuid.UUID（nil 指针返回 uuid.Nil）。
func resolveTenantID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}
