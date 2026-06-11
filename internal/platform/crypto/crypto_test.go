package crypto

import (
	"context"
	"encoding/base64"
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

func TestMockKMS_DEK_Deterministic(t *testing.T) {
	kms := testKMS(t)
	ctx := context.Background()

	dek1, err := kms.DEK(ctx, "tenant_a")
	require.NoError(t, err)
	dek2, err := kms.DEK(ctx, "tenant_a")
	require.NoError(t, err)
	assert.Equal(t, dek1, dek2)
}

func TestMockKMS_DEK_DifferentTenants(t *testing.T) {
	kms := testKMS(t)
	ctx := context.Background()

	dek1, err := kms.DEK(ctx, "tenant_a")
	require.NoError(t, err)
	dek2, err := kms.DEK(ctx, "tenant_b")
	require.NoError(t, err)
	assert.NotEqual(t, dek1, dek2)
}
