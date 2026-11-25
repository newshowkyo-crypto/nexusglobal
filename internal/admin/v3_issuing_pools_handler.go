package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	issuing "github.com/nexusglobal/mamr/internal/issuing"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/configvalidator"
)

type V3IssuingPoolsHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	svc  *ApprovalService // G08: 高危操作委托给审批服务
}

func NewV3IssuingPoolsHandler(pool *pgxpool.Pool, rdb *redis.Client) *V3IssuingPoolsHandler {
	svc := NewApprovalService(pool, rdb)
	return &V3IssuingPoolsHandler{pool: pool, rdb: rdb, svc: svc}
}

// NewV3IssuingPoolsHandlerWithService 创建带注入审批服务的 handler（推荐路径）。
func NewV3IssuingPoolsHandlerWithService(pool *pgxpool.Pool, rdb *redis.Client, svc *ApprovalService) *V3IssuingPoolsHandler {
	return &V3IssuingPoolsHandler{pool: pool, rdb: rdb, svc: svc}
}

// SetRuntimeMode 注入运行时模式到内部 ApprovalService。R19 P0-2。
func (h *V3IssuingPoolsHandler) SetRuntimeMode(mode configvalidator.RuntimeMode) {
	if h.svc != nil {
		h.svc.SetRuntimeMode(mode)
	}
}

func (h *V3IssuingPoolsHandler) publishConfigUpdate(poolID uuid.UUID, version int) {
	if h.rdb != nil {
		payload := fmt.Sprintf("%s|%d", poolID.String(), version)
		h.rdb.Publish(context.Background(), "config:issuing_pool:reload", payload)
	}
}

