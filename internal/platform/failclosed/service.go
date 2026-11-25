// Package failclosed 实现持久化、全节点可恢复的 Kill Switch (G07)。
//
// 设计目标：
//   - DB system_control_state 表是唯一真相（RED-14: Redis 不能作为资金安全边界）
//   - epoch 单调递增，用于缓存失效与跨节点事件排序
//   - 状态机: OPEN → LOCKDOWN_PENDING → LOCKED → RECOVERY_PENDING → OPEN
//   - 触发封锁在单一 DB 事务内完成：锁 control row、更新状态/epoch、禁用通道、
//     隔离组置 RISK_HOLD、写审计、写 outbox，然后 commit
//   - Redis publish 由 outbox worker 在事务外完成（RED-01: 事务内禁止外部网络 I/O）
//   - 恢复必须走审批（maker-checker）且 epoch 单调
//   - 进程内 atomic.Bool 仅作为快速缓存，DB 是真相
//
// 多节点同步：
//   - 启动时从 DB 加载状态到进程内缓存
//   - 订阅 Redis pub/sub 事件，只接受更高 epoch（拒绝旧 epoch 回放）
//   - Redis 断连期间金融入口通过 DB admission barrier fail closed
package failclosed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// State 表示 system_control_state.state 的状态机值。
type State string

const (
	// StateOpen 正常运行
	StateOpen State = "OPEN"
	// StateLockdownPending 已触发封锁，事务进行中（瞬态）
	StateLockdownPending State = "LOCKDOWN_PENDING"
	// StateLocked 全局封锁生效
	StateLocked State = "LOCKED"
	// StateRecoveryPending 解除请求已创建，待审批
	StateRecoveryPending State = "RECOVERY_PENDING"
	// StateDegraded 降级（保留）
	StateDegraded State = "DEGRADED"
)

// IsLocked 判断该状态是否应当阻断新金融动作。
// LOCKDOWN_PENDING、LOCKED、RECOVERY_PENDING 都视为封锁态（fail-closed）。
// RECOVERY_PENDING 表示解除请求已创建但尚未审批通过，仍需封锁。
// 只有 ExecuteRecovery 事务成功提交 RECOVERY_PENDING → OPEN 后才解除。
func (s State) IsLocked() bool {
	return s == StateLockdownPending || s == StateLocked || s == StateRecoveryPending
}

// ControlRecord 是 system_control_state 表的内存投影。
type ControlRecord struct {
	State       State
	Epoch       int64
	Operator    string
	Reason      string
	ActivatedAt *time.Time
	ReleasedAt  *time.Time
	UpdatedAt   time.Time
}

// IsLocked 是 ControlRecord 的便捷方法。
func (r *ControlRecord) IsLocked() bool {
	if r == nil {
		return false
	}
	return r.State.IsLocked()
}

// LockdownResult 是 TriggerLockdown 的返回值，包含受影响行数与最终状态。
type LockdownResult struct {
	Record           *ControlRecord
	DisabledChannels int64
	RiskHoldGroups   int64
}

// RecoveryResult 是 ExecuteRecovery 的返回值。
type RecoveryResult struct {
	Record     *ControlRecord
	ApprovalID string
	Checker    string
}

// PubSubChannels 是 Kill Switch 事件使用的 Redis 频道。
const (
	ChannelLockdownEvent  = "system.fail_closed.lockdown"
	ChannelRecoveryEvent  = "system.fail_closed.recovery"
	ChannelConfigReload   = "config:acquirer_channel:reload"
	ChannelIssuingReload  = "config:issuing_pool:reload"
	ChannelChannelUpdated = "channel.config.updated"
)

// OutboxEventType 是写入 transactional_outbox 的事件类型，由 outbox worker 消费并 publish 到 Redis。
const (
	OutboxEventLockdown = "SYSTEM_LOCKDOWN_PUBLISH"
	OutboxEventRecovery = "SYSTEM_RECOVERY_PUBLISH"
)

// ApprovalActionType 是写入 admin_approval_requests 的 action_type。
// P0-1: 与 admin.ActionSystemLockdownRecovery 保持一致，统一为 SYSTEM_LOCKDOWN_RECOVERY。
const ApprovalActionTypeSystemRecovery = "SYSTEM_LOCKDOWN_RECOVERY"

// ErrIllegalTransition 表示状态机非法转移。
var ErrIllegalTransition = errors.New("ILLEGAL_STATE_TRANSITION: system control state cannot transition")

// ErrStaleEpoch 表示收到的事件 epoch 不大于当前 epoch，应拒绝。
var ErrStaleEpoch = errors.New("STALE_EPOCH: event epoch is not greater than current epoch")

// ErrApprovalRequired 表示解除大闸需要审批。
var ErrApprovalRequired = errors.New("APPROVAL_REQUIRED: system recovery requires maker-checker approval")

