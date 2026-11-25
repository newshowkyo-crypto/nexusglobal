// Package issuing — worker_adapters.go 提供 Worker 组合根所需的适配器实现。
//
// P0-7: CARD_FROZEN_SUSPEND handler 使用 ProviderCardFreezeService（专用冻结同步服务），
// 不再依赖完整 issuing.Service 和 FraudGuard。
// VaultService 从 crypto.Encryptor + DB 适配，实现真实凭证解密。
//
// P0-7 最终闭环修复:
// - F-04: Vault 请求传递权威租户身份（uuid.UUID 强类型）
// - F-05: ExpectedIssuingProviderID 被 resolver 强制校验
// - F-06: Vault SQL 加载并比较 credential_type
// - F-07: Credential ownership 连接权威 tenant/entity 数据
// - F-08: 字段名规范化（api_key <-> API_KEY），不做 raw string 比较
// - 错误信息不泄露 record ID、tenant ID、provider ID、AAD、密文或秘密
package issuing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

// credentialRow 是从 adapter_credential_vault 读取的凭证行。
type credentialRow struct {
	Ciphertext        []byte
	Nonce             []byte
	AADContext        string
	Status            string
	KeyVersion        int
	ExpiresAt         *time.Time
	ProviderID        uuid.UUID
	TenantID          *uuid.UUID
	IssuingProviderID *uuid.UUID // P0-1/R18: 发卡 Provider 归属
	CredentialType    string
}

// credentialReader 是从 DB 读取凭证的窄接口，便于单元测试。
type credentialReader interface {
	ReadCredential(ctx context.Context, recordID, keyVersion string) (*credentialRow, error)
}

// dbCredentialReader 是 credentialReader 的 DB 实现。
type dbCredentialReader struct {
	pool *pgxpool.Pool
}

func (r *dbCredentialReader) ReadCredential(ctx context.Context, recordID, keyVersion string) (*credentialRow, error) {
	var row credentialRow
	err := r.pool.QueryRow(ctx, `
		SELECT ciphertext, nonce, aad_context, status, key_version, expires_at,
		       provider_id, tenant_id, issuing_provider_id, credential_type
		FROM adapter_credential_vault
		WHERE id = $1 AND key_version = $2
	`, recordID, keyVersion).Scan(
		&row.Ciphertext, &row.Nonce, &row.AADContext, &row.Status, &row.KeyVersion, &row.ExpiresAt,
		&row.ProviderID, &row.TenantID, &row.IssuingProviderID, &row.CredentialType,
	)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CryptoVaultService 将 crypto.Encryptor + DB 适配为 issuing.VaultService 接口。
//
// CredentialVaultKey 格式：vault://tenant_id/record_id/field_name/key_version
// Decrypt 从 vault key 解析元数据，从 adapter_credential_vault 表读取 ACTIVE 密文，
// 使用 crypto.Encryptor 和正确 AAD 解密，返回 map[string]string 格式的凭证。
type CryptoVaultService struct {
	enc    *crypto.Encryptor
	reader credentialReader
}

// NewCryptoVaultService 从 crypto.Encryptor 和 DB pool 创建 VaultService 适配器。
func NewCryptoVaultService(enc *crypto.Encryptor, pool *pgxpool.Pool) *CryptoVaultService {
	return &CryptoVaultService{enc: enc, reader: &dbCredentialReader{pool: pool}}
}

// NewCryptoVaultServiceWithReader 从 crypto.Encryptor 和 credentialReader 创建 VaultService。
// 用于单元测试。
func NewCryptoVaultServiceWithReader(enc *crypto.Encryptor, reader credentialReader) *CryptoVaultService {
	return &CryptoVaultService{enc: enc, reader: reader}
}

// vaultKeyParts 解析 vault:// 路径的各部分。
type vaultKeyParts struct {
	tenantID   string
	recordID   string
	fieldName  string
	keyVersion string
}

// parseVaultKey 解析 vault://tenant_id/record_id/field_name/key_version 路径。
func parseVaultKey(vaultKey string) (*vaultKeyParts, error) {
	if vaultKey == "" {
		return nil, ErrCredentialVaultKeyEmpty
	}
	if !strings.HasPrefix(vaultKey, "vault://") {
		return nil, fmt.Errorf("issuing: invalid vault key format")
	}
	body := strings.TrimPrefix(vaultKey, "vault://")
	parts := strings.SplitN(body, "/", 4)
	if len(parts) != 4 {
		return nil, fmt.Errorf("issuing: invalid vault key format")
	}
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("issuing: invalid vault key format")
		}
	}
	return &vaultKeyParts{
		tenantID:   parts[0],
		recordID:   parts[1],
		fieldName:  parts[2],
		keyVersion: parts[3],
	}, nil
}