func (h *V3IssuingPoolsHandler) ListPools(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// ADM-001: 对象级租户 scope（issuing_providers 经 entity_id 关联 corporate_entity.tenant_id）
	query := `SELECT ip.id::text, COALESCE(ip.entity_id::text, ''), ip.issuing_tier, ip.funding_mode, ip.provider_code, ip.pool_code,
		 ip.status, ip.version, ip.provider_config::text, ip.velocity_config::text,
		 ip.adapter_type::text, COALESCE(ip.adapter_provider_id::text, ''),
		 ip.credential_vault_key,
		 ip.created_at::text, ip.updated_at::text
		 FROM issuing_providers ip`
	args := []interface{}{}
	if tenantID, scoped := tenantScopeOf(ctx); scoped {
		query += ` JOIN corporate_entity ce ON ce.id = ip.entity_id WHERE ce.tenant_id = $1`
		args = append(args, tenantID)
	}
	query += ` ORDER BY ip.created_at DESC`
	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, entityID, issuingTier, fundingMode, providerCode, poolCode, status, providerConfig, velocityConfig, created, updated string
		var adapterType, adapterProviderID, credentialVaultKey string
		var version int
		if err := rows.Scan(&id, &entityID, &issuingTier, &fundingMode, &providerCode, &poolCode, &status, &version, &providerConfig, &velocityConfig, &adapterType, &adapterProviderID, &credentialVaultKey, &created, &updated); err != nil {
			writeErr(w, err)
			return
		}

		items = append(items, map[string]interface{}{
			"id":                  id,
			"entity_id":           entityID,
			"issuing_tier":        issuingTier,
			"funding_mode":        fundingMode,
			"provider_code":       providerCode,
			"pool_code":           poolCode,
			"status":              status,
			"version":             version,
			"provider_config":     sanitizeJSONText(providerConfig),
			"velocity_config":     sanitizeJSONText(velocityConfig),
			"adapter_type":        adapterType,
			"adapter_provider_id": adapterProviderID,
			"created_at":          created,
			"updated_at":          updated,
			"has_credential":      credentialVaultKey != "",
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3IssuingPoolsHandler) CreatePool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityID           string                 `json:"entity_id"`
		IssuingTier        string                 `json:"issuing_tier"`
		FundingMode        string                 `json:"funding_mode"`
		ProviderCode       string                 `json:"provider_code"`
		PoolCode           string                 `json:"pool_code"`
		CredentialVaultKey string                 `json:"credential_vault_key"`
		ProviderConfig     map[string]interface{} `json:"provider_config"`
		VelocityConfig     map[string]interface{} `json:"velocity_config"`
		// Section 5: 新增 adapter 配置字段
		AdapterType       string `json:"adapter_type"`
		AdapterProviderID string `json:"adapter_provider_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if req.ProviderConfig == nil {
		req.ProviderConfig = map[string]interface{}{}
	}
	if req.VelocityConfig == nil {
		req.VelocityConfig = map[string]interface{}{}
	}

	// Section 5: 空 adapter_type 默认为 UNSUPPORTED（与迁移 000050 DEFAULT 'UNSUPPORTED' 对齐）
	if req.AdapterType == "" {
		req.AdapterType = "UNSUPPORTED"
	}

	// G11: 调用统一 Validator
	if err := configvalidator.ValidateIssuingPool(&configvalidator.IssuingPoolConfig{
		EntityID:           req.EntityID,
		IssuingTier:        req.IssuingTier,
		FundingMode:        req.FundingMode,
		ProviderCode:       req.ProviderCode,
		PoolCode:           req.PoolCode,
		CredentialVaultKey: req.CredentialVaultKey,
		ProviderConfig:     req.ProviderConfig,
		VelocityConfig:     req.VelocityConfig,
		AdapterType:        req.AdapterType,
		AdapterProviderID:  req.AdapterProviderID,
	}); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	var entityUUID *uuid.UUID
	if req.EntityID != "" {
		id, _ := uuid.Parse(req.EntityID)
		entityUUID = &id
	}

	if _, exists := req.VelocityConfig["declined_count_sliding_window"]; !exists {
		req.VelocityConfig["declined_count_sliding_window"] = 5
	}

	if issuing.IssuingTier(req.IssuingTier) == issuing.IssuingTierAggregator {
		req.ProviderConfig["ACQUIRING_SETTLEMENT_BALANCE"] = "INITIALIZED"
		req.ProviderConfig["VCC_AVAILABLE_BALANCE"] = "INITIALIZED"
	}

	providerConfigJSON, _ := json.Marshal(req.ProviderConfig)
	velocityConfigJSON, _ := json.Marshal(req.VelocityConfig)

	// Section 5: 解析 adapter_provider_id（可为空）
	var adapterProviderUUID *uuid.UUID
	if req.AdapterProviderID != "" {
		pid, _ := uuid.Parse(req.AdapterProviderID)
		adapterProviderUUID = &pid
	}

	// R18 P0-3: 创建卡池时验证 adapter_provider_id 的 capability。
	// 如果指定了 adapter_provider_id，必须验证该 Provider 存在、ACTIVE、adapter_type=CARD_ISSUER。
	if adapterProviderUUID != nil {
		var apStatus, apAdapterType string
		err := h.pool.QueryRow(ctx,
			`SELECT status, adapter_type::text FROM adapter_provider WHERE id = $1`,
			adapterProviderUUID).Scan(&apStatus, &apAdapterType)
		if err != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("adapter_provider_id not found"))
			return
		}
		if apStatus != "ACTIVE" {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage(
				fmt.Sprintf("adapter_provider is not ACTIVE (current: %s)", apStatus)))
			return
		}
		if apAdapterType != "CARD_ISSUER" {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage(
				fmt.Sprintf("adapter_provider adapter_type must be CARD_ISSUER (current: %s)", apAdapterType)))
			return
		}
	}

	var id uuid.UUID
	// Section 5: 新创建的 provider 状态为 DISABLED（禁止创建即 ACTIVE，必须显式 enable 审批）
	// 同时写入 adapter_type / adapter_provider_id 列（迁移 000050 新增）
	err := h.pool.QueryRow(ctx,
		`INSERT INTO issuing_providers (
			entity_id, issuing_tier, funding_mode, provider_code, pool_code, 
			credential_vault_key, provider_config, velocity_config,
			adapter_type, adapter_provider_id, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'DISABLED') RETURNING id`,
		entityUUID, req.IssuingTier, req.FundingMode, req.ProviderCode, req.PoolCode,
		req.CredentialVaultKey, providerConfigJSON, velocityConfigJSON,
		req.AdapterType, adapterProviderUUID,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *V3IssuingPoolsHandler) UpdatePool(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid pool id"))
		return
	}

	var req struct {
		IssuingTier        string                 `json:"issuing_tier"`
		FundingMode        string                 `json:"funding_mode"`
		CredentialVaultKey string                 `json:"credential_vault_key"`
		ProviderConfig     map[string]interface{} `json:"provider_config"`
		VelocityConfig     map[string]interface{} `json:"velocity_config"`
		// Section 5: 新增 adapter 配置字段
		AdapterType       string `json:"adapter_type"`
		AdapterProviderID string `json:"adapter_provider_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	hasUpdate := req.IssuingTier != "" || req.FundingMode != "" || req.CredentialVaultKey != "" ||
		req.ProviderConfig != nil || req.VelocityConfig != nil ||
		req.AdapterType != "" || req.AdapterProviderID != ""
	if !hasUpdate {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("no update fields provided"))
		return
	}

	ctx := r.Context()

	// G11: PATCH 在 SELECT ... FOR UPDATE 下读取旧版本，合并完整对象后统一校验。
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback(ctx)

	var oldTier, oldFunding, oldProviderCode, oldPoolCode, oldCredKey string
	var oldProviderConfig, oldVelocityConfig []byte
	var oldAdapterType string
	var oldAdapterProviderID *string
	var currentVersion int
	err = tx.QueryRow(ctx, `
		SELECT issuing_tier, funding_mode, provider_code, pool_code,
		       credential_vault_key, provider_config::bytea, velocity_config::bytea, version,
		       adapter_type::text, adapter_provider_id::text
		FROM issuing_providers WHERE id = $1 FOR UPDATE`, id).Scan(
		&oldTier, &oldFunding, &oldProviderCode, &oldPoolCode,
		&oldCredKey, &oldProviderConfig, &oldVelocityConfig, &currentVersion,
		&oldAdapterType, &oldAdapterProviderID)
	if err != nil {
		writeErr(w, err)
		return
	}

	oldPC := map[string]interface{}{}
	if len(oldProviderConfig) > 0 {
		json.Unmarshal(oldProviderConfig, &oldPC)
	}
	oldVC := map[string]interface{}{}
	if len(oldVelocityConfig) > 0 {
		json.Unmarshal(oldVelocityConfig, &oldVC)
	}

	oldAdapterProviderIDStr := ""
	if oldAdapterProviderID != nil {
		oldAdapterProviderIDStr = *oldAdapterProviderID
	}

	oldCfg := &configvalidator.IssuingPoolConfig{
		IssuingTier:        oldTier,
		FundingMode:        oldFunding,
		ProviderCode:       oldProviderCode,
		PoolCode:           oldPoolCode,
		CredentialVaultKey: oldCredKey,
		ProviderConfig:     oldPC,
		VelocityConfig:     oldVC,
		AdapterType:        oldAdapterType,
		AdapterProviderID:  oldAdapterProviderIDStr,
	}
	patchCfg := &configvalidator.IssuingPoolConfig{
		IssuingTier:        req.IssuingTier,
		FundingMode:        req.FundingMode,
		CredentialVaultKey: req.CredentialVaultKey,
		ProviderConfig:     req.ProviderConfig,
		VelocityConfig:     req.VelocityConfig,
		AdapterType:        req.AdapterType,
		AdapterProviderID:  req.AdapterProviderID,
	}

	merged := configvalidator.MergeIssuingPatch(oldCfg, patchCfg)

	if err := configvalidator.ValidateIssuingPool(merged); err != nil {
		writeErr(w, err)
		return
	}

	// R18 P0-3: 更新卡池时验证 adapter_provider_id 的 capability。
	if merged.AdapterProviderID != "" {
		apID, parseErr := uuid.Parse(merged.AdapterProviderID)
		if parseErr != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid adapter_provider_id format"))
			return
		}
		var apStatus, apAdapterType string
		checkErr := tx.QueryRow(ctx,
			`SELECT status, adapter_type::text FROM adapter_provider WHERE id = $1`,
			apID).Scan(&apStatus, &apAdapterType)
		if checkErr != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("adapter_provider_id not found"))
			return
		}
		if apStatus != "ACTIVE" {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage(
				fmt.Sprintf("adapter_provider is not ACTIVE (current: %s)", apStatus)))
			return
		}
		if apAdapterType != "CARD_ISSUER" {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage(
				fmt.Sprintf("adapter_provider adapter_type must be CARD_ISSUER (current: %s)", apAdapterType)))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, err)
		return
	}

	// P0-9: 不直接 UPDATE issuing_providers，创建 PENDING 审批
	payload := map[string]interface{}{
		"pool_id":              idStr,
		"issuing_tier":         req.IssuingTier,
		"funding_mode":         req.FundingMode,
		"credential_vault_key": req.CredentialVaultKey,
		"provider_config":      req.ProviderConfig,
		"velocity_config":      req.VelocityConfig,
		"adapter_type":         req.AdapterType,
		"adapter_provider_id":  req.AdapterProviderID,
	}

	approvalID, err := h.svc.CreateApprovalRequest(
		ctx,
		ActionUpdateIssuingPool,
		TargetTypeIssuingPool,
		&idStr,
		&currentVersion,
		payload,
		"update issuing pool config",
		nil,
	)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"id":          id.String(),
		"approval_id": approvalID,
		"status":      "PENDING",
		"action_type": ActionUpdateIssuingPool,
		"message":     "已创建审批请求，待复核后生效",
	})
}

