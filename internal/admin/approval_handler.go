// Package admin — approval_handler.go 是 Maker-Checker 审批流的 HTTP handler (G08)。
//
// G08 修复：
//  1. 身份从已验证 Context 获取（httpx.ResolveOperator），拒绝 body 中 requester/checker（RED-15）
//  2. 为每个保护动作列唯一 action type 和 typed payload，禁止 map[string]any 直接执行任意 SQL/字段
//  3. 高危操作只能创建 PENDING，不直接执行
//  4. Approve 调用 ApprovalService.ApplyTransaction 原子应用 payload
//  5. 拒绝自审（checker=requester）403
//  6. 版本锁（target_version）
//
// Kill Switch 可立即触发，但解除大闸必须审批（在 killswitch.go 中实现）。
package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ApprovalHandler 处理 Maker-Checker 审批流 API。
//
// G08: 委托给 ApprovalService 执行业务逻辑，handler 只负责 HTTP 层。
type ApprovalHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	svc  *ApprovalService
}

// NewApprovalHandler 创建审批处理器（旧式，内部构造 Service）。
func NewApprovalHandler(pool *pgxpool.Pool, rdb *redis.Client) *ApprovalHandler {
	svc := NewApprovalService(pool, rdb)
	return &ApprovalHandler{pool: pool, rdb: rdb, svc: svc}
}

// NewApprovalHandlerWithService 创建带注入 Service 的处理器（推荐路径）。
func NewApprovalHandlerWithService(svc *ApprovalService) *ApprovalHandler {
	return &ApprovalHandler{pool: svc.Pool(), rdb: svc.Rdb(), svc: svc}
}

