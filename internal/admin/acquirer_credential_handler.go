// Package admin — acquirer_credential_handler.go 实现收单通道 Vault 凭证写入 API。
//
// 可视化凭证管理闭环（收单侧）：
//   - Dashboard「填凭证」弹窗提交明文凭证 → 本 API 用 KMS 加密入 provider_credential_vault
//   - ciphertext 列保存 crypto.Encrypt 完整信封（NXE2 自包含格式），
//     与 acquirer.registry.loadBundle 的 Decrypt(cred.Ciphertext) 直接解密格式一致（不拆 nonce）
//   - 解密 Meta 与 loadBundle 消费格式严格一致：
//     RecordID = tenant_acquirer_channel.id、FieldName = credential_type 原样大写
//   - 写入即绑定：同事务更新 acquirer_channels.credential_vault_key 并 bump control/runtime version
//   - 重复写入即轮换：key_version = MAX+1（advisory lock + 事务内重验 + 唯一约束兜底）
//   - provider_credential_vault 无 status 列：运行时按 ORDER BY key_version DESC LIMIT 1 天然取最新
//   - Commit 后发布 Redis 配置更新通知（channel.config.updated + config:acquirer_channel:reload）
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// AcquirerCredentialWriteRequest 是收单通道凭证写入 API 的请求体。
// credential_material 是单段明文（API Key / Secret / 私钥 PEM / 回调密钥）。
type AcquirerCredentialWriteRequest struct {
	CredentialType     string `json:"credential_type"`
	CredentialMaterial string `json:"credential_material"`
	ExpectedVersion    *int   `json:"expected_version,omitempty"`
}

// AcquirerCredentialWriteResponse 是收单通道凭证写入 API 的响应体。
// 只返回 vault:// 引用和非敏感元数据，不回显秘密。
type AcquirerCredentialWriteResponse struct {
	VaultKey         string `json:"vault_key"`
	RecordID         string `json:"record_id"`
	KeyVersion       int    `json:"key_version"`
	CredentialType   string `json:"credential_type"`
	CreatedAt        string `json:"created_at"`
	ControlVersion   int    `json:"control_version"`
	RuntimeVersion   int    `json:"runtime_version"`
	RuntimeChannelID string `json:"runtime_channel_id"`
}

// acquirerCredentialTypes 是运行时 loadBundle 实际消费的凭证类型白名单
// （CERTIFICATE 不被 registry 消费，不接受写入）。
var acquirerCredentialTypes = map[string]bool{
	string(domain.CredentialTypeAPIKey):        true,
	string(domain.CredentialTypeSecret):        true,
	string(domain.CredentialTypePrivateKey):    true,
	string(domain.CredentialTypeWebhookSecret): true,
}

// AcquirerCredentialHandler 处理收单通道凭证写入请求。
type AcquirerCredentialHandler struct {
	db  vaultDBConn
	enc vaultEncryptor
	rdb *redis.Client
}

// NewAcquirerCredentialHandler 创建收单通道凭证写入 handler。
// Encryptor 缺失时（production/real）返回 nil，调用方不得注册路由（fail-closed）。
func NewAcquirerCredentialHandler(pool *pgxpool.Pool, enc *crypto.Encryptor, rdb *redis.Client) *AcquirerCredentialHandler {
	handler := &AcquirerCredentialHandler{rdb: rdb}
	if pool != nil {
		handler.db = pool
	}
	if enc != nil {
		handler.enc = enc
	}
	return handler
}

// publishConfigUpdate 复刻 V3ChannelsHandler 的通知格式：runtime_channel_id|version。
func (h *AcquirerCredentialHandler) publishConfigUpdate(channelID uuid.UUID, version int) {
	if h.rdb != nil {
		payload := fmt.Sprintf("%s|%d", channelID.String(), version)
		h.rdb.Publish(context.Background(), "channel.config.updated", payload)
		h.rdb.Publish(context.Background(), "config:acquirer_channel:reload", payload)
	}
}

