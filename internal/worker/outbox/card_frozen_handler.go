// Package outbox — card_frozen_handler.go 实现 CARD_FROZEN_SUSPEND 事件的真实处理器。
//
// P0-7 核心修复：
//   - Handler 调用 ProviderCardFreezeService.SyncProviderFreeze 同步 Provider 侧冻结
//   - 传递 event.ID 和 event.IdempotencyKey 给专用服务
//   - event.IdempotencyKey 为空时 Fail-Closed，不得生成随机替代值
//   - 超时/不确定结果：返回可重试错误（绝不能 return nil ACK）
//   - 普通失败：返回 error，进入重试/DLQ
//   - 成功：return nil ACK
//   - 已 SUCCEEDED 重放由持久化状态机返回成功，不再次调用 Provider
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/issuing"
)

// CardFreezeService 定义 Handler 所需的卡片冻结服务接口。
// Worker 组合根注入 issuing.ProviderCardFreezeService 实现。
type CardFreezeService interface {
	SyncProviderFreeze(ctx context.Context, req issuing.ProviderFreezeSyncRequest) error
}

// CardFrozenSuspendHandler 处理 CARD_FROZEN_SUSPEND 事件。
// 卡片已在 DB 标记为 FROZEN（真相源），需同步 Provider 侧冻结。
type CardFrozenSuspendHandler struct {
	svc    CardFreezeService
	logger *slog.Logger
}

// NewCardFrozenSuspendHandler 创建 CARD_FROZEN_SUSPEND 处理器。
func NewCardFrozenSuspendHandler(svc CardFreezeService, logger *slog.Logger) *CardFrozenSuspendHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &CardFrozenSuspendHandler{svc: svc, logger: logger}
}

// cardFrozenPayload 解析 CARD_FROZEN_SUSPEND outbox payload。
// 由 SuspendActiveCards 写入：
//
//	{"card_id":"...","new_status":"FROZEN","reason":"...","operator":"...","sync_hint":"local_frozen_provider_sync_pending"}
type cardFrozenPayload struct {
	CardID    string `json:"card_id"`
	NewStatus string `json:"new_status"`
	Reason    string `json:"reason"`
	Operator  string `json:"operator"`
	SyncHint  string `json:"sync_hint"`
}

// Handle 处理 CARD_FROZEN_SUSPEND 事件。
//
// 处理逻辑：
//  1. 从 payload 解析 card_id 和 reason
//  2. Fail-Closed：event.IdempotencyKey 为空时拒绝处理
//  3. 调用 SyncProviderFreeze 同步 Provider 侧冻结
//  4. 成功：return nil（ACK）
//  5. 超时/不确定结果：返回可重试错误，绝不能 return nil ACK
//  6. 普通失败：返回 error，进入重试/DLQ
func (h *CardFrozenSuspendHandler) Handle(ctx context.Context, event OutboxEvent) error {
	var payload cardFrozenPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("card_frozen_suspend: unmarshal payload: %w", err)
	}

	// 优先从 payload.card_id 解析，回退到 event.AggregateID
	cardIDStr := payload.CardID
	if cardIDStr == "" {
		cardIDStr = event.AggregateID
	}
	if cardIDStr == "" {
		return fmt.Errorf("card_frozen_suspend: payload missing card_id and aggregate_id is empty")
	}

	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		return fmt.Errorf("card_frozen_suspend: invalid card_id %q: %w", cardIDStr, err)
	}

	// Task G: Fail-Closed — IdempotencyKey 为空时拒绝处理，不得生成随机替代值
	if event.IdempotencyKey == "" {
		return fmt.Errorf("card_frozen_suspend: event.IdempotencyKey is empty (fail-closed, cannot proceed without stable idempotency key)")
	}

	reason := payload.Reason
	if reason == "" {
		reason = "card_frozen_suspend"
	}

	h.logger.Info("card_frozen_suspend: calling SyncProviderFreeze",
		"event_id", event.ID,
		"card_id", cardID,
		"idempotency_key", event.IdempotencyKey,
		"reason", reason,
		"retry_count", event.RetryCount,
	)

	// 构造同步请求
	req := issuing.ProviderFreezeSyncRequest{
		EventID:        event.ID,
		IdempotencyKey: event.IdempotencyKey,
		CardID:         cardID,
		Reason:         reason,
	}

	if err := h.svc.SyncProviderFreeze(ctx, req); err != nil {
		h.logger.Error("card_frozen_suspend: SyncProviderFreeze failed",
			"event_id", event.ID,
			"card_id", cardID,
			"err", err,
		)
		return fmt.Errorf("card_frozen_suspend: SyncProviderFreeze failed: %w", err)
	}

	h.logger.Info("card_frozen_suspend: provider sync completed",
		"event_id", event.ID,
		"card_id", cardID,
		"reason", reason,
	)
	return nil
}