// Create 创建审批请求（PENDING）。
//
// G08 修复：
//   - 身份从已验证 Context 获取（RED-15），拒绝 body 中 requester
//   - 为每个保护动作列唯一 action type 和 typed payload
//   - 高危操作只能创建 PENDING，不直接执行
func (h *ApprovalHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. 身份只从已验证 Context 获取（RED-15）
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil {
		writeErr(w, apperr.ErrForbidden.WithMessage("admin identity not found in authenticated context"))
		return
	}

	// 2. 解析请求体（不含身份字段）
	var body struct {
		ActionType       string                 `json:"action_type"`
		TargetID         string                 `json:"target_id"`
		TargetVersion    *int                   `json:"target_version"`
		Reason           string                 `json:"reason"`
		RequestedPayload map[string]interface{} `json:"requested_payload"`
		RequestID        string                 `json:"request_id"`
		// 已废弃字段：身份从 Context 获取，忽略 body 中的 requester/checker
		Requester string `json:"requester,omitempty"`
		Checker   string `json:"checker,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if body.ActionType == "" {
		writeError(w, "action_type is required", http.StatusBadRequest)
		return
	}

	// 3. 构造 targetID 指针
	var targetID *string
	if body.TargetID != "" {
		targetID = &body.TargetID
	}

	// 4. 构造 requestID 指针（幂等键，RED-10）
	var requestID *string
	if body.RequestID != "" {
		requestID = &body.RequestID
	}

	// 5. 委托给 Service 创建 PENDING 审批
	id, err := h.svc.CreateApprovalRequest(
		ctx,
		body.ActionType,
		"", // target_type 由 Service 根据 action_type 推断
		targetID,
		body.TargetVersion,
		body.RequestedPayload,
		body.Reason,
		requestID,
	)
	if err != nil {
		writeErr(w, err)
		return
	}

	// 6. 返回结果（不暴露身份给客户端，身份从 Context 获取）
	writeJSON(w, map[string]interface{}{
		"id":          id,
		"action_type": body.ActionType,
		"status":      "PENDING",
		"requester":   identity.Username, // 从 Context 获取，非 body
		"message":     "审批请求已创建，待复核后生效",
	})
}

// List 列出审批请求。
//
// 支持按 status 过滤，返回审批 inbox（含 before/after、目标、版本、风险说明、maker/checker）。
// 不显示凭证或 token（before_snapshot 已通过 sanitize 脱敏）。
func (h *ApprovalHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	status := r.URL.Query().Get("status")
	limit := queryLimit(r, 50)
	offset := queryInt(r, "offset", 0)

	query := `SELECT id::text, action_type,
	         COALESCE(target_type, ''), COALESCE(target_id::text, ''), target_version,
	         requester, COALESCE(requester_role, ''), COALESCE(reason, ''),
	         status, checker, COALESCE(review_reason, ''),
	         before_snapshot::text,
	         created_at::text, reviewed_at::text, applied_at::text
	      FROM admin_approval_requests`
	args := []interface{}{}
	argIdx := 1

	// ADM-001: 对象级租户 scope
	var whereClauses []string
	if tenantID, scoped := tenantScopeOf(ctx); scoped {
		whereClauses = append(whereClauses, "tenant_id = $"+strconv.Itoa(argIdx))
		args = append(args, tenantID)
		argIdx++
	}

	if status != "" {
		whereClauses = append(whereClauses, "status = $"+strconv.Itoa(argIdx))
		args = append(args, status)
		argIdx++
	}

	if len(whereClauses) > 0 {
		query += " WHERE " + joinWithAnd(whereClauses)
	}

	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(argIdx)
	args = append(args, limit)
	argIdx++
	query += " OFFSET $" + strconv.Itoa(argIdx)
	args = append(args, offset)

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, actionType, targetType, requester, requesterRole, statusVal, created string
		var targetID, reason, checker, reviewReason, beforeSnapshot, reviewedAt, appliedAt *string
		var targetVersion *int
		if err := rows.Scan(&id, &actionType, &targetType, &targetID, &targetVersion,
			&requester, &requesterRole, &reason,
			&statusVal,
			&checker, &reviewReason, &beforeSnapshot,
			&created, &reviewedAt, &appliedAt); err != nil {
			writeErr(w, err)
			return
		}

		item := map[string]interface{}{
			"id":             id,
			"action_type":    actionType,
			"target_type":    targetType,
			"requester":      requester,
			"requester_role": requesterRole,
			"status":         statusVal,
			"created_at":     created,
			"status_cn":      presentApprovalStatusCN(statusVal),
		}
		if reason != nil {
			item["reason"] = *reason
		}
		if targetID != nil {
			item["target_id"] = *targetID
		}
		if targetVersion != nil {
			item["target_version"] = *targetVersion
		}
		if checker != nil {
			item["checker"] = *checker
		}
		if reviewReason != nil {
			item["review_reason"] = *reviewReason
		}
		if beforeSnapshot != nil && *beforeSnapshot != "" {
			// P0-5: before_snapshot 防御性递归脱敏
			var snap map[string]interface{}
			if json.Unmarshal([]byte(*beforeSnapshot), &snap) == nil {
				item["before_snapshot"] = SanitizeMap(snap)
			}
		}
		if reviewedAt != nil {
			item["reviewed_at"] = *reviewedAt
		}
		if appliedAt != nil {
			item["applied_at"] = *appliedAt
		}
		items = append(items, item)
	}

	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// Get 获取审批详情（含 before/after、目标、版本、风险说明、maker/checker）。
//
// 不显示凭证或 token（before_snapshot 已通过 sanitize 脱敏）。
func (h *ApprovalHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		writeError(w, "id is required", http.StatusBadRequest)
		return
	}

	var actionType, targetType, requester, requesterRole, reason, statusVal, created string
	var targetID, checker, reviewReason, beforeSnapshot, validatedPayload, reviewedAt, appliedAt *string
	var targetVersion *int
	err := h.pool.QueryRow(ctx, `
		SELECT action_type, COALESCE(target_type, ''), COALESCE(target_id::text, ''), target_version,
		       requester, COALESCE(requester_role, ''), COALESCE(reason, ''),
		       status, checker, COALESCE(review_reason, ''),
		       before_snapshot::text, validated_payload::text,
		       created_at::text, reviewed_at::text, applied_at::text
		FROM admin_approval_requests WHERE id = $1`, id).Scan(
		&actionType, &targetType, &targetID, &targetVersion,
		&requester, &requesterRole, &reason,
		&statusVal, &checker, &reviewReason,
		&beforeSnapshot, &validatedPayload,
		&created, &reviewedAt, &appliedAt)
	if err != nil {
		writeErr(w, err)
		return
	}

	item := map[string]interface{}{
		"id":             id,
		"action_type":    actionType,
		"target_type":    targetType,
		"requester":      requester,
		"requester_role": requesterRole,
		"reason":         reason,
		"status":         statusVal,
		"status_cn":      presentApprovalStatusCN(statusVal),
		"created_at":     created,
		"risk_note":      presentApprovalRiskNote(actionType),
	}
	if targetID != nil {
		item["target_id"] = *targetID
	}
	if targetVersion != nil {
		item["target_version"] = *targetVersion
	}
	if checker != nil {
		item["checker"] = *checker
	}
	if reviewReason != nil {
		item["review_reason"] = *reviewReason
	}
	if beforeSnapshot != nil && *beforeSnapshot != "" {
		var snap map[string]interface{}
		if json.Unmarshal([]byte(*beforeSnapshot), &snap) == nil {
			item["before_snapshot"] = SanitizeMap(snap)
		}
	}
	if validatedPayload != nil && *validatedPayload != "" {
		var snap map[string]interface{}
		if json.Unmarshal([]byte(*validatedPayload), &snap) == nil {
			item["validated_payload"] = SanitizeMap(snap)
		}
	}
	if reviewedAt != nil {
		item["reviewed_at"] = *reviewedAt
	}
	if appliedAt != nil {
		item["applied_at"] = *appliedAt
	}

	writeJSON(w, item)
}

// Approve 审批通过并原子应用 payload。
//
// G08 修复：
//   - 身份从已验证 Context 获取（RED-15），拒绝 body 中 checker
//   - 调用 ApprovalService.ApplyTransaction 原子应用
//   - 拒绝自审 403
//   - 版本锁检查
//   - DB 事务内禁止外部网络 I/O（RED-01）：config reload 由 worker 在事务外完成
func (h *ApprovalHandler) Approve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		writeError(w, "id is required", http.StatusBadRequest)
		return
	}

	// body 只含 review_reason，不含 checker（身份从 Context 获取）
	var body struct {
		ReviewReason string `json:"review_reason"`
		// 已废弃字段：身份从 Context 获取，忽略 body 中的 checker
		Checker string `json:"checker,omitempty"`
	}

	// 容忍空 body
	_ = json.NewDecoder(r.Body).Decode(&body)

	// 委托给 Service 执行 ApplyTransaction
	result, err := h.svc.ApplyTransaction(ctx, id, body.ReviewReason)
	if err != nil {
		writeErr(w, err)
		return
	}

	// 事务已提交，outbox 已写入。
	// RED-01: config reload 由 worker 在事务外完成（best-effort 直接 publish 加速跨节点同步）。
	if h.rdb != nil && result.ReloadTopic != "" {
		payload := result.TargetID + "|" + strconv.Itoa(result.NewVersion)
		_ = h.rdb.Publish(ctx, result.ReloadTopic, payload).Err()
	}

	writeJSON(w, map[string]interface{}{
		"id":           id,
		"status":       "APPLIED",
		"action_type":  result.ActionType,
		"target_type":  result.TargetType,
		"target_id":    result.TargetID,
		"new_version":  result.NewVersion,
		"checker":      result.Checker,
		"event_type":   result.EventType,
		"reload_topic": result.ReloadTopic,
		"message":      "审批已通过，配置已原子应用",
	})
}

// Reject 审批拒绝。
//
// G08 修复：
//   - 身份从已验证 Context 获取（RED-15），拒绝 body 中 checker
//   - 拒绝自审 403
func (h *ApprovalHandler) Reject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		writeError(w, "id is required", http.StatusBadRequest)
		return
	}

	var body struct {
		ReviewReason string `json:"review_reason"`
		// 已废弃字段：身份从 Context 获取，忽略 body 中的 checker
		Checker string `json:"checker,omitempty"`
	}

	_ = json.NewDecoder(r.Body).Decode(&body)

	if err := h.svc.RejectApproval(ctx, id, body.ReviewReason); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"id":      id,
		"status":  "REJECTED",
		"message": "审批已拒绝",
	})
}

// ── 辅助函数 ──

// presentApprovalStatusCN 将审批状态翻译成中文（用于 UI 展示）。
func presentApprovalStatusCN(status string) string {
	switch status {
	case "PENDING":
		return "已申请"
	case "APPLYING":
		return "应用中"
	case "APPLIED":
		return "已应用"
	case "REJECTED":
		return "已拒绝"
	case "FAILED_STALE":
		return "应用失败（版本冲突）"
	default:
		return status
	}
}

// presentApprovalRiskNote 为审批详情生成风险说明。
func presentApprovalRiskNote(actionType string) string {
	switch actionType {
	case ActionDisableAcquirerChannel:
		return "禁用收单通道将立即停止该通道的所有交易路由，进行中的交易可能失败。"
	case ActionEnableAcquirerChannel:
		return "启用收单通道将允许该通道接受新交易路由。"
	case ActionUpdateAcquirerChannel:
		return "更新收单通道配置可能影响交易路由、资金清算和风控策略。"
	case ActionDisableIssuingPool:
		return "禁用发卡卡池将立即停止该卡池的发卡能力，进行中的发卡请求可能失败。"
	case ActionEnableIssuingPool:
		return "启用发卡卡池将允许该卡池接受新发卡请求。"
	case ActionUpdateIssuingPool:
		return "更新发卡卡池配置可能影响发卡能力、资金清算和风控策略。"
	case ActionUpdateMerchantReserve:
		return "更新商户准备金比例将影响资金预留和结算。"
	case ActionDisableWalletPolicy:
		return "禁用钱包策略将阻止相关钱包地址的交易。"
	case ActionDisableVAAccount:
		return "禁用 VA 账号将停止该虚拟账号的收款能力。"
	case ActionUpdateChannelRisk:
		return "更新通道风控参数将影响该通道的风险拦截策略。"
	case ActionSystemLockdownRecovery:
		return "解除系统封锁将恢复所有通道和隔离组的正常运行。此操作高风险，必须由 security-admin 审批。"
	default:
		return "此操作为高危操作，需 Maker-Checker 审批。"
	}
}

// Helper function to write error response（保留旧函数以兼容其他 handler）。
func writeError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
		},
	})
}