// WriteCredential 处理 POST /admin/api/v3/channels/{id}/credentials 请求（id = acquirer_channels.id）。
func (h *AcquirerCredentialHandler) WriteCredential(w http.ResponseWriter, r *http.Request) {
	// 1. 身份从已验证 Context 获取
	identity, ok := httpx.GetAdminIdentity(r.Context())
	if !ok || identity == nil || !identity.HasPermission(httpx.PermAdminCredentialWrite) {
		httpx.WriteError(w, apperr.ErrForbidden.WithMessage("admin identity not found in authenticated context"))
		return
	}
	adminTenant, err := uuid.Parse(identity.TenantID)
	if err != nil || adminTenant == uuid.Nil {
		httpx.WriteError(w, apperr.ErrForbidden.WithMessage("valid tenant scope required"))
		return
	}
	if h.db == nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("credential database unavailable"))
		return
	}

	// 2. 解析 URL 通道 ID（控制面 acquirer_channels.id）
	channelID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("invalid channel id"))
		return
	}

	// 3. 解析请求体（write-only：拒绝未知字段）
	var req AcquirerCredentialWriteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	// 4. 校验必填字段与凭证类型白名单
	if req.CredentialType == "" {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("credential_type is required"))
		return
	}
	if !acquirerCredentialTypes[req.CredentialType] {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage(
			"credential_type must be one of API_KEY, SECRET, PRIVATE_KEY, WEBHOOK_SECRET"))
		return
	}
	if strings.TrimSpace(req.CredentialMaterial) == "" {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("credential_material is required and must be non-empty"))
		return
	}

	// 5. 校验 Encryptor 存在（fail-closed）
	if h.enc == nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("vault encryptor not available (fail-closed)"))
		return
	}

	ctx := r.Context()

	// 6. 从 DB 推导 tenant、entity、runtime 绑定（信任链：通道 → 实体 → 租户）
	var entityID, tenantID, runtimeChannelID *uuid.UUID
	var providerCode, channelStatus string
	err = h.db.QueryRow(ctx, `
		SELECT ac.provider_code, ac.entity_id, ce.tenant_id, ac.status
		FROM acquirer_channels ac
		JOIN corporate_entity ce ON ac.entity_id = ce.id
		WHERE ac.id = $1 AND ce.tenant_id = $2`, channelID, adminTenant).Scan(&providerCode, &entityID, &tenantID, &channelStatus)
	if err != nil {
		if err == pgx.ErrNoRows {
			httpx.WriteError(w, apperr.ErrNotFound.WithMessage("acquirer channel not found"))
			return
		}
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to load acquirer channel"))
		return
	}
	if tenantID == nil || *tenantID == uuid.Nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("entity has no tenant_id"))
		return
	}
	if !canStoreAcquirerCredential(channelStatus) {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage(
			fmt.Sprintf("acquirer channel cannot accept credentials (status=%s)", channelStatus)))
		return
	}

	// 6b. 派生 payment_provider（provider_code 唯一定位）
	var providerID uuid.UUID
	err = h.db.QueryRow(ctx,
		`SELECT id FROM payment_provider WHERE provider_code = $1`, providerCode).Scan(&providerID)
	if err != nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage(
			fmt.Sprintf("payment_provider not found for provider_code=%s", providerCode)))
		return
	}

	// 6c. 派生 runtime 绑定（1:1：tenant_acquirer_channel.acquirer_channel_id）
	err = h.db.QueryRow(ctx,
		`SELECT id FROM tenant_acquirer_channel
		  WHERE acquirer_channel_id = $1 AND tenant_id = $2 AND merchant_id = $3 AND provider_id = $4`,
		channelID, adminTenant, *entityID, providerID).Scan(&runtimeChannelID)
	if err != nil {
		if err == pgx.ErrNoRows {
			httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("no runtime channel bound to acquirer_channel_id"))
			return
		}
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to load runtime channel binding"))
		return
	}

	// 7. 事务外预查当前最大 key_version，新版本 = MAX + 1
	// （并发写入由步骤 10a advisory lock + 10b 版本重验 + 唯一约束兜底）
	var maxVersion int
	err = h.db.QueryRow(ctx,
		`SELECT COALESCE(MAX(key_version), 0) FROM provider_credential_vault
		 WHERE provider_id = $1 AND channel_id = $2 AND credential_type = $3`,
		providerID, *runtimeChannelID, req.CredentialType).Scan(&maxVersion)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to query current credential version"))
		return
	}
	if req.ExpectedVersion != nil && *req.ExpectedVersion != maxVersion {
		httpx.WriteError(w, apperr.ErrIdempotencyConflict.WithMessage("credential version changed; refresh before editing"))
		return
	}
	keyVersion := maxVersion + 1

	// 8. KMS 加密发生在 DB 事务外；ciphertext 保存完整信封（不拆 nonce）
	// Meta 与 loadBundle 消费格式严格一致：RecordID=runtimeChannelID、FieldName=类型大写原样
	meta := crypto.Meta{
		TenantID:   tenantID.String(),
		RecordID:   runtimeChannelID.String(),
		FieldName:  req.CredentialType,
		KeyVersion: fmt.Sprintf("%d", keyVersion),
	}
	encrypted, err := h.enc.Encrypt(ctx, []byte(req.CredentialMaterial), meta)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("vault encryption failed (fail-closed)"))
		return
	}
	canonicalAAD := fmt.Sprintf("%s|%s|%s|%d", tenantID.String(), runtimeChannelID.String(), req.CredentialType, keyVersion)

	// 9. 写入事务：锁定 control + runtime + entity，重验后原子写入并绑定
	tx, err := h.db.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to begin transaction"))
		return
	}
	defer tx.Rollback(ctx)

	// 10a. 同通道+类型的并发写入用事务级 advisory lock 串行化
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		channelID.String()+"|"+req.CredentialType)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to acquire credential write lock"))
		return
	}

	// 10b. 重锁 control 通道并重验 status/provider_code 未变化（FOR UPDATE）
	var recheckProviderCode, recheckStatus string
	err = tx.QueryRow(ctx, `
		SELECT provider_code, status FROM acquirer_channels WHERE id = $1 AND entity_id = $2 FOR UPDATE`,
		channelID, *entityID).Scan(&recheckProviderCode, &recheckStatus)
	if err != nil {
		if err == pgx.ErrNoRows {
			httpx.WriteError(w, apperr.ErrNotFound.WithMessage("acquirer channel not found at write time"))
			return
		}
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to relock acquirer channel"))
		return
	}
	if recheckStatus != channelStatus || !canStoreAcquirerCredential(recheckStatus) {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage(
			fmt.Sprintf("acquirer channel status changed to %s during write", recheckStatus)))
		return
	}
	if recheckProviderCode != providerCode {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("provider_code changed during write"))
		return
	}

	// 10c. 重锁 runtime 绑定并重验 1:1 关系未变化（FOR UPDATE）
	var recheckRuntimeID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM tenant_acquirer_channel
		  WHERE acquirer_channel_id = $1 AND tenant_id = $2 AND merchant_id = $3 AND provider_id = $4 FOR UPDATE`,
		channelID, adminTenant, *entityID, providerID).Scan(&recheckRuntimeID)
	if err != nil {
		if err == pgx.ErrNoRows {
			httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("runtime channel binding missing at write time"))
			return
		}
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to relock runtime channel binding"))
		return
	}
	if recheckRuntimeID != *runtimeChannelID {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("runtime channel binding changed during write"))
		return
	}

	// 10d. 重验 corporate_entity 权威 tenant 未变化（FOR SHARE）
	var recheckTenantID *uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT tenant_id FROM corporate_entity WHERE id = $1 FOR SHARE`,
		*entityID).Scan(&recheckTenantID)
	if err != nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("entity not found at write time"))
		return
	}
	if recheckTenantID == nil || *recheckTenantID != *tenantID {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("tenant changed during write"))
		return
	}

	// 10e. 事务内重验 MAX(key_version) 未变化（>= keyVersion 说明并发已写入，回滚重试）
	var recheckMaxVersion int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(key_version), 0) FROM provider_credential_vault
		 WHERE provider_id = $1 AND channel_id = $2 AND credential_type = $3`,
		providerID, *runtimeChannelID, req.CredentialType).Scan(&recheckMaxVersion)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to recheck credential version"))
		return
	}
	if recheckMaxVersion >= keyVersion {
		httpx.WriteError(w, apperr.ErrIdempotencyConflict.WithMessage(
			fmt.Sprintf("credential version conflict: expected next version %d, but version %d already exists", keyVersion, recheckMaxVersion)))
		return
	}

	// 11. INSERT provider_credential_vault（唯一约束 (provider,channel,type,version) 兜底）
	_, err = tx.Exec(ctx, `
		INSERT INTO provider_credential_vault (
			provider_id, channel_id, credential_type, key_version,
			ciphertext, aad_context
		) VALUES ($1, $2, $3, $4, $5, $6)`,
		providerID, *runtimeChannelID, req.CredentialType, keyVersion,
		encrypted, canonicalAAD)
	if err != nil {
		if isUniqueViolation(err) {
			httpx.WriteError(w, apperr.ErrIdempotencyConflict.WithMessage(
				fmt.Sprintf("credential version %d already exists for this channel/type", keyVersion)))
			return
		}
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to insert vault credential"))
		return
	}

	// 11b. 写入即绑定：credential_vault_key 原子指向新版本引用，control version bump
	vaultKey := fmt.Sprintf("vault://%s/%s/%s/%d", tenantID.String(), runtimeChannelID.String(), req.CredentialType, keyVersion)
	var newControlVersion int
	err = tx.QueryRow(ctx,
		`UPDATE acquirer_channels SET credential_vault_key = $2, version = version + 1, updated_at = now()
		 WHERE id = $1 RETURNING version`,
		channelID, vaultKey).Scan(&newControlVersion)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to bind vault credential to channel"))
		return
	}

	// 11c. runtime version bump（触发运行时重载凭证）
	var newRuntimeVersion int
	err = tx.QueryRow(ctx,
		`UPDATE tenant_acquirer_channel SET version = version + 1, updated_at = now()
		 WHERE id = $1 RETURNING version`,
		*runtimeChannelID).Scan(&newRuntimeVersion)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to bump runtime channel version"))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to commit vault credential write"))
		return
	}

	// 12. 发布 Redis 配置更新通知（运行时重载 bundle，读取最新 key_version）
	h.publishConfigUpdate(*runtimeChannelID, newRuntimeVersion)

	// 13. 响应只返回 vault:// 引用和非敏感元数据
	resp := AcquirerCredentialWriteResponse{
		VaultKey:         vaultKey,
		RecordID:         runtimeChannelID.String(),
		KeyVersion:       keyVersion,
		CredentialType:   req.CredentialType,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ControlVersion:   newControlVersion,
		RuntimeVersion:   newRuntimeVersion,
		RuntimeChannelID: runtimeChannelID.String(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}