// EnablePool 创建启用卡池的审批请求（G08: 高危操作不直接执行）。
//
// G08 修复：原先直接 UPDATE status='ACTIVE'，现在改为创建 PENDING 审批请求，
// 由 approver 审批后通过 ApprovalService.ApplyTransaction 原子应用。
func (h *V3IssuingPoolsHandler) EnablePool(w http.ResponseWriter, r *http.Request) {
	h.createPoolStatusApproval(w, r, ActionEnableIssuingPool)
}

// DisablePool 创建禁用卡池的审批请求（G08: 高危操作不直接执行）。
//
// G08 修复：原先直接 UPDATE status='INACTIVE'，现在改为创建 PENDING 审批请求，
// 由 approver 审批后通过 ApprovalService.ApplyTransaction 原子应用。
func (h *V3IssuingPoolsHandler) DisablePool(w http.ResponseWriter, r *http.Request) {
	h.createPoolStatusApproval(w, r, ActionDisableIssuingPool)
}

// createPoolStatusApproval 创建卡池状态变更的审批请求。
func (h *V3IssuingPoolsHandler) createPoolStatusApproval(w http.ResponseWriter, r *http.Request, actionType string) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid pool id"))
		return
	}

	ctx := r.Context()

	// Section 5: 启用卡池前查询 adapter_type，禁止启用 UNSUPPORTED 适配器
	if actionType == ActionEnableIssuingPool {
		adapterType, err := h.getAdapterType(ctx, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		if adapterType == "UNSUPPORTED" {
			writeErr(w, apperr.ErrConfigInconsistent.WithMessage(
				"cannot activate UNSUPPORTED adapter type: configure adapter_type=NEXUS_ISSUING_HTTP_V1 or MOCK_LOCAL first"))
			return
		}
	}

	var currentVersion int
	err = h.pool.QueryRow(ctx, `SELECT version FROM issuing_providers WHERE id = $1`, id).Scan(&currentVersion)
	if err != nil {
		writeErr(w, apperr.ErrNotFound.WithMessage("pool not found"))
		return
	}

	payload := map[string]interface{}{
		"pool_id": id.String(),
	}

	approvalID, err := h.svc.CreateApprovalRequest(
		ctx,
		actionType,
		TargetTypeIssuingPool,
		&idStr,
		&currentVersion,
		payload,
		"pool status change: "+actionType,
		nil,
	)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"id":          id.String(),
		"approval_id": approvalID,
		"status":      "PENDING",
		"action_type": actionType,
		"message":     "已创建审批请求，待复核后生效",
	})
}

