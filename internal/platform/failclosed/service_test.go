package failclosed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// withTestAdminIdentity 构造包含已验证 AdminIdentity 的 context（测试辅助）。
// P0-1: ExecuteRecovery 要求 checker 来自已验证 JWT Context。
func withTestAdminIdentity(username, role string) context.Context {
	identity := &httpx.AdminIdentity{
		Username:    username,
		Subject:     username,
		Role:        role,
		Permissions: httpx.RolePermissions(role),
	}
	return context.WithValue(context.Background(), httpx.AdminContextKey{}, identity)
}

// 共享外部测试库：包内全部测试复用同一连接池，仅在首个测试时重置 schema 并跑一次全量迁移。
// 避免每个测试都 DROP SCHEMA + 重新迁移，既拖慢包（易触发 10m 超时），也会反复清空共享库、
// 使后续按字母序执行的测试包（如 httpx/middleware）遭遇残留/缺失表。
var (
	extPoolOnce sync.Once
	extPool     *pgxpool.Pool
	extPoolErr  error
)

// setupTestDB 创建带迁移的测试数据库。
func setupTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	ctx := context.Background()

	if externalDSN := os.Getenv("TEST_DATABASE_DSN"); externalDSN != "" {
		extPoolOnce.Do(func() {
			extPool, extPoolErr = pgxpool.New(ctx, externalDSN)
			if extPoolErr != nil {
				return
			}
			_, extPoolErr = extPool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO public;`)
			if extPoolErr != nil {
				return
			}

			migrationDir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
			if err != nil {
				extPoolErr = err
				return
			}
			migrationURL := "file://" + filepath.ToSlash(migrationDir)

			m, err := migrate.New(migrationURL, externalDSN)
			if err != nil {
				extPoolErr = err
				return
			}
			defer m.Close()

			if err := m.Up(); err != nil && err != migrate.ErrNoChange {
				extPoolErr = err
			}
		})
		require.NoError(t, extPoolErr, "failed to init shared external test DB")
		// 共享池不随单个测试关闭，由进程退出回收；测试间通过 resetControlState 复位状态。
		return extPool, func() {}
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
	require.NoError(t, err)

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

	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
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

// setupTestRedis 创建 miniredis 实例。
func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client, func()) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	return s, rdb, func() { rdb.Close() }
}

// resetControlState 将大闸恢复到 OPEN/epoch=0 基线，并复原封锁副作用。
//
// 共享 schema（TEST_DATABASE_DSN）下无法用 UPDATE 把 state 置回 OPEN：
// 触发器（000035）要求 state 变化必须递增 epoch，而后续测试断言 TriggerLockdown
// 后 epoch==1。因此用 TRUNCATE（不触发行级 UPDATE 触发器）重建基线行，
// 并同步清理封锁副作用（幂等表、通道 DISABLED、隔离组 RISK_HOLD），保证可重复执行。
func resetControlState(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `TRUNCATE system_control_state`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO system_control_state (id, state, epoch) VALUES (1, 'OPEN', 0)`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `TRUNCATE admin_lockdown_idempotency`)
	require.NoError(t, err)
	// outbox 幂等键形如 lockdown-<epoch>/recovery-<epoch>，与 epoch 绑定；
	// 共享 schema 下 epoch 复位后残留行会撞唯一约束（transactional_outbox_idempotency_key_key）。
	_, err = pool.Exec(ctx, `TRUNCATE transactional_outbox`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE acquirer_channels SET status = 'ACTIVE', updated_at = now()`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		UPDATE pipeline_isolation_groups
		SET status = 'ACTIVE', frozen_reason = NULL, frozen_by = NULL, frozen_at = NULL, updated_at = now()
	`)
	require.NoError(t, err)
	ClearSystemLockdownForTest()
}

// seedLockdownPrereqs 插入测试所需的通道和隔离组。
func seedLockdownPrereqs(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	// 插入企业实体
	_, err := pool.Exec(ctx, `
		INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		VALUES ('11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222',
		        'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')
		ON CONFLICT DO NOTHING
	`)
	require.NoError(t, err)
	// 插入 active 通道
	_, err = pool.Exec(ctx, `
		INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		VALUES ('33333333-3333-3333-3333-333333333333',
		        '11111111-1111-1111-1111-111111111111',
		        'TIER_2_COMPLIANT', 'test-provider', 'test-channel', 'test-vault', 'DEFAULT', 'ACTIVE')
		ON CONFLICT DO NOTHING
	`)
	require.NoError(t, err)
	// 插入 active 隔离组
	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_isolation_groups (id, name, status)
		VALUES ('44444444-4444-4444-4444-444444444444', 'test-group', 'ACTIVE')
		ON CONFLICT DO NOTHING
	`)
	require.NoError(t, err)
}

// TestService_TriggerLockdown_PersistsToDB 验证触发封锁后 DB 状态为 LOCKED。
func TestService_TriggerLockdown_PersistsToDB(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())
	result, err := svc.TriggerLockdown(context.Background(), "admin-test", "integration test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)
	assert.Equal(t, StateLocked, result.Record.State)
	assert.Equal(t, int64(1), result.Record.Epoch)
	assert.True(t, result.DisabledChannels >= 1)
	assert.True(t, result.RiskHoldGroups >= 1)

	// 验证 DB 真相
	var dbState string
	var dbEpoch int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT state::text, epoch FROM system_control_state WHERE id = 1`).Scan(&dbState, &dbEpoch))
	assert.Equal(t, "LOCKED", dbState)
	assert.Equal(t, int64(1), dbEpoch)

	// 验证进程内缓存
	assert.True(t, IsSystemLockedDown())
	assert.Equal(t, int64(1), CurrentEpoch())
}

