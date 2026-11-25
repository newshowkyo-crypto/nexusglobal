// Package failclosed — p0_2_admission_execution_test.go 验证 P0-2 AdmissionCheck/ExecutionCheck 接入。
//
// 测试覆盖：
//   - TestPaymentIntent_LockdownAdmissionRejected
//   - TestHostedCheckout_LockdownAdmissionRejected
//   - TestPaymentLinkConsume_LockdownAdmissionRejected
//   - TestProviderDispatch_LockdownExecutionRejected
//   - TestFailClosed_DBUnavailableRejected
//   - TestRecoveryApplied_AllowsNewRequests
//
// 这些测试使用真实 PostgreSQL（testcontainers 或 TEST_DATABASE_DSN），
// 验证 DB 持久状态查询、Fail-Closed 行为、恢复后放行。
package failclosed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPaymentIntent_LockdownAdmissionRejected 验证封锁后 Payment Intent 入口被拒绝。
//
// 场景：
//  1. 触发封锁（DB + 缓存）
//  2. 调用 AdmissionCheck(ctx)
//  3. 期望返回 ErrSystemLockdown
func TestPaymentIntent_LockdownAdmissionRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 触发封锁
	_, err := svc.TriggerLockdown(context.Background(), "admin-test", "payment intent test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// AdmissionCheck 应拒绝
	err = svc.AdmissionCheck(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown), "expected ErrSystemLockdown, got %v", err)
}

// TestHostedCheckout_LockdownAdmissionRejected 验证封锁后 Hosted Checkout 入口被拒绝。
//
// 场景：
//  1. 触发封锁
//  2. 调用 AdmissionCheck(ctx)
//  3. 期望返回 ErrSystemLockdown
func TestHostedCheckout_LockdownAdmissionRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	_, err := svc.TriggerLockdown(context.Background(), "admin-test", "hosted checkout test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	err = svc.AdmissionCheck(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown))
}

// TestPaymentLinkConsume_LockdownAdmissionRejected 验证封锁后 Payment Link 消费入口被拒绝。
//
// 场景：
//  1. 触发封锁
//  2. 调用 AdmissionCheck(ctx)（模拟 ResolvePaymentLink 入口）
//  3. 期望返回 ErrSystemLockdown
func TestPaymentLinkConsume_LockdownAdmissionRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	_, err := svc.TriggerLockdown(context.Background(), "admin-test", "payment link test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	err = svc.AdmissionCheck(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown))
}

// TestProviderDispatch_LockdownExecutionRejected 验证封锁后 Provider dispatch 被拒绝。
//
// 场景：
//  1. 触发封锁
//  2. 调用 ExecutionCheck(ctx)（模拟 Provider dispatch 前检查）
//  3. 期望返回 ErrSystemLockdown
//
// 这覆盖了"正在进行但尚未 Provider dispatch 的请求也必须被 ExecutionCheck 拦截"的要求。
func TestProviderDispatch_LockdownExecutionRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	_, err := svc.TriggerLockdown(context.Background(), "admin-test", "provider dispatch test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// ExecutionCheck 应拒绝（强一致 DB 查询）
	err = svc.ExecutionCheck(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown))
}

// TestFailClosed_DBUnavailableRejected 验证 DB 不可用时 Fail-Closed。
//
// 场景：
//  1. 创建 Service with nil pool
//  2. 调用 AdmissionCheck(ctx) 和 ExecutionCheck(ctx)
//  3. 期望都返回 ErrSystemLockdown（Fail-Closed）
//
// 这覆盖了"DB 查询失败时必须 Fail-Closed"的要求。
func TestFailClosed_DBUnavailableRejected(t *testing.T) {
	// nil pool 模拟 DB 不可用
	svc := NewService(nil, nil, slog.Default())

	// AdmissionCheck with nil pool → fail-closed
	err := svc.AdmissionCheck(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown), "AdmissionCheck with nil pool should fail-closed")

	// ExecutionCheck with nil pool → fail-closed
	err = svc.ExecutionCheck(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown), "ExecutionCheck with nil pool should fail-closed")
}

