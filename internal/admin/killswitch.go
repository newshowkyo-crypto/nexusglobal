// Package admin — killswitch.go 实现 Kill Switch Admin handler。
//
// G07 改造：
//   - 使用持久化 failclosed.Service 触发大闸（DB 是真相）
//   - 身份从已验证 Context 获取（httpx.ResolveOperator），拒绝 body 中伪造的 operator（RED-15）
//   - Kill Switch 可立即触发，但解除大闸必须审批（maker-checker）
//   - Redis publish 由 outbox worker 在事务外完成（RED-01）
//
// P0-7 改造：
//   - SSE 使用同源 Admin Session Cookie 鉴权，添加连接状态指示
//   - Lockdown 请求体添加 idempotency_key
//   - 新增 SuspendActiveCards 全卡安全离场端点
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// KillSwitchHandler 处理系统封锁与解除的 Admin API。
type KillSwitchHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	svc  *failclosed.Service // 持久化服务，G07 新增；可为 nil（回退到旧路径）
}

// NewKillSwitchHandler 创建旧式 handler（无持久化服务，回退路径）。
// 保留以兼容现有 router 装配。
func NewKillSwitchHandler(pool *pgxpool.Pool, rdb *redis.Client) *KillSwitchHandler {
	return &KillSwitchHandler{pool: pool, rdb: rdb}
}

// NewKillSwitchHandlerWithService 创建带持久化服务的 handler（推荐路径）。
func NewKillSwitchHandlerWithService(pool *pgxpool.Pool, rdb *redis.Client, svc *failclosed.Service) *KillSwitchHandler {
	return &KillSwitchHandler{pool: pool, rdb: rdb, svc: svc}
}

// Lockdown 触发全局封锁。
//
// 身份：从已验证 AdminIdentity Context 获取（httpx.ResolveOperator），
// 严格拒绝 body 中伪造的 operator 字段（RED-15）。
// 若 Context 中无已验证身份，返回 403。
//
// 若 svc 非 nil：使用持久化路径（DB 事务 + outbox）
// 若 svc 为 nil：回退到旧路径（直接 SQL + atomic）
func (h *KillSwitchHandler) Lockdown(w http.ResponseWriter, r *http.Request) {
	// 身份只从已验证 Context 获取（RED-15）
	operator := httpx.ResolveOperator(r.Context())
	if operator == "" || operator == "unknown" {
		writeErr(w, apperr.ErrForbidden.WithMessage("operator identity not found in authenticated context"))
		return
	}

	var req struct {
		Operator       string `json:"operator"` // 已废弃：身份从 Context 获取，此字段被忽略
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotency_key"` // P0-7: 幂等键，相同键不会重复触发封锁
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.Reason == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("reason is required"))
		return
	}
	if req.IdempotencyKey == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("idempotency_key is required"))
		return
	}

	ctx := r.Context()

	// 优先使用持久化服务
	if h.svc != nil {
		h.lockdownWithService(ctx, w, operator, req.Reason, req.IdempotencyKey)
		return
	}

	// P0-7: production/real 下 lockdownLegacy 运行时路径已删除；持久化服务缺失必须 fail-closed
	writeErr(w, apperr.ErrInternal.WithMessage("kill-switch persistent service not configured; lockdown rejected (fail-closed)"))
}

// lockdownWithService 使用持久化服务触发封锁。
func (h *KillSwitchHandler) lockdownWithService(ctx context.Context, w http.ResponseWriter, operator, reason, idempotencyKey string) {
	result, err := h.svc.TriggerLockdown(ctx, operator, reason, idempotencyKey)
	if err != nil {
		// 检查是否是状态机错误
		if isIllegalTransition(err) {
			writeErr(w, apperr.ErrIllegalTransition.WithMessage(err.Error()))
			return
		}
		writeErr(w, err)
		return
	}

	// 事务已提交，outbox 已写入。Redis publish 由 outbox worker 异步完成。
	// 响应中明确告知：DB 已封锁，pub/sub 同步为最终一致。
	response := map[string]interface{}{
		"event":             "SYSTEM_LOCKDOWN",
		"operator":          operator,
		"reason":            reason,
		"idempotency_key":   idempotencyKey,
		"epoch":             result.Record.Epoch,
		"state":             string(result.Record.State),
		"disabled_channels": result.DisabledChannels,
		"risk_hold_groups":  result.RiskHoldGroups,
		"ts":                time.Now().UTC().Format(time.RFC3339Nano),
		// 同步语义说明：DB 是真相，本机已封锁；跨节点通过 pub/sub 最终一致
		"sync_semantics": "db_committed_local_blocked_cross_node_eventual",
	}

	// Best-effort 直接 publish（加速跨节点同步，失败由 outbox worker 兜底）
	if h.rdb != nil {
		_ = h.svc.PublishLockdownEvent(ctx, response)
	}

	writeJSON(w, response)
}