// ErrSelfReview 表示审批人不能与申请人相同（maker-checker）。
var ErrSelfReview = errors.New("SELF_REVIEW_REJECTED: checker cannot be the same as requester")

// ErrInvalidApprovalState 表示审批记录状态不在允许的转移源状态内。
var ErrInvalidApprovalState = errors.New("INVALID_APPROVAL_STATE: approval is not in the expected state")

// ErrMissingIdentity 表示 Context 中缺少已验证的 AdminIdentity（RED-15）。
var ErrMissingIdentity = errors.New("MISSING_IDENTITY: checker identity not found in authenticated context")

// ErrDirectLockedRecovery 表示禁止从 LOCKED 直接恢复，必须先走 RECOVERY_PENDING。
var ErrDirectLockedRecovery = errors.New("DIRECT_LOCKED_RECOVERY_PROHIBITED: must request recovery first to transition LOCKED → RECOVERY_PENDING")

// Service 提供持久化、多节点同步的 Kill Switch 操作。
//
// 关键约束：
//   - 所有写操作在单一 DB 事务内完成（RED-01）
//   - Redis publish 由 outbox worker 在事务外完成
//   - epoch 单调递增（由 DB 触发器强制）
//   - 身份从已验证 Context 获取，调用方负责传入正确的 operator
type Service struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	log  *slog.Logger
}

// NewService 创建持久化 Kill Switch 服务。
// pool 必须非 nil；rdb 可为 nil（仅失去 pub/sub 能力，DB 仍是真相）。
func NewService(pool *pgxpool.Pool, rdb *redis.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, rdb: rdb, log: log}
}

// Pool 返回底层连接池（供 main.go 启动加载使用）。
func (s *Service) Pool() *pgxpool.Pool { return s.pool }

// LoadFromDB 从 DB 加载当前 system_control_state 到进程内缓存。
// 启动时调用一次，确保进程内 atomic 反映 DB 真相。
//
// 若 DB 表不存在或查询失败，返回错误，调用方应 fail-closed（拒绝启动或拒绝金融流量）。
func (s *Service) LoadFromDB(ctx context.Context) (*ControlRecord, error) {
	rec, err := s.GetCurrentState(ctx)
	if err != nil {
		return nil, fmt.Errorf("load system control state: %w", err)
	}
	applyRecordToCache(rec)
	s.log.Info("system control state loaded from DB",
		"state", rec.State,
		"epoch", rec.Epoch,
		"operator", rec.Operator,
	)
	return rec, nil
}

// GetCurrentState 从 DB 读取当前 system_control_state（强一致读）。
// 不更新进程内缓存。
func (s *Service) GetCurrentState(ctx context.Context) (*ControlRecord, error) {
	rec := &ControlRecord{}
	var activatedAt, releasedAt *time.Time
	// operator 列可空（初始行未有任何操作者），扫描需走 *string 否则启动即崩。
	var operator *string
	err := s.pool.QueryRow(ctx, `
		SELECT state::text, epoch, operator, reason, activated_at, released_at, updated_at
		FROM system_control_state WHERE id = 1
	`).Scan(&rec.State, &rec.Epoch, &operator, &rec.Reason, &activatedAt, &releasedAt, &rec.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("query system_control_state: %w", err)
	}
	if operator != nil {
		rec.Operator = *operator
	}
	rec.ActivatedAt = activatedAt
	rec.ReleasedAt = releasedAt
	return rec, nil
}

