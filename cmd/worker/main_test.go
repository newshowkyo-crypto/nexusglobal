// Package main — main_test.go
//
// R26 Task 2: 验证 newOutboxProcessor 复用注入的 Registry 实例，
// 而不是每次调用重新构造或使用快照。
package main

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/nexusglobal/mamr/internal/worker/outbox"
)

// TestOutboxProcessorReusesInjectedRegistry 验证 newOutboxProcessor 返回的
// Processor 持有同一个 Registry 实例，而不是快照或重新构造。
//
// 测试逻辑：
//  1. 创建 Registry，注册有计数副作用的 Handler
//  2. 调用 newOutboxProcessor 得到 Processor
//  3. 连续处理两个事件，断言 Handler 状态累计为 2
//  4. 在第一次处理后注册另一个 Handler，再通过原 Processor 处理该事件，
//     证明 Processor 持有同一个 Registry（能看到后续注册的 Handler）
func TestOutboxProcessorReusesInjectedRegistry(t *testing.T) {
	registry := outbox.NewRegistry(nil)

	// 1. 注册有计数副作用的 Handler
	var callCount int32
	assert.NoError(t, registry.Register(outbox.EventLedgerTransactionCreated, func(ctx context.Context, event outbox.OutboxEvent) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	}))

	// 2. 调用 newOutboxProcessor 得到 Processor
	processor := newOutboxProcessor(registry)

	// 3. 连续处理两个事件，断言 Handler 状态累计为 2
	event1 := outbox.OutboxEvent{
		ID:        "event-1",
		EventType: outbox.EventLedgerTransactionCreated,
		Payload:   []byte(`{}`),
	}
	event2 := outbox.OutboxEvent{
		ID:        "event-2",
		EventType: outbox.EventLedgerTransactionCreated,
		Payload:   []byte(`{}`),
	}

	assert.NoError(t, processor(context.Background(), event1))
	assert.NoError(t, processor(context.Background(), event2))
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount),
		"Handler should be called twice through the same Registry")

	// 4. 在第一次处理后注册另一个 Handler，再通过原 Processor 处理该事件，
	//    证明 Processor 持有同一个 Registry（能看到后续注册的 Handler）
	var secondHandlerCalled int32
	assert.NoError(t, registry.Register(outbox.EventConfigReload, func(ctx context.Context, event outbox.OutboxEvent) error {
		atomic.AddInt32(&secondHandlerCalled, 1)
		return nil
	}))

	event3 := outbox.OutboxEvent{
		ID:        "event-3",
		EventType: outbox.EventConfigReload,
		Payload:   []byte(`{}`),
	}
	assert.NoError(t, processor(context.Background(), event3))
	assert.Equal(t, int32(1), atomic.LoadInt32(&secondHandlerCalled),
		"Processor must see handlers registered after its creation — proving it holds the same Registry, not a snapshot")
}
