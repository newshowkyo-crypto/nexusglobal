// Package outbox — p0_4_registry_test.go 验证 P0-4 Outbox 事件处理器注册。
//
// 测试覆盖：
//   - TestOutbox_UnknownEventNotAcknowledged
//   - TestOutbox_PublishFailureRetries
//   - TestOutbox_KnownEventsHaveHandlers
//   - TestOutbox_HandlerIdempotency
//   - TestOutbox_MaxRetriesMovesToDeadLetter
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/payout/repository/db"
)

// TestOutbox_UnknownEventNotAcknowledged 验证未知 EventType 不被 ACK。
//
// 场景：
//  1. 创建 Registry，注册部分已知 EventType
//  2. 发送一个未知 EventType（不在 KnownEventTypes 列表）
//  3. 期望 Process 返回 ErrUnknownEventType（不 ACK）
//  4. worker 会重试或进入死信
func TestOutbox_UnknownEventNotAcknowledged(t *testing.T) {
	registry := NewRegistry(slog.Default())

	// 注册一个已知 EventType
	require.NoError(t, registry.Register(EventFXLockRequested, func(ctx context.Context, event OutboxEvent) error {
		return nil
	}))

	// 未知 EventType
	unknownEvent := OutboxEvent{
		ID:        uuid.New().String(),
		EventType: "COMPLETELY_UNKNOWN_EVENT_TYPE",
		Payload:   []byte(`{}`),
	}

	err := registry.Process(context.Background(), unknownEvent)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownEventType), "expected ErrUnknownEventType, got %v", err)
}

// TestOutbox_KnownButUnregisteredEvent_ReturnsError 验证已知但未注册 handler 的 EventType 返回 error。
func TestOutbox_KnownButUnregisteredEvent_ReturnsError(t *testing.T) {
	registry := NewRegistry(slog.Default())

	// 不注册任何 handler，直接发送已知 EventType
	knownEvent := OutboxEvent{
		ID:        uuid.New().String(),
		EventType: EventLedgerTransactionCreated,
		Payload:   []byte(`{}`),
	}

	err := registry.Process(context.Background(), knownEvent)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrHandlerNotRegistered), "expected ErrHandlerNotRegistered, got %v", err)
}

// TestOutbox_PublishFailureRetries 验证 Redis 发布失败时返回 error（不 ACK），触发重试。
//
// 场景：
//  1. 注册一个 handler，模拟 Redis 发布失败
//  2. 发送事件
//  3. 期望 Process 返回 error（不 ACK）
//  4. worker 会重试
func TestOutbox_PublishFailureRetries(t *testing.T) {
	registry := NewRegistry(slog.Default())

	// 模拟 Redis 发布失败的 handler
	publishError := errors.New("redis publish failed")
	require.NoError(t, registry.Register(EventSystemLockdownPublish, func(ctx context.Context, event OutboxEvent) error {
		return publishError
	}))

	event := OutboxEvent{
		ID:        uuid.New().String(),
		EventType: EventSystemLockdownPublish,
		Payload:   []byte(`{"event":"SYSTEM_LOCKDOWN"}`),
	}

	err := registry.Process(context.Background(), event)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, publishError), "expected publish error, got %v", err)
}

// TestOutbox_KnownEventsHaveHandlers 验证全部已知 EventType 都可注册 handler。
//
// 场景：
//  1. 创建 Registry
//  2. 为全部 KnownEventTypes 注册 handler
//  3. 期望 AllKnownHandlersRegistered 返回 true
func TestOutbox_KnownEventsHaveHandlers(t *testing.T) {
	registry := NewRegistry(slog.Default())

	// 为全部已知 EventType 注册 no-op handler
	for _, eventType := range KnownEventTypes() {
		require.NoError(t, registry.Register(eventType, func(ctx context.Context, event OutboxEvent) error {
			return nil
		}))
	}

	allRegistered, missing := registry.AllKnownHandlersRegistered()
	assert.True(t, allRegistered, "all known event types should have handlers, missing: %v", missing)
}

// TestOutbox_HandlerIdempotency 验证 handler 幂等性。
//
// 场景：
//  1. 注册一个幂等 handler（基于 idempotency_key 去重）
//  2. 发送同一事件两次
//  3. 期望两次都返回 nil（ACK），且副作用只发生一次
func TestOutbox_HandlerIdempotency(t *testing.T) {
	registry := NewRegistry(slog.Default())

	// 模拟幂等 handler：基于 idempotency_key 去重
	processedKeys := make(map[string]bool)
	var callCount int32

	require.NoError(t, registry.Register(EventLedgerTransactionCreated, func(ctx context.Context, event OutboxEvent) error {
		atomic.AddInt32(&callCount, 1)
		if processedKeys[event.IdempotencyKey] {
			// 已处理，幂等返回 nil
			return nil
		}
		processedKeys[event.IdempotencyKey] = true
		return nil
	}))

	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventLedgerTransactionCreated,
		Payload:        []byte(`{"transaction_id":"test-txn-1"}`),
		IdempotencyKey: "ledger_outbox_test-txn-1",
	}

	// 第一次处理
	err := registry.Process(context.Background(), event)
	assert.NoError(t, err)

	// 第二次处理（重复）
	err = registry.Process(context.Background(), event)
	assert.NoError(t, err)

	// handler 被调用两次，但副作用（processedKeys 设置）只发生一次
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
	assert.True(t, processedKeys[event.IdempotencyKey])
}