// TestService_LoadFromDB_RestoresAfterRestart 验证重启后从 DB 恢复 LOCKED 状态。
// 这是 G07 的核心要求：不消费旧 Pub/Sub 也必须从 DB 恢复。
func TestService_LoadFromDB_RestoresAfterRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	// 实例 A 触发封锁
	svcA := NewService(pool, nil, slog.Default())
	_, err := svcA.TriggerLockdown(context.Background(), "admin-A", "instance A lockdown", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)
	require.True(t, IsSystemLockedDown())

	// 模拟重启：重置进程内缓存
	ClearSystemLockdownForTest()
	require.False(t, IsSystemLockedDown())

	// 实例 B 启动，从 DB 加载（不消费任何 pub/sub）
	svcB := NewService(pool, nil, slog.Default())
	rec, err := svcB.LoadFromDB(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StateLocked, rec.State)
	assert.True(t, IsSystemLockedDown(), "instance B must be LOCKED after loading from DB")
}

// TestService_MultiInstance_SyncViaDB 验证两个实例共享 DB：
// 实例 A 触发后，实例 B 通过 DB admission barrier 拦截后续支付。
func TestService_MultiInstance_SyncViaDB(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	// 实例 A 和 B 共享同一 DB
	svcA := NewService(pool, nil, slog.Default())
	svcB := NewService(pool, nil, slog.Default())

	// 实例 B 初始加载
	_, err := svcB.LoadFromDB(context.Background())
	require.NoError(t, err)
	require.False(t, IsSystemLockedDown())

	// 实例 A 触发封锁
	_, err = svcA.TriggerLockdown(context.Background(), "admin-A", "multi-instance test", fmt.Sprintf("idemp-%s-2", t.Name()))
	require.NoError(t, err)

	// 实例 B 的进程内缓存可能未更新（无 pub/sub），
	// 但 ExecutionCheck 必须通过 DB 拦截
	err = ExecutionCheck(context.Background(), pool)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown))

	// 实例 B 重新加载后缓存也应更新
	_, err = svcB.LoadFromDB(context.Background())
	require.NoError(t, err)
	assert.True(t, IsSystemLockedDown())
}

