package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKMS(t *testing.T) *MockKMS {
	t.Helper()
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i)
	}
	kms, err := NewMockKMS(base64.StdEncoding.EncodeToString(masterKey))
	require.NoError(t, err)
	return kms
}

func testMeta() Meta {
	return Meta{
		TenantID:   "tenant_a",
		RecordID:   "rec_001",
		FieldName:  "api_key",
		KeyVersion: "v1",
	}
}

func TestCrypto_RoundTrip(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	plaintext := []byte("super-secret-api-key-12345")
	meta := testMeta()

	ciphertext, err := enc.Encrypt(ctx, plaintext, meta)
	require.NoError(t, err)
	assert.NotEmpty(t, ciphertext)
	assert.NotEqual(t, plaintext, ciphertext)

	decrypted, err := enc.Decrypt(ctx, ciphertext, meta)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestCrypto_AADMismatch_TenantID(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	plaintext := []byte("secret-data")
	meta := testMeta()

	ciphertext, err := enc.Encrypt(ctx, plaintext, meta)
	require.NoError(t, err)

	wrongMeta := meta
	wrongMeta.TenantID = "tenant_b"
	_, err = enc.Decrypt(ctx, ciphertext, wrongMeta)
	assert.Error(t, err)
}

func TestCrypto_AADMismatch_RecordID(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	plaintext := []byte("secret-data")
	meta := testMeta()

	ciphertext, err := enc.Encrypt(ctx, plaintext, meta)
	require.NoError(t, err)

	wrongMeta := meta
	wrongMeta.RecordID = "rec_999"
	_, err = enc.Decrypt(ctx, ciphertext, wrongMeta)
	assert.Error(t, err)
}

func TestCrypto_AADMismatch_FieldName(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	plaintext := []byte("secret-data")
	meta := testMeta()

	ciphertext, err := enc.Encrypt(ctx, plaintext, meta)
	require.NoError(t, err)

	wrongMeta := meta
	wrongMeta.FieldName = "webhook_secret"
	_, err = enc.Decrypt(ctx, ciphertext, wrongMeta)
	assert.Error(t, err)
}

func TestCrypto_AADMismatch_KeyVersion(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	plaintext := []byte("secret-data")
	meta := testMeta()

	ciphertext, err := enc.Encrypt(ctx, plaintext, meta)
	require.NoError(t, err)

	wrongMeta := meta
	wrongMeta.KeyVersion = "v2"
	_, err = enc.Decrypt(ctx, ciphertext, wrongMeta)
	assert.Error(t, err)
}

func TestCrypto_NonceUniqueness(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	plaintext := []byte("same-plaintext")
	meta := testMeta()

	c1, err := enc.Encrypt(ctx, plaintext, meta)
	require.NoError(t, err)

	c2, err := enc.Encrypt(ctx, plaintext, meta)
	require.NoError(t, err)

	assert.NotEqual(t, c1, c2, "same plaintext should produce different ciphertext due to random nonce")
}

func TestCrypto_CiphertextTooShort(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	meta := testMeta()

	_, err := enc.Decrypt(ctx, []byte{0x01, 0x02}, meta)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestCrypto_EmptyPlaintext(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	meta := testMeta()

	ciphertext, err := enc.Encrypt(ctx, []byte{}, meta)
	require.NoError(t, err)

	decrypted, err := enc.Decrypt(ctx, ciphertext, meta)
	require.NoError(t, err)
	assert.Empty(t, decrypted)
}

func TestNewMockKMS_InvalidBase64(t *testing.T) {
	_, err := NewMockKMS("not-valid-base64!!!")
	assert.Error(t, err)
}

func TestNewMockKMS_WrongLength(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	_, err := NewMockKMS(short)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "32 bytes")
}

func TestNewMockKMS_Valid(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	kms, err := NewMockKMS(key)
	assert.NoError(t, err)
	assert.NotNil(t, kms)
}

func TestMockKMS_StableDEK_Deterministic(t *testing.T) {
	kms := testKMS(t)
	ctx := context.Background()

	dek1, err := kms.StableDEK(ctx, "tenant_a")
	require.NoError(t, err)
	dek2, err := kms.StableDEK(ctx, "tenant_a")
	require.NoError(t, err)
	assert.Equal(t, dek1, dek2)
}

func TestMockKMS_StableDEK_DifferentTenants(t *testing.T) {
	kms := testKMS(t)
	ctx := context.Background()

	dek1, err := kms.StableDEK(ctx, "tenant_a")
	require.NoError(t, err)
	dek2, err := kms.StableDEK(ctx, "tenant_b")
	require.NoError(t, err)
	assert.NotEqual(t, dek1, dek2)
}

// --- SEC-001 P0 证据测试（envelope encryption）---

func TestCrypto_EnvelopeRoundTrip_CrossInstance(t *testing.T) {
	// 模拟跨进程：两个独立 Encryptor 实例（共享同一 KMS 后端）。
	kms := testKMS(t)
	enc1 := NewEncryptor(kms)
	enc2 := NewEncryptor(kms)
	ctx := context.Background()
	meta := testMeta()

	ct, err := enc1.Encrypt(ctx, []byte("cross-process-secret"), meta)
	require.NoError(t, err)
	require.True(t, hasEnvelopeMagic(ct), "new ciphertext must use envelope v2 format")

	got, err := enc2.Decrypt(ctx, ct, meta)
	require.NoError(t, err)
	assert.Equal(t, []byte("cross-process-secret"), got)
}

func TestCrypto_LegacyV1Fallback_MockKMS(t *testing.T) {
	// 旧 v1 原始密文（nonce||ct + 确定性派生 DEK）在 MockKMS 下仍可解密。
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	meta := testMeta()

	// 构造 legacy v1 密文：StableDEK 派生 + GCM，无信封头。
	dek, err := kms.StableDEK(ctx, meta.TenantID)
	require.NoError(t, err)
	block, err := aes.NewCipher(dek)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	nonce := make([]byte, gcm.NonceSize())
	_, err = io.ReadFull(rand.Reader, nonce)
	require.NoError(t, err)
	legacy := gcm.Seal(nonce, nonce, []byte("legacy-data"), buildAAD(
		meta.TenantID, meta.RecordID, meta.FieldName, meta.KeyVersion))
	require.False(t, hasEnvelopeMagic(legacy))

	got, err := enc.Decrypt(ctx, legacy, meta)
	require.NoError(t, err)
	assert.Equal(t, []byte("legacy-data"), got)
}

func TestCrypto_EnvelopeTamperFailClosed(t *testing.T) {
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	meta := testMeta()

	ct, err := enc.Encrypt(ctx, []byte("secret"), meta)
	require.NoError(t, err)

	// 篡改 wrapped DEK 第一个字节 → unwrap 认证失败 → 报错（不产生错误明文）。
	bad := append([]byte{}, ct...)
	bad[6] ^= 0xFF
	_, err = enc.Decrypt(ctx, bad, meta)
	assert.Error(t, err)

	// 篡改数据密文最后一个字节（GCM tag）→ open 失败。
	bad2 := append([]byte{}, ct...)
	bad2[len(bad2)-1] ^= 0xFF
	_, err = enc.Decrypt(ctx, bad2, meta)
	assert.Error(t, err)
}

func TestCrypto_EnvelopeWrongTenant_CannotUnwrapDEK(t *testing.T) {
	// wrapped DEK 与 tenant AAD 绑定：换租户解包 DEK 必须失败。
	kms := testKMS(t)
	enc := NewEncryptor(kms)
	ctx := context.Background()
	meta := testMeta()

	ct, err := enc.Encrypt(ctx, []byte("secret"), meta)
	require.NoError(t, err)

	wrongTenant := meta
	wrongTenant.TenantID = "tenant_b"
	_, err = enc.Decrypt(ctx, ct, wrongTenant)
	assert.Error(t, err)
}
