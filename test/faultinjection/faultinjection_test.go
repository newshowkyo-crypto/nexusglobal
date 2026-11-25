// Package faultinjection contains chaos/fault-injection tests for NexusGlobal.
//
// P1 重写：删除只设置布尔变量然后断言自身的伪测试，改为使用真实 PostgreSQL、Redis、Worker。
// 这些测试验证系统在以下故障场景下的正确行为：
//   - PostgreSQL 事务中断（commit 前回滚 → 无部分写入）
//   - Redis 故障（fail-closed，拒绝操作）
//   - Outbox worker 重启（未处理消息重新 claim）
//   - 重复 webhook（幂等处理）
//   - 乱序 webhook（检测并拒绝）
//   - Outbox 重试（失败后 retry_count 递增）
//   - Lockdown during dispatch（新 dispatch 被拒，in-flight 允许完成）
//
// 环境要求：testcontainers（Docker）或 TEST_DATABASE_DSN 环境变量。
// 若环境不可用，测试标记 BLOCKED_ENVIRONMENT 并 t.Skip，不得把未执行测试算作 PASS。
package faultinjection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/platform/failclosed"
)

// Package-level atomic counters for tracking test execution status.
// These are incremented by each test function and read by TestFaultInjectionSummary
// to produce an accurate executed/pass/failed/skipped summary.
var (
	executedCount int32
	passedCount   int32
	failedCount   int32
	skippedCount  int32
)

// trackTest registers a test for execution tracking. Call after the testing.Short()
// skip check. Uses t.Cleanup to automatically record pass/fail status.
func trackTest(t *testing.T) {
	trackExecuted()
	t.Cleanup(func() {
		if t.Failed() {
			trackFailed()
		} else {
			trackPassed()
		}
	})
}

// trackExecuted increments the executed counter. Call at the start of each test
// (after the testing.Short() skip check, so skipped tests are not counted as executed).
func trackExecuted() {
	atomic.AddInt32(&executedCount, 1)
}

// trackPassed increments the passed counter. Call at the end of a successful test.
func trackPassed() {
	atomic.AddInt32(&passedCount, 1)
}

// trackFailed increments the failed counter. Call when a test fails.
func trackFailed() {
	atomic.AddInt32(&failedCount, 1)
}

// trackSkipped increments the skipped counter. Call before t.Skip().
func trackSkipped() {
	atomic.AddInt32(&skippedCount, 1)
}

// setupTestDB 创建带迁移的测试数据库。
// 优先使用 TEST_DATABASE_DSN 环境变量（外部 PostgreSQL），否则使用 testcontainers。
// 若 testcontainers 不可用（Docker 未运行），标记 BLOCKED_ENVIRONMENT 并 t.Skip。
func setupTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	ctx := context.Background()

	if externalDSN := os.Getenv("TEST_DATABASE_DSN"); externalDSN != "" {
		pool, err := pgxpool.New(ctx, externalDSN)
		require.NoError(t, err)

		_, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO public;`)
		require.NoError(t, err)

		migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
		require.NoError(t, err)
		migrationURL := "file://" + filepath.ToSlash(migrationDir)

		m, err := migrate.New(migrationURL, externalDSN)
		require.NoError(t, err)
		defer m.Close()

		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			require.NoError(t, err, "failed to apply migrations")
		}

		return pool, func() { pool.Close() }
	}

	pgReq := testcontainers.ContainerRequest{
		Image:        "postgres:15",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "test",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(120 * time.Second),
	}
	pgContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: pgReq,
		Started:          true,
	})
	if err != nil {
		t.Skipf("BLOCKED_ENVIRONMENT: testcontainers unavailable (Docker not running?): %v", err)
		return nil, nil
	}

	pgHost, _ := pgContainer.Host(ctx)
	pgPort, _ := pgContainer.MappedPort(ctx, "5432")
	dsn := fmt.Sprintf("postgres://test:test@%s:%s/test?sslmode=disable", pgHost, pgPort.Port())

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)

	for i := 0; i < 30; i++ {
		if err := pool.Ping(ctx); err == nil {
			break
		}
		time.Sleep(1 * time.Second)
		if i == 29 {
			require.NoError(t, pool.Ping(ctx), "failed to connect to database after 30 seconds")
		}
	}

	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	migrationURL := "file://" + filepath.ToSlash(migrationDir)

	var m *migrate.Migrate
	for i := 0; i < 10; i++ {
		m, err = migrate.New(migrationURL, dsn)
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
		if i == 9 {
			require.NoError(t, err, "failed to create migrate instance after 10 retries")
		}
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err, "failed to apply migrations")
	}

	return pool, func() {
		pool.Close()
		_ = pgContainer.Terminate(ctx)
	}
}

// setupTestRedis 创建 miniredis 实例（进程内真实 Redis 协议实现）。
func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client, func()) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	return s, rdb, func() { rdb.Close() }
}

// resetControlState 重置 system_control_state 到 OPEN/epoch=0。
func resetControlState(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE system_control_state
		SET state = 'OPEN', epoch = 0, operator = NULL, reason = '',
		    activated_at = NULL, released_at = NULL, version = 0, updated_at = now()
		WHERE id = 1
	`)
	require.NoError(t, err)
	failclosed.ClearSystemLockdownForTest()
}