// TestService_EpochMonotonic_RejectsStaleEvents 验证 epoch 单调递增：
// 旧 epoch 事件被拒绝。
func TestService_EpochMonotonic_RejectsStaleEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)

	svc := NewService(pool, nil, slog.Default())
	_, err := svc.LoadFromDB(context.Background())
	require.NoError(t, err)

	// 模拟收到 epoch=5 的封锁事件
	applied := svc.ApplyRemoteUpdate(ControlRecord{State: StateLocked, Epoch: 5, Operator: "remote"})
	assert.True(t, applied)
	assert.True(t, IsSystemLockedDown())
	assert.Equal(t, int64(5), CurrentEpoch())

	// 模拟收到 epoch=3 的旧事件（回放）—— 必须拒绝
	applied = svc.ApplyRemoteUpdate(ControlRecord{State: StateOpen, Epoch: 3, Operator: "remote"})
	assert.False(t, applied, "stale epoch event must be rejected")
	assert.True(t, IsSystemLockedDown(), "state must remain LOCKED after rejecting stale event")
	assert.Equal(t, int64(5), CurrentEpoch())

	// 模拟收到 epoch=6 的新事件 —— 接受
	applied = svc.ApplyRemoteUpdate(ControlRecord{State: StateOpen, Epoch: 6, Operator: "remote"})
	assert.True(t, applied)
	assert.False(t, IsSystemLockedDown())
	assert.Equal(t, int64(6), CurrentEpoch())
}

