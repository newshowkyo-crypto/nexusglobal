// Package failclosed — recovery_pending_test.go 验证 RECOVERY_PENDING 状态的封锁行为。
//
// 这些是纯单元测试，不使用 testcontainers，仅测试进程内逻辑：
//   - TestStateRecoveryPending_IsLocked
//   - TestAdmissionCheck_RecoveryPendingRejected
//   - TestExecutionCheck_RecoveryPendingRejected
//   - TestRequestRecovery_DoesNotOpenLocalCache
package failclosed

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStateRecoveryPending_IsLocked 验证 RECOVERY_PENDING 状态被 IsLocked 视为封锁态。
func TestStateRecoveryPending_IsLocked(t *testing.T) {
	assert.True(t, StateRecoveryPending.IsLocked(),
		"RECOVERY_PENDING must be treated as locked state")

	// 确保其他封锁态仍然正确
	assert.True(t, StateLockdownPending.IsLocked(), "LOCKDOWN_PENDING must be locked")
	assert.True(t, StateLocked.IsLocked(), "LOCKED must be locked")

	// 确保非封锁态不被误判
	assert.False(t, StateOpen.IsLocked(), "OPEN must not be locked")
	assert.False(t, StateDegraded.IsLocked(), "DEGRADED must not be locked")
}

// TestAdmissionCheck_RecoveryPendingRejected 验证缓存为 RECOVERY_PENDING 时 AdmissionCheck 拒绝。
func TestAdmissionCheck_RecoveryPendingRejected(t *testing.T) {
	ClearSystemLockdownForTest()
	defer ClearSystemLockdownForTest()

	// 模拟 RequestRecovery 后的缓存状态：locked=true（因为 RECOVERY_PENDING.IsLocked()=true）
	locked.Store(true)
	defer locked.Store(false)

	err := AdmissionCheck()
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown),
		"AdmissionCheck must reject when cache reflects RECOVERY_PENDING (locked=true)")
}

// TestExecutionCheck_RecoveryPendingRejected 验证缓存为 RECOVERY_PENDING 时 ExecutionCheck 拒绝。
// 使用 nil pool 模拟：缓存命中封锁时快速返回，不查 DB。
func TestExecutionCheck_RecoveryPendingRejected(t *testing.T) {
	ClearSystemLockdownForTest()
	defer ClearSystemLockdownForTest()

	// 模拟 RequestRecovery 后的缓存状态
	locked.Store(true)
	defer locked.Store(false)

	// nil pool：缓存命中封锁应快速返回 ErrSystemLockdown，不查 DB
	err := ExecutionCheck(context.Background(), nil)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdown),
		"ExecutionCheck must reject when cache reflects RECOVERY_PENDING (locked=true)")
}

// TestRequestRecovery_DoesNotOpenLocalCache 验证 RequestRecovery 后本地缓存保持 locked=true。
//
// 场景：
//  1. 缓存处于 LOCKED 状态（locked=true）
//  2. 调用 UpdateLocalCache(StateRecoveryPending, epoch+1) 模拟 RequestRecovery 的缓存更新
//  3. 验证 locked 仍为 true（RECOVERY_PENDING 不解除缓存）
func TestRequestRecovery_DoesNotOpenLocalCache(t *testing.T) {
	ClearSystemLockdownForTest()
	defer ClearSystemLockdownForTest()

	// 1. 模拟 LOCKED 状态：locked=true, epoch=1
	locked.Store(true)
	currentEpoch.Store(1)

	// 2. 模拟 RequestRecovery 的缓存更新：RECOVERY_PENDING, epoch=2
	applied := UpdateLocalCache(StateRecoveryPending, 2)
	assert.True(t, applied, "UpdateLocalCache should accept higher epoch")

	// 3. 验证 locked 仍为 true — RECOVERY_PENDING 不解除封锁
	assert.True(t, locked.Load(),
		"locked must remain true after RECOVERY_PENDING update (RequestRecovery must not open local cache)")

	// 4. 验证 IsSystemLockedDown 也返回 true
	assert.True(t, IsSystemLockedDown(),
		"IsSystemLockedDown must return true when state is RECOVERY_PENDING")
}

// 确保 atomic 包被使用（避免 unused import）
var _ = atomic.Bool{}
