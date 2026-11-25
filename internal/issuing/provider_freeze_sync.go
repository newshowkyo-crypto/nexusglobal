package issuing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
)

// ProviderFreezeSyncRequest 是 Provider 冻结同步请求。
type ProviderFreezeSyncRequest struct {
	EventID        string
	IdempotencyKey string
	CardID         uuid.UUID
	Reason         string
}

// ProviderCardFreezeService 是专用的 Provider 冻结同步服务。
//
// Task A: 不依赖 FraudGuard、发卡创建、Webhook 或其他无关组件。
// 只依赖 CardRepository、ProviderRegistry、VaultService、IssuingAdapterFactory、
// ProviderFreezeSyncRepository 和 logger。
//
// Task C: 本地卡片状态在 SuspendActiveCards 中已设为 FROZEN，是本地授权真相源。
// Provider 同步过程中不得修改本地 virtual_cards.status。
// - 成功：sync 表 SUCCEEDED，本地仍 FROZEN
// - 超时/不确定：sync 表 UNKNOWN_EXTERNAL_STATE，本地仍 FROZEN，返回可重试错误
// - 失败：sync 表 FAILED，本地仍 FROZEN，返回错误
//
// P0-7 第二轮:
// - P0-2: workerInstanceID 使用真实进程实例 ID（hostname+PID+UUID）
// - P0-4: 先 Claim 再访问 Vault（已 SUCCEEDED 直接返回，不访问 Vault）
// - P0-5: 禁止忽略状态机写入错误（删除所有 _ = MarkFailed/MarkUnknownExternalState）
// - P0-3: provider_request_key 改为 freeze:{provider_id}:{provider_card_ref}:{event_id}
type ProviderCardFreezeService struct {
	cardRepo         CardRepository
	registry         ProviderRegistry
	vaultService     VaultService
	factory          IssuingAdapterFactory
	syncRepo         ProviderFreezeSyncRepository
	logger           *slog.Logger
	leaseTTL         time.Duration
	workerInstanceID string
}

// NewProviderCardFreezeService 创建专用 Provider 冻结同步服务。
func NewProviderCardFreezeService(
	cardRepo CardRepository,
	registry ProviderRegistry,
	vaultService VaultService,
	factory IssuingAdapterFactory,
	syncRepo ProviderFreezeSyncRepository,
	logger *slog.Logger,
) *ProviderCardFreezeService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProviderCardFreezeService{
		cardRepo:         cardRepo,
		registry:         registry,
		vaultService:     vaultService,
		factory:          factory,
		syncRepo:         syncRepo,
		logger:           logger,
		leaseTTL:         2 * time.Minute,
		workerInstanceID: generateWorkerInstanceID(),
	}
}

// generateWorkerInstanceID 生成真实进程实例 ID（hostname+PID+UUID）。
func generateWorkerInstanceID() string {
	hostname, _ := os.Hostname()
	return fmt.Sprintf("%s-pid%d-%s", hostname, os.Getpid(), uuid.New().String()[:8])
}

// resolveAdapterProviderID 将 *uuid.UUID 转换为 uuid.UUID（nil 指针返回 uuid.Nil）。
func resolveAdapterProviderID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

