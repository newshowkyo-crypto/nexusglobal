package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupRedis(t *testing.T) (*redis.Client, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	ctx := context.Background()
	// OPS-007: 外部共享 Redis 优先（TEST_REDIS_ADDR），testcontainers 兜底。
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		require.NoError(t, rdb.Ping(ctx).Err(), "ping external TEST_REDIS_ADDR")
		return rdb, func() { _ = rdb.Close() }
	}

	req := testcontainers.ContainerRequest{
		Image:        "redis:7",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "6379")
	addr := fmt.Sprintf("%s:%s", host, port.Port())

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, rdb.Ping(ctx).Err())

	cleanup := func() {
		rdb.Close()
		container.Terminate(ctx)
	}

	return rdb, cleanup
}

// TestReclaimPending_TriggersDLQ 是真实的端到端测试
// 验证：handler 失败 N 次后，ReclaimPending 真正把消息送进 DLQ
func TestReclaimPending_TriggersDLQ(t *testing.T) {
	rdb, cleanup := setupRedis(t)
	defer cleanup()

	ctx := context.Background()
	streamName := "test:e2e:stream"
	groupName := "test-e2e-group"
	dlqStreamName := "test:e2e:dlq"
	maxDeliveries := 3

	// 创建 stream 和 group
	require.NoError(t, rdb.XGroupCreateMkStream(ctx, streamName, groupName, "0").Err())

	// 发布一条消息
	msgID, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamName,
		Values: map[string]any{
			"key": "value",
		},
	}).Result()
	require.NoError(t, err)
	assert.NotEmpty(t, msgID)
	t.Logf("Published message: %s", msgID)

	// 用 initial-consumer 消费消息但不 ack（留 PEL）
	res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: "initial-consumer",
		Streams:  []string{streamName, ">"},
		Count:    1,
		Block:    100 * time.Millisecond,
	}).Result()
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Len(t, res[0].Messages, 1)
	t.Logf("Consumed message by initial-consumer: %s", res[0].Messages[0].ID)

	// XClaim 用 initial-consumer 反复认领增加 RetryCount
	for i := 0; i < maxDeliveries+2; i++ {
		claimed, err := rdb.XClaim(ctx, &redis.XClaimArgs{
			Stream:   streamName,
			Group:    groupName,
			Consumer: "initial-consumer",
			MinIdle:  0,
			Messages: []string{msgID},
		}).Result()
		require.NoError(t, err)
		t.Logf("XClaim round %d: claimed %d messages", i, len(claimed))
		time.Sleep(50 * time.Millisecond) // 每次认领后等待
	}

	// 等待足够时间让消息变为空闲
	time.Sleep(500 * time.Millisecond)

	// 验证 PEL 中消息的 RetryCount
	pendingExt, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamName,
		Group:  groupName,
		Start:  "-",
		End:    "+",
		Count:  10,
	}).Result()
	require.NoError(t, err)
	require.Len(t, pendingExt, 1)
	t.Logf("PEL RetryCount: %d", pendingExt[0].RetryCount)
	assert.GreaterOrEqual(t, pendingExt[0].RetryCount, int64(maxDeliveries),
		"RetryCount should be >= maxDeliveries")

	// 创建 reclaim-consumer 用于回收
	minIdleZero := time.Duration(0)
	reclaimConsumer := NewConsumer(ConsumerConfig{
		RDB:           rdb,
		Stream:        streamName,
		Group:         groupName,
		ConsumerID:    "reclaim-consumer",
		MaxDeliveries: maxDeliveries,
		Handler: func(ctx context.Context, msg redis.XMessage) error {
			return errors.New("handler always fails")
		},
		Logger:    slog.Default(),
		DLQStream: dlqStreamName,
		MinIdle:   &minIdleZero,
	})

	// 调用真实的 reclaimConsumer.ReclaimPending(ctx)
	// XAutoClaim 会认领属于 initial-consumer 的空闲消息
	reclaimed, err := reclaimConsumer.ReclaimPending(ctx)
	require.NoError(t, err)
	t.Logf("ReclaimPending reclaimed: %d", reclaimed)
	assert.Greater(t, reclaimed, int64(0), "should reclaim at least one message")

	// 断言：消息出现在 dlqStream 里
	dlqRes, err := rdb.XRead(ctx, &redis.XReadArgs{
		Streams: []string{dlqStreamName, "0"},
		Count:   10,
		Block:   1 * time.Second,
	}).Result()
	require.NoError(t, err)
	require.Len(t, dlqRes, 1, "DLQ stream should have messages")
	require.Len(t, dlqRes[0].Messages, 1, "DLQ should have exactly 1 message")

	dlqMsg := dlqRes[0].Messages[0]
	assert.NotEmpty(t, dlqMsg.Values["payload"], "DLQ message should have payload")
	t.Logf("Message sent to DLQ: %s", dlqMsg.ID)

	// 断言：原 PEL 已清空
	pendingAfter, err := rdb.XPending(ctx, streamName, groupName).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), pendingAfter.Count, "PEL should be empty after DLQ")
	t.Logf("PEL count after DLQ: %d", pendingAfter.Count)
}

