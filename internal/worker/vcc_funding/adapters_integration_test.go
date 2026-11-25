// Package vcc_funding — adapters_integration_test.go
//
// R25 C: 替换 VCC lease 占位测试为真实 DB 集成测试。
// R26 Task 3: 修复测试隔离性 — 不假设数据库中只有一条事件。
//
// 重要：TEST_DATABASE_URL 必须指向隔离测试数据库，禁止指向生产数据库。
// 测试从 TEST_DATABASE_URL 获取 PostgreSQL 测试数据库；未配置时 t.Skip。
// 每个测试使用独立 UUID 和 idempotency_key，t.Cleanup 只删除自己创建的数据。
package vcc_funding

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDBPool 从 TEST_DATABASE_URL 创建 pgxpool 连接池。
// 未配置 TEST_DATABASE_URL 时跳过测试。
func testDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIPPED_MISSING_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "failed to create pgxpool from TEST_DATABASE_URL")
	t.Cleanup(func() {
		pool.Close()
	})
	return pool
}

// insertTestOutboxEvent 插入一条 Pending VCC Outbox 事件用于 lease 测试。
// 返回插入的事件 ID。
func insertTestOutboxEvent(t *testing.T, pool *pgxpool.Pool, eventType string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	eventID := uuid.New()
	idempotencyKey := fmt.Sprintf("lease_test:%s:%s", eventType, eventID.String())
	aggregateID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("lease_test:"+eventID.String()))

	_, err := pool.Exec(ctx, `
		INSERT INTO transactional_outbox
			(id, aggregate_type, aggregate_id, event_type, payload, idempotency_key,
			 status, retry_count, next_retry_at, created_at, updated_at)
		VALUES ($1, 'vcc_funding_test', $2, $3, '{"test": true}', $4,
			 'Pending', 0, now(), now(), now())`,
		eventID, aggregateID, eventType, idempotencyKey)
	require.NoError(t, err, "failed to insert test outbox event")

	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM transactional_outbox WHERE id = $1`, eventID)
	})

	return eventID
}

// TestClaimPendingEvents_LeaseNotExpired_NotReclaimed 验证未过期的 Processing lease
// 不会被第二个 worker 重新领取。
//
// 场景：
//  1. 插入 Pending VCC Outbox 事件
//  2. Worker A 调用 ClaimPendingEvents 成功领取事件
//  3. Worker B 在 5 分钟 lease 内再次调用 ClaimPendingEvents
//  4. 断言 Worker B 不能领取相同事件（因为 locked_at 未过期）
func TestClaimPendingEvents_LeaseNotExpired_NotReclaimed(t *testing.T) {
	pool := testDBPool(t)
	ctx := context.Background()
	repo := NewDBOutboxRepository(pool)

	// 1. 插入 Pending VCC Outbox 事件
	eventID := insertTestOutboxEvent(t, pool, "VCC_FUNDING_REQUESTED")

	// 2. Worker A claim 事件
	eventsA, err := repo.ClaimPendingEvents(ctx, 10)
	require.NoError(t, err, "Worker A ClaimPendingEvents failed")

	// 在返回的事件中查找本测试创建的 eventID（不假设数据库只有一条事件）
	foundA := false
	for _, e := range eventsA {
		if e.ID == eventID {
			foundA = true
			assert.Equal(t, "Processing", e.Status, "event status should be Processing after claim")
			break
		}
	}
	require.True(t, foundA, "Worker A should claim the test event")

	// 3. Worker B 在 5 分钟 lease 内再次 claim
	eventsB, err := repo.ClaimPendingEvents(ctx, 10)
	require.NoError(t, err, "Worker B ClaimPendingEvents failed")

	// 4. 断言 Worker B 不能领取相同事件
	for _, e := range eventsB {
		assert.NotEqual(t, eventID, e.ID, "Worker B should NOT reclaim the event within lease period")
	}
}

// TestClaimPendingEvents_LeaseExpired_Reclaimed 验证过期的 Processing lease
// 可被另一个 worker 重新领取。
//
// 场景：
//  1. 插入 Pending VCC Outbox 事件
//  2. Worker A 调用 ClaimPendingEvents 成功领取事件
//  3. 将 locked_at 回拨到超过 5 分钟前
//  4. Worker B 再次调用 ClaimPendingEvents
//  5. 断言事件被 Worker B 重新领取（因为 lease 已过期）
func TestClaimPendingEvents_LeaseExpired_Reclaimed(t *testing.T) {
	pool := testDBPool(t)
	ctx := context.Background()
	repo := NewDBOutboxRepository(pool)

	// 1. 插入 Pending VCC Outbox 事件
	eventID := insertTestOutboxEvent(t, pool, "VCC_FUNDING_REQUESTED")

	// 2. Worker A claim 事件
	eventsA, err := repo.ClaimPendingEvents(ctx, 10)
	require.NoError(t, err, "Worker A ClaimPendingEvents failed")

	// 在返回的事件中查找本测试创建的 eventID（不假设数据库只有一条事件）
	foundA := false
	for _, e := range eventsA {
		if e.ID == eventID {
			foundA = true
			break
		}
	}
	require.True(t, foundA, "Worker A should claim the test event")

	// 3. 将 locked_at 回拨到超过 5 分钟前
	_, err = pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET locked_at = now() - interval '6 minutes'
		WHERE id = $1`, eventID)
	require.NoError(t, err, "failed to backdate locked_at")

	// 4. Worker B 再次 claim（lease 已过期）
	eventsB, err := repo.ClaimPendingEvents(ctx, 10)
	require.NoError(t, err, "Worker B ClaimPendingEvents failed")

	// 5. 断言事件被 Worker B 重新领取
	found := false
	for _, e := range eventsB {
		if e.ID == eventID {
			found = true
			break
		}
	}
	assert.True(t, found, "Worker B should reclaim the event after lease expired (locked_at backdated > 5 minutes)")
}
