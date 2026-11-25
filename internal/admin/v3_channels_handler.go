package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/configvalidator"
)

type V3ChannelsHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	svc  *ApprovalService // G08: 高危操作委托给审批服务
}

func NewV3ChannelsHandler(pool *pgxpool.Pool, rdb *redis.Client) *V3ChannelsHandler {
	svc := NewApprovalService(pool, rdb)
	return &V3ChannelsHandler{pool: pool, rdb: rdb, svc: svc}
}

// NewV3ChannelsHandlerWithService 创建带注入审批服务的 handler（推荐路径）。
func NewV3ChannelsHandlerWithService(pool *pgxpool.Pool, rdb *redis.Client, svc *ApprovalService) *V3ChannelsHandler {
	return &V3ChannelsHandler{pool: pool, rdb: rdb, svc: svc}
}

func (h *V3ChannelsHandler) publishConfigUpdate(channelID uuid.UUID, version int) {
	if h.rdb != nil {
		payload := fmt.Sprintf("%s|%d", channelID.String(), version)
		h.rdb.Publish(context.Background(), "channel.config.updated", payload)
		h.rdb.Publish(context.Background(), "config:acquirer_channel:reload", payload)
	}
}

func (h *V3ChannelsHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// ADM-001: 对象级租户 scope
	// 凭证增强：LATERAL 取 tenant_acquirer_channel 对应 provider_credential_vault 最新版本（与运行时 loadBundle 取数口径一致）
	query := `SELECT ac.id::text, ac.entity_id::text, ce.name, ac.provider_tier, ac.provider_code, ac.channel_code,
		 ac.strategy_mode, ac.status, ac.version, ac.provider_config::text, ac.routing_config::text,
		 ac.created_at::text, ac.updated_at::text,
		 pv.key_version, pv.expires_at::text
		 FROM acquirer_channels ac
		 JOIN corporate_entity ce ON ce.id = ac.entity_id
		 LEFT JOIN tenant_acquirer_channel tac ON tac.acquirer_channel_id = ac.id AND tac.tenant_id=ce.tenant_id AND tac.merchant_id=ce.id
		 LEFT JOIN LATERAL (
			SELECT key_version, expires_at FROM provider_credential_vault
			WHERE provider_id = tac.provider_id AND channel_id = tac.id
			ORDER BY key_version DESC LIMIT 1
		 ) pv ON true`
	args := []interface{}{}
	if tenantID, scoped := tenantScopeOf(ctx); scoped {
		query += ` WHERE ce.tenant_id = $1`
		args = append(args, tenantID)
	}
	query += ` ORDER BY ac.created_at DESC`
	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, entityID, merchant, providerTier, providerCode, channelCode, strategyMode, status, providerConfig, routingConfig, created, updated string
		var version int
		var credVersion *int
		var credExpires *string
		if err := rows.Scan(&id, &entityID, &merchant, &providerTier, &providerCode, &channelCode, &strategyMode, &status, &version, &providerConfig, &routingConfig, &created, &updated, &credVersion, &credExpires); err != nil {
			writeErr(w, err)
			return
		}

		items = append(items, map[string]interface{}{
			"id":                    id,
			"entity_id":             entityID,
			"merchant_name":         merchant,
			"provider_tier":         providerTier,
			"provider_code":         providerCode,
			"channel_code":          channelCode,
			"strategy_mode":         strategyMode,
			"status":                status,
			"version":               version,
			"provider_config":       sanitizeJSONText(providerConfig),
			"routing_config":        sanitizeJSONText(routingConfig),
			"created_at":            created,
			"updated_at":            updated,
			"has_credential":        credVersion != nil,
			"credential_version":    credVersion,
			"credential_expires_at": credExpires,
		})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3ChannelsHandler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityID           string                 `json:"entity_id"`
		ProviderTier       string                 `json:"provider_tier"`
		ProviderCode       string                 `json:"provider_code"`
		ChannelCode        string                 `json:"channel_code"`
		CredentialVaultKey string                 `json:"credential_vault_key"`
		StrategyMode       string                 `json:"strategy_mode"`
		FundingMode        string                 `json:"funding_mode"`
		ProviderConfig     map[string]interface{} `json:"provider_config"`
		RoutingConfig      map[string]interface{} `json:"routing_config"`
		// P0-2: tenant_id 和 merchant_id 必须提供，用于在同一事务中创建 tenant_acquirer_channel 记录
		TenantID   string `json:"tenant_id"`
		MerchantID string `json:"merchant_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	// P0-2: tenant_id 和 merchant_id 必须提供
	if req.TenantID == "" || req.MerchantID == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("tenant_id and merchant_id are required"))
		return
	}
	if tenant, scoped := tenantScopeOf(r.Context()); scoped && req.TenantID != tenant {
		writeErr(w, apperr.ErrForbidden.WithMessage("merchant is outside the authenticated account"))
		return
	}

	controlStatus := "ACTIVE"
	if req.CredentialVaultKey == "" {
		req.CredentialVaultKey = "vault://pending/" + uuid.NewString()
		controlStatus = "INACTIVE"
	}

	// G11: 调用统一 Validator（POST/PATCH/审批/runtime 共用同一 pure function）
	if req.ProviderConfig == nil {
		req.ProviderConfig = map[string]interface{}{}
	}
	if req.RoutingConfig == nil {
		req.RoutingConfig = map[string]interface{}{}
	}

	// P0-2: 规范化顶层 funding_mode 到 routing_config.vcc_funding_mode（唯一持久化事实源）。
	// POST 是全量创建、无旧值，冲突检查+写入在此完成后由 Validate 统一校验 enum。
	normCfg := &configvalidator.AcquirerChannelConfig{
		FundingMode:   req.FundingMode,
		RoutingConfig: req.RoutingConfig,
	}
	if err := configvalidator.NormalizeAcquirerFundingMode(normCfg); err != nil {
		writeErr(w, err)
		return
	}
	req.RoutingConfig = normCfg.RoutingConfig

	if err := configvalidator.ValidateAcquirerChannel(&configvalidator.AcquirerChannelConfig{
		EntityID:           req.EntityID,
		ProviderTier:       req.ProviderTier,
		ProviderCode:       req.ProviderCode,
		ChannelCode:        req.ChannelCode,
		CredentialVaultKey: req.CredentialVaultKey,
		StrategyMode:       req.StrategyMode,
		FundingMode:        req.FundingMode,
		ProviderConfig:     req.ProviderConfig,
		RoutingConfig:      req.RoutingConfig,
	}); err != nil {
		writeErr(w, err)
		return
	}

	tenantUUID, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid tenant_id"))
		return
	}
	merchantUUID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}

	ctx := r.Context()
	entityUUID, _ := uuid.Parse(req.EntityID)

	// P0-2: 在事务内读取并锁定 merchant_routing_policy，防止并发策略变更竞态
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback(ctx)

	// P0-2: FOR UPDATE 锁定 policy 行，防止并发修改（比 FOR SHARE 更严格，确保事务内可安全写入依赖行）
	var policyTier, policyStatus string
	err = tx.QueryRow(ctx,
		`SELECT provider_tier, status FROM merchant_routing_policy WHERE merchant_id = $1 FOR UPDATE`, merchantUUID).Scan(&policyTier, &policyStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("merchant has no routing policy, create one first"))
			return
		}
		writeErr(w, err)
		return
	}
	if policyStatus != "ACTIVE" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("merchant routing policy is not ACTIVE"))
		return
	}
	if domain.ProviderTier(policyTier) != domain.ProviderTier(req.ProviderTier) {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("channel provider_tier does not match merchant routing policy tier"))
		return
	}

	// P0-2: 跨租户绑定防护 — entity_id 必须等于 merchant_id（corporate_entity.id = merchant_id）
	if req.EntityID != req.MerchantID {
		writeErr(w, apperr.ErrConfigInconsistent.WithMessage(fmt.Sprintf("entity_id must equal merchant_id (corporate_entity.id), got entity_id=%s merchant_id=%s", req.EntityID, req.MerchantID)))
		return
	}

	// P0-2: 跨租户绑定防护 — tenant_id 必须与 corporate_entity.tenant_id 一致
	var entityTenantID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT tenant_id FROM corporate_entity WHERE id = $1 FOR UPDATE`, merchantUUID).Scan(&entityTenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("corporate_entity not found for merchant_id=%s", req.MerchantID)))
			return
		}
		writeErr(w, err)
		return
	}
	if entityTenantID != tenantUUID {
		writeErr(w, apperr.ErrConfigInconsistent.WithMessage(fmt.Sprintf("tenant_id does not match corporate_entity.tenant_id, got request_tenant_id=%s entity_tenant_id=%s", req.TenantID, entityTenantID.String())))
		return
	}

	if domain.ProviderTier(req.ProviderTier) == domain.ProviderTierHighRisk {
		req.ProviderConfig["ACQUIRING_SETTLEMENT_BALANCE"] = "INITIALIZED"
		req.ProviderConfig["VCC_AVAILABLE_BALANCE"] = "INITIALIZED"
	}

	providerConfigJSON, _ := json.Marshal(req.ProviderConfig)
	routingConfigJSON, _ := json.Marshal(req.RoutingConfig)

	// P0-2: 在一个事务内创建 acquirer_channels 和 tenant_acquirer_channel，任一步失败整体回滚

	var id uuid.UUID
	err = tx.QueryRow(ctx,
		`INSERT INTO acquirer_channels (
			entity_id, provider_tier, provider_code, channel_code, 
			credential_vault_key, strategy_mode, provider_config, routing_config, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		entityUUID, req.ProviderTier, req.ProviderCode, req.ChannelCode,
		req.CredentialVaultKey, req.StrategyMode, providerConfigJSON, routingConfigJSON, controlStatus,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	// The global adapter catalogue and acquiring registry are separate tables.
	// Provision only built-in providers here so a fresh installation can add
	// its first account. This does not provision credentials or activate it.
	if name, supported := map[string]string{"stripe": "Stripe", "nxtapay": "Nxtapay"}[req.ProviderCode]; supported {
		if _, err := tx.Exec(ctx, `INSERT INTO payment_provider (provider_code, provider_name, provider_type)
			VALUES ($1, $2, 'CARD') ON CONFLICT (provider_code) DO NOTHING`, req.ProviderCode, name); err != nil {
			writeErr(w, err)
			return
		}
	}
	var providerID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM payment_provider WHERE provider_code = $1`, req.ProviderCode).Scan(&providerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("此支付机构还未完成系统接入"))
			return
		}
		writeErr(w, fmt.Errorf("find payment_provider by code: %w", err))
		return
	}

	currencies, err := configvalidator.AcquirerCurrencies(req.ProviderConfig)
	if err != nil {
		writeErr(w, err)
		return
	}
	if currencies == nil {
		currencies = []string{"USD"}
	}
	currenciesJSON, _ := json.Marshal(currencies)

	// P0-5: 在事务内创建 tenant_acquirer_channel（带 acquirer_channel_id 绑定）
	// 移除 ON CONFLICT DO UPDATE — 禁止静默重新绑定，冲突时返回错误并回滚事务
	// P0-4: 基于已通过 Validator 的请求构造完整 AcquirerChannelConfig，派生 runtime 状态
	createCfg := &configvalidator.AcquirerChannelConfig{
		ProviderTier:       req.ProviderTier,
		ProviderCode:       req.ProviderCode,
		ChannelCode:        req.ChannelCode,
		CredentialVaultKey: req.CredentialVaultKey,
		StrategyMode:       req.StrategyMode,
		FundingMode:        req.FundingMode,
		ProviderConfig:     req.ProviderConfig,
		RoutingConfig:      req.RoutingConfig,
	}
	runtimeConfig, runtimeIsActive := buildRuntimeChannelConfig(createCfg, controlStatus)
	runtimeConfigJSON, _ := json.Marshal(runtimeConfig)
	_, err = tx.Exec(ctx,
		`INSERT INTO tenant_acquirer_channel (tenant_id, merchant_id, channel_code, provider_id, currencies, channel_config, is_active, acquirer_channel_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		tenantUUID, merchantUUID, req.ChannelCode, providerID, currenciesJSON, runtimeConfigJSON, runtimeIsActive, id)
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, apperr.ErrIdempotencyConflict.WithMessage("tenant_acquirer_channel already exists for this tenant/merchant/channel_code combination"))
			return
		}
		writeErr(w, fmt.Errorf("create tenant_acquirer_channel: %w", err))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *V3ChannelsHandler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid channel id"))
		return
	}

	var req struct {
		ProviderTier       string                 `json:"provider_tier"`
		CredentialVaultKey string                 `json:"credential_vault_key"`
		StrategyMode       string                 `json:"strategy_mode"`
		FundingMode        string                 `json:"funding_mode"`
		ProviderConfig     map[string]interface{} `json:"provider_config"`
		RoutingConfig      map[string]interface{} `json:"routing_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	// 检查是否有至少一个更新字段
	hasUpdate := req.ProviderTier != "" || req.StrategyMode != "" || req.FundingMode != "" ||
		req.CredentialVaultKey != "" || req.ProviderConfig != nil || req.RoutingConfig != nil
	if !hasUpdate {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("no update fields provided"))
		return
	}

	ctx := r.Context()

	// G11: PATCH 必须在 SELECT ... FOR UPDATE 下读取旧版本，
	// 合并完整对象后用统一 Validator 校验，不能只验证 patch 片段。
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback(ctx)

	// 读取旧版本（FOR UPDATE 锁定行）
	var oldTier, oldStrategy, oldProviderCode, oldChannelCode, oldCredKey string
	var oldProviderConfig, oldRoutingConfig []byte
	var currentVersion int
	err = tx.QueryRow(ctx, `
		SELECT provider_tier, strategy_mode, provider_code, channel_code,
		       credential_vault_key, provider_config::bytea, routing_config::bytea, version
		FROM acquirer_channels WHERE id = $1 FOR UPDATE`, id).Scan(
		&oldTier, &oldStrategy, &oldProviderCode, &oldChannelCode,
		&oldCredKey, &oldProviderConfig, &oldRoutingConfig, &currentVersion)
	if err != nil {
		writeErr(w, err)
		return
	}

	// 解析旧 JSON
	oldPC := map[string]interface{}{}
	if len(oldProviderConfig) > 0 {
		json.Unmarshal(oldProviderConfig, &oldPC)
	}
	oldRC := map[string]interface{}{}
	if len(oldRoutingConfig) > 0 {
		json.Unmarshal(oldRoutingConfig, &oldRC)
	}
	// 旧 funding_mode 从 routing_config.vcc_funding_mode 读取
	oldFunding, _ := oldRC["vcc_funding_mode"].(string)

	// 构造旧配置和 patch 配置
	oldCfg := &configvalidator.AcquirerChannelConfig{
		ProviderTier:       oldTier,
		ProviderCode:       oldProviderCode,
		ChannelCode:        oldChannelCode,
		CredentialVaultKey: oldCredKey,
		StrategyMode:       oldStrategy,
		FundingMode:        oldFunding,
		ProviderConfig:     oldPC,
		RoutingConfig:      oldRC,
	}
	// P0-2: 顶层 funding_mode 唯一持久化事实源是 routing_config["vcc_funding_mode"]。
	// patch 只带顶层 funding_mode（RoutingConfig==nil）时：深拷贝 oldRC，写入新 vcc_funding_mode，
	// 保留旧 routing 字段（risk_isolation_group_id 等），赋给 req.RoutingConfig。
	// patch 显式带 routing_config 时：Normalize 拒绝请求内 top/nested 冲突。
	if req.FundingMode != "" && req.RoutingConfig == nil {
		copiedRouting := make(map[string]interface{}, len(oldRC)+1)
		for k, v := range oldRC {
			copiedRouting[k] = v
		}
		copiedRouting["vcc_funding_mode"] = req.FundingMode
		req.RoutingConfig = copiedRouting
	} else if req.RoutingConfig != nil {
		patchNorm := &configvalidator.AcquirerChannelConfig{
			FundingMode:   req.FundingMode,
			RoutingConfig: req.RoutingConfig,
		}
		if err := configvalidator.NormalizeAcquirerFundingMode(patchNorm); err != nil {
			writeErr(w, err)
			return
		}
		req.RoutingConfig = patchNorm.RoutingConfig
	}

	patchCfg := &configvalidator.AcquirerChannelConfig{
		ProviderTier:       req.ProviderTier,
		StrategyMode:       req.StrategyMode,
		FundingMode:        req.FundingMode,
		CredentialVaultKey: req.CredentialVaultKey,
		ProviderConfig:     req.ProviderConfig,
		RoutingConfig:      req.RoutingConfig,
	}

	// 合并 patch 到旧配置
	merged := configvalidator.MergeAcquirerPatch(oldCfg, patchCfg)

	// 统一 Validator 校验合并后的完整对象
	if err := configvalidator.ValidateAcquirerChannel(merged); err != nil {
		writeErr(w, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, err)
		return
	}

	// P0-9: 不直接 UPDATE acquirer_channels，创建 PENDING 审批
	payload := map[string]interface{}{
		"channel_id":           idStr,
		"provider_tier":        req.ProviderTier,
		"strategy_mode":        req.StrategyMode,
		"funding_mode":         req.FundingMode,
		"credential_vault_key": req.CredentialVaultKey,
		"provider_config":      req.ProviderConfig,
		"routing_config":       req.RoutingConfig,
	}

	approvalID, err := h.svc.CreateApprovalRequest(
		ctx,
		ActionUpdateAcquirerChannel,
		TargetTypeAcquirerChannel,
		&idStr,
		&currentVersion,
		payload,
		"update acquirer channel config",
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
		"action_type": ActionUpdateAcquirerChannel,
		"message":     "已创建审批请求，待复核后生效",
	})
}

