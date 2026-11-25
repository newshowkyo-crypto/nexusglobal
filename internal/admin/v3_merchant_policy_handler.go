package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// V3MerchantPolicyHandler 处理商户路由策略的 Admin API。
//
// P0-4: policy 的创建和修改必须走 Maker-Checker 审批流程，
// 不能直接写入数据库。ListPolicies 是只读操作，直接读取 DB。
type V3MerchantPolicyHandler struct {
	pool *pgxpool.Pool
	svc  *ApprovalService
}

func NewV3MerchantPolicyHandler(pool *pgxpool.Pool, rdb *redis.Client) *V3MerchantPolicyHandler {
	svc := NewApprovalService(pool, rdb)
	return &V3MerchantPolicyHandler{pool: pool, svc: svc}
}

// ListPolicies 列出所有商户路由策略（只读，admin:read 权限）。
func (h *V3MerchantPolicyHandler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := `SELECT p.merchant_id::text, p.provider_tier, p.status, p.version,
		p.created_at::text, p.updated_at::text FROM merchant_routing_policy p
		JOIN corporate_entity ce ON ce.id = p.merchant_id`
	args := []interface{}{}
	if tenant, scoped := tenantScopeOf(ctx); scoped {
		query += ` WHERE ce.tenant_id = $1`
		args = append(args, tenant)
	}
	query += ` ORDER BY p.updated_at DESC`
	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var merchantID, providerTier, status, createdAt, updatedAt string
		var version int
		if err := rows.Scan(&merchantID, &providerTier, &status, &version, &createdAt, &updatedAt); err != nil {
			writeErr(w, err)
			return
		}
		items = append(items, map[string]interface{}{
			"merchant_id":   merchantID,
			"provider_tier": providerTier,
			"status":        status,
			"version":       version,
			"created_at":    createdAt,
			"updated_at":    updatedAt,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// CreatePolicy 创建商户路由策略审批请求（admin:write:config 权限）。
//
// P0-4: 不直接写入 merchant_routing_policy，而是创建 PENDING 审批记录，
// 由审批服务在批准后执行实际写入。
func (h *V3MerchantPolicyHandler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MerchantID   string `json:"merchant_id"`
		ProviderTier string `json:"provider_tier"`
		Status       string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if req.MerchantID == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("merchant_id is required"))
		return
	}
	if _, err := uuid.Parse(req.MerchantID); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}
	if req.ProviderTier != "TIER_1_HIGH_RISK" && req.ProviderTier != "TIER_2_COMPLIANT" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("provider_tier must be TIER_1_HIGH_RISK or TIER_2_COMPLIANT"))
		return
	}
	if req.Status == "" {
		req.Status = "ACTIVE"
	}
	if req.Status != "ACTIVE" && req.Status != "INACTIVE" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("status must be ACTIVE or INACTIVE"))
		return
	}

	ctx := r.Context()

	if tenant, scoped := tenantScopeOf(ctx); scoped {
		var owned bool
		if err := h.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corporate_entity WHERE id=$1 AND tenant_id=$2)`, req.MerchantID, tenant).Scan(&owned); err != nil {
			writeErr(w, err)
			return
		}
		if !owned {
			writeErr(w, apperr.ErrForbidden.WithMessage("merchant is outside the authenticated account"))
			return
		}
	}
	payload := map[string]interface{}{
		"merchant_id":   req.MerchantID,
		"provider_tier": req.ProviderTier,
		"status":        req.Status,
	}

	approvalID, err := h.svc.CreateApprovalRequest(
		ctx,
		ActionCreateMerchantPolicy,
		TargetTypeMerchantPolicy,
		&req.MerchantID,
		nil,
		payload,
		"create merchant routing policy",
		nil,
	)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"approval_id": approvalID,
		"status":      "PENDING",
		"action_type": ActionCreateMerchantPolicy,
		"message":     "已创建审批请求，待复核后生效",
	})
}

// UpdatePolicy 更新商户路由策略审批请求（admin:write:config 权限）。
//
// P0-4: 不直接更新 merchant_routing_policy，而是创建 PENDING 审批记录，
// 由审批服务在批准后执行实际更新。
func (h *V3MerchantPolicyHandler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	merchantUUID, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}

	var req struct {
		ProviderTier string `json:"provider_tier"`
		Status       string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if req.ProviderTier != "TIER_1_HIGH_RISK" && req.ProviderTier != "TIER_2_COMPLIANT" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("provider_tier must be TIER_1_HIGH_RISK or TIER_2_COMPLIANT"))
		return
	}
	if req.Status != "ACTIVE" && req.Status != "INACTIVE" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("status must be ACTIVE or INACTIVE"))
		return
	}

	ctx := r.Context()

	// 读取当前版本用于版本锁
	var currentVersion int
	err = h.pool.QueryRow(ctx, `SELECT version FROM merchant_routing_policy WHERE merchant_id = $1`, merchantUUID).Scan(&currentVersion)
	if err != nil {
		writeErr(w, apperr.ErrNotFound.WithMessage("merchant routing policy not found"))
		return
	}

	payload := map[string]interface{}{
		"merchant_id":   idStr,
		"provider_tier": req.ProviderTier,
		"status":        req.Status,
	}

	approvalID, err := h.svc.CreateApprovalRequest(
		ctx,
		ActionUpdateMerchantPolicy,
		TargetTypeMerchantPolicy,
		&idStr,
		&currentVersion,
		payload,
		"update merchant routing policy",
		nil,
	)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"merchant_id": idStr,
		"approval_id": approvalID,
		"status":      "PENDING",
		"action_type": ActionUpdateMerchantPolicy,
		"message":     "已创建审批请求，待复核后生效",
	})
}