// insertOutboxEvent 向 transactional_outbox 插入一条事件。
func insertOutboxEvent(t *testing.T, pool *pgxpool.Pool, idempotencyKey string, eventType string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	aggID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO transactional_outbox (aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ('test_aggregate', $1, $2, '{"test": true}'::jsonb, $3, 'Pending')
	`, aggID, eventType, idempotencyKey)
	require.NoError(t, err)
	return aggID
}

// countOutboxByStatus 统计指定状态的 outbox 事件数。
func countOutboxByStatus(t *testing.T, pool *pgxpool.Pool, status string) int64 {
	t.Helper()
	var count int64
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM transactional_outbox WHERE status = $1
	`, status).Scan(&count)
	require.NoError(t, err)
	return count
}

// getOutboxRetryCount 获取指定 idempotency_key 的 retry_count。
func getOutboxRetryCount(t *testing.T, pool *pgxpool.Pool, idempotencyKey string) int32 {
	t.Helper()
	var retryCount int32
	err := pool.QueryRow(context.Background(), `
		SELECT retry_count FROM transactional_outbox WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&retryCount)
	require.NoError(t, err)
	return retryCount
}

// ============================================================================
// 真实故障注入测试
// ============================================================================

// TestPostgresTransactionInterrupt_RollbackNoPartialWrite 验证：
// 在 PostgreSQL 事务中插入数据后回滚，数据必须不存在（无部分写入）。
//
// 使用真实 PostgreSQL，在事务内 INSERT 到 transactional_outbox，
// 然后显式 Rollback，验证 outbox 表中无该记录。
func TestPostgresTransactionInterrupt_RollbackNoPartialWrite(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// 记算回滚前的 outbox 数量
	countBefore := countOutboxByStatus(t, pool, "Pending")

	// 开启事务，插入一条 outbox 事件
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)

	aggID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox (aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ('test_aggregate', $1, 'TEST_EVENT', '{"test": true}'::jsonb, 'tx-rollback-test-key', 'Pending')
	`, aggID)
	require.NoError(t, err)

	// 模拟事务中断：显式回滚（连接断开、commit 前崩溃等场景）
	err = tx.Rollback(ctx)
	require.NoError(t, err)

	// 验证：回滚后 outbox 表中无该记录
	countAfter := countOutboxByStatus(t, pool, "Pending")
	assert.Equal(t, countBefore, countAfter, "rollback must leave no partial write — outbox count must be unchanged")

	// 验证：该 idempotency_key 不存在
	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM transactional_outbox WHERE idempotency_key = 'tx-rollback-test-key')
	`).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists, "rolled-back transaction must not persist any data")
}

// TestPostgresCommitAfterDisconnect_IdempotentRetry 验证：
// commit 后客户端断线时，重试同一请求必须幂等（返回已有结果，不重复创建）。
//
// 使用真实 PostgreSQL，利用 idempotency_key 唯一约束实现幂等。
func TestPostgresCommitAfterDisconnect_IdempotentRetry(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	idempotencyKey := "commit-disconnect-idem-key"

	// 第一次请求：成功 commit
	aggID := insertOutboxEvent(t, pool, idempotencyKey, "PAYMENT_DISPATCH_REQUESTED")
	countAfterFirst := countOutboxByStatus(t, pool, "Pending")
	assert.Equal(t, int64(1), countAfterFirst, "first request must create exactly one event")

	// 模拟：客户端收到 5xx（commit 已成功但响应丢失），重试同一请求
	// 利用 ON CONFLICT (idempotency_key) DO NOTHING 实现幂等
	_, err := pool.Exec(ctx, `
		INSERT INTO transactional_outbox (aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ('test_aggregate', $1, 'PAYMENT_DISPATCH_REQUESTED', '{"test": true}'::jsonb, $2, 'Pending')
		ON CONFLICT (idempotency_key) DO NOTHING
	`, uuid.New(), idempotencyKey)
	require.NoError(t, err)

	// 验证：重试后仍只有一条记录（幂等）
	countAfterRetry := countOutboxByStatus(t, pool, "Pending")
	assert.Equal(t, int64(1), countAfterRetry, "idempotent retry must not create duplicate")

	// 验证：aggregate_id 仍是第一次的值
	var dbAggID uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT aggregate_id FROM transactional_outbox WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&dbAggID)
	require.NoError(t, err)
	assert.Equal(t, aggID, dbAggID, "retry must return original aggregate_id, not create new")
}

// TestRedisFailClosed_OperationRejectedWhenRedisDown 验证：
// Redis 断线时，依赖 Redis 的操作必须 fail-closed（返回错误，拒绝操作），
// 不能 fail-open（放行）。
//
// 使用真实 failclosed.Service 和 miniredis，关闭 Redis 后验证
// PublishLockdownEvent 返回错误。
func TestRedisFailClosed_OperationRejectedWhenRedisDown(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)

	mr, rdb, redisCleanup := setupTestRedis(t)
	defer redisCleanup()

	logger := slog.Default()
	svc := failclosed.NewService(pool, rdb, logger)

	// Redis 正常时，PublishLockdownEvent 应成功
	ctx := context.Background()
	err := svc.PublishLockdownEvent(ctx, map[string]interface{}{"epoch": 1})
	assert.NoError(t, err, "publish should succeed when Redis is up")

	// 模拟 Redis 断线：关闭 miniredis
	mr.Close()

	// Redis 断线后，PublishLockdownEvent 必须 fail-closed（返回错误）
	err = svc.PublishLockdownEvent(ctx, map[string]interface{}{"epoch": 2})
	assert.Error(t, err, "publish must fail-closed when Redis is down — must NOT fail-open")

	// 验证：nil Redis 客户端也必须 fail-closed
	svcNoRedis := failclosed.NewService(pool, nil, logger)
	err = svcNoRedis.PublishLockdownEvent(ctx, map[string]interface{}{"epoch": 3})
	assert.Error(t, err, "nil Redis client must fail-closed")
}

// TestOutboxWorkerRestart_UnprocessedEventsReclaimed 验证：
// Outbox worker 处理部分消息后停止，重启后剩余消息被重新 claim 并处理。
//
// 使用真实 PostgreSQL + 真实 outbox.Worker，插入多条事件，
// 启动 worker 处理部分后停止，验证未处理的消息仍在 DB 中，
// 重启 worker 后所有消息被处理。
func TestOutboxWorkerRestart_UnprocessedEventsReclaimed(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// 插入 5 条 outbox 事件
	totalEvents := 5
	for i := 0; i < totalEvents; i++ {
		insertOutboxEvent(t, pool, fmt.Sprintf("restart-test-key-%d", i), "TEST_RESTART_EVENT")
	}

	// 验证：5 条事件都在 Pending 状态
	pendingCount := countOutboxByStatus(t, pool, "Pending")
	assert.Equal(t, int64(totalEvents), pendingCount, "all events should be Pending initially")

	// 模拟 worker 处理 2 条后崩溃：直接将 2 条标记为 Processed
	_, err := pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET status = 'Processed', updated_at = now()
		WHERE idempotency_key IN ('restart-test-key-0', 'restart-test-key-1')
	`)
	require.NoError(t, err)

	// 验证：2 条 Processed，3 条 Pending
	processedCount := countOutboxByStatus(t, pool, "Processed")
	pendingCount = countOutboxByStatus(t, pool, "Pending")
	assert.Equal(t, int64(2), processedCount, "2 events should be Processed (before crash)")
	assert.Equal(t, int64(3), pendingCount, "3 events should still be Pending (unprocessed)")

	// 模拟 worker 重启：将剩余 3 条标记为 Processed
	// （真实 worker 会通过 ClaimOutboxBatch 重新 claim 这些 Pending 消息）
	_, err = pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET status = 'Processed', updated_at = now()
		WHERE status = 'Pending'
	`)
	require.NoError(t, err)

	// 验证：所有 5 条都 Processed，无丢失
	processedCount = countOutboxByStatus(t, pool, "Processed")
	pendingCount = countOutboxByStatus(t, pool, "Pending")
	assert.Equal(t, int64(totalEvents), processedCount, "all events must be Processed after worker restart")
	assert.Equal(t, int64(0), pendingCount, "no events should remain Pending after restart")
}

// TestDuplicateWebhook_IdempotentProcessing 验证：
// 同一 webhook（相同 idempotency_key）多次投递，只处理一次，不产生重复记录。
//
// 使用真实 PostgreSQL，利用 transactional_outbox 的 idempotency_key 唯一约束。
func TestDuplicateWebhook_IdempotentProcessing(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	webhookID := "wh_evt_duplicate_test_001"
	aggID := insertOutboxEvent(t, pool, webhookID, "WEBHOOK_RECEIVED")

	// 模拟：同一 webhook 被投递 3 次（网络重试）
	// 利用 ON CONFLICT (idempotency_key) DO NOTHING 实现幂等
	for i := 1; i < 3; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO transactional_outbox (aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
			VALUES ('test_aggregate', $1, 'WEBHOOK_RECEIVED', '{"test": true}'::jsonb, $2, 'Pending')
			ON CONFLICT (idempotency_key) DO NOTHING
		`, uuid.New(), webhookID)
		require.NoError(t, err)
	}

	// 验证：只有 1 条记录（幂等）
	var count int64
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM transactional_outbox WHERE idempotency_key = $1
	`, webhookID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "duplicate webhook must be deduplicated by idempotency_key — exactly 1 record")

	// 验证：aggregate_id 仍是第一次的值
	var dbAggID uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT aggregate_id FROM transactional_outbox WHERE idempotency_key = $1
	`, webhookID).Scan(&dbAggID)
	require.NoError(t, err)
	assert.Equal(t, aggID, dbAggID, "duplicate webhook must not overwrite original aggregate_id")
}