// isIllegalTransition 判断错误是否为状态机非法转移。
func isIllegalTransition(err error) bool {
	if err == nil {
		return false
	}
	// 用 errors.Is 检查包装链
	for e := err; e != nil; {
		if e.Error() == failclosed.ErrIllegalTransition.Error() {
			return true
		}
		// 简单字符串包含检查（避免 import cycle 复杂性）
		if contains(e.Error(), "ILLEGAL_STATE_TRANSITION") {
			return true
		}
		break
	}
	return false
}

// contains 是局部字符串包含检查。
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// RequestRecovery 创建解除大闸的审批请求（maker-checker）。
// 解除不能直接执行，必须先创建审批请求，由 approver 审批后调用 ExecuteRecovery。
func (h *KillSwitchHandler) RequestRecovery(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("persistent service not configured"))
		return
	}

	operator := httpx.ResolveOperator(r.Context())
	if operator == "" || operator == "unknown" {
		writeErr(w, apperr.ErrForbidden.WithMessage("operator identity not found in authenticated context"))
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.Reason == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("reason is required"))
		return
	}

	approvalID, err := h.svc.RequestRecovery(r.Context(), operator, req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"approval_id": approvalID,
		"status":      "PENDING",
		"operator":    operator,
		"message":     "recovery request created, pending approval",
	})
}

// ExecuteRecovery 在审批通过后执行解除大闸。
// 由 approval handler 调用，不直接暴露为 API（审批流由 ApprovalHandler.Approve 触发）。
//
// P0-1: checker 必须来自已验证 JWT Context（RED-15）。
// Service.ExecuteRecovery 内部会从 ctx 获取 AdminIdentity 并验证 checker != requester。
func (h *KillSwitchHandler) ExecuteRecovery(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("persistent service not configured"))
		return
	}

	// P0-1: 身份从已验证 Context 获取（RED-15）
	// Service.ExecuteRecovery 内部也会从 ctx 获取 AdminIdentity，这里仅做前置检查
	checker := httpx.ResolveOperator(r.Context())
	if checker == "" || checker == "unknown" {
		writeErr(w, apperr.ErrForbidden.WithMessage("checker identity not found in authenticated context"))
		return
	}

	var req struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.ApprovalID == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("approval_id is required"))
		return
	}

	// 传递 r.Context()，Service.ExecuteRecovery 会从中提取 AdminIdentity
	result, err := h.svc.ExecuteRecovery(r.Context(), req.ApprovalID, checker)
	if err != nil {
		writeErr(w, err)
		return
	}

	// Best-effort publish
	if h.rdb != nil {
		_ = h.svc.PublishRecoveryEvent(r.Context(), map[string]interface{}{
			"event":   "SYSTEM_RECOVERY",
			"checker": checker,
			"epoch":   result.Record.Epoch,
			"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		})
	}

	writeJSON(w, map[string]interface{}{
		"state":       string(result.Record.State),
		"epoch":       result.Record.Epoch,
		"checker":     checker,
		"approval_id": result.ApprovalID,
		"message":     "system recovery executed",
	})
}

// EventsStream 是 Admin SSE 事件流。
// P0-7: 使用同源 Admin Session Cookie 鉴权，添加连接状态指示。
// 修复：单 goroutine select 循环，禁止并发写 ResponseWriter。
func (h *KillSwitchHandler) EventsStream(w http.ResponseWriter, r *http.Request) {
	// P0-7: 鉴权检查 — 身份必须从已验证 Context 获取
	operator := httpx.ResolveOperator(r.Context())
	if operator == "" || operator == "unknown" {
		writeErr(w, apperr.ErrForbidden.WithMessage("admin authentication required for event stream"))
		return
	}

	if h.rdb == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("redis is required for admin event stream"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 防止 nginx 缓冲

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, apperr.ErrInternal.WithMessage("streaming is not supported"))
		return
	}

	// P0-7: 发送初始连接状态 — connecting（SSE 尚未建立，等待 open 事件）
	writeSSE(w, flusher, "connection_status", map[string]interface{}{
		"status": "connecting",
		"ts":     time.Now().UTC().Format(time.RFC3339Nano),
	})

	pubsub := h.rdb.Subscribe(r.Context(), "system.fail_closed.lockdown", "issuing.pool.fraud_tripped")
	defer pubsub.Close()

	// 使用 Channel() 获取消息通道，配合 select 实现单 goroutine 事件循环
	msgCh := pubsub.Channel()

	done := r.Context().Done()
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	// 单 goroutine select 循环：心跳和 Redis 消息通过 select 复用，
	// 消除并发写 ResponseWriter 的竞态条件。
	for {
		select {
		case <-done:
			// 客户端断开连接
			return

		case <-heartbeatTicker.C:
			writeSSE(w, flusher, "heartbeat", map[string]interface{}{
				"ts": time.Now().UTC().Format(time.RFC3339Nano),
			})

		case msg, ok := <-msgCh:
			if !ok {
				// Redis 订阅通道关闭
				writeSSE(w, flusher, "connection_status", map[string]interface{}{
					"status": "disconnected",
					"reason": "redis subscription closed",
					"ts":     time.Now().UTC().Format(time.RFC3339Nano),
				})
				return
			}

			eventName := "SYSTEM_LOCKDOWN"
			if msg.Channel == "issuing.pool.fraud_tripped" {
				eventName = "ISSUING_POOL_FRAUD_TRIPPED"
			}
			writeSSE(w, flusher, eventName, map[string]interface{}{
				"payload": msg.Payload,
			})
		}
	}
}

