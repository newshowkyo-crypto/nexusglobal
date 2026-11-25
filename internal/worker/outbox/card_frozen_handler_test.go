// Package outbox — card_frozen_handler_test.go 验证 CARD_FROZEN_SUSPEND 真实 Handler 行为。
//
// P0-7 核心修复验证：
//   - Handler 从 payload 解析 card_id，调用 SyncProviderFreeze
//   - 成功：return nil（ACK）
//   - 超时/不确定结果：返回可重试错误，绝不能 return nil
//   - 普通失败：返回 error，进入重试/DLQ
//   - IdempotencyKey 为空时 Fail-Closed
//   - 重放幂等：由持久化状态机保证，Handler 不去重
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/issuing"
)

// mockCardFreezeService 实现 CardFreezeService 接口，用于测试。
type mockCardFreezeService struct {
	syncProviderFreezeFunc func(ctx context.Context, req issuing.ProviderFreezeSyncRequest) error
	callCount              int32
	lastReq                issuing.ProviderFreezeSyncRequest
}

func (m *mockCardFreezeService) SyncProviderFreeze(ctx context.Context, req issuing.ProviderFreezeSyncRequest) error {
	atomic.AddInt32(&m.callCount, 1)
	m.lastReq = req
	if m.syncProviderFreezeFunc != nil {
		return m.syncProviderFreezeFunc(ctx, req)
	}
	return nil
}

func TestCardFrozenSuspendHandler_Success(t *testing.T) {
	mockSvc := &mockCardFreezeService{}
	handler := NewCardFrozenSuspendHandler(mockSvc, slog.Default())

	cardID := uuid.New()
	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventCardFrozenSuspend,
		AggregateID:    cardID.String(),
		Payload:        []byte(`{"card_id":"` + cardID.String() + `","new_status":"FROZEN","reason":"fraud_detected","operator":"admin","sync_hint":"local_frozen_provider_sync_pending"}`),
		IdempotencyKey: "card-frozen-suspend-test-001",
	}

	err := handler.Handle(context.Background(), event)
	assert.NoError(t, err, "successful SyncProviderFreeze should return nil (ACK)")
	assert.Equal(t, int32(1), atomic.LoadInt32(&mockSvc.callCount), "SyncProviderFreeze should be called exactly once")
	assert.Equal(t, cardID, mockSvc.lastReq.CardID, "card_id should match payload")
	assert.Equal(t, "fraud_detected", mockSvc.lastReq.Reason, "reason should match payload")
	assert.Equal(t, "card-frozen-suspend-test-001", mockSvc.lastReq.IdempotencyKey, "idempotency_key should match event")
}

func TestCardFrozenSuspendHandler_TimeoutReturnsRetryableError(t *testing.T) {
	mockSvc := &mockCardFreezeService{
		syncProviderFreezeFunc: func(_ context.Context, _ issuing.ProviderFreezeSyncRequest) error {
			return issuing.ErrUnknownExternalState
		},
	}
	handler := NewCardFrozenSuspendHandler(mockSvc, slog.Default())

	cardID := uuid.New()
	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventCardFrozenSuspend,
		AggregateID:    cardID.String(),
		Payload:        []byte(`{"card_id":"` + cardID.String() + `","reason":"provider_timeout"}`),
		IdempotencyKey: "card-frozen-suspend-timeout-001",
	}

	err := handler.Handle(context.Background(), event)
	assert.Error(t, err, "ErrUnknownExternalState must NOT return nil ACK")
	assert.Equal(t, int32(1), atomic.LoadInt32(&mockSvc.callCount), "SyncProviderFreeze should be called")
}

func TestCardFrozenSuspendHandler_FailureReturnsError(t *testing.T) {
	expectedErr := errors.New("provider connection refused")
	mockSvc := &mockCardFreezeService{
		syncProviderFreezeFunc: func(_ context.Context, _ issuing.ProviderFreezeSyncRequest) error {
			return expectedErr
		},
	}
	handler := NewCardFrozenSuspendHandler(mockSvc, slog.Default())

	cardID := uuid.New()
	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventCardFrozenSuspend,
		AggregateID:    cardID.String(),
		Payload:        []byte(`{"card_id":"` + cardID.String() + `","reason":"safety_offline"}`),
		IdempotencyKey: "card-frozen-suspend-fail-001",
	}

	err := handler.Handle(context.Background(), event)
	assert.Error(t, err, "provider failure must return error (not nil ACK)")
	assert.True(t, errors.Is(err, expectedErr), "error should wrap original error")
}