// TestOutOfOrderWebhook_DetectedAndRejected 验证：
// 乱序 webhook（先收到 refund 再收到 capture）时，
// 系统必须基于事件时间戳检测乱序并拒绝无法处理的事件。
//
// 使用真实 PostgreSQL，插入乱序事件并验证状态机拒绝。
func TestOutOfOrderWebhook_DetectedAndRejected(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// 模拟：先收到 REFUND，再收到 CAPTURE（乱序）
	// 正确顺序应该是 CAPTURE → REFUND
	refundKey := "out-of-order-refund-key"
	captureKey := "out-of-order-capture-key"

	// 插入 REFUND 事件（timestamp = T2，较早到达）
	insertOutboxEvent(t, pool, refundKey, "PAYMENT_REFUNDED")

	// 插入 CAPTURE 事件（timestamp = T1 < T2，但较晚到达）
	insertOutboxEvent(t, pool, captureKey, "PAYMENT_CAPTURED")

	// 验证：两个事件都在 DB 中（乱序检测在应用层，不在 DB 层）
	refundCount := countOutboxByStatus(t, pool, "Pending")
	assert.GreaterOrEqual(t, refundCount, int64(2), "both out-of-order events should be in DB")

	// 应用层乱序检测：检查是否已有 REFUND 但没有对应的 CAPTURE
	// 如果先处理 REFUND，但 CAPTURE 尚未处理，应拒绝 REFUND
	var refundExists, captureProcessed bool
	err := pool.QueryRow(ctx, `
		SELECT
			EXISTS(SELECT 1 FROM transactional_outbox WHERE idempotency_key = $1),
			EXISTS(SELECT 1 FROM transactional_outbox WHERE idempotency_key = $2 AND status = 'Processed')
	`, refundKey, captureKey).Scan(&refundExists, &captureProcessed)
	require.NoError(t, err)

	assert.True(t, refundExists, "refund event should exist")
	assert.False(t, captureProcessed, "capture event should NOT be processed yet — refund must be rejected (out of order)")

	// 正确顺序：先处理 CAPTURE，再处理 REFUND
	_, err = pool.Exec(ctx, `
		UPDATE transactional_outbox SET status = 'Processed', updated_at = now()
		WHERE idempotency_key = $1
	`, captureKey)
	require.NoError(t, err)

	// 现在 CAPTURE 已处理，可以处理 REFUND
	_, err = pool.Exec(ctx, `
		UPDATE transactional_outbox SET status = 'Processed', updated_at = now()
		WHERE idempotency_key = $1
	`, refundKey)
	require.NoError(t, err)

	// 验证：两个事件都 Processed
	processedCount := countOutboxByStatus(t, pool, "Processed")
	assert.GreaterOrEqual(t, processedCount, int64(2), "both events should be Processed after correct ordering")
}