func TestDLQ_PendingExt_ReturnsRetryCount(t *testing.T) {
	rdb, cleanup := setupRedis(t)
	defer cleanup()

	ctx := context.Background()
	streamName := "test:pending:stream"
	groupName := "test-pending-group"

	// 创建 stream 和 group
	require.NoError(t, rdb.XGroupCreateMkStream(ctx, streamName, groupName, "0").Err())

	// 发布一条消息
	msgID, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamName,
		Values: map[string]any{
			"key": "value",
		},
	}).Result()
	require.NoError(t, err)

	// 消费消息但不 XAck
	res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: "test-consumer",
		Streams:  []string{streamName, ">"},
		Count:    1,
		Block:    100 * time.Millisecond,
	}).Result()
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Len(t, res[0].Messages, 1)

	// 使用 XPendingExt 获取重试次数
	pendingExt, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamName,
		Group:  groupName,
		Start:  "-",
		End:    "+",
		Count:  10,
	}).Result()
	require.NoError(t, err)
	require.Len(t, pendingExt, 1)

	t.Logf("Message ID: %s, RetryCount: %d", pendingExt[0].ID, pendingExt[0].RetryCount)
	assert.Equal(t, msgID, pendingExt[0].ID)
	assert.Greater(t, pendingExt[0].RetryCount, int64(0), "retry count should be > 0")
}

func TestDLQ_SendToDLQ(t *testing.T) {
	rdb, cleanup := setupRedis(t)
	defer cleanup()

	ctx := context.Background()
	streamName := "test:dlq:send:stream"
	groupName := "test-dlq-send-group"
	dlqStreamName := "test:dlq:send:dlq"

	// 创建 stream 和 group
	require.NoError(t, rdb.XGroupCreateMkStream(ctx, streamName, groupName, "0").Err())

	// 发布一条消息
	msgID, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamName,
		Values: map[string]any{
			"key": "value",
		},
	}).Result()
	require.NoError(t, err)
	assert.NotEmpty(t, msgID)

	// 创建 Consumer
	consumer := NewConsumer(ConsumerConfig{
		RDB:           rdb,
		Stream:        streamName,
		Group:         groupName,
		ConsumerID:    "test-consumer",
		MaxDeliveries: 3,
		Handler: func(ctx context.Context, msg redis.XMessage) error {
			return errors.New("handler always fails")
		},
		Logger:    slog.Default(),
		DLQStream: dlqStreamName,
	})

	// 发送消息到 DLQ
	consumer.sendToDLQ(ctx, redis.XMessage{
		ID:     msgID,
		Values: map[string]any{"key": "value"},
	})

	// 验证消息进入 DLQ
	dlqRes, err := rdb.XRead(ctx, &redis.XReadArgs{
		Streams: []string{dlqStreamName, "0"},
		Count:   10,
		Block:   1 * time.Second,
	}).Result()
	require.NoError(t, err)
	require.Len(t, dlqRes, 1)
	require.Len(t, dlqRes[0].Messages, 1, "message should be in DLQ")

	// 验证 DLQ 消息内容
	dlqMsg := dlqRes[0].Messages[0]
	assert.NotEmpty(t, dlqMsg.Values["payload"])
}

func TestReclaimPending_NoMessagesToReclaim(t *testing.T) {
	rdb, cleanup := setupRedis(t)
	defer cleanup()

	ctx := context.Background()
	streamName := "test:reclaim:empty:stream"
	groupName := "test-reclaim-empty-group"
	dlqStreamName := "test:reclaim:empty:dlq"

	// 创建 stream 和 group
	require.NoError(t, rdb.XGroupCreateMkStream(ctx, streamName, groupName, "0").Err())

	// 创建 Consumer
	consumer := NewConsumer(ConsumerConfig{
		RDB:           rdb,
		Stream:        streamName,
		Group:         groupName,
		ConsumerID:    "test-consumer",
		MaxDeliveries: 3,
		Handler: func(ctx context.Context, msg redis.XMessage) error {
			return errors.New("handler always fails")
		},
		Logger:    slog.Default(),
		DLQStream: dlqStreamName,
	})

	// 运行 ReclaimPending - 应该没有消息可回收
	reclaimed, err := consumer.ReclaimPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), reclaimed, "should reclaim 0 messages when PEL is empty")
}
