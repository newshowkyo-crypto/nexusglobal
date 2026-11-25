package issuing

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCredentialReader 是 credentialReader 的内存实现，用于单元测试。
type fakeCredentialReader struct {
	row *credentialRow
	err error
}

func (f *fakeCredentialReader) ReadCredential(_ context.Context, _, _ string) (*credentialRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.row, nil
}

// noopEncryptor 是 crypto.Encryptor 的占位（Vault 测试中解密前会因校验失败返回）。
// 实际解密测试使用 MockVaultService。

// TestVaultCredentialResolver_RejectsCrossTenantReference 验证 Vault 拒绝跨租户读取。
// F-04: ExpectedTenantID 与 vault key 中的 tenant_id 不一致时拒绝。
func TestVaultCredentialResolver_RejectsCrossTenantReference(t *testing.T) {
	svc := NewCryptoVaultService(nil, nil) // enc/reader 为 nil，但预校验先执行

	tenantA := uuid.New()
	tenantB := uuid.New()
	issuingProviderID := uuid.New()
	adapterProviderID := uuid.New()
	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantA.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantB, // 不匹配
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: adapterProviderID,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialCrossTenant)
}

// TestVaultCredentialResolver_RejectsFieldTypeMismatch 验证 Vault 拒绝字段类型不匹配。
// F-08: ExpectedCredentialType 与规范化后的 vault key field_name 不一致时拒绝。
func TestVaultCredentialResolver_RejectsFieldTypeMismatch(t *testing.T) {
	svc := NewCryptoVaultService(nil, nil)

	tenantA := uuid.New()
	issuingProviderID := uuid.New()
	adapterProviderID := uuid.New()
	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantA.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantA, // 匹配
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: adapterProviderID,
		ExpectedCredentialType:    CredentialTypeAPISecret, // 不匹配 normalized(api_key) = API_KEY
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialFieldTypeMismatch)
}

// TestVaultCredentialResolver_RejectsInvalidVaultKey 验证 Vault 拒绝无效 vault key 格式。
func TestVaultCredentialResolver_RejectsInvalidVaultKey(t *testing.T) {
	svc := NewCryptoVaultService(nil, nil)

	req := VaultCredentialRequest{
		VaultKey: "not-a-vault-key",
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
}

// TestVaultCredentialResolver_RejectsEmptyVaultKey 验证 Vault 拒绝空 vault key。
func TestVaultCredentialResolver_RejectsEmptyVaultKey(t *testing.T) {
	svc := NewCryptoVaultService(nil, nil)

	req := VaultCredentialRequest{
		VaultKey: "",
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialVaultKeyEmpty)
}

// TestVaultCredentialResolver_RejectsWrongAdapterProvider 验证 Vault 拒绝错误的 adapter_provider_id。
// F-16: 使用 fakeCredentialReader，不需要真实 DB。
func TestVaultCredentialResolver_RejectsWrongAdapterProvider(t *testing.T) {
	tenantID := uuid.New()
	expectedProvider := uuid.New()
	wrongProvider := uuid.New()
	issuingProviderID := uuid.New()
	now := time.Now()

	reader := &fakeCredentialReader{
		row: &credentialRow{
			Ciphertext:        []byte("ciphertext"),
			Nonce:             []byte("nonce"),
			AADContext:        tenantID.String() + "|record-001|api_key|1",
			Status:            "ACTIVE",
			KeyVersion:        1,
			ProviderID:        wrongProvider, // 不匹配
			TenantID:          &tenantID,
			IssuingProviderID: &issuingProviderID,
			CredentialType:    "API_KEY",
		},
	}
	reader.row.ExpiresAt = &now

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: expectedProvider,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialAdapterProviderMismatch)
}

// TestVaultCredentialResolver_RejectsAADMismatch 验证 Vault 拒绝 AAD 不匹配。
// F-16: 使用 fakeCredentialReader，不需要真实 DB。
func TestVaultCredentialResolver_RejectsAADMismatch(t *testing.T) {
	tenantID := uuid.New()
	providerID := uuid.New()
	issuingProviderID := uuid.New()
	now := time.Now()

	reader := &fakeCredentialReader{
		row: &credentialRow{
			Ciphertext:        []byte("ciphertext"),
			Nonce:             []byte("nonce"),
			AADContext:        "wrong-aad-context", // 不匹配
			Status:            "ACTIVE",
			KeyVersion:        1,
			ProviderID:        providerID,
			TenantID:          &tenantID,
			IssuingProviderID: &issuingProviderID,
			CredentialType:    "API_KEY",
		},
	}
	reader.row.ExpiresAt = &now

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: providerID,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialAADMismatch)
}