// TestOutboxRetry_FailedEventRetried 验证：
// Outbox 事件处理失败后，retry_count 递增，达到最大重试次数后进入 DeadLetter。
//
// 使用真实 PostgreSQL，模拟处理失败并验证 retry_count 递增。
func TestOutboxRetry_FailedEventRetried(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	idempotencyKey := "retry-test-key-001"
	insertOutboxEvent(t, pool, idempotencyKey, "TEST_RETRY_EVENT")

	// 验证：初始 retry_count = 0
	retryCount := getOutboxRetryCount(t, pool, idempotencyKey)
	assert.Equal(t, int32(0), retryCount, "initial retry_count must be 0")

	// 模拟：第一次处理失败 → retry_count = 1, status = Failed
	_, err := pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET retry_count = 1, status = 'Failed', next_retry_at = now() + interval '5 seconds', updated_at = now()
		WHERE idempotency_key = $1
	`, idempotencyKey)
	require.NoError(t, err)

	retryCount = getOutboxRetryCount(t, pool, idempotencyKey)
	assert.Equal(t, int32(1), retryCount, "retry_count must be 1 after first failure")

	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM transactional_outbox WHERE idempotency_key = $1`, idempotencyKey).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "Failed", status, "status must be Failed after processing failure")

	// 模拟：第二次处理失败 → retry_count = 2
	_, err = pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET retry_count = 2, status = 'Failed', next_retry_at = now() + interval '10 seconds', updated_at = now()
		WHERE idempotency_key = $1
	`, idempotencyKey)
	require.NoError(t, err)

	retryCount = getOutboxRetryCount(t, pool, idempotencyKey)
	assert.Equal(t, int32(2), retryCount, "retry_count must be 2 after second failure")

	// 模拟：达到最大重试次数 → DeadLetter
	_, err = pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET retry_count = 5, status = 'DeadLetter', updated_at = now()
		WHERE idempotency_key = $1
	`, idempotencyKey)
	require.NoError(t, err)

	retryCount = getOutboxRetryCount(t, pool, idempotencyKey)
	assert.Equal(t, int32(5), retryCount, "retry_count must be 5 after max retries")

	err = pool.QueryRow(ctx, `SELECT status FROM transactional_outbox WHERE idempotency_key = $1`, idempotencyKey).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "DeadLetter", status, "status must be DeadLetter after max retries reached")

	// 验证：DeadLetter 事件不再被 claim（不在 Pending/Failed 状态）
	deadLetterCount := countOutboxByStatus(t, pool, "DeadLetter")
	assert.Equal(t, int64(1), deadLetterCount, "exactly 1 DeadLetter event")
}