func TestCardFrozenSuspendHandler_InvalidPayload(t *testing.T) {
	mockSvc := &mockCardFreezeService{}
	handler := NewCardFrozenSuspendHandler(mockSvc, slog.Default())

	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventCardFrozenSuspend,
		AggregateID:    uuid.New().String(),
		Payload:        []byte(`{invalid json`),
		IdempotencyKey: "card-frozen-suspend-invalid-001",
	}

	err := handler.Handle(context.Background(), event)
	assert.Error(t, err, "invalid JSON payload should return error")
	assert.Equal(t, int32(0), atomic.LoadInt32(&mockSvc.callCount), "SyncProviderFreeze should NOT be called for invalid payload")
}

func TestCardFrozenSuspendHandler_MissingCardID(t *testing.T) {
	mockSvc := &mockCardFreezeService{}
	handler := NewCardFrozenSuspendHandler(mockSvc, slog.Default())

	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventCardFrozenSuspend,
		AggregateID:    "",
		Payload:        []byte(`{"new_status":"FROZEN","reason":"test"}`),
		IdempotencyKey: "card-frozen-suspend-no-card-001",
	}

	err := handler.Handle(context.Background(), event)
	assert.Error(t, err, "missing card_id should return error")
	assert.Equal(t, int32(0), atomic.LoadInt32(&mockSvc.callCount), "SyncProviderFreeze should NOT be called when card_id is missing")
}

func TestCardFrozenSuspendHandler_FallbackToAggregateID(t *testing.T) {
	mockSvc := &mockCardFreezeService{}
	handler := NewCardFrozenSuspendHandler(mockSvc, slog.Default())

	aggregateID := uuid.New()
	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventCardFrozenSuspend,
		AggregateID:    aggregateID.String(),
		Payload:        []byte(`{"new_status":"FROZEN","reason":"fallback_test"}`),
		IdempotencyKey: "card-frozen-suspend-fallback-001",
	}

	err := handler.Handle(context.Background(), event)
	assert.NoError(t, err, "should fall back to AggregateID when card_id is empty")
	assert.Equal(t, aggregateID, mockSvc.lastReq.CardID, "should use AggregateID as cardID")
}

// TestCardFrozenSuspendHandler_MissingIdempotencyKeyRejected 验证 IdempotencyKey 为空时 Fail-Closed。
func TestCardFrozenSuspendHandler_MissingIdempotencyKeyRejected(t *testing.T) {
	mockSvc := &mockCardFreezeService{}
	handler := NewCardFrozenSuspendHandler(mockSvc, slog.Default())

	cardID := uuid.New()
	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventCardFrozenSuspend,
		AggregateID:    cardID.String(),
		Payload:        []byte(`{"card_id":"` + cardID.String() + `","reason":"test"}`),
		IdempotencyKey: "", // 空幂等键
	}

	err := handler.Handle(context.Background(), event)
	assert.Error(t, err, "empty IdempotencyKey must be rejected (fail-closed)")
	assert.Contains(t, err.Error(), "IdempotencyKey is empty", "error should mention empty idempotency key")
	assert.Equal(t, int32(0), atomic.LoadInt32(&mockSvc.callCount), "SyncProviderFreeze should NOT be called when IdempotencyKey is empty")
}

func TestCardFrozenSuspendHandler_IdempotentReplay(t *testing.T) {
	// 验证重放幂等：Handler 层面每次都调用 SyncProviderFreeze，
	// 幂等由持久化状态机（ProviderFreezeSyncRepository）保证。
	var callCount int32
	mockSvc := &mockCardFreezeService{
		syncProviderFreezeFunc: func(_ context.Context, _ issuing.ProviderFreezeSyncRequest) error {
			atomic.AddInt32(&callCount, 1)
			return nil // 模拟幂等：持久化状态机返回成功
		},
	}
	handler := NewCardFrozenSuspendHandler(mockSvc, slog.Default())

	cardID := uuid.New()
	event := OutboxEvent{
		ID:             uuid.New().String(),
		EventType:      EventCardFrozenSuspend,
		AggregateID:    cardID.String(),
		Payload:        []byte(`{"card_id":"` + cardID.String() + `","reason":"idempotent_test"}`),
		IdempotencyKey: "card-frozen-suspend-idempotent-001",
	}

	// 第一次处理
	err := handler.Handle(context.Background(), event)
	require.NoError(t, err)

	// 重放同一事件
	err = handler.Handle(context.Background(), event)
	require.NoError(t, err)

	// handler 层面调用两次（handler 不去重，幂等由持久化状态机保证）
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount),
		"handler calls SyncProviderFreeze on each replay; idempotency is state-machine responsibility")
}