// getAdapterType 查询 issuing_providers.adapter_type，供启用审批前置校验使用。
// Section 5: UNSUPPORTED 适配器禁止启用，必须在创建启用审批前查询并拒绝。
func (h *V3IssuingPoolsHandler) getAdapterType(ctx context.Context, id uuid.UUID) (string, error) {
	var adapterType string
	err := h.pool.QueryRow(ctx,
		`SELECT adapter_type::text FROM issuing_providers WHERE id = $1`, id).Scan(&adapterType)
	if err != nil {
		return "", apperr.ErrNotFound.WithMessage("pool not found")
	}
	return adapterType, nil
}

// ListAdapterProviders 返回状态为 ACTIVE 且 adapter_type=CARD_ISSUER 的 adapter_provider 列表，供 Dashboard select 控件使用。
// R18 P0-3: 只返回 CARD_ISSUER capability 的 Provider，不允许绑定 CARD_ACQUIRER 等无关 Provider。
func (h *V3IssuingPoolsHandler) ListAdapterProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, provider_code, provider_name, adapter_type::text, status
		 FROM adapter_provider WHERE status = 'ACTIVE' AND adapter_type = 'CARD_ISSUER' ORDER BY provider_name`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, providerCode, providerName, adapterType, status string
		if err := rows.Scan(&id, &providerCode, &providerName, &adapterType, &status); err != nil {
			writeErr(w, err)
			return
		}
		items = append(items, map[string]interface{}{
			"id":            id,
			"provider_code": providerCode,
			"provider_name": providerName,
			"adapter_type":  adapterType,
			"status":        status,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3IssuingPoolsHandler) ReloadPool(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid pool id"))
		return
	}

	ctx := r.Context()
	var newVersion int
	err = h.pool.QueryRow(ctx, `UPDATE issuing_providers SET version = version + 1 WHERE id = $1 RETURNING version`, id).Scan(&newVersion)
	if err != nil {
		writeErr(w, err)
		return
	}

	h.publishConfigUpdate(id, newVersion)

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "reload_triggered", "version": newVersion})
}