// EnableChannel 创建启用通道的审批请求（G08: 高危操作不直接执行）。
//
// G08 修复：原先直接 UPDATE status='ACTIVE'，现在改为创建 PENDING 审批请求，
// 由 approver 审批后通过 ApprovalService.ApplyTransaction 原子应用。
func (h *V3ChannelsHandler) EnableChannel(w http.ResponseWriter, r *http.Request) {
	h.createChannelStatusApproval(w, r, ActionEnableAcquirerChannel)
}

// DisableChannel 创建禁用通道的审批请求（G08: 高危操作不直接执行）。
//
// G08 修复：原先直接 UPDATE status='INACTIVE'，现在改为创建 PENDING 审批请求，
// 由 approver 审批后通过 ApprovalService.ApplyTransaction 原子应用。
func (h *V3ChannelsHandler) DisableChannel(w http.ResponseWriter, r *http.Request) {
	h.createChannelStatusApproval(w, r, ActionDisableAcquirerChannel)
}

// createChannelStatusApproval 创建通道状态变更的审批请求。
func (h *V3ChannelsHandler) createChannelStatusApproval(w http.ResponseWriter, r *http.Request, actionType string) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid channel id"))
		return
	}

	ctx := r.Context()

	// 读取当前版本（用于版本锁）
	var currentVersion int
	err = h.pool.QueryRow(ctx, `SELECT version FROM acquirer_channels WHERE id = $1`, id).Scan(&currentVersion)
	if err != nil {
		writeErr(w, apperr.ErrNotFound.WithMessage("channel not found"))
		return
	}

	// 构造 typed payload
	payload := map[string]interface{}{
		"channel_id": id.String(),
	}

	// 委托给审批服务创建 PENDING
	approvalID, err := h.svc.CreateApprovalRequest(
		ctx,
		actionType,
		TargetTypeAcquirerChannel,
		&idStr,
		&currentVersion,
		payload,
		"channel status change: "+actionType,
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

func (h *V3ChannelsHandler) ReloadChannel(w http.ResponseWriter, r *http.Request) {
	tenant, ok := requireAdminTenant(w, r)
	if !ok {
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid channel id"))
		return
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback(ctx)

	// P0-2: 锁定 control channel FOR UPDATE，防止并发修改
	var merchantID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT ac.entity_id FROM acquirer_channels ac
		 JOIN corporate_entity ce ON ce.id=ac.entity_id AND ce.tenant_id=$2
		 WHERE ac.id=$1 FOR UPDATE OF ac`, id, tenant).Scan(&merchantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, apperr.ErrNotFound.WithMessage("control channel not found: "+idStr))
			return
		}
		writeErr(w, err)
		return
	}

	// P0-2: 锁定唯一 runtime binding FOR UPDATE，防止并发修改
	var runtimeChannelID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM tenant_acquirer_channel WHERE acquirer_channel_id=$1
		 AND tenant_id=$2 AND merchant_id=$3 FOR UPDATE`, id, tenant, merchantID).Scan(&runtimeChannelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, apperr.ErrNotFound.WithMessage("no runtime channel bound to acquirer_channel_id: "+idStr))
			return
		}
		writeErr(w, err)
		return
	}

	// P0-2: 同时更新 control 和 runtime version
	var newControlVersion int
	err = tx.QueryRow(ctx,
		`UPDATE acquirer_channels SET version = version + 1, updated_at = now() WHERE id = $1 RETURNING version`,
		id).Scan(&newControlVersion)
	if err != nil {
		writeErr(w, err)
		return
	}

	var newRuntimeVersion int
	err = tx.QueryRow(ctx,
		`UPDATE tenant_acquirer_channel SET version = version + 1, updated_at = now() WHERE id = $1 RETURNING version`,
		runtimeChannelID).Scan(&newRuntimeVersion)
	if err != nil {
		writeErr(w, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, err)
		return
	}

	// P0-2: 发布 runtime_channel_id|runtime_version
	h.publishConfigUpdate(runtimeChannelID, newRuntimeVersion)

	writeJSON(w, map[string]interface{}{
		"id":                 id.String(),
		"runtime_channel_id": runtimeChannelID.String(),
		"status":             "reload_triggered",
		"control_version":    newControlVersion,
		"runtime_version":    newRuntimeVersion,
	})
}

// joinWithComma 用逗号连接字符串切片。
func joinWithComma(items []string) string {
	if len(items) == 0 {
		return ""
	}
	result := items[0]
	for i := 1; i < len(items); i++ {
		result += ", " + items[i]
	}
	return result
}

// joinWithAnd 将 WHERE 条件用 AND 连接（ADM-018：多条件 WHERE 禁止逗号拼接）。
func joinWithAnd(items []string) string {
	if len(items) == 0 {
		return ""
	}
	result := items[0]
	for i := 1; i < len(items); i++ {
		result += " AND " + items[i]
	}
	return result
}