// buildCanonicalAAD 构建 canonical AAD 字符串（与 crypto.buildAAD 一致）。
func buildCanonicalAAD(tenantID, recordID, fieldName, keyVersion string) string {
	return fmt.Sprintf("%s|%s|%s|%s", tenantID, recordID, fieldName, keyVersion)
}

// Decrypt 实现 issuing.VaultService 接口。
//
// R18 P0-1: 严格校验全部归属关系，fail-closed。
// vaultKey 格式：vault://tenant_id/record_id/field_name/key_version
//
// 校验顺序：
// 1. 解析 vault:// 路径
// 2. ExpectedTenantID 非 uuid.Nil（fail-closed，不回退信任 vault:// 路径）
// 3. ExpectedIssuingProviderID 非 uuid.Nil（fail-closed）
// 4. ExpectedAdapterProviderID 非 uuid.Nil（真实 HTTP Provider，fail-closed）
// 5. 路径 tenant_id 与 ExpectedTenantID 一致
// 6. 路径 field_name 规范化后与 ExpectedCredentialType 一致
// 7. 从 DB 读取凭证行
// 8. DB tenant_id 非空且与 ExpectedTenantID 完全匹配
// 9. DB issuing_provider_id 非空且与 ExpectedIssuingProviderID 完全匹配
// 10. DB provider_id 与 ExpectedAdapterProviderID 完全匹配
// 11. DB credential_type 与 ExpectedCredentialType 一致
// 12. canonical AAD 与 DB aad_context 一致
// 13. status = ACTIVE
// 14. 未过期
// 15. nonce 和 ciphertext 非空
// 16. 使用权威租户解密
//
// 错误信息使用通用外部错误，不包含 record ID、tenant ID、provider ID、AAD、密文或秘密。
func (v *CryptoVaultService) Decrypt(ctx context.Context, req VaultCredentialRequest) (map[string]string, error) {
	// 1. 解析 vault:// 路径
	parts, err := parseVaultKey(req.VaultKey)
	if err != nil {
		return nil, err
	}

	// 2. R18 P0-1: ExpectedTenantID 必须非 uuid.Nil（fail-closed，不回退信任 vault:// 路径）
	if req.ExpectedTenantID == uuid.Nil {
		return nil, ErrCredentialMissingTenant
	}

	// 3. R18 P0-1: ExpectedIssuingProviderID 必须非 uuid.Nil（fail-closed）
	if req.ExpectedIssuingProviderID == uuid.Nil {
		return nil, ErrCredentialIssuingProviderMismatch
	}

	// 4. R18 P0-1: ExpectedAdapterProviderID 必须非 uuid.Nil（真实 HTTP Provider，fail-closed）
	if req.ExpectedAdapterProviderID == uuid.Nil {
		return nil, ErrCredentialAdapterProviderMismatch
	}

	// 5. 校验路径中的 tenant_id 与 ExpectedTenantID 一致
	pathTenantUUID, parseErr := uuid.Parse(parts.tenantID)
	if parseErr != nil {
		return nil, ErrCredentialCrossTenant
	}
	if pathTenantUUID != req.ExpectedTenantID {
		return nil, ErrCredentialCrossTenant
	}

	// 6. F-08: 规范化路径字段名并校验凭证类型
	normalizedFieldType := normalizeCredentialType(parts.fieldName)
	if req.ExpectedCredentialType != "" && normalizedFieldType != req.ExpectedCredentialType {
		return nil, ErrCredentialFieldTypeMismatch
	}

	// 7. 检查 reader 非空
	if v.reader == nil {
		return nil, ErrCredentialDecryptFailed
	}

	// 8. F-06/F-07: 从 adapter_credential_vault 表读取密文 + 全部归属字段
	row, err := v.reader.ReadCredential(ctx, parts.recordID, parts.keyVersion)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCredentialDecryptFailed
	}
	if err != nil {
		return nil, ErrCredentialDecryptFailed
	}

	// 9. R18 P0-1: DB tenant_id 非空且与 ExpectedTenantID 完全匹配
	if row.TenantID == nil {
		return nil, ErrCredentialMissingTenant
	}
	if *row.TenantID != req.ExpectedTenantID {
		return nil, ErrCredentialCrossTenant
	}

	// 10. R18 P0-1: DB issuing_provider_id 非空且与 ExpectedIssuingProviderID 完全匹配
	if row.IssuingProviderID == nil {
		return nil, ErrCredentialIssuingProviderMismatch
	}
	if *row.IssuingProviderID != req.ExpectedIssuingProviderID {
		return nil, ErrCredentialIssuingProviderMismatch
	}

	// 11. R18 P0-1: DB provider_id 与 ExpectedAdapterProviderID 完全匹配
	if row.ProviderID != req.ExpectedAdapterProviderID {
		return nil, ErrCredentialAdapterProviderMismatch
	}

	// 12. F-06: 校验 DB 的 credential_type 与期望类型一致
	if req.ExpectedCredentialType != "" && row.CredentialType != req.ExpectedCredentialType.String() {
		return nil, ErrCredentialFieldTypeMismatch
	}

	// 13. 校验 canonical AAD 与 DB 的 aad_context 一致
	canonicalAAD := buildCanonicalAAD(parts.tenantID, parts.recordID, parts.fieldName, parts.keyVersion)
	if row.AADContext != canonicalAAD {
		return nil, ErrCredentialAADMismatch
	}

	// 14. 校验 status = ACTIVE
	if row.Status != "ACTIVE" {
		return nil, ErrCredentialRevokedOrExpired
	}

	// 15. 校验未过期
	if row.ExpiresAt != nil && time.Now().After(*row.ExpiresAt) {
		return nil, ErrCredentialRevokedOrExpired
	}

	// 16. 校验密文和 nonce 非空
	if len(row.Ciphertext) == 0 || len(row.Nonce) == 0 {
		return nil, ErrCredentialDecryptFailed
	}

	// 17. 构造 AAD 并解密（使用权威租户，非路径控制租户）
	if v.enc == nil {
		return nil, ErrCredentialDecryptFailed
	}

	combined := make([]byte, len(row.Nonce)+len(row.Ciphertext))
	copy(combined, row.Nonce)
	copy(combined[len(row.Nonce):], row.Ciphertext)

	// R18 P0-1: 始终使用 ExpectedTenantID（已校验非 uuid.Nil）
	meta := crypto.Meta{
		TenantID:   req.ExpectedTenantID.String(),
		RecordID:   parts.recordID,
		FieldName:  parts.fieldName,
		KeyVersion: parts.keyVersion,
	}

	plaintext, err := v.enc.Decrypt(ctx, combined, meta)
	if err != nil {
		return nil, ErrCredentialDecryptFailed
	}

	// 18. 解析 JSON 为 credential_map
	var credential map[string]string
	if err := json.Unmarshal(plaintext, &credential); err != nil {
		return nil, ErrCredentialDecryptFailed
	}

	if len(credential) == 0 {
		return nil, ErrCredentialDecryptFailed
	}

	return credential, nil
}