// writeSSE 写入 SSE 事件并 flush。
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, data interface{}) {
	dataBytes, _ := json.Marshal(data)
	_, _ = w.Write([]byte("event: " + event + "\n"))
	_, _ = w.Write([]byte("data: " + string(dataBytes) + "\n\n"))
	flusher.Flush()
}

// SuspendActiveCards 全卡安全离场。
// P0-7: 一个 DB 事务中把所有 ACTIVE 卡转为 FROZEN，写 Outbox 事件和 immutable audit。
//
// 规则：
//   - 权限：admin:kill_switch
//   - 幂等：相同 idempotency_key 不会重复冻结或重复产生资金/审计副作用
//     事务内 INSERT...ON CONFLICT DO NOTHING 原子认领，RowsAffected()==0 表示已有请求处理
//   - 不得在冻结事务内调用 Redis、KMS 或 Provider
//   - Provider 超时或不确定结果写 UNKNOWN_EXTERNAL_STATE/SYNC_PENDING，本地仍保持冻结
//   - 写 immutable audit
//   - UPDATE...RETURNING 获取真正被冻结的卡片 ID（原子，无 TOCTOU）
func (h *KillSwitchHandler) SuspendActiveCards(w http.ResponseWriter, r *http.Request) {
	operator := httpx.ResolveOperator(r.Context())
	if operator == "" || operator == "unknown" {
		writeErr(w, apperr.ErrForbidden.WithMessage("operator identity not found in authenticated context"))
		return
	}

	var req struct {
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.Reason == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("reason is required"))
		return
	}
	if req.IdempotencyKey == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("idempotency_key is required"))
		return
	}

	if h.pool == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("database pool not configured"))
		return
	}

	ctx := r.Context()

	// DB 事务：原子认领幂等键 + 冻结所有 ACTIVE 卡 + 写 Outbox + 写 Audit + 写幂等记录
	var frozenCardIDs []uuid.UUID
	var frozenCount int

	tx, txErr := h.pool.Begin(ctx)
	if txErr != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("failed to begin transaction: "+txErr.Error()))
		return
	}
	defer tx.Rollback(ctx)

	// 1. 事务内原子认领幂等键：INSERT...ON CONFLICT DO NOTHING
	//    RowsAffected()==0 表示该 idempotency_key 已被处理过，返回已有结果
	idempTag, idempErr := tx.Exec(ctx, `
		INSERT INTO admin_suspend_idempotency (idempotency_key, operator, reason, frozen_count, created_at)
		VALUES ($1, $2, $3, 0, now())
		ON CONFLICT (idempotency_key) DO NOTHING
	`, req.IdempotencyKey, operator, req.Reason)
	if idempErr != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("idempotency claim failed: "+idempErr.Error()))
		return
	}
	if idempTag.RowsAffected() == 0 {
		// 幂等键已存在：查询已有结果并返回
		existing, existingErr := readSuspendIdempotency(ctx, tx, req.IdempotencyKey)
		if existingErr != nil {
			writeErr(w, apperr.ErrInternal.WithMessage("idempotency read failed: "+existingErr.Error()))
			return
		}
		writeJSON(w, existing)
		return
	}

	// 2. UPDATE...RETURNING 原子冻结并获取真正被冻结的卡片 ID
	rows, updateErr := tx.Query(ctx, `
		UPDATE virtual_cards
		SET status = 'FROZEN', frozen_reason = $1, updated_at = now()
		WHERE status = 'ACTIVE'
		RETURNING id
	`, fmt.Sprintf("suspend-active-cards: %s (by %s)", req.Reason, operator))
	if updateErr != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("failed to freeze active cards: "+updateErr.Error()))
		return
	}
	for rows.Next() {
		var cardID uuid.UUID
		if scanErr := rows.Scan(&cardID); scanErr != nil {
			rows.Close()
			writeErr(w, apperr.ErrInternal.WithMessage("failed to scan frozen card id: "+scanErr.Error()))
			return
		}
		frozenCardIDs = append(frozenCardIDs, cardID)
	}
	rows.Close()

	frozenCount = len(frozenCardIDs)

	// 3. 更新幂等记录的 frozen_count 为实际冻结数
	if frozenCount > 0 {
		_, updateIdempErr := tx.Exec(ctx, `
			UPDATE admin_suspend_idempotency SET frozen_count = $1 WHERE idempotency_key = $2
		`, frozenCount, req.IdempotencyKey)
		if updateIdempErr != nil {
			writeErr(w, apperr.ErrInternal.WithMessage("failed to update idempotency frozen_count: "+updateIdempErr.Error()))
			return
		}
	}

	// 4. 为每张卡写 Outbox 事件（事务内，Worker 事务外调用 IssuingAdapter.FreezeCard）
	for _, cardID := range frozenCardIDs {
		outboxPayload, _ := json.Marshal(map[string]interface{}{
			"card_id":    cardID.String(),
			"new_status": "FROZEN",
			"reason":     req.Reason,
			"operator":   operator,
			"sync_hint":  "local_frozen_provider_sync_pending",
		})
		outboxID := uuid.New()
		_, outboxErr := tx.Exec(ctx, `
			INSERT INTO transactional_outbox (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
			VALUES ($1, 'virtual_card', $2, 'CARD_FROZEN_SUSPEND', $3, $4, 'Pending')
		`, outboxID, cardID, outboxPayload, fmt.Sprintf("card_frozen_suspend_%s_%s", cardID.String(), req.IdempotencyKey))
		if outboxErr != nil {
			// Outbox 写入失败，回滚整个事务
			writeErr(w, apperr.ErrInternal.WithMessage("failed to write outbox event for card "+cardID.String()+": "+outboxErr.Error()))
			return
		}
	}

	// 5. 写 immutable audit
	if auditErr := writeSuspendAudit(ctx, tx, operator, req.IdempotencyKey, req.Reason, frozenCount); auditErr != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("failed to write audit: "+auditErr.Error()))
		return
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("failed to commit transaction: "+commitErr.Error()))
		return
	}

	// Best-effort publish suspend event
	if h.rdb != nil {
		_ = h.rdb.Publish(ctx, "system.fail_closed.suspend_active_cards", fmt.Sprintf(
			`{"event":"SUSPEND_ACTIVE_CARDS","operator":"%s","frozen_count":%d,"idempotency_key":"%s"}`,
			operator, frozenCount, req.IdempotencyKey,
		)).Err()
	}

	writeJSON(w, map[string]interface{}{
		"event":           "SUSPEND_ACTIVE_CARDS",
		"operator":        operator,
		"reason":          req.Reason,
		"idempotency_key": req.IdempotencyKey,
		"frozen_count":    frozenCount,
		"card_ids":        cardIDsToStrings(frozenCardIDs),
		"ts":              time.Now().UTC().Format(time.RFC3339Nano),
		"sync_semantics":  "db_committed_local_frozen_provider_sync_eventual",
	})
}