// TestOutbox_CardFrozenSuspendRegistered 验证 CARD_FROZEN_SUSPEND 事件类型存在于 KnownEventTypes。
func TestOutbox_CardFrozenSuspendRegistered(t *testing.T) {
	// 验证 EventCardFrozenSuspend 在 KnownEventTypes 中
	found := false
	for _, et := range KnownEventTypes() {
		if et == EventCardFrozenSuspend {
			found = true
			break
		}
	}
	assert.True(t, found, "EventCardFrozenSuspend should be in KnownEventTypes")
}

// TestOutbox_MaxRetriesMovesToDeadLetter 验证达到最大重试次数后进入 DeadLetter。
//
// 场景：
//  1. 创建 mock repo，事件 RetryCount >= MaxRetries
//  2. 启动 worker
//  3. 期望事件被标记为 DeadLetter，不被 ACK 或 Fail
func TestOutbox_MaxRetriesMovesToDeadLetter(t *testing.T) {
	eventID := uuid.New()
	repo := &mockRepo{
		claimed: []db.TransactionalOutbox{
			{
				ID:            eventID,
				AggregateType: "test",
				AggregateID:   uuid.New(),
				EventType:     EventFXLockRequested,
				RetryCount:    5, // >= MaxRetries
			},
		},
	}

	processor := func(_ context.Context, evt OutboxEvent) error {
		t.Error("processor should NOT be called for dead-lettered event")
		return nil
	}

	w := New(repo, processor, Config{
		Concurrency:  1,
		BatchSize:    1,
		PollInterval: 10 * time.Millisecond,
		MaxRetries:   5,
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	repo.mu.Lock()
	assert.Contains(t, repo.deadLetterIDs, eventID, "event should be dead-lettered")
	assert.NotContains(t, repo.ackedIDs, eventID, "dead-lettered event should NOT be acked")
	assert.NotContains(t, repo.failedIDs, eventID, "dead-lettered event should NOT be marked as Failed")
	repo.mu.Unlock()
}

// TestOutbox_UnknownEventInWorker_FailsNotAcks 验证 worker 处理未知 EventType 时
// 调用 FailOutboxEvent（不 ACK），最终达到 MaxRetries 后进入 DeadLetter。
//
// 场景：
//  1. 创建 Registry（不注册 handler）
//  2. worker processor 使用 Registry.Process
//  3. 发送未知 EventType
//  4. 期望事件被 Fail（不 ACK），重试后进入 DeadLetter
func TestOutbox_UnknownEventInWorker_FailsNotAcks(t *testing.T) {
	registry := NewRegistry(slog.Default())

	eventID := uuid.New()
	repo := &mockRepo{
		claimed: []db.TransactionalOutbox{
			{
				ID:            eventID,
				AggregateType: "test",
				AggregateID:   uuid.New(),
				EventType:     "COMPLETELY_UNKNOWN_EVENT",
				RetryCount:    0,
			},
		},
	}

	processor := registry.Process

	w := New(repo, processor, Config{
		Concurrency:  1,
		BatchSize:    1,
		PollInterval: 10 * time.Millisecond,
		MaxRetries:   3,
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	repo.mu.Lock()
	// 未知 EventType 应被 Fail（不 ACK）
	assert.NotContains(t, repo.ackedIDs, eventID, "unknown event should NOT be acked")
	assert.Contains(t, repo.failedIDs, eventID, "unknown event should be marked as failed")
	repo.mu.Unlock()
}

// TestRegistry_RegisterUnknownEventType_ReturnsError 验证注册未知 EventType 返回 error。
func TestRegistry_RegisterUnknownEventType_ReturnsError(t *testing.T) {
	registry := NewRegistry(slog.Default())

	err := registry.Register("UNKNOWN_EVENT_TYPE", func(ctx context.Context, event OutboxEvent) error {
		return nil
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownEventType))
}

// TestRegistry_RegisterNilHandler_ReturnsError 验证注册 nil handler 返回 error。
func TestRegistry_RegisterNilHandler_ReturnsError(t *testing.T) {
	registry := NewRegistry(slog.Default())

	err := registry.Register(EventFXLockRequested, nil)
	assert.Error(t, err)
}

// TestOutboxRegistryValidation_StrictRejectsMissingHandler 验证 strict 模式下缺失 Handler 返回 error，拒绝启动。
//
// R25 B: production/real 模式发现缺少任何已知 Handler 必须拒绝启动。
func TestOutboxRegistryValidation_StrictRejectsMissingHandler(t *testing.T) {
	registry := NewRegistry(slog.Default())

	// 只注册一个 handler，其余已知 EventType 缺失
	require.NoError(t, registry.Register(EventFXLockRequested, func(ctx context.Context, event OutboxEvent) error {
		return nil
	}))

	err := ValidateOutboxRegistry(registry, true) // strict=true
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "strict mode")
}

// TestOutboxRegistryValidation_NonStrictAllowsMissingHandler 验证 non-strict 模式下缺失 Handler 只记录 warning，不返回 error。
//
// R25 B: local/mock 模式可以记录明确 warning，不拒绝启动。
func TestOutboxRegistryValidation_NonStrictAllowsMissingHandler(t *testing.T) {
	registry := NewRegistry(slog.Default())

	// 只注册一个 handler，其余已知 EventType 缺失
	require.NoError(t, registry.Register(EventFXLockRequested, func(ctx context.Context, event OutboxEvent) error {
		return nil
	}))

	err := ValidateOutboxRegistry(registry, false) // strict=false
	assert.NoError(t, err)
}

// TestOutboxRegistryValidation_AllKnownHandlersRegistered 验证全部已知 EventType 都注册后验证通过。
//
// R25 B: 全部注册完成后 ValidateOutboxRegistry 应返回 nil（无论 strict 或 non-strict）。
func TestOutboxRegistryValidation_AllKnownHandlersRegistered(t *testing.T) {
	registry := NewRegistry(slog.Default())

	// 为全部已知 EventType 注册 handler
	for _, eventType := range KnownEventTypes() {
		require.NoError(t, registry.Register(eventType, func(ctx context.Context, event OutboxEvent) error {
			return nil
		}))
	}

	// strict 模式也应通过
	err := ValidateOutboxRegistry(registry, true)
	assert.NoError(t, err)

	// non-strict 模式也应通过
	err = ValidateOutboxRegistry(registry, false)
	assert.NoError(t, err)
}

// TestKnownEventTypes_IncludesRequired 验证任务要求的 EventType 都在已知列表中。
//
// 任务要求至少覆盖：
//   - SYSTEM_LOCKDOWN_PUBLISH
//   - SYSTEM_RECOVERY_PUBLISH
//   - CONFIG_RELOAD
//   - ACQUIRER_CHANNEL_RELOAD
//   - ISSUING_POOL_RELOAD
//   - LEDGER_TRANSACTION_CREATED
//   - PAYMENT_DISPATCH_REQUESTED
func TestKnownEventTypes_IncludesRequired(t *testing.T) {
	required := []string{
		EventSystemLockdownPublish,
		EventSystemRecoveryPublish,
		EventConfigReload,
		EventAcquirerChannelReload,
		EventIssuingPoolReload,
		EventLedgerTransactionCreated,
		EventPaymentDispatchRequested,
	}

	for _, r := range required {
		assert.True(t, IsKnownEventType(r), "required event type %s should be known", r)
	}
}

// TestKnownEventTypes_IncludesAllWritten 验证代码中实际写入的全部 EventType 都在已知列表中。
//
// 这防止未来新增 EventType 时忘记注册到 KnownEventTypes。
func TestKnownEventTypes_IncludesAllWritten(t *testing.T) {
	// 从代码搜索得到的全部实际写入的 EventType
	writtenInCode := []string{
		"SYSTEM_LOCKDOWN_PUBLISH",
		"SYSTEM_RECOVERY_PUBLISH",
		"LEDGER_TRANSACTION_CREATED",
		"LEDGER_RESERVATION_CREATED",
		"LEDGER_RESERVATION_CAPTURED",
		"LEDGER_RESERVATION_RELEASED",
		"FX_LOCK_REQUESTED",
		"LP_PAYOUT_REQUESTED",
		"VCC_FUNDING_REQUESTED",
		"VCC_CRYPTO_BRIDGE_REQUESTED",
		"acquirer_channel.status_changed",
		"acquirer_channel.config_updated",
		"issuing_pool.status_changed",
		"issuing_pool.config_updated",
		"merchant_reserve.updated",
		"wallet_policy.disabled",
		"va_account.disabled",
		"va_account.released",
		"channel_risk.updated",
		"system.recovery_executed",
		"adapter_channel.disabled",
		"adapter_channel.enabled",
		"bridge_profile.disabled",
		"corporate_entity.risk_hold",
		"isolation_group.frozen",
		"isolation_group.unfrozen",
	}

	for _, w := range writtenInCode {
		assert.True(t, IsKnownEventType(w), "event type %s written in code should be in KnownEventTypes", w)
	}
}