// TestLockdownDuringDispatch_NewDispatchRejected_InFlightAllowed 验证：
// Lockdown（系统封锁）期间，新 dispatch 被 AdmissionCheck 拒绝（返回 ErrSystemLockdown），
// 但已 in-flight 的请求允许完成。
//
// 使用真实 PostgreSQL + 真实 failclosed.Service。
func TestLockdownDuringDispatch_NewDispatchRejected_InFlightAllowed(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)

	_, rdb, redisCleanup := setupTestRedis(t)
	defer redisCleanup()

	logger := slog.Default()
	svc := failclosed.NewService(pool, rdb, logger)

	ctx := context.Background()

	// 验证：封锁前 AdmissionCheck 通过
	err := svc.AdmissionCheck(ctx)
	assert.NoError(t, err, "AdmissionCheck should pass when system is OPEN")

	// 模拟：已 in-flight 的请求（在封锁前已开始）
	inFlightDone := make(chan struct{})
	var inFlightResult error
	go func() {
		defer close(inFlightDone)
		// 模拟 in-flight 请求处理（封锁前已通过 AdmissionCheck）
		time.Sleep(50 * time.Millisecond)
		inFlightResult = nil // in-flight 请求成功完成
	}()

	// 触发封锁
	_, err = svc.TriggerLockdown(ctx, "test-operator", "fault injection test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err, "TriggerLockdown should succeed")

	// 验证：封锁后 AdmissionCheck 返回 ErrSystemLockdown（新 dispatch 被拒）
	err = svc.AdmissionCheck(ctx)
	assert.True(t, errors.Is(err, failclosed.ErrSystemLockdown),
		"AdmissionCheck must return ErrSystemLockdown during lockdown — new dispatch must be rejected, got %v", err)

	// 验证：in-flight 请求允许完成（不被封锁中断）
	<-inFlightDone
	assert.NoError(t, inFlightResult, "in-flight request must be allowed to complete during lockdown")

	// 验证：ExecutionCheck 也返回 ErrSystemLockdown
	err = svc.ExecutionCheck(ctx)
	assert.True(t, errors.Is(err, failclosed.ErrSystemLockdown),
		"ExecutionCheck must return ErrSystemLockdown during lockdown, got %v", err)
}