// readSuspendIdempotency 从事务内读取已有幂等结果。
// 用于 INSERT...ON CONFLICT DO NOTHING 认领失败后返回已有结果。
func readSuspendIdempotency(ctx context.Context, tx pgx.Tx, idempotencyKey string) (map[string]interface{}, error) {
	var operator string
	var reason string
	var frozenCount int
	var createdAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT operator, reason, frozen_count, created_at
		FROM admin_suspend_idempotency
		WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&operator, &reason, &frozenCount, &createdAt)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"event":           "SUSPEND_ACTIVE_CARDS",
		"operator":        operator,
		"reason":          reason,
		"idempotency_key": idempotencyKey,
		"frozen_count":    frozenCount,
		"ts":              createdAt.UTC().Format(time.RFC3339Nano),
		"idempotent":      true,
	}, nil
}

// writeSuspendAudit 写入 immutable audit。
func writeSuspendAudit(ctx context.Context, tx pgx.Tx, operator, idempotencyKey, reason string, frozenCount int) error {
	details, _ := json.Marshal(map[string]interface{}{
		"action":          "SUSPEND_ACTIVE_CARDS",
		"operator":        operator,
		"reason":          reason,
		"idempotency_key": idempotencyKey,
		"frozen_count":    frozenCount,
	})
	_, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_log (actor_type, action, entity_type, entity_id, details)
		VALUES ('ADMIN', 'SUSPEND_ACTIVE_CARDS', 'system', NULL, $1::jsonb)
	`, string(details))
	return err
}

// cardIDsToStrings 将 UUID 列表转为字符串列表。
func cardIDsToStrings(ids []uuid.UUID) []string {
	result := make([]string, len(ids))
	for i, id := range ids {
		result[i] = id.String()
	}
	return result
}