// TestVaultCredentialResolver_RejectsMissingTenant 验证 Vault 拒绝缺少 tenant_id 的凭证。
// F-07: tenant_id 为 NULL 时拒绝。
func TestVaultCredentialResolver_RejectsMissingTenant(t *testing.T) {
	tenantID := uuid.New()
	providerID := uuid.New()
	issuingProviderID := uuid.New()
	now := time.Now()

	reader := &fakeCredentialReader{
		row: &credentialRow{
			Ciphertext:        []byte("ciphertext"),
			Nonce:             []byte("nonce"),
			AADContext:        tenantID.String() + "|record-001|api_key|1",
			Status:            "ACTIVE",
			KeyVersion:        1,
			ProviderID:        providerID,
			TenantID:          nil, // F-07: 缺少 tenant_id
			IssuingProviderID: &issuingProviderID,
			CredentialType:    "API_KEY",
		},
	}
	reader.row.ExpiresAt = &now

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: providerID,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialMissingTenant)
}

// TestVaultCredentialResolver_RejectsRevokedCredential 验证 Vault 拒绝已撤销凭证。
func TestVaultCredentialResolver_RejectsRevokedCredential(t *testing.T) {
	tenantID := uuid.New()
	providerID := uuid.New()
	issuingProviderID := uuid.New()

	reader := &fakeCredentialReader{
		row: &credentialRow{
			Ciphertext:        []byte("ciphertext"),
			Nonce:             []byte("nonce"),
			AADContext:        tenantID.String() + "|record-001|api_key|1",
			Status:            "REVOKED", // 已撤销
			KeyVersion:        1,
			ProviderID:        providerID,
			TenantID:          &tenantID,
			IssuingProviderID: &issuingProviderID,
			CredentialType:    "API_KEY",
		},
	}

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: providerID,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialRevokedOrExpired)
}

// TestVaultCredentialResolver_RejectsCredentialTypeMismatch 验证 Vault 拒绝 DB credential_type 不匹配。
// F-06: DB credential_type 与 ExpectedCredentialType 不一致时拒绝。
func TestVaultCredentialResolver_RejectsCredentialTypeMismatch(t *testing.T) {
	tenantID := uuid.New()
	providerID := uuid.New()
	issuingProviderID := uuid.New()
	now := time.Now()

	reader := &fakeCredentialReader{
		row: &credentialRow{
			Ciphertext:        []byte("ciphertext"),
			Nonce:             []byte("nonce"),
			AADContext:        tenantID.String() + "|record-001|api_key|1",
			Status:            "ACTIVE",
			KeyVersion:        1,
			ProviderID:        providerID,
			TenantID:          &tenantID,
			IssuingProviderID: &issuingProviderID,
			CredentialType:    "API_SECRET", // DB 中是 API_SECRET，但期望 API_KEY
		},
	}
	reader.row.ExpiresAt = &now

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: providerID,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialFieldTypeMismatch)
}

// TestVaultCredentialResolver_RejectsNotFound 验证 Vault 拒绝不存在的凭证。
func TestVaultCredentialResolver_RejectsNotFound(t *testing.T) {
	tenantID := uuid.New()
	issuingProviderID := uuid.New()
	adapterProviderID := uuid.New()

	reader := &fakeCredentialReader{
		err: pgx.ErrNoRows,
	}

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: adapterProviderID,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialDecryptFailed)
}

// TestVaultCredentialResolver_FieldNameNormalization 验证字段名规范化。
// F-08: api_key 和 API_KEY 应被规范化为同一类型。
func TestVaultCredentialResolver_FieldNameNormalization(t *testing.T) {
	// 路径中用小写 api_key，期望类型用大写 API_KEY
	normalized := normalizeCredentialType("api_key")
	assert.Equal(t, CredentialTypeAPIKey, normalized)

	normalized = normalizeCredentialType("API_KEY")
	assert.Equal(t, CredentialTypeAPIKey, normalized)

	normalized = normalizeCredentialType("Api_Key")
	assert.Equal(t, CredentialTypeAPIKey, normalized)
}

// TestVaultCredentialResolver_ErrorDoesNotLeakIDs 验证错误信息不泄露 ID。
func TestVaultCredentialResolver_ErrorDoesNotLeakIDs(t *testing.T) {
	tenantID := uuid.New()
	providerID := uuid.New()
	wrongProvider := uuid.New()
	issuingProviderID := uuid.New()
	now := time.Now()

	reader := &fakeCredentialReader{
		row: &credentialRow{
			Ciphertext:        []byte("ciphertext"),
			Nonce:             []byte("nonce"),
			AADContext:        tenantID.String() + "|record-001|api_key|1",
			Status:            "ACTIVE",
			KeyVersion:        1,
			ProviderID:        wrongProvider,
			TenantID:          &tenantID,
			IssuingProviderID: &issuingProviderID,
			CredentialType:    "API_KEY",
		},
	}
	reader.row.ExpiresAt = &now

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: providerID,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	// 错误信息不应包含 provider ID
	errMsg := err.Error()
	assert.NotContains(t, errMsg, wrongProvider.String())
	assert.NotContains(t, errMsg, providerID.String())
}

// TestMockVaultService_Decrypt 验证 MockVaultService 正常工作。
func TestMockVaultService_Decrypt(t *testing.T) {
	credential := map[string]string{"api_key": "test-key-123"}
	svc := NewMockVaultService(credential)

	result, err := svc.Decrypt(context.Background(), VaultCredentialRequest{
		VaultKey: "vault://tenant1/record1/api_key/1",
	})
	require.NoError(t, err)
	assert.Equal(t, "test-key-123", result["api_key"])
}

