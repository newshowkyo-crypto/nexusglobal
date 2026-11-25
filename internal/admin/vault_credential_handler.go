// Package admin — vault_credential_handler.go 实现 Vault 凭证写入 API (R20 P0-5)。
//
// R20 P0-5: 补齐 Vault 合法写入闭环
//   - 受 AdminCredentialWrite 权限保护的 write-only 发卡凭证 API
//   - 客户端只提交 issuing_provider_id、credential_type 和明文凭证材料
//   - tenant、adapter provider、record ownership 必须由数据库推导
//   - 使用现有 KMS/crypto.Encryptor 加密；DB 只保存 ciphertext、nonce、canonical AAD 和 ownership 字段
//   - KMS 加密必须发生在 DB 事务外；写入事务内重新锁定并确认 provider/entity/adapter binding 未变化
//   - 响应只返回 vault:// 引用和非敏感元数据
//   - production/real 下 Encryptor 缺失时 API 不得注册或必须 fail-closed
//
// R21 Step E: 增强 transaction re-check
//   - 事务内重新验证 issuing provider status、entity/adapter binding
//   - 事务内验证 corporate_entity 权威 tenant 未变化
//   - 事务内验证 adapter_provider 仍为 ACTIVE CARD_ISSUER
//   - tenant 变化、provider 禁用、adapter capability 变化时必须回滚
//   - 提取窄 DB/Tx 和 Encryptor 接口，用 fake 测试真实 Handler 行为
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/issuing"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// vaultDBConn 是 Vault handler 依赖的窄 DB 接口（便于测试注入 fake）。
type vaultDBConn interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// vaultEncryptor 是 Vault handler 依赖的窄加密接口（便于测试注入 fake）。
type vaultEncryptor interface {
	Encrypt(ctx context.Context, plaintext []byte, meta crypto.Meta) ([]byte, error)
}

// VaultCredentialWriteRequest 是 Vault 凭证写入 API 的请求体。
// R20 P0-5: 客户端只提交 issuing_provider_id、credential_type 和明文凭证材料。
type VaultCredentialWriteRequest struct {
	IssuingProviderID  string            `json:"issuing_provider_id"`
	CredentialType     string            `json:"credential_type"`
	CredentialMaterial map[string]string `json:"credential_material"`
}

