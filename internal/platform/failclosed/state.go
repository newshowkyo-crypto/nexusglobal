// Package failclosed — state.go 维护进程内 Kill Switch 快速缓存。
//
// 设计：
//   - locked atomic.Bool 是进程内快速缓存，仅供 AdmissionCheck 使用
//   - currentEpoch atomic.Int64 跟踪已应用的最新 epoch，防止旧事件回放
//   - DB system_control_state 是真相（RED-14）
//   - 启动时由 Service.LoadFromDB 加载 DB 状态到本缓存
//   - 跨节点更新由 Subscriber 接收 pub/sub 并调用 UpdateLocalCache
//
// 检查策略：
//   - AdmissionCheck（入口检查）：仅查 atomic 缓存，O(1)，无 I/O
//   - ExecutionCheck（执行时检查）：先查缓存，再查 DB 强一致
//   - 支付在入口和真正 external dispatch 前各检查一次
//
// Allowlist（封锁后仍允许的动作）：
//   - 查询（GET）
//   - webhook 接收（异步入账）
//   - 对账（reconcile）
//   - 已授权撤销/退款（风险收敛动作）
//   - 退款（refund）
package failclosed

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SystemLockdownCode 是封锁错误的稳定代码字符串。
const SystemLockdownCode = "SYSTEM_LOCKDOWN_FAIL_CLOSED"

// ErrSystemLockdown 是入口/执行检查失败时返回的错误。
var ErrSystemLockdown = errors.New(SystemLockdownCode)

// 进程内快速缓存。DB 是真相，这里仅作为热路径优化。
var (
	locked       atomic.Bool
	currentEpoch atomic.Int64
)

// ActivateSystemLockdown 直接置位进程内封锁缓存。
//
// 仅用于：
//   - Service.TriggerLockdown 事务提交后
//   - 测试辅助
//   - 兼容旧调用方（killswitch.go 旧路径）
//
// 新代码应使用 UpdateLocalCache 或 Service.TriggerLockdown。
func ActivateSystemLockdown() {
	locked.Store(true)
}

// ClearSystemLockdownForTest 重置进程内缓存到 OPEN/epoch=0。
// 仅用于测试。
func ClearSystemLockdownForTest() {
	locked.Store(false)
	currentEpoch.Store(0)
}

// IsSystemLockedDown 返回进程内缓存是否处于封锁态。
// 这是 AdmissionCheck 的核心：O(1)，无 I/O。
//
// 注意：此函数仅反映本进程已知的最新状态，可能滞后于 DB。
// 强一致检查请使用 ExecutionCheck。
func IsSystemLockedDown() bool {
	return locked.Load()
}

// CurrentEpoch 返回进程内已应用的最新 epoch。
func CurrentEpoch() int64 {
	return currentEpoch.Load()
}

// UpdateLocalCache 用新状态/epoch 更新进程内缓存。
//
// 关键约束：只接受更高 epoch（拒绝旧 epoch 回放或乱序）。
// 返回 true 表示已应用，false 表示已拒绝（旧 epoch）。
//
// 此函数是线程安全的，由以下路径调用：
//   - Service.LoadFromDB（启动加载）
//   - Service.TriggerLockdown/ExecuteRecovery（本节点触发后）
//   - Subscriber.handleMessage（远端节点事件）
func UpdateLocalCache(state State, epoch int64) bool {
	for {
		localEpoch := currentEpoch.Load()
		if epoch < localEpoch {
			return false
		}
		if epoch == localEpoch {
			// 同 epoch：仅在状态从封锁→开放或反向时更新
			// 这里采用保守策略：同 epoch 不更新（epoch 应严格单调）
			return false
		}
		// CAS 尝试
		if currentEpoch.CompareAndSwap(localEpoch, epoch) {
			locked.Store(state.IsLocked())
			return true
		}
		// CAS 失败，重试
	}
}

// applyRecordToCache 是 Service 内部使用的便捷函数。
func applyRecordToCache(rec *ControlRecord) {
	if rec == nil {
		return
	}
	UpdateLocalCache(rec.State, rec.Epoch)
}

// AdmissionCheck 是金融入口的轻量级封锁检查。
// 仅查 atomic 缓存，O(1)，无 I/O。
//
// 用于：
//   - 支付入口（payment intent create、checkout charge）
//   - 出金入口（payout request）
//   - 发卡入口
//
// 若缓存命中封锁，返回 ErrSystemLockdown。
// 若缓存未命中，仍可能被 ExecutionCheck 在 dispatch 前拦截。
func AdmissionCheck() error {
	if locked.Load() {
		return ErrSystemLockdown
	}
	return nil
}

// ExecutionCheck 是真正 external dispatch 前的强一致封锁检查。
//
// 先查 atomic 缓存（快速路径），若未命中再查 DB（强一致）。
// DB 查询失败时 fail closed（返回 ErrSystemLockdown），因为：
//   - RED-14: DB 是真相，无法确认真相时不能放行资金
//   - Redis 断连不能作为放行理由
//
// 用于：
//   - acquirer gateway Charge 前
//   - payout submit 前
//   - 任何调用外部 LP/Bank/MAE 前
func ExecutionCheck(ctx context.Context, pool *pgxpool.Pool) error {
	// 快速路径：缓存命中封锁
	if locked.Load() {
		return ErrSystemLockdown
	}
	if pool == nil {
		// 无 DB 池时 fail closed
		return ErrSystemLockdown
	}
	// 强一致路径：查 DB
	var state string
	err := pool.QueryRow(ctx, `
		SELECT state::text FROM system_control_state WHERE id = 1
	`).Scan(&state)
	if err != nil {
		// DB 错误：fail closed
		return ErrSystemLockdown
	}
	if State(state).IsLocked() {
		// DB 显示封锁，更新本地缓存
		UpdateLocalCache(State(state), CurrentEpoch()+1)
		return ErrSystemLockdown
	}
	return nil
}