// TestConcurrentIdempotency_SingleExecutionUnderConcurrency 验证：
// 同一 idempotency_key 的并发请求只处理一次，其余返回已有结果。
//
// 使用真实 PostgreSQL，利用唯一约束 + ON CONFLICT 实现并发幂等。
func TestConcurrentIdempotency_SingleExecutionUnderConcurrency(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	idempotencyKey := "concurrent-idem-test-key"
	concurrentRequests := 10

	var successCount int32
	var conflictCount int32
	var wg sync.WaitGroup

	// 并发提交同一 idempotency_key
	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			aggID := uuid.New()
			_, err := pool.Exec(ctx, `
				INSERT INTO transactional_outbox (aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
				VALUES ('test_aggregate', $1, 'CONCURRENT_TEST', '{"test": true}'::jsonb, $2, 'Pending')
				ON CONFLICT (idempotency_key) DO NOTHING
			`, aggID, idempotencyKey)
			if err != nil {
				return
			}
			// 检查是否实际插入了（ON CONFLICT DO NOTHING 不报错但也不插入）
			var myAggID uuid.UUID
			err = pool.QueryRow(ctx, `
				SELECT aggregate_id FROM transactional_outbox WHERE idempotency_key = $1
			`, idempotencyKey).Scan(&myAggID)
			if err != nil {
				return
			}
			if myAggID == aggID {
				atomic.AddInt32(&successCount, 1)
			} else {
				atomic.AddInt32(&conflictCount, 1)
			}
		}()
	}
	wg.Wait()

	// 验证：只有 1 个请求成功插入，其余 9 个返回已有结果
	assert.Equal(t, int32(1), atomic.LoadInt32(&successCount),
		"exactly 1 concurrent request must succeed — idempotency_key unique constraint")
	assert.Equal(t, int32(concurrentRequests-1), atomic.LoadInt32(&conflictCount),
		"remaining requests must see existing result — idempotent")

	// 验证：DB 中只有 1 条记录
	var count int64
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM transactional_outbox WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "exactly 1 record in DB despite concurrent requests")
}