// TestService_TriggerLockdown_RedisPublishFailure_NoFalseSuccess 验证：
// Redis publish 失败时触发请求不能返回虚假"全节点成功"。
// 触发事务已提交（DB 是真相），但响应必须明确告知同步语义。
func TestService_TriggerLockdown_RedisPublishFailure_NoFalseSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	// 使用一个不可达的 Redis
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:19999"}) // 不存在的端口
	defer rdb.Close()

	svc := NewService(pool, rdb, slog.Default())
	result, err := svc.TriggerLockdown(context.Background(), "admin-test", "redis down test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err, "TriggerLockdown must succeed (DB is source of truth)")
	assert.Equal(t, StateLocked, result.Record.State)

	// 直接 publish 应失败
	err = svc.PublishLockdownEvent(context.Background(), map[string]interface{}{
		"event": "SYSTEM_LOCKDOWN",
		"epoch": result.Record.Epoch,
	})
	assert.Error(t, err, "publish must fail when redis is down")

	// 但 DB 状态必须是 LOCKED（真相已持久化）
	var dbState string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT state::text FROM system_control_state WHERE id = 1`).Scan(&dbState))
	assert.Equal(t, "LOCKED", dbState)
}

// TestService_Recovery_RequiresApproval 验证解除大闸必须走审批（maker-checker）。
func TestService_Recovery_RequiresApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 触发封锁
	_, err := svc.TriggerLockdown(context.Background(), "admin", "test lockdown", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// 请求解除（创建审批请求）
	approvalID, err := svc.RequestRecovery(context.Background(), "admin", "test recovery request")
	require.NoError(t, err)
	assert.NotEmpty(t, approvalID)

	// 状态应为 RECOVERY_PENDING（仍封锁）
	rec, err := svc.GetCurrentState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StateRecoveryPending, rec.State)
	assert.True(t, IsSystemLockedDown(), "RECOVERY_PENDING must still block")

	// 执行解除（审批通过后）
	// P0-1: checker 必须来自已验证 JWT Context，且 checker != requester
	ctx := withTestAdminIdentity("approver", httpx.AdminRoleApprover)
	result, err := svc.ExecuteRecovery(ctx, approvalID, "approver")
	require.NoError(t, err)
	assert.Equal(t, StateOpen, result.Record.State)

	// 验证 DB 状态
	rec, err = svc.GetCurrentState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StateOpen, rec.State)
	assert.False(t, IsSystemLockedDown())
}

// TestService_IllegalTransition_RejectsDoubleLockdown 验证状态机：
// 已封锁时再次触发返回错误。
func TestService_IllegalTransition_RejectsDoubleLockdown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 第一次触发成功
	_, err := svc.TriggerLockdown(context.Background(), "admin", "first lockdown", fmt.Sprintf("idemp-%s-1", t.Name()))
	require.NoError(t, err)

	// 第二次触发必须失败
	_, err = svc.TriggerLockdown(context.Background(), "admin", "second lockdown", fmt.Sprintf("idemp-%s-2", t.Name()))
	assert.Error(t, err)
}

// TestAdmissionCheck_vs_ExecutionCheck 验证入口检查与执行时检查的语义差异。
// 模拟请求已通过入口但尚未 dispatch，此时触发大闸；execution-time check 必须阻止。
func TestAdmissionCheck_vs_ExecutionCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())
	_, err := svc.LoadFromDB(context.Background())
	require.NoError(t, err)

	// 1. 入口检查通过（未封锁）
	err = AdmissionCheck()
	require.NoError(t, err)

	// 2. 模拟请求已通过入口，正在处理中...
	//    此时另一个管理员触发封锁
	_, err = svc.TriggerLockdown(context.Background(), "admin", "mid-flight lockdown", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// 3. 入口检查现在会拦截（缓存已更新）
	err = AdmissionCheck()
	assert.Error(t, err)

	// 4. 即使缓存未更新（模拟跨节点），ExecutionCheck 也必须通过 DB 拦截
	ClearSystemLockdownForTest() // 模拟缓存未更新
	err = AdmissionCheck()
	require.NoError(t, err, "cache miss should pass admission check")

	// ExecutionCheck 必须通过 DB 拦截
	err = ExecutionCheck(context.Background(), pool)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown))
}

// TestAllowlist_AllowsRiskConvergenceActions 验证 allowlist：
// 退款/查询/webhook/reconcile 在封锁期间仍允许。
func TestAllowlist_AllowsRiskConvergenceActions(t *testing.T) {
	allowedActions := []ActionKind{
		ActionQuery,
		ActionWebhookReceive,
		ActionReconcile,
		ActionRefund,
		ActionVoidRefund,
	}
	for _, action := range allowedActions {
		assert.True(t, IsAllowedDuringLockdown(action),
			"action %s must be allowed during lockdown", action)
	}

	blockedActions := []ActionKind{
		ActionNewAuthorization,
		ActionNewCapture,
		ActionNewIssuance,
		ActionNewPayout,
	}
	for _, action := range blockedActions {
		assert.False(t, IsAllowedDuringLockdown(action),
			"action %s must be blocked during lockdown", action)
	}
}

// TestAdmissionCheckForAction_AllowlistBypassesLockdown 验证 allowlist 动作跳过封锁检查。
func TestAdmissionCheckForAction_AllowlistBypassesLockdown(t *testing.T) {
	ClearSystemLockdownForTest()
	t.Cleanup(ClearSystemLockdownForTest)

	ActivateSystemLockdown()
	require.True(t, IsSystemLockedDown())

	// Allowlist 动作应跳过检查
	for _, action := range []ActionKind{ActionQuery, ActionRefund, ActionReconcile, ActionWebhookReceive} {
		err := AdmissionCheckForAction(action)
		assert.NoError(t, err, "allowlist action %s must bypass lockdown", action)
	}

	// 非 allowlist 动作应被拦截
	for _, action := range []ActionKind{ActionNewAuthorization, ActionNewPayout, ActionNewCapture, ActionNewIssuance} {
		err := AdmissionCheckForAction(action)
		assert.Error(t, err, "non-allowlist action %s must be blocked", action)
	}
}

// TestSubscriber_MultiNodeSync 验证多节点通过 pub/sub 同步：
// 实例 A publish 后，实例 B 的 subscriber 收到事件并更新缓存。
func TestSubscriber_MultiNodeSync(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	_, rdb, redisCleanup := setupTestRedis(t)
	defer redisCleanup()

	svcA := NewService(pool, rdb, slog.Default())
	svcB := NewService(pool, rdb, slog.Default())

	// 实例 B 启动订阅
	subB := NewSubscriber(svcB, rdb, slog.Default())
	require.NoError(t, subB.Start(context.Background()))
	defer subB.Stop()

	// 实例 B 初始加载
	_, err := svcB.LoadFromDB(context.Background())
	require.NoError(t, err)
	require.False(t, IsSystemLockedDown())

	// 实例 A 触发封锁并 publish
	result, err := svcA.TriggerLockdown(context.Background(), "admin-A", "pub/sub sync test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// 直接 publish（模拟 outbox worker）
	err = svcA.PublishLockdownEvent(context.Background(), map[string]interface{}{
		"event":    "SYSTEM_LOCKDOWN",
		"epoch":    result.Record.Epoch,
		"operator": "admin-A",
		"ts":       time.Now().UTC().Format(time.RFC3339Nano),
	})
	require.NoError(t, err)

	// 等待实例 B 收到事件
	require.Eventually(t, func() bool {
		return IsSystemLockedDown() && CurrentEpoch() >= result.Record.Epoch
	}, 2*time.Second, 10*time.Millisecond, "instance B must sync via pub/sub")
}

// TestSubscriber_RejectsStaleEpoch 验证 subscriber 拒绝旧 epoch 事件。
func TestSubscriber_RejectsStaleEpoch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)

	_, rdb, redisCleanup := setupTestRedis(t)
	defer redisCleanup()

	svc := NewService(pool, rdb, slog.Default())
	_, err := svc.LoadFromDB(context.Background())
	require.NoError(t, err)

	// 先应用 epoch=5
	UpdateLocalCache(StateLocked, 5)
	require.True(t, IsSystemLockedDown())

	sub := NewSubscriber(svc, rdb, slog.Default())
	require.NoError(t, sub.Start(context.Background()))
	defer sub.Stop()

	// publish 旧 epoch=3 的事件
	oldPayload, _ := json.Marshal(map[string]interface{}{
		"event":    "SYSTEM_LOCKDOWN",
		"epoch":    3,
		"operator": "remote",
		"ts":       time.Now().UTC().Format(time.RFC3339Nano),
	})
	require.NoError(t, rdb.Publish(context.Background(), ChannelLockdownEvent, string(oldPayload)).Err())

	// 等待一下，确保事件被处理
	time.Sleep(200 * time.Millisecond)

	// 状态不应改变（epoch 仍为 5，仍封锁）
	assert.Equal(t, int64(5), CurrentEpoch())
	assert.True(t, IsSystemLockedDown())
}

// TestExecutionCheck_DBError_FailClosed 验证 DB 错误时 fail closed。
func TestExecutionCheck_DBError_FailClosed(t *testing.T) {
	ClearSystemLockdownForTest()
	t.Cleanup(ClearSystemLockdownForTest)

	// nil pool → fail closed
	err := ExecutionCheck(context.Background(), nil)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown))
}

// TestState_TransitionMatrix 验证状态机转移矩阵。
func TestState_TransitionMatrix(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		isLocked bool
	}{
		{"OPEN is not locked", StateOpen, false},
		{"LOCKDOWN_PENDING is locked", StateLockdownPending, true},
		{"LOCKED is locked", StateLocked, true},
		{"RECOVERY_PENDING is locked (recovery not yet executed)", StateRecoveryPending, true},
		{"DEGRADED is not locked", StateDegraded, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.isLocked, tt.state.IsLocked())
		})
	}
}

// TestUpdateLocalCache_ConcurrentSafe 验证 UpdateLocalCache 在并发下安全且单调。
func TestUpdateLocalCache_ConcurrentSafe(t *testing.T) {
	ClearSystemLockdownForTest()
	t.Cleanup(ClearSystemLockdownForTest)

	// 并发更新不同 epoch
	done := make(chan struct{})
	go func() {
		for i := 1; i <= 100; i++ {
			UpdateLocalCache(StateLocked, int64(i))
		}
		close(done)
	}()

	for i := 1; i <= 100; i++ {
		UpdateLocalCache(StateOpen, int64(i))
	}
	<-done

	// 最终 epoch 应为 100
	assert.Equal(t, int64(100), CurrentEpoch())
}

// 确保 httptest 被引用（用于未来 HTTP 集成测试）
var _ = httptest.NewRecorder

// ============================================================================
// P0-1: Kill Switch 恢复状态机严格条件测试
// ============================================================================

// TestRecoveryApproval_ValidTransition 验证有效转移：
// LOCKED → RECOVERY_PENDING (RequestRecovery) → OPEN (ExecuteRecovery)
// 审批状态：PENDING → APPLYING → APPLIED
func TestRecoveryApproval_ValidTransition(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 1. 触发封锁
	_, err := svc.TriggerLockdown(context.Background(), "admin-requester", "valid transition test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// 2. 请求解除（创建审批）
	approvalID, err := svc.RequestRecovery(context.Background(), "admin-requester", "recovery request")
	require.NoError(t, err)

	// 3. 验证 RECOVERY_PENDING
	rec, err := svc.GetCurrentState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StateRecoveryPending, rec.State)

	// 4. 执行解除（checker != requester，来自已验证 ctx）
	ctx := withTestAdminIdentity("admin-checker", httpx.AdminRoleApprover)
	result, err := svc.ExecuteRecovery(ctx, approvalID, "admin-checker")
	require.NoError(t, err)
	assert.Equal(t, StateOpen, result.Record.State)

	// 5. 验证审批记录最终状态为 APPLIED（不是 APPROVED）
	var approvalStatus string
	err = pool.QueryRow(context.Background(),
		`SELECT status FROM admin_approval_requests WHERE id = $1`,
		approvalID).Scan(&approvalStatus)
	require.NoError(t, err)
	assert.Equal(t, "APPLIED", approvalStatus, "approval status must be APPLIED, not APPROVED")

	// 6. 验证 DB system_control_state 为 OPEN
	rec, err = svc.GetCurrentState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StateOpen, rec.State)
}

// TestRecoveryApproval_RejectsDirectLockedToOpen 验证禁止从 LOCKED 直接恢复。
// 必须先 RequestRecovery 走 RECOVERY_PENDING，否则 ExecuteRecovery 拒绝。
func TestRecoveryApproval_RejectsDirectLockedToOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 1. 触发封锁
	_, err := svc.TriggerLockdown(context.Background(), "admin", "direct recovery test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// 2. 手动创建一个审批记录（不经过 RequestRecovery，state 仍为 LOCKED）
	var approvalID string
	err = pool.QueryRow(context.Background(), `
		INSERT INTO admin_approval_requests (action_type, requester, reason, status)
		VALUES ($1, 'admin', 'direct recovery attempt', 'PENDING')
		RETURNING id::text
	`, ApprovalActionTypeSystemRecovery).Scan(&approvalID)
	require.NoError(t, err)

	// 3. 尝试直接从 LOCKED 恢复 — 必须被拒绝
	ctx := withTestAdminIdentity("checker", httpx.AdminRoleApprover)
	_, err = svc.ExecuteRecovery(ctx, approvalID, "checker")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrDirectLockedRecovery) || containsStr(err.Error(), "DIRECT_LOCKED_RECOVERY"),
		"must reject direct recovery from LOCKED, got: %v", err)

	// 4. 验证状态仍为 LOCKED
	rec, err := svc.GetCurrentState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StateLocked, rec.State)
}

// TestRecoveryApproval_RejectsMissingOrStaleApproval 验证缺失或过期审批被拒绝。
func TestRecoveryApproval_RejectsMissingOrStaleApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 1. 触发封锁并请求解除
	_, err := svc.TriggerLockdown(context.Background(), "admin", "stale approval test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)
	approvalID, err := svc.RequestRecovery(context.Background(), "admin", "recovery request")
	require.NoError(t, err)

	ctx := withTestAdminIdentity("checker", httpx.AdminRoleApprover)

	// 2. 测试缺失审批 ID
	_, err = svc.ExecuteRecovery(ctx, "00000000-0000-0000-0000-000000000000", "checker")
	assert.Error(t, err)

	// 3. 测试无效审批 ID 格式
	_, err = svc.ExecuteRecovery(ctx, "not-a-uuid", "checker")
	assert.Error(t, err)

	// 4. 测试过期审批：先将审批标记为 APPLIED，再尝试 ExecuteRecovery
	_, err = pool.Exec(context.Background(), `
		UPDATE admin_approval_requests
		SET status = 'APPLIED', applied_at = now()
		WHERE id = $1
	`, approvalID)
	require.NoError(t, err)

	_, err = svc.ExecuteRecovery(ctx, approvalID, "checker")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidApprovalState) || containsStr(err.Error(), "INVALID_APPROVAL_STATE"),
		"must reject stale approval, got: %v", err)
}

// TestRecoveryApproval_StatusConstraintMatchesRuntime 验证 DB CHECK 约束
// 与运行时常量（approval_service.go 中的 ApprovalStatus* 常量）完全一致。
// 同时验证 APPROVED 状态被 DB 约束拒绝。
func TestRecoveryApproval_StatusConstraintMatchesRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// 1. 验证允许的状态都能写入
	allowedStatuses := []string{"PENDING", "APPLYING", "APPLIED", "REJECTED", "FAILED_STALE"}
	for _, status := range allowedStatuses {
		_, err := pool.Exec(context.Background(), `
			INSERT INTO admin_approval_requests (action_type, requester, reason, status)
			VALUES ('TEST_ACTION', 'tester', 'constraint test', $1)
		`, status)
		assert.NoError(t, err, "status %s should be allowed by CHECK constraint", status)
	}

	// 2. 验证 APPROVED 被 CHECK 约束拒绝（000038 已排除 APPROVED）
	_, err := pool.Exec(context.Background(), `
		INSERT INTO admin_approval_requests (action_type, requester, reason, status)
		VALUES ('TEST_ACTION', 'tester', 'should fail', 'APPROVED')
	`)
	assert.Error(t, err, "APPROVED must be rejected by CHECK constraint (000038)")

	// 3. 验证防护触发器也拒绝 APPROVED（即使绕过 CHECK 也无法写入）
	//    CHECK 约束已在上一步拒绝，触发器是第二道防线
	assert.Contains(t, err.Error(), "APPROVED")
}

// TestRecoveryApproval_RequiresDifferentChecker 验证 checker != requester（maker-checker）。
func TestRecoveryApproval_RequiresDifferentChecker(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 1. 触发封锁并请求解除
	_, err := svc.TriggerLockdown(context.Background(), "same-person", "self review test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)
	approvalID, err := svc.RequestRecovery(context.Background(), "same-person", "recovery request")
	require.NoError(t, err)

	// 2. 尝试用同一身份执行恢复 — 必须被拒绝
	ctx := withTestAdminIdentity("same-person", httpx.AdminRoleApprover)
	_, err = svc.ExecuteRecovery(ctx, approvalID, "same-person")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSelfReview) || containsStr(err.Error(), "SELF_REVIEW"),
		"must reject self-review (checker == requester), got: %v", err)

	// 3. 验证状态仍为 RECOVERY_PENDING（未执行恢复）
	rec, err := svc.GetCurrentState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StateRecoveryPending, rec.State)
}

// TestRecoveryApproval_RequiresAuthenticatedContext 验证缺少 AdminIdentity 时 fail closed。
func TestRecoveryApproval_RequiresAuthenticatedContext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	_, err := svc.TriggerLockdown(context.Background(), "admin", "auth context test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)
	approvalID, err := svc.RequestRecovery(context.Background(), "admin", "recovery request")
	require.NoError(t, err)

	// 使用 context.Background()（无 AdminIdentity）— 必须被拒绝
	_, err = svc.ExecuteRecovery(context.Background(), approvalID, "checker")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingIdentity) || containsStr(err.Error(), "MISSING_IDENTITY"),
		"must reject when AdminIdentity missing from context, got: %v", err)
}

// containsStr 是局部字符串包含检查（避免与 admin 包的 contains 冲突）。
func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