// ActionKind 标识金融动作的类型，用于 Allowlist 判定。
type ActionKind string

const (
	// ActionNewAuthorization 新授权（封锁后禁止）
	ActionNewAuthorization ActionKind = "new_authorization"
	// ActionNewCapture 新捕获（封锁后禁止）
	ActionNewCapture ActionKind = "new_capture"
	// ActionNewIssuance 新发卡（封锁后禁止）
	ActionNewIssuance ActionKind = "new_issuance"
	// ActionNewPayout 新出金（封锁后禁止）
	ActionNewPayout ActionKind = "new_payout"
	// ActionQuery 查询（封锁后允许）
	ActionQuery ActionKind = "query"
	// ActionWebhookReceive webhook 接收（封锁后允许，异步入账）
	ActionWebhookReceive ActionKind = "webhook_receive"
	// ActionReconcile 对账（封锁后允许，风险收敛）
	ActionReconcile ActionKind = "reconcile"
	// ActionRefund 退款（封锁后允许，已授权的风险收敛动作）
	ActionRefund ActionKind = "refund"
	// ActionVoidRefund 已授权撤销/退款（封锁后允许）
	ActionVoidRefund ActionKind = "void_or_refund_authorized"
)

// IsAllowedDuringLockdown 返回该动作是否在封锁期间仍允许。
//
// Allowlist（明确允许）：
//   - 查询、webhook 接收、对账、退款、已授权撤销/退款
//
// Blocklist（明确禁止）：
//   - 新授权、新捕获、新发卡、新出金
func IsAllowedDuringLockdown(kind ActionKind) bool {
	switch kind {
	case ActionQuery,
		ActionWebhookReceive,
		ActionReconcile,
		ActionRefund,
		ActionVoidRefund:
		return true
	default:
		return false
	}
}

// AdmissionCheckForAction 根据动作类型决定是否检查封锁。
//
// 若动作在 Allowlist 内，跳过封锁检查（返回 nil）。
// 否则执行 AdmissionCheck。
func AdmissionCheckForAction(kind ActionKind) error {
	if IsAllowedDuringLockdown(kind) {
		return nil
	}
	return AdmissionCheck()
}

// ExecutionCheckForAction 根据动作类型决定是否执行强一致检查。
//
// 若动作在 Allowlist 内，跳过。
// 否则执行 ExecutionCheck。
func ExecutionCheckForAction(ctx context.Context, pool *pgxpool.Pool, kind ActionKind) error {
	if IsAllowedDuringLockdown(kind) {
		return nil
	}
	return ExecutionCheck(ctx, pool)
}

// ── Service 方法版本（P0-2）──────────────────────────────────────────────
//
// AdmissionCheck(ctx) 与 ExecutionCheck(ctx) 是 Service 的方法版本，
// 使用 Service 持有的 pool 进行 DB 查询。
//
// 设计原则：
//   - AdmissionCheck：先查进程内 atomic 缓存（O(1)），命中封锁直接拒绝；
//     未命中时再查 DB（强一致），DB 错误 fail-closed。
//     这是为了防止进程刚启动但缓存尚未加载时放行资金。
//   - ExecutionCheck：在真正 external dispatch 前强一致检查 DB。
//     DB 错误 fail-closed（RED-14: DB 是真相）。
//
// 与包级函数 AdmissionCheck()/ExecutionCheck(ctx, pool) 的区别：
//   - 包级函数 AdmissionCheck() 仅查缓存，不查 DB（性能优先）
//   - 方法版本 AdmissionCheck(ctx) 在缓存未命中时回退到 DB（安全优先）
//
// 选用哪个？
//   - HTTP 入口中间件、热路径：使用包级 AdmissionCheck()（O(1)）
//   - 业务关键路径（创建支付意图、checkout session）：使用方法版本（DB 兜底）
//   - 真正 external dispatch 前：使用 ExecutionCheck(ctx)（强一致）

// admissionCheckMethod 是 Service.AdmissionCheck 的内部实现，便于测试。
// 逻辑：
//  1. 缓存命中封锁 → 返回 ErrSystemLockdown
//  2. pool == nil → fail-closed（无法确认 DB 真相）
//  3. 查 DB system_control_state
//  4. DB 查询错误 → fail-closed
//  5. DB 显示封锁 → 更新本地缓存，返回 ErrSystemLockdown
//  6. DB 显示 OPEN → 返回 nil
func admissionCheckMethod(ctx context.Context, pool *pgxpool.Pool) error {
	// 快速路径：缓存命中封锁
	if locked.Load() {
		return ErrSystemLockdown
	}
	if pool == nil {
		// 无 DB 池时 fail closed（无法确认真相）
		return ErrSystemLockdown
	}
	// 回退路径：查 DB（防止进程刚启动缓存未加载）
	var state string
	err := pool.QueryRow(ctx, `
		SELECT state::text FROM system_control_state WHERE id = 1
	`).Scan(&state)
	if err != nil {
		// DB 错误：fail closed
		return ErrSystemLockdown
	}
	if State(state).IsLocked() {
		// DB 显示封锁，更新本地缓存
		UpdateLocalCache(State(state), CurrentEpoch()+1)
		return ErrSystemLockdown
	}
	return nil
}

// executionCheckMethod 是 Service.ExecutionCheck 的内部实现，便于测试。
// 逻辑与 ExecutionCheck(ctx, pool) 相同，强一致 DB 查询。
func executionCheckMethod(ctx context.Context, pool *pgxpool.Pool) error {
	return ExecutionCheck(ctx, pool)
}