// MockVaultService 是本地/mock 模式的 VaultService 实现。
// 不访问 DB，直接返回预设凭证。仅用于测试和 local/mock 模式。
// production/real 模式禁止使用。
type MockVaultService struct {
	credential map[string]string
}

// NewMockVaultService 创建 MockVaultService。
func NewMockVaultService(credential map[string]string) *MockVaultService {
	return &MockVaultService{credential: credential}
}

// Decrypt 实现 issuing.VaultService 接口。
// MockVaultService 跳过全部归属校验，直接返回预设凭证。
func (v *MockVaultService) Decrypt(_ context.Context, _ VaultCredentialRequest) (map[string]string, error) {
	if v.credential == nil {
		return nil, ErrCredentialDecryptFailed
	}
	return v.credential, nil
}

// MockIssuingAdapter 是本地/mock 模式的 IssuingAdapter 实现。
// R18 P0-5: 使用 mutex 保证并发安全，按 provider request key 幂等。
// - 相同 key 重放不增加副作用次数
// - 不同 key 才增加次数
type MockIssuingAdapter struct {
	mu          sync.Mutex
	freezeCount int32
	seenKeys    map[string]bool
	lastReq     FreezeCardRequest
}

// NewMockIssuingAdapter 创建 MockIssuingAdapter。
func NewMockIssuingAdapter() *MockIssuingAdapter {
	return &MockIssuingAdapter{seenKeys: make(map[string]bool)}
}

func (a *MockIssuingAdapter) CreateCard(_ context.Context, _ CreateCardRequest) (*CreateCardResult, error) {
	return nil, fmt.Errorf("issuing: MockIssuingAdapter does not support CreateCard")
}

func (a *MockIssuingAdapter) FreezeCard(_ context.Context, req FreezeCardRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// R18 P0-5: 按 provider request key 幂等
	// 相同 key 重放不增加副作用次数
	if _, alreadySeen := a.seenKeys[req.IdempotencyKey]; alreadySeen {
		a.lastReq = req
		return nil // 幂等重放，不增加 count
	}

	a.seenKeys[req.IdempotencyKey] = true
	a.freezeCount++
	a.lastReq = req
	return nil
}

// FreezeCount 返回副作用次数（线程安全）。
func (a *MockIssuingAdapter) FreezeCount() int32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.freezeCount
}

func (a *MockIssuingAdapter) TerminateCard(_ context.Context, _ TerminateCardRequest) error {
	return nil
}

func (a *MockIssuingAdapter) ProcessAuthWebhook(_ context.Context, _ ProcessAuthWebhookRequest) (*CardAuthEvent, error) {
	return nil, nil
}