// TriggerLockdown 触发全局封锁。
//
// 事务内步骤（RED-01: 事务内禁止任何外部网络 I/O）：
//  1. SELECT FOR UPDATE control row（行锁，防止并发触发）
//  2. 校验状态机：OPEN → LOCKDOWN_PENDING → LOCKED
//  3. 更新 state/epoch/operator/reason/activated_at
//  4. 禁用所有 ACTIVE acquirer_channels（status='DISABLED'）
//  5. 将 pipeline_isolation_groups 置 RISK_HOLD
//  6. 写 admin_override_audit_log
//  7. 写 transactional_outbox（事件由 outbox worker 在事务外 publish 到 Redis）
//  8. commit
//
// 事务提交后，更新进程内 atomic 缓存。
// Redis publish 由 outbox worker 异步完成，失败可重试。
//
// operator 必须来自已验证的 AdminIdentity Context，禁止从请求体读取（RED-15）。
// idempotencyKey 用于幂等认领：同一 key 的并发请求仅执行一次，后续重放返回首次结果。
func (s *Service) TriggerLockdown(ctx context.Context, operator, reason, idempotencyKey string) (*LockdownResult, error) {
	if operator == "" {
		return nil, errors.New("operator is required (must come from authenticated context)")
	}
	if reason == "" {
		return nil, errors.New("reason is required")
	}
	if idempotencyKey == "" {
		return nil, errors.New("idempotency_key is required")
	}

	result := &LockdownResult{Record: &ControlRecord{}}

	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// 0. 幂等认领：INSERT...ON CONFLICT DO NOTHING
		//    RowsAffected()==0 表示该 key 已被其他请求认领，查询已有结果返回。
		idempTag, idempErr := tx.Exec(ctx, `
			INSERT INTO admin_lockdown_idempotency (idempotency_key, operator, reason, disabled_channels, risk_hold_groups, epoch, created_at)
			VALUES ($1, $2, $3, 0, 0, 0, now())
			ON CONFLICT (idempotency_key) DO NOTHING
		`, idempotencyKey, operator, reason)
		if idempErr != nil {
			return fmt.Errorf("idempotency claim: %w", idempErr)
		}
		if idempTag.RowsAffected() == 0 {
			// 幂等键已存在：读取已有结果并返回
			existing, readErr := readLockdownIdempotency(ctx, tx, idempotencyKey)
			if readErr != nil {
				return fmt.Errorf("read idempotency result: %w", readErr)
			}
			result.DisabledChannels = existing.DisabledChannels
			result.RiskHoldGroups = existing.RiskHoldGroups
			result.Record.State = StateLocked
			result.Record.Epoch = existing.Epoch
			result.Record.Operator = existing.Operator
			result.Record.Reason = existing.Reason
			return nil
		}

		// 1. 锁 control row
		var currentState State
		var currentEpoch int64
		err := tx.QueryRow(ctx, `
			SELECT state::text, epoch FROM system_control_state WHERE id = 1 FOR UPDATE
		`).Scan(&currentState, &currentEpoch)
		if err != nil {
			return fmt.Errorf("lock system_control_state: %w", err)
		}

		// 2. 校验状态机
		if currentState.IsLocked() || currentState == StateRecoveryPending {
			return fmt.Errorf("%w: current state %s cannot transition to LOCKED", ErrIllegalTransition, currentState)
		}
		if currentState != StateOpen && currentState != StateDegraded {
			return fmt.Errorf("%w: unexpected current state %s", ErrIllegalTransition, currentState)
		}

		newEpoch := currentEpoch + 1

		// 3. 更新 control row 到 LOCKED（单事务内直接到 LOCKED）
		//    LOCKDOWN_PENDING 用于跨事务的协调场景；单事务内直接到 LOCKED 即可。
		//    DB 触发器（000035）要求 state 变化时 epoch 必须递增，因此直接 OPEN → LOCKED，epoch+1。
		_, err = tx.Exec(ctx, `
			UPDATE system_control_state
			SET state = 'LOCKED', epoch = $1, operator = $2, reason = $3,
			    activated_at = now(), released_at = NULL, version = version + 1, updated_at = now()
			WHERE id = 1
		`, newEpoch, operator, reason)
		if err != nil {
			return fmt.Errorf("update to LOCKED: %w", err)
		}

		// 4. 禁用所有 ACTIVE 通道
		channelTag, err := tx.Exec(ctx, `
			UPDATE acquirer_channels
			SET status = 'DISABLED', version = version + 1, updated_at = now()
			WHERE status = 'ACTIVE'
		`)
		if err != nil {
			return fmt.Errorf("disable acquirer_channels: %w", err)
		}
		result.DisabledChannels = channelTag.RowsAffected()

		// 5. 隔离组置 RISK_HOLD
		groupTag, err := tx.Exec(ctx, `
			UPDATE pipeline_isolation_groups
			SET status = 'RISK_HOLD', frozen_reason = $1, frozen_by = $2,
			    frozen_at = now(), updated_at = now()
			WHERE status IN ('ACTIVE', 'FROZEN')
		`, reason, operator)
		if err != nil {
			return fmt.Errorf("risk-hold isolation groups: %w", err)
		}
		result.RiskHoldGroups = groupTag.RowsAffected()

		// 6. 写审计日志
		auditValue, _ := json.Marshal(map[string]interface{}{
			"disabled_channels": result.DisabledChannels,
			"risk_hold_groups":  result.RiskHoldGroups,
			"from_epoch":        currentEpoch,
			"to_epoch":          newEpoch,
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO admin_override_audit_log (
				entity_type, entity_id, override_type, reason, operator, new_value, created_at
			) VALUES (
				'SYSTEM', gen_random_uuid(), 'SYSTEM_LOCKDOWN', $1, $2, $3::jsonb, now()
			)
		`, reason, operator, string(auditValue))
		if err != nil {
			return fmt.Errorf("insert audit log: %w", err)
		}

		// 7. 写 outbox（Redis publish 由 outbox worker 在事务外完成）
		//    state 已在第 3 步直接更新为 LOCKED（epoch 递增满足触发器要求）
		outboxPayload, _ := json.Marshal(map[string]interface{}{
			"event":             "SYSTEM_LOCKDOWN",
			"operator":          operator,
			"reason":            reason,
			"epoch":             newEpoch,
			"disabled_channels": result.DisabledChannels,
			"risk_hold_groups":  result.RiskHoldGroups,
			"ts":                time.Now().UTC().Format(time.RFC3339Nano),
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO transactional_outbox (
				aggregate_type, aggregate_id, event_type, payload, idempotency_key, status
			) VALUES (
				'SYSTEM_CONTROL', '00000000-0000-0000-0000-000000000001', $1, $2::jsonb, $3, 'Pending'
			)
		`, OutboxEventLockdown, string(outboxPayload), fmt.Sprintf("lockdown-%d", newEpoch))
		if err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}

		// 8. 更新幂等记录的实际结果
		_, err = tx.Exec(ctx, `
			UPDATE admin_lockdown_idempotency
			SET disabled_channels = $1, risk_hold_groups = $2, epoch = $3
			WHERE idempotency_key = $4
		`, result.DisabledChannels, result.RiskHoldGroups, newEpoch, idempotencyKey)
		if err != nil {
			return fmt.Errorf("update idempotency result: %w", err)
		}

		// 填充返回值
		result.Record.State = StateLocked
		result.Record.Epoch = newEpoch
		result.Record.Operator = operator
		result.Record.Reason = reason
		now := time.Now()
		result.Record.ActivatedAt = &now
		result.Record.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 事务提交后更新进程内缓存
	applyRecordToCache(result.Record)
	s.log.Info("system lockdown triggered",
		"epoch", result.Record.Epoch,
		"operator", operator,
		"disabled_channels", result.DisabledChannels,
		"risk_hold_groups", result.RiskHoldGroups,
		"idempotency_key", idempotencyKey,
	)

	return result, nil
}