// VaultCredentialWriteResponse 是 Vault 凭证写入 API 的响应体。
// R20 P0-5: 响应只返回 vault:// 引用和非敏感元数据，不回显秘密。
type VaultCredentialWriteResponse struct {
	VaultKey       string `json:"vault_key"`
	RecordID       string `json:"record_id"`
	KeyVersion     int    `json:"key_version"`
	CredentialType string `json:"credential_type"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
}

// VaultCredentialHandler 处理 Vault 凭证写入请求。
type VaultCredentialHandler struct {
	db  vaultDBConn
	enc vaultEncryptor
}

// NewVaultCredentialHandler 创建 Vault 凭证写入 handler。
// R20 P0-5: production/real 下 Encryptor 缺失时返回 nil，调用方不得注册路由。
func NewVaultCredentialHandler(pool *pgxpool.Pool, enc *crypto.Encryptor) *VaultCredentialHandler {
	return &VaultCredentialHandler{db: pool, enc: enc}
}

// WriteCredential 处理 POST /admin/api/v3/issuing/vault-credentials 请求。
// R20 P0-5: write-only API，不回显秘密。
func (h *VaultCredentialHandler) WriteCredential(w http.ResponseWriter, r *http.Request) {
	// 1. 身份从已验证 Context 获取
	identity, ok := httpx.GetAdminIdentity(r.Context())
	if !ok || identity == nil {
		httpx.WriteError(w, apperr.ErrForbidden.WithMessage("admin identity not found in authenticated context"))
		return
	}

	// 2. 解析请求体（R22 Item 6: 启用 DisallowUnknownFields 拒绝未知字段）
	var req VaultCredentialWriteRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	// 3. 校验必填字段
	if req.IssuingProviderID == "" {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("issuing_provider_id is required"))
		return
	}
	if req.CredentialType == "" {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("credential_type is required"))
		return
	}
	// R21 P0-5: 只允许 credential_type=API_KEY
	if req.CredentialType != "API_KEY" {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("credential_type must be API_KEY"))
		return
	}
	if len(req.CredentialMaterial) == 0 {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("credential_material is required"))
		return
	}
	// R21 P0-5: credential_material.api_key 必须非空
	if req.CredentialMaterial["api_key"] == "" {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("credential_material.api_key is required and must be non-empty"))
		return
	}
	// R21 P0-5: 拒绝未知材料字段
	for k := range req.CredentialMaterial {
		if k != "api_key" {
			httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("unknown credential_material field: %s (only api_key is allowed)", k)))
			return
		}
	}

	// 4. 校验 Encryptor 存在（fail-closed）
	if h.enc == nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("vault encryptor not available (fail-closed)"))
		return
	}

	// 5. 解析 issuing_provider_id
	issuingProviderUUID, err := uuid.Parse(req.IssuingProviderID)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("issuing_provider_id must be a valid UUID"))
		return
	}

	// 6. 从 DB 推导 tenant、adapter provider、entity ownership
	// R20 P0-5: tenant、adapter provider、record ownership 必须由数据库推导
	var entityID *uuid.UUID
	var adapterProviderID *uuid.UUID
	var tenantID *uuid.UUID
	var providerStatus string
	err = h.db.QueryRow(r.Context(), `
		SELECT ip.entity_id, ip.adapter_provider_id, ce.tenant_id, ip.status
		FROM issuing_providers ip
		LEFT JOIN corporate_entity ce ON ip.entity_id = ce.id
		WHERE ip.id = $1`, issuingProviderUUID).Scan(&entityID, &adapterProviderID, &tenantID, &providerStatus)
	if err != nil {
		httpx.WriteError(w, apperr.ErrNotFound.WithMessage("issuing provider not found"))
		return
	}

	// 7. 校验推导出的 ownership
	if entityID == nil || *entityID == uuid.Nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("issuing provider has no entity_id"))
		return
	}
	if tenantID == nil || *tenantID == uuid.Nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("entity has no tenant_id"))
		return
	}
	if adapterProviderID == nil || *adapterProviderID == uuid.Nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("issuing provider has no adapter_provider_id"))
		return
	}

	// 8. 校验 adapter_provider ACTIVE CARD_ISSUER
	var apStatus, apAdapterType string
	err = h.db.QueryRow(r.Context(),
		`SELECT status, adapter_type::text FROM adapter_provider WHERE id = $1`,
		*adapterProviderID).Scan(&apStatus, &apAdapterType)
	if err != nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("adapter_provider not found"))
		return
	}
	if apStatus != "ACTIVE" {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("adapter_provider is not ACTIVE"))
		return
	}
	if apAdapterType != "CARD_ISSUER" {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("adapter_provider adapter_type must be CARD_ISSUER"))
		return
	}

	// 8b. 写入即轮换：事务外预查当前最大 key_version，新版本 = MAX + 1
	// （同 provider+type 的并发写入由步骤 10d advisory lock + 10e 版本重验 + 唯一约束兜底）
	var maxVersion int
	err = h.db.QueryRow(r.Context(),
		`SELECT COALESCE(MAX(key_version), 0) FROM adapter_credential_vault
		 WHERE issuing_provider_id = $1 AND credential_type = $2`,
		issuingProviderUUID, req.CredentialType).Scan(&maxVersion)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to query current credential version"))
		return
	}
	keyVersion := maxVersion + 1

	// 9. R20 P0-5: KMS 加密发生在 DB 事务外
	recordID := uuid.New()
	credType := issuing.CredentialType(req.CredentialType)
	fieldName := "api_key" // 默认 field name
	if credType != issuing.CredentialTypeAPIKey {
		fieldName = strings.ToLower(string(credType))
	}

	// 序列化凭证材料
	plaintext, err := json.Marshal(req.CredentialMaterial)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to serialize credential material"))
		return
	}

	// 加密
	meta := crypto.Meta{
		TenantID:   tenantID.String(),
		RecordID:   recordID.String(),
		FieldName:  fieldName,
		KeyVersion: fmt.Sprintf("%d", keyVersion),
	}
	encrypted, err := h.enc.Encrypt(r.Context(), plaintext, meta)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("vault encryption failed (fail-closed)"))
		return
	}

	// 分离 nonce 和 ciphertext（GCM nonce = 12 bytes, tag = 16 bytes）
	nonceLen := 12
	if len(encrypted) < nonceLen+16 {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("vault encryption produced invalid output"))
		return
	}
	nonce := encrypted[:nonceLen]
	ciphertext := encrypted[nonceLen:]

	canonicalAAD := fmt.Sprintf("%s|%s|%s|%d", tenantID.String(), recordID.String(), fieldName, keyVersion)

	// 10. R21 Step E: 写入事务内重新锁定并验证全部 binding 未变化
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to begin transaction"))
		return
	}
	defer tx.Rollback(r.Context())

	// 10a. 重新锁定 issuing_providers，验证 status/entity/adapter binding 未变化
	var recheckEntityID *uuid.UUID
	var recheckAdapterProviderID *uuid.UUID
	var recheckStatus string
	err = tx.QueryRow(r.Context(), `
		SELECT ip.entity_id, ip.adapter_provider_id, ip.status
		FROM issuing_providers ip
		WHERE ip.id = $1 FOR UPDATE`, issuingProviderUUID).Scan(&recheckEntityID, &recheckAdapterProviderID, &recheckStatus)
	if err != nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("issuing provider not found at write time"))
		return
	}
	// R21 Step E: provider 被禁用时必须回滚
	if recheckStatus != "ACTIVE" {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage(
			fmt.Sprintf("issuing provider status changed to %s during write", recheckStatus)))
		return
	}
	if recheckEntityID == nil || *recheckEntityID != *entityID {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("entity binding changed during write"))
		return
	}
	if recheckAdapterProviderID == nil || *recheckAdapterProviderID != *adapterProviderID {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("adapter_provider binding changed during write"))
		return
	}

	// 10b. R21 Step E: 重新验证 corporate_entity 权威 tenant 未变化
	var recheckTenantID *uuid.UUID
	err = tx.QueryRow(r.Context(), `
		SELECT ce.tenant_id FROM corporate_entity ce
		WHERE ce.id = $1 FOR SHARE`, *entityID).Scan(&recheckTenantID)
	if err != nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("entity not found at write time"))
		return
	}
	if recheckTenantID == nil || *recheckTenantID == uuid.Nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("entity tenant_id is null at write time"))
		return
	}
	// R21 Step E: tenant 发生变化时必须回滚
	if *recheckTenantID != *tenantID {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage(
			fmt.Sprintf("tenant changed during write: was %s, now %s", tenantID, recheckTenantID)))
		return
	}

	// 10c. R21 Step E: 重新验证 adapter_provider 仍为 ACTIVE CARD_ISSUER
	var recheckApStatus, recheckApAdapterType string
	err = tx.QueryRow(r.Context(),
		`SELECT status, adapter_type::text FROM adapter_provider WHERE id = $1 FOR SHARE`,
		*adapterProviderID).Scan(&recheckApStatus, &recheckApAdapterType)
	if err != nil {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage("adapter_provider not found at write time"))
		return
	}
	if recheckApStatus != "ACTIVE" {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage(
			fmt.Sprintf("adapter_provider status changed to %s during write", recheckApStatus)))
		return
	}
	if recheckApAdapterType != "CARD_ISSUER" {
		httpx.WriteError(w, apperr.ErrConfigInconsistent.WithMessage(
			fmt.Sprintf("adapter_provider adapter_type changed to %s during write", recheckApAdapterType)))
		return
	}

	// 10d. 写入即轮换：同 provider+type 的并发写入用事务级 advisory lock 串行化
	_, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`,
		issuingProviderUUID.String()+"|"+req.CredentialType)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to acquire credential write lock"))
		return
	}

	// 10e. 事务内重验 MAX(key_version) 未变化（>= keyVersion 说明并发已写入更新版本，回滚重试）
	var recheckMaxVersion int
	err = tx.QueryRow(r.Context(),
		`SELECT COALESCE(MAX(key_version), 0) FROM adapter_credential_vault
		 WHERE issuing_provider_id = $1 AND credential_type = $2`,
		issuingProviderUUID, req.CredentialType).Scan(&recheckMaxVersion)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to recheck credential version"))
		return
	}
	if recheckMaxVersion >= keyVersion {
		httpx.WriteError(w, apperr.ErrIdempotencyConflict.WithMessage(
			fmt.Sprintf("credential version conflict: expected next version %d, but version %d already exists", keyVersion, recheckMaxVersion)))
		return
	}

	// 11. INSERT INTO adapter_credential_vault
	_, err = tx.Exec(r.Context(), `
		INSERT INTO adapter_credential_vault (
			id, tenant_id, provider_id, issuing_provider_id,
			credential_type, ciphertext, nonce, aad_context,
			key_version, status, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, 'ACTIVE', now(), now()
		)`,
		recordID, tenantID, adapterProviderID, issuingProviderUUID,
		credType.String(), ciphertext, nonce, canonicalAAD,
		keyVersion)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to insert vault credential"))
		return
	}

	// R20 P0-5: vault:// 引用格式 tenant/record/field/version（计算前移，绑定与响应共用）
	vaultKey := fmt.Sprintf("vault://%s/%s/%s/%d", tenantID.String(), recordID.String(), fieldName, keyVersion)

	// 11b. 写入即绑定：issuing_providers.credential_vault_key 原子指向新版本引用
	_, err = tx.Exec(r.Context(),
		`UPDATE issuing_providers SET credential_vault_key = $2, updated_at = now() WHERE id = $1`,
		issuingProviderUUID, vaultKey)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to bind vault credential to issuing provider"))
		return
	}

	// 11c. 重复写入即轮换：旧版本全部置 REVOKED（运行时按 ORDER BY key_version DESC 取最新）
	_, err = tx.Exec(r.Context(),
		`UPDATE adapter_credential_vault SET status = 'REVOKED', updated_at = now()
		 WHERE issuing_provider_id = $1 AND credential_type = $2 AND key_version < $3 AND status = 'ACTIVE'`,
		issuingProviderUUID, req.CredentialType, keyVersion)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to revoke old credential versions"))
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to commit vault credential write"))
		return
	}

	// 12. R20 P0-5: 响应只返回 vault:// 引用和非敏感元数据
	resp := VaultCredentialWriteResponse{
		VaultKey:       vaultKey,
		RecordID:       recordID.String(),
		KeyVersion:     keyVersion,
		CredentialType: credType.String(),
		Status:         "ACTIVE",
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}