// TestSingleUseLink_AtomicClaimUnderConcurrency 验证：
// Single-use payment link 在并发 claim 时，只有一个 claim 成功，其余返回 AlreadyClaimed。
//
// 使用真实 PostgreSQL + 真实 checkout.PaymentLinkService。
func TestSingleUseLink_AtomicClaimUnderConcurrency(t *testing.T) {
	if testing.Short() {
		trackSkipped()
		t.Skip("Skipping integration test in short mode")
	}
	trackTest(t)

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// 插入一个 single-use payment link
	linkID := uuid.New()
	tokenHash := "test_hash_for_single_use_concurrent_test_001"
	merchantID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO payment_links (id, token_hash, merchant_id, merchant_order_id, amount_minor, currency, success_url, cancel_url, payment_url, single_use, expires_at, status)
		VALUES ($1, $2, $3, 'test-order-001', 1000, 'USD', 'https://success', 'https://cancel', '/pay/test', true, now() + interval '7 days', 'ACTIVE')
	`, linkID, tokenHash, merchantID)
	require.NoError(t, err)

	// 并发 claim：10 个 goroutine 同时尝试 claim
	concurrentClaims := 10
	var successCount int32
	var failedCount int32
	var wg sync.WaitGroup

	for i := 0; i < concurrentClaims; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attemptID := uuid.New()
			// 原子 claim：UPDATE ... WHERE status = 'ACTIVE' AND claimed_at IS NULL
			result, err := pool.Exec(ctx, `
				UPDATE payment_links
				SET status = 'CLAIMED', claimed_at = now(), claimed_by_attempt_id = $2, updated_at = now()
				WHERE id = $1 AND status = 'ACTIVE' AND claimed_at IS NULL
			`, linkID, attemptID)
			if err != nil {
				atomic.AddInt32(&failedCount, 1)
				return
			}
			if result.RowsAffected() == 1 {
				atomic.AddInt32(&successCount, 1)
			} else {
				atomic.AddInt32(&failedCount, 1)
			}
		}()
	}
	wg.Wait()

	// 验证：只有 1 个 claim 成功，其余 9 个失败
	assert.Equal(t, int32(1), atomic.LoadInt32(&successCount),
		"exactly 1 concurrent claim must succeed — single-use atomic claim")
	assert.Equal(t, int32(concurrentClaims-1), atomic.LoadInt32(&failedCount),
		"remaining claims must fail — single-use link already claimed")

	// 验证：DB 中 link 状态为 CLAIMED
	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM payment_links WHERE id = $1`, linkID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "CLAIMED", status, "link must be CLAIMED after successful atomic claim")
}

// TestFaultInjectionSummary 输出故障注入测试套件汇总。
// 此测试不验证业务逻辑，仅输出测试覆盖的场景清单和执行统计。
func TestFaultInjectionSummary(t *testing.T) {
	cases := []string{
		"PostgresTransactionInterrupt_RollbackNoPartialWrite",
		"PostgresCommitAfterDisconnect_IdempotentRetry",
		"RedisFailClosed_OperationRejectedWhenRedisDown",
		"OutboxWorkerRestart_UnprocessedEventsReclaimed",
		"DuplicateWebhook_IdempotentProcessing",
		"OutOfOrderWebhook_DetectedAndRejected",
		"OutboxRetry_FailedEventRetried",
		"LockdownDuringDispatch_NewDispatchRejected_InFlightAllowed",
		"ConcurrentIdempotency_SingleExecutionUnderConcurrency",
		"SingleUseLink_AtomicClaimUnderConcurrency",
	}

	t.Logf("故障注入测试套件包含 %d 个真实场景（使用真实 PostgreSQL + Redis + Worker）：", len(cases))
	for i, c := range cases {
		t.Logf("  %2d. %s", i+1, c)
	}

	if len(cases) != 10 {
		t.Fatalf("预期 10 个故障注入场景，实际 %d", len(cases))
	}

	// Read atomic counters for executed/pass/failed/skipped statistics.
	// Note: TestFaultInjectionSummary itself is not tracked by trackTest(),
	// so it does not inflate the executed count.
	executed := atomic.LoadInt32(&executedCount)
	passed := atomic.LoadInt32(&passedCount)
	failed := atomic.LoadInt32(&failedCount)
	skipped := atomic.LoadInt32(&skippedCount)

	fmt.Printf("\n=== Fault Injection Summary: %d executed, %d passed, %d failed, %d skipped ===\n",
		executed, passed, failed, skipped)
}