// lockdownIdempotencyResult 是从 admin_lockdown_idempotency 读取的幂等结果。
type lockdownIdempotencyResult struct {
	Operator         string
	Reason           string
	DisabledChannels int64
	RiskHoldGroups   int64
	Epoch            int64
}

// readLockdownIdempotency 从事务内读取已有幂等结果。
func readLockdownIdempotency(ctx context.Context, tx pgx.Tx, idempotencyKey string) (*lockdownIdempotencyResult, error) {
	var r lockdownIdempotencyResult
	err := tx.QueryRow(ctx, `
		SELECT operator, reason, disabled_channels, risk_hold_groups, epoch
		FROM admin_lockdown_idempotency
		WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&r.Operator, &r.Reason, &r.DisabledChannels, &r.RiskHoldGroups, &r.Epoch)
	if err != nil {
		return nil, fmt.Errorf("query admin_lockdown_idempotency: %w", err)
	}
	return &r, nil
}

// RequestRecovery 创建解除大闸的审批请求（maker-checker）。
//
// 不直接解除，而是写入 admin_approval_requests 表，等待 approver 审批。
// 状态机：LOCKED → RECOVERY_PENDING（标记意图，仍封锁）
//
// operator 必须来自已验证 Context。
func (s *Service) RequestRecovery(ctx context.Context, operator, reason string) (string, error) {
	if operator == "" {
		return "", errors.New("operator is required (must come from authenticated context)")
	}
	if reason == "" {
		return "", errors.New("reason is required")
	}

	var approvalID string
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// 锁 control row
		var currentState State
		var currentEpoch int64
		err := tx.QueryRow(ctx, `
			SELECT state::text, epoch FROM system_control_state WHERE id = 1 FOR UPDATE
		`).Scan(&currentState, &currentEpoch)
		if err != nil {
			return fmt.Errorf("lock system_control_state: %w", err)
		}

		if currentState != StateLocked {
			return fmt.Errorf("%w: recovery request requires state LOCKED, got %s", ErrIllegalTransition, currentState)
		}

		newEpoch := currentEpoch + 1

		// 推进到 RECOVERY_PENDING（仍封锁，等待审批）
		_, err = tx.Exec(ctx, `
			UPDATE system_control_state
			SET state = 'RECOVERY_PENDING', epoch = $1, reason = $2, operator = $3,
			    version = version + 1, updated_at = now()
			WHERE id = 1
		`, newEpoch, reason, operator)
		if err != nil {
			return fmt.Errorf("update to RECOVERY_PENDING: %w", err)
		}

		// 写审批请求
		payload, _ := json.Marshal(map[string]interface{}{
			"from_epoch": currentEpoch,
			"to_epoch":   newEpoch,
			"reason":     reason,
		})
		// ADM-007: 同步写入 validated_payload（ApplyTransaction 读取该列）
		err = tx.QueryRow(ctx, `
			INSERT INTO admin_approval_requests (action_type, requested_payload, validated_payload, requester, reason, status)
			VALUES ($1, $2::jsonb, $2::jsonb, $3, $4, 'PENDING')
			RETURNING id::text
		`, ApprovalActionTypeSystemRecovery, string(payload), operator, reason).Scan(&approvalID)
		if err != nil {
			return fmt.Errorf("insert approval request: %w", err)
		}

		// 写审计
		_, err = tx.Exec(ctx, `
			INSERT INTO admin_override_audit_log (
				entity_type, entity_id, override_type, reason, operator, new_value, created_at
			) VALUES (
				'SYSTEM', gen_random_uuid(), 'SYSTEM_RECOVERY_REQUESTED', $1, $2, $3::jsonb, now()
			)
		`, reason, operator, string(payload))
		if err != nil {
			return fmt.Errorf("insert audit log: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	// 更新进程内缓存为 RECOVERY_PENDING（仍封锁）
	UpdateLocalCache(StateRecoveryPending, parseEpochFromApprovalID(approvalID))
	// 重新从 DB 加载以获得准确 epoch
	if rec, err := s.GetCurrentState(ctx); err == nil {
		applyRecordToCache(rec)
	}

	s.log.Info("recovery requested, pending approval",
		"approval_id", approvalID,
		"operator", operator,
	)
	return approvalID, nil
}

// ExecuteRecovery 在审批通过后执行解除大闸。
//
// P0-1: 严格恢复执行条件 — 必须满足全部前置条件才允许执行：
//   - system_control_state.state == RECOVERY_PENDING
//   - approval.action_type == SYSTEM_LOCKDOWN_RECOVERY
//   - approval.status == PENDING
//   - checker != requester（maker-checker）
//   - checker 来自已验证 JWT Context（RED-15）
//
// 同一事务内完成（RED-01: 事务内禁止外部网络 I/O）：
//  1. 锁定审批记录 (SELECT FOR UPDATE)
//  2. 校验 action_type == SYSTEM_LOCKDOWN_RECOVERY 且 status == PENDING
//  3. 校验 checker != requester
//  4. PENDING → APPLYING（检查 RowsAffected()==1）
//  5. 锁定 system_control_state (SELECT FOR UPDATE)
//  6. 校验 state == RECOVERY_PENDING（禁止从 LOCKED 直接恢复）
//  7. RECOVERY_PENDING → OPEN（检查 RowsAffected()==1）
//  8. 写审计日志
//  9. 写 Outbox（Redis publish 由 outbox worker 在事务外完成）
//
// 10. APPLYING → APPLIED（检查 RowsAffected()==1）
//
// checker 参数保留以兼容现有调用方，但实际 checker 从 ctx 中的 AdminIdentity 获取。
// 若 ctx 中无 AdminIdentity，返回 ErrMissingIdentity（fail closed）。
func (s *Service) ExecuteRecovery(ctx context.Context, approvalID, checker string) (*RecoveryResult, error) {
	if approvalID == "" {
		return nil, errors.New("approval_id is required")
	}

	// P0-1: checker 必须来自已验证 JWT Context（RED-15）
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil {
		return nil, fmt.Errorf("%w: ExecuteRecovery requires authenticated AdminIdentity in context", ErrMissingIdentity)
	}
	resolvedChecker := identity.Username
	if resolvedChecker == "" {
		resolvedChecker = identity.Subject
	}
	if resolvedChecker == "" || resolvedChecker == "unknown" {
		return nil, fmt.Errorf("%w: checker identity not found in authenticated context", ErrMissingIdentity)
	}
	// 若调用方传入的 checker 与 ctx 中的不一致，以 ctx 为准（RED-15: 身份只从 Context 获取）
	if checker != "" && checker != resolvedChecker {
		s.log.Warn("ExecuteRecovery: caller-provided checker mismatch with context identity, using context identity",
			"caller_checker", checker,
			"context_checker", resolvedChecker,
		)
	}
	checker = resolvedChecker

	result := &RecoveryResult{Record: &ControlRecord{}, ApprovalID: approvalID, Checker: checker}

	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// 1. 锁定审批记录 (SELECT FOR UPDATE)
		var approvalStatus, approvalActionType, approvalRequester string
		err := tx.QueryRow(ctx, `
			SELECT status, action_type, requester
			FROM admin_approval_requests
			WHERE id = $1
			FOR UPDATE
		`, approvalID).Scan(&approvalStatus, &approvalActionType, &approvalRequester)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("approval request not found: %w", ErrInvalidApprovalState)
			}
			return fmt.Errorf("lock approval request: %w", err)
		}

		// 2. 校验 action_type == SYSTEM_LOCKDOWN_RECOVERY
		if approvalActionType != ApprovalActionTypeSystemRecovery {
			return fmt.Errorf("%w: expected action_type %s, got %s",
				ErrInvalidApprovalState, ApprovalActionTypeSystemRecovery, approvalActionType)
		}

		// 3. 校验 status == PENDING
		if approvalStatus != "PENDING" {
			return fmt.Errorf("%w: approval is not PENDING (current=%s), cannot execute recovery",
				ErrInvalidApprovalState, approvalStatus)
		}

		// 4. 校验 checker != requester（maker-checker）
		if checker == approvalRequester {
			return fmt.Errorf("%w: checker=%s cannot approve own request (requester=%s)",
				ErrSelfReview, checker, approvalRequester)
		}

		// 5. PENDING → APPLYING（检查 RowsAffected()==1）
		applyingTag, err := tx.Exec(ctx, `
			UPDATE admin_approval_requests
			SET status = 'APPLYING', checker = $1, reviewed_at = now()
			WHERE id = $2 AND status = 'PENDING'
		`, checker, approvalID)
		if err != nil {
			return fmt.Errorf("mark approval as APPLYING: %w", err)
		}
		if applyingTag.RowsAffected() != 1 {
			return fmt.Errorf("%w: PENDING → APPLYING transition failed (RowsAffected=%d), concurrent approval or stale state",
				ErrInvalidApprovalState, applyingTag.RowsAffected())
		}

		// 6. 锁定 system_control_state (SELECT FOR UPDATE)
		var currentState State
		var currentEpoch int64
		err = tx.QueryRow(ctx, `
			SELECT state::text, epoch FROM system_control_state WHERE id = 1 FOR UPDATE
		`).Scan(&currentState, &currentEpoch)
		if err != nil {
			return fmt.Errorf("lock system_control_state: %w", err)
		}

		// 7. 校验 state == RECOVERY_PENDING（禁止从 LOCKED 直接恢复）
		if currentState == StateLocked {
			return fmt.Errorf("%w: current state is LOCKED, must call RequestRecovery first",
				ErrDirectLockedRecovery)
		}
		if currentState != StateRecoveryPending {
			return fmt.Errorf("%w: recovery execution requires state RECOVERY_PENDING, got %s",
				ErrIllegalTransition, currentState)
		}

		newEpoch := currentEpoch + 1

		// 8. RECOVERY_PENDING → OPEN（检查 RowsAffected()==1）
		recoveryTag, err := tx.Exec(ctx, `
			UPDATE system_control_state
			SET state = 'OPEN', epoch = $1, released_at = now(),
			    version = version + 1, updated_at = now()
			WHERE id = 1 AND state = 'RECOVERY_PENDING'
		`, newEpoch)
		if err != nil {
			return fmt.Errorf("update to OPEN: %w", err)
		}
		if recoveryTag.RowsAffected() != 1 {
			return fmt.Errorf("%w: RECOVERY_PENDING → OPEN transition failed (RowsAffected=%d), concurrent recovery or stale state",
				ErrIllegalTransition, recoveryTag.RowsAffected())
		}

		// 9. 写审计
		auditValue, _ := json.Marshal(map[string]interface{}{
			"approval_id": approvalID,
			"action_type": approvalActionType,
			"checker":     checker,
			"requester":   approvalRequester,
			"from_epoch":  currentEpoch,
			"to_epoch":    newEpoch,
			"from_state":  string(currentState),
			"to_state":    string(StateOpen),
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO admin_override_audit_log (
				entity_type, entity_id, override_type, reason, operator, new_value, created_at
			) VALUES (
				'SYSTEM', gen_random_uuid(), 'SYSTEM_RECOVERY_EXECUTED', $1, $2, $3::jsonb, now()
			)
		`, "recovery executed", checker, string(auditValue))
		if err != nil {
			return fmt.Errorf("insert audit log: %w", err)
		}

		// 10. 写 outbox（通知其他节点，Redis publish 由 outbox worker 在事务外完成）
		outboxPayload, _ := json.Marshal(map[string]interface{}{
			"event":       "SYSTEM_RECOVERY",
			"checker":     checker,
			"approval_id": approvalID,
			"epoch":       newEpoch,
			"ts":          time.Now().UTC().Format(time.RFC3339Nano),
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO transactional_outbox (
				aggregate_type, aggregate_id, event_type, payload, idempotency_key, status
			) VALUES (
				'SYSTEM_CONTROL', '00000000-0000-0000-0000-000000000001', $1, $2::jsonb, $3, 'Pending'
			)
		`, OutboxEventRecovery, string(outboxPayload), fmt.Sprintf("recovery-%d", newEpoch))
		if err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}

		// 11. APPLYING → APPLIED（检查 RowsAffected()==1）
		appliedTag, err := tx.Exec(ctx, `
			UPDATE admin_approval_requests
			SET status = 'APPLIED', applied_at = now()
			WHERE id = $1 AND status = 'APPLYING'
		`, approvalID)
		if err != nil {
			return fmt.Errorf("mark approval as APPLIED: %w", err)
		}
		if appliedTag.RowsAffected() != 1 {
			return fmt.Errorf("%w: APPLYING → APPLIED transition failed (RowsAffected=%d), concurrent modification",
				ErrInvalidApprovalState, appliedTag.RowsAffected())
		}

		result.Record.State = StateOpen
		result.Record.Epoch = newEpoch
		result.Record.Operator = checker
		now := time.Now()
		result.Record.ReleasedAt = &now
		result.Record.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}

	applyRecordToCache(result.Record)
	s.log.Info("system recovery executed",
		"epoch", result.Record.Epoch,
		"checker", checker,
		"approval_id", approvalID,
	)
	return result, nil
}

// ApplyRemoteUpdate 接收来自其他节点（通过 Redis pub/sub）的状态更新。
//
// 只接受更高 epoch（拒绝旧 epoch 回放或乱序）。
// 返回 true 表示已应用，false 表示已拒绝（旧 epoch）。
//
// 注意：此方法不写 DB（DB 真相由触发节点的事务保证），仅更新进程内缓存。
func (s *Service) ApplyRemoteUpdate(rec ControlRecord) bool {
	applied := UpdateLocalCache(rec.State, rec.Epoch)
	if applied {
		s.log.Info("remote system control update applied",
			"state", rec.State,
			"epoch", rec.Epoch,
			"operator", rec.Operator,
		)
	} else {
		s.log.Debug("remote system control update rejected (stale epoch)",
			"remote_epoch", rec.Epoch,
			"local_epoch", currentEpoch.Load(),
		)
	}
	return applied
}

// AdmissionCheck 是支付入口的封锁检查方法（P0-2）。
//
// 逻辑：
//  1. 先查进程内 atomic 缓存（O(1)），命中封锁直接返回 ErrSystemLockdown
//  2. 缓存未命中时回退到 DB 查询（强一致），防止进程刚启动缓存未加载时放行资金
//  3. DB 查询失败 fail-closed（返回 ErrSystemLockdown）
//
// 用于：
//   - Hosted Checkout 创建 session
//   - Payment Intent 创建
//   - Payment Link 消费
//   - Resolver 解析（消费入口）
//
// 与包级 AdmissionCheck() 的区别：
//   - 包级函数仅查缓存（O(1)，无 I/O），适合超热路径
//   - 此方法在缓存未命中时回退到 DB，适合业务关键路径（创建支付意图等）
//
// 若 pool == nil，fail-closed（无法确认 DB 真相）。
func (s *Service) AdmissionCheck(ctx context.Context) error {
	return admissionCheckMethod(ctx, s.pool)
}

// ExecutionCheck 是真正 external dispatch 前的强一致封锁检查方法（P0-2）。
//
// 逻辑：
//  1. 先查进程内 atomic 缓存（快速路径）
//  2. 缓存未命中时查 DB（强一致）
//  3. DB 查询失败 fail-closed（返回 ErrSystemLockdown）
//
// 用于：
//   - acquirer gateway Charge 前
//   - payout submit 前
//   - 任何调用外部 LP/Bank/MAE 前
//   - Provider dispatch 前
//
// RED-14: DB 是真相，Redis/atomic 仅作为热路径优化。
// DB 查询失败时 fail-closed，因为无法确认真相时不能放行资金。
//
// 若 pool == nil，fail-closed。
func (s *Service) ExecutionCheck(ctx context.Context) error {
	return executionCheckMethod(ctx, s.pool)
}

// PublishLockdownEvent 直接 publish 封锁事件到 Redis（事务外，由 outbox worker 调用）。
//
// 注意：此函数执行外部网络 I/O，禁止在 DB 事务内调用（RED-01）。
// 失败时返回错误，调用方应重试并告警，但不能让触发请求返回虚假"全节点成功"。
func (s *Service) PublishLockdownEvent(ctx context.Context, payload map[string]interface{}) error {
	if s.rdb == nil {
		return errors.New("redis unavailable, cannot publish lockdown event")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if err := s.rdb.Publish(ctx, ChannelLockdownEvent, string(body)).Err(); err != nil {
		return fmt.Errorf("publish lockdown event: %w", err)
	}
	// 通知配置重载
	_ = s.rdb.Publish(ctx, ChannelChannelUpdated, "SYSTEM_LOCKDOWN").Err()
	_ = s.rdb.Publish(ctx, ChannelConfigReload, "SYSTEM_LOCKDOWN").Err()
	_ = s.rdb.Publish(ctx, ChannelIssuingReload, "SYSTEM_LOCKDOWN").Err()
	return nil
}

// PublishRecoveryEvent 直接 publish 解除事件到 Redis（事务外）。
func (s *Service) PublishRecoveryEvent(ctx context.Context, payload map[string]interface{}) error {
	if s.rdb == nil {
		return errors.New("redis unavailable, cannot publish recovery event")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if err := s.rdb.Publish(ctx, ChannelRecoveryEvent, string(body)).Err(); err != nil {
		return fmt.Errorf("publish recovery event: %w", err)
	}
	_ = s.rdb.Publish(ctx, ChannelConfigReload, "SYSTEM_RECOVERY").Err()
	_ = s.rdb.Publish(ctx, ChannelIssuingReload, "SYSTEM_RECOVERY").Err()
	return nil
}

// PublishLockdownEventFromOutbox 从 outbox 事件载荷 publish 封锁事件到 Redis（P0-4）。
//
// 此方法由 outbox worker 在事务外调用（消费 SYSTEM_LOCKDOWN_PUBLISH 事件）。
// payload 是 outbox 事件载荷（JSON），直接作为 pub/sub 消息体发布。
//
// 幂等性：Redis pub/sub 是 fire-and-forget，重复 publish 同一载荷不会产生副作用
// （订阅方通过 epoch 去重，旧 epoch 会被 UpdateLocalCache 拒绝）。
//
// Redis 发布失败返回 error（不 ACK），触发 outbox 重试。
func (s *Service) PublishLockdownEventFromOutbox(ctx context.Context, payload []byte) error {
	if s.rdb == nil {
		return errors.New("redis unavailable, cannot publish lockdown event")
	}
	if err := s.rdb.Publish(ctx, ChannelLockdownEvent, string(payload)).Err(); err != nil {
		return fmt.Errorf("publish lockdown event: %w", err)
	}
	// 通知配置重载（best-effort，失败不影响主流程）
	_ = s.rdb.Publish(ctx, ChannelChannelUpdated, "SYSTEM_LOCKDOWN").Err()
	_ = s.rdb.Publish(ctx, ChannelConfigReload, "SYSTEM_LOCKDOWN").Err()
	_ = s.rdb.Publish(ctx, ChannelIssuingReload, "SYSTEM_LOCKDOWN").Err()
	return nil
}

// PublishRecoveryEventFromOutbox 从 outbox 事件载荷 publish 解除事件到 Redis（P0-4）。
//
// 此方法由 outbox worker 在事务外调用（消费 SYSTEM_RECOVERY_PUBLISH 事件）。
// payload 是 outbox 事件载荷（JSON），直接作为 pub/sub 消息体发布。
//
// 幂等性：同 PublishLockdownEventFromOutbox，订阅方通过 epoch 去重。
//
// Redis 发布失败返回 error（不 ACK），触发 outbox 重试。
func (s *Service) PublishRecoveryEventFromOutbox(ctx context.Context, payload []byte) error {
	if s.rdb == nil {
		return errors.New("redis unavailable, cannot publish recovery event")
	}
	if err := s.rdb.Publish(ctx, ChannelRecoveryEvent, string(payload)).Err(); err != nil {
		return fmt.Errorf("publish recovery event: %w", err)
	}
	_ = s.rdb.Publish(ctx, ChannelConfigReload, "SYSTEM_RECOVERY").Err()
	_ = s.rdb.Publish(ctx, ChannelIssuingReload, "SYSTEM_RECOVERY").Err()
	return nil
}

// parseEpochFromApprovalID 是一个 no-op 占位符，实际 epoch 从 DB 读取。
// 保留以避免 RequestRecovery 中临时变量被优化掉。
func parseEpochFromApprovalID(_ string) int64 { return 0 }

// EnsureServiceInterface 在编译期确保 Service 实现预期方法签名。
// 这是一种防御性设计，防止重构时意外破坏 API。
var _ ServiceAPI = (*Service)(nil)

// ServiceAPI 是 Service 的接口契约（用于测试 mock 与文档）。
type ServiceAPI interface {
	LoadFromDB(ctx context.Context) (*ControlRecord, error)
	GetCurrentState(ctx context.Context) (*ControlRecord, error)
	TriggerLockdown(ctx context.Context, operator, reason, idempotencyKey string) (*LockdownResult, error)
	RequestRecovery(ctx context.Context, operator, reason string) (string, error)
	ExecuteRecovery(ctx context.Context, approvalID, checker string) (*RecoveryResult, error)
	ApplyRemoteUpdate(rec ControlRecord) bool
	// P0-2: AdmissionCheck/ExecutionCheck 接入支付入口与执行点
	AdmissionCheck(ctx context.Context) error
	ExecutionCheck(ctx context.Context) error
}

// uuidForOutbox 是 system_control_state 的固定 aggregate_id（单行表）。
var uuidForOutbox = uuid.Nil