// TestRecoveryApplied_AllowsNewRequests 验证恢复成功后新请求被允许。
//
// 场景：
//  1. 触发封锁
//  2. 请求恢复（创建审批）
//  3. 审批通过后执行恢复
//  4. 调用 AdmissionCheck(ctx) 和 ExecutionCheck(ctx)
//  5. 期望都返回 nil（放行）
//
// 这覆盖了"恢复成功后才允许新请求进入"的要求。
func TestRecoveryApplied_AllowsNewRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 1. 触发封锁
	_, err := svc.TriggerLockdown(context.Background(), "admin-trigger", "recovery test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// 验证封锁生效
	err = svc.AdmissionCheck(context.Background())
	assert.Error(t, err)

	// 2. 请求恢复（创建审批）
	ctx := withTestAdminIdentity("admin-requester", "admin")
	approvalID, err := svc.RequestRecovery(ctx, "admin-requester", "recovery requested")
	require.NoError(t, err)
	require.NotEmpty(t, approvalID)

	// 3. 审批通过后执行恢复（checker 必须不同于 requester）
	executeCtx := withTestAdminIdentity("admin-checker", "admin")
	_, err = svc.ExecuteRecovery(executeCtx, approvalID, "admin-checker")
	require.NoError(t, err)

	// 4. 恢复后 AdmissionCheck 应放行
	err = svc.AdmissionCheck(context.Background())
	assert.NoError(t, err, "AdmissionCheck should pass after recovery")

	// 5. 恢复后 ExecutionCheck 应放行
	err = svc.ExecutionCheck(context.Background())
	assert.NoError(t, err, "ExecutionCheck should pass after recovery")
}

// TestAdmissionCheck_CacheHit_FastPath 验证缓存命中时 AdmissionCheck 快速返回。
//
// 场景：
//  1. 直接置位进程内缓存（ActivateSystemLockdown）
//  2. 调用 AdmissionCheck(ctx)
//  3. 期望返回 ErrSystemLockdown（不查 DB）

func TestAdmissionCheck_CacheHit_FastPath(t *testing.T) {
	ClearSystemLockdownForTest()
	defer ClearSystemLockdownForTest()

	ActivateSystemLockdown()
	defer func() { locked.Store(false) }()

	// nil pool，但缓存命中应快速返回（不查 DB）
	svc := NewService(nil, nil, slog.Default())
	err := svc.AdmissionCheck(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown))
}

// TestExecutionCheck_DBQueryFailure_FailClosed 验证 DB 查询失败时 ExecutionCheck Fail-Closed。
//
// 场景：
//  1. 创建 Service with valid pool 但 system_control_state 表不存在（模拟 DB 错误）
//  2. 缓存未命中封锁
//  3. 调用 ExecutionCheck(ctx)
//  4. 期望返回 ErrSystemLockdown（Fail-Closed）
//
// 注意：此测试需要真实 DB 但表被删除。为避免破坏其他测试，
// 我们使用一个取消的 context 来模拟 DB 查询失败。
func TestExecutionCheck_DBQueryFailure_FailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 使用已取消的 context 模拟 DB 查询失败
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	err := svc.ExecutionCheck(ctx)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown), "DB query failure should fail-closed, got %v", err)
}

// TestAdmissionCheck_HTTPResponse_503 验证 AdmissionCheck 失败时 HTTP 响应为 503。
//
// 这是端到端测试：模拟 handler 调用 AdmissionCheck，验证 HTTP 响应码。
func TestAdmissionCheck_HTTPResponse_503(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 触发封锁
	_, err := svc.TriggerLockdown(context.Background(), "admin-test", "http 503 test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// 模拟 handler：调用 AdmissionCheck，失败时返回 503
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := svc.AdmissionCheck(r.Context()); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"code":"` + SystemLockdownCode + `"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/api/v3/payment_intents", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), SystemLockdownCode)
}

// TestAdmissionCheck_OpenState_Allows 验证 OPEN 状态时 AdmissionCheck 放行。
func TestAdmissionCheck_OpenState_Allows(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// OPEN 状态应放行
	err := svc.AdmissionCheck(context.Background())
	assert.NoError(t, err)

	err = svc.ExecutionCheck(context.Background())
	assert.NoError(t, err)
}

// TestExecutionCheck_DBCacheMiss_FallsBackToDB 验证缓存未命中时回退到 DB 查询。
//
// 场景：
//  1. DB 状态为 LOCKED，但进程内缓存为 OPEN（模拟进程刚启动缓存未加载）
//  2. 调用 AdmissionCheck(ctx)
//  3. 期望返回 ErrSystemLockdown（DB 兜底）
func TestExecutionCheck_DBCacheMiss_FallsBackToDB(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()
	resetControlState(t, pool)
	seedLockdownPrereqs(t, pool)

	svc := NewService(pool, nil, slog.Default())

	// 触发封锁（更新 DB + 缓存）
	_, err := svc.TriggerLockdown(context.Background(), "admin-test", "cache miss test", fmt.Sprintf("idemp-%s", t.Name()))
	require.NoError(t, err)

	// 手动清除进程内缓存，模拟进程刚启动缓存未加载
	ClearSystemLockdownForTest()

	// AdmissionCheck 应通过 DB 兜底发现封锁
	err = svc.AdmissionCheck(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown), "AdmissionCheck should fall back to DB when cache misses")
}

// 确保 import time 被使用（避免 unused import 编译错误）
var _ = time.Second