// SyncProviderFreeze 同步 Provider 侧冻结。
//
// 返回值：
//   - nil：同步成功（包括重放已 SUCCEEDED 的记录）
//   - ErrUnknownExternalState：Provider 超时/不确定，需重试
//   - ErrSyncLeaseLost：fencing token 失效，租约被其他 Worker 抢占
//   - ErrSyncPayloadMismatch：同一 event_id 重放但 payload 不一致
//   - 其他 error：同步失败
//
// 幂等保证：
//   - 同一 event_id 重放不再次调用 Provider
//   - 并发处理相同 event_id 最多一个执行者获得调用权
//   - 重试复用同一 provider_request_key
//
// P0-4: 先 Claim 再访问 Vault。
// 已 SUCCEEDED 的记录在 Claim 阶段直接返回，不访问 Vault。
func (s *ProviderCardFreezeService) SyncProviderFreeze(ctx context.Context, req ProviderFreezeSyncRequest) error {
	if req.EventID == "" {
		return fmt.Errorf("issuing: SyncProviderFreeze requires non-empty EventID")
	}
	if req.IdempotencyKey == "" {
		return fmt.Errorf("issuing: SyncProviderFreeze requires non-empty IdempotencyKey (fail-closed)")
	}
	if req.CardID == uuid.Nil {
		return fmt.Errorf("issuing: SyncProviderFreeze requires non-nil CardID")
	}

	// 1. 查询本地卡片状态
	card, err := s.cardRepo.GetByID(ctx, req.CardID)
	if err != nil {
		return fmt.Errorf("issuing: get card for freeze sync: %w", err)
	}

	// Task C: TERMINATED 卡不调用冻结接口
	if card.Status == CardStatusTerminated {
		return fmt.Errorf("issuing: cannot freeze TERMINATED card %s", req.CardID)
	}

	// Task C: 本地状态必须为 FROZEN，否则 Fail-Closed
	if card.Status != CardStatusFrozen {
		return fmt.Errorf("issuing: card %s local status is %s, expected FROZEN (fail-closed, not calling provider)", req.CardID, card.Status)
	}

	// 2. 查询 Provider
	provider, err := s.registry.GetProvider(ctx, card.ProviderID)
	if err != nil {
		return fmt.Errorf("issuing: get provider for freeze sync: %w", err)
	}

	if provider.Status != "ACTIVE" {
		return fmt.Errorf("issuing: provider %s not active: %w", card.ProviderID, ErrProviderNotActive)
	}

	// 3. 构造稳定的 provider_request_key（P0-3: 包含 provider_id）
	providerRequestKey := fmt.Sprintf("freeze:%s:%s:%s", card.ProviderID, card.ProviderCardRef, req.EventID)

	// P0-4: 4. 先 Claim 同步任务（短事务）— 在访问 Vault 之前
	syncRec, alreadySucceeded, err := s.syncRepo.ClaimOrGetSucceeded(
		ctx, req.EventID, req.IdempotencyKey,
		req.CardID, card.ProviderID,
		card.ProviderCardRef, providerRequestKey,
		s.workerInstanceID, s.leaseTTL,
	)
	if err != nil {
		return fmt.Errorf("issuing: claim freeze sync: %w", err)
	}

	// P0-4: 已成功：直接返回，不访问 Vault，不调用 Provider
	if alreadySucceeded {
		s.logger.Info("provider freeze sync: already succeeded, skipping provider call (no vault access)",
			"event_id", req.EventID,
			"card_id", req.CardID,
			"attempts", syncRec.Attempts,
		)
		return nil
	}

	// P0-4: 5. 获得 claim_token 后才访问 Vault（事务外）
	// F-04/F-07: 传递权威租户 ID
	var expectedTenantID uuid.UUID
	if provider.TenantID != nil {
		expectedTenantID = *provider.TenantID
	}
	credential, err := s.vaultService.Decrypt(ctx, VaultCredentialRequest{
		VaultKey:                  provider.CredentialVaultKey,
		ExpectedIssuingProviderID: provider.ID,
		ExpectedAdapterProviderID: resolveAdapterProviderID(provider.AdapterProviderID),
		ExpectedTenantID:          expectedTenantID,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	})
	if err != nil {
		// P0-5: 禁止忽略状态机写入错误
		markErr := s.syncRepo.MarkFailed(ctx, req.EventID, syncRec.ClaimToken, syncRec.LeaseVersion, err.Error())
		if markErr != nil {
			// R18 P1-1: errors.Join 保留原始错误和 Mark 错误，errors.Is 可分别识别
			return fmt.Errorf("issuing: decrypt credential failed: %w", errors.Join(err, markErr))
		}
		return fmt.Errorf("issuing: decrypt credential for freeze sync: %w", err)
	}

	// 6. 外部 Provider 调用（事务外）
	adapter, err := s.factory.Create(ctx, provider.ProviderCode, credential)
	if err != nil {
		// P0-5: 禁止忽略状态机写入错误
		markErr := s.syncRepo.MarkFailed(ctx, req.EventID, syncRec.ClaimToken, syncRec.LeaseVersion, err.Error())
		if markErr != nil {
			// R18 P1-1: errors.Join 保留原始错误和 Mark 错误
			return fmt.Errorf("issuing: create adapter failed: %w", errors.Join(err, markErr))
		}
		return fmt.Errorf("issuing: create adapter for freeze sync: %w", err)
	}

	freezeReq := FreezeCardRequest{
		LocalCardID:     req.CardID,
		ProviderID:      card.ProviderID,
		ProviderCardRef: card.ProviderCardRef,
		IdempotencyKey:  syncRec.ProviderRequestKey, // F-10: 发送 provider_request_key，不是内部 outbox key
		FrozenReason:    req.Reason,
		CardID:          req.CardID,
	}

	if err := adapter.FreezeCard(ctx, freezeReq); err != nil {
		if IsTimeout(err) {
			// Task C: 超时/不确定 → sync 表 UNKNOWN_EXTERNAL_STATE，本地仍 FROZEN
			// P0-5: 禁止忽略状态机写入错误
			markErr := s.syncRepo.MarkUnknownExternalState(ctx, req.EventID, syncRec.ClaimToken, syncRec.LeaseVersion, err.Error())
			if markErr != nil {
				// P0-5: 超时+Mark 失败 → errors.Join 保留两个错误
				return fmt.Errorf("issuing: provider freeze sync uncertain and mark unknown state failed: %w",
					errors.Join(ErrUnknownExternalState, markErr))
			}
			s.logger.Warn("provider freeze sync: timeout/unknown, local card stays FROZEN",
				"event_id", req.EventID,
				"card_id", req.CardID,
				"err", err,
			)
			return fmt.Errorf("issuing: provider freeze sync uncertain: %w", ErrUnknownExternalState)
		}
		// Task C: 普通失败 → sync 表 FAILED，本地仍 FROZEN
		// P0-5: 禁止忽略状态机写入错误
		markErr := s.syncRepo.MarkFailed(ctx, req.EventID, syncRec.ClaimToken, syncRec.LeaseVersion, err.Error())
		if markErr != nil {
			// R18 P1-1: errors.Join 保留原始错误和 Mark 错误
			return fmt.Errorf("issuing: provider freeze sync failed: %w", errors.Join(err, markErr))
		}
		s.logger.Error("provider freeze sync: failed, local card stays FROZEN",
			"event_id", req.EventID,
			"card_id", req.CardID,
			"err", err,
		)
		return fmt.Errorf("issuing: provider freeze sync failed: %w", err)
	}

	// 7. 成功 → sync 表 SUCCEEDED，本地仍 FROZEN
	// P0-5: Provider 成功但 MarkSucceeded 失败 → 返回错误不 ACK
	if err := s.syncRepo.MarkSucceeded(ctx, req.EventID, syncRec.ClaimToken, syncRec.LeaseVersion); err != nil {
		return fmt.Errorf("issuing: mark sync succeeded: %w", err)
	}

	s.logger.Info("provider freeze sync: succeeded, local card stays FROZEN",
		"event_id", req.EventID,
		"card_id", req.CardID,
		"attempts", syncRec.Attempts,
	)

	return nil
}