// TestMockVaultService_NilCredential 验证 MockVaultService 空凭证返回错误。
func TestMockVaultService_NilCredential(t *testing.T) {
	svc := NewMockVaultService(nil)

	_, err := svc.Decrypt(context.Background(), VaultCredentialRequest{
		VaultKey: "vault://tenant1/record1/api_key/1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialDecryptFailed)
}

// ── R18 P0-1: Vault 发卡 Provider 和租户权威绑定 fail-closed 测试 ──

// TestVaultCredentialResolver_RejectsZeroExpectedTenantID 验证 ExpectedTenantID 为 uuid.Nil 时 fail-closed。
// R18 P0-1: 不回退信任 vault:// 路径中的租户。
func TestVaultCredentialResolver_RejectsZeroExpectedTenantID(t *testing.T) {
	svc := NewCryptoVaultService(nil, nil)

	tenantA := uuid.New()
	req := VaultCredentialRequest{
		VaultKey:         "vault://" + tenantA.String() + "/record-001/api_key/1",
		ExpectedTenantID: uuid.Nil, // fail-closed
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialMissingTenant)
}

// TestVaultCredentialResolver_RejectsZeroExpectedIssuingProviderID 验证 ExpectedIssuingProviderID 为 uuid.Nil 时 fail-closed。
// R18 P0-1: 发卡 Provider 归属必须显式提供。
func TestVaultCredentialResolver_RejectsZeroExpectedIssuingProviderID(t *testing.T) {
	svc := NewCryptoVaultService(nil, nil)

	tenantA := uuid.New()
	adapterProviderID := uuid.New()
	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantA.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantA,
		ExpectedIssuingProviderID: uuid.Nil, // fail-closed
		ExpectedAdapterProviderID: adapterProviderID,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialIssuingProviderMismatch)
}

// TestVaultCredentialResolver_RejectsZeroExpectedAdapterProviderID 验证 ExpectedAdapterProviderID 为 uuid.Nil 时 fail-closed。
// R18 P0-1: 真实 HTTP Provider 必须显式提供 adapter_provider_id。
func TestVaultCredentialResolver_RejectsZeroExpectedAdapterProviderID(t *testing.T) {
	svc := NewCryptoVaultService(nil, nil)

	tenantA := uuid.New()
	issuingProviderID := uuid.New()
	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantA.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantA,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: uuid.Nil, // fail-closed
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialAdapterProviderMismatch)
}

// TestVaultCredentialResolver_RejectsWrongIssuingProviderID 验证 DB issuing_provider_id 不匹配时拒绝。
// R18 P0-1: DB issuing_provider_id 必须与 ExpectedIssuingProviderID 完全匹配。
func TestVaultCredentialResolver_RejectsWrongIssuingProviderID(t *testing.T) {
	tenantID := uuid.New()
	providerID := uuid.New()
	expectedIssuingProvider := uuid.New()
	wrongIssuingProvider := uuid.New()
	now := time.Now()

	reader := &fakeCredentialReader{
		row: &credentialRow{
			Ciphertext:        []byte("ciphertext"),
			Nonce:             []byte("nonce"),
			AADContext:        tenantID.String() + "|record-001|api_key|1",
			Status:            "ACTIVE",
			KeyVersion:        1,
			ProviderID:        providerID,
			TenantID:          &tenantID,
			IssuingProviderID: &wrongIssuingProvider, // 不匹配
			CredentialType:    "API_KEY",
		},
	}
	reader.row.ExpiresAt = &now

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: expectedIssuingProvider,
		ExpectedAdapterProviderID: providerID,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialIssuingProviderMismatch)
}

// TestVaultCredentialResolver_RejectsNullIssuingProviderID 验证 DB issuing_provider_id 为 NULL 时拒绝。
// R18 P0-1: DB issuing_provider_id 必须非空。
func TestVaultCredentialResolver_RejectsNullIssuingProviderID(t *testing.T) {
	tenantID := uuid.New()
	providerID := uuid.New()
	issuingProviderID := uuid.New()
	now := time.Now()

	reader := &fakeCredentialReader{
		row: &credentialRow{
			Ciphertext:        []byte("ciphertext"),
			Nonce:             []byte("nonce"),
			AADContext:        tenantID.String() + "|record-001|api_key|1",
			Status:            "ACTIVE",
			KeyVersion:        1,
			ProviderID:        providerID,
			TenantID:          &tenantID,
			IssuingProviderID: nil, // NULL in DB
			CredentialType:    "API_KEY",
		},
	}
	reader.row.ExpiresAt = &now

	svc := NewCryptoVaultServiceWithReader(nil, reader)

	req := VaultCredentialRequest{
		VaultKey:                  "vault://" + tenantID.String() + "/record-001/api_key/1",
		ExpectedTenantID:          tenantID,
		ExpectedIssuingProviderID: issuingProviderID,
		ExpectedAdapterProviderID: providerID,
		ExpectedCredentialType:    CredentialTypeAPIKey,
	}

	_, err := svc.Decrypt(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialIssuingProviderMismatch)
}
