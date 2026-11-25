package checkout

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAESGCMTokenEncryptor_EncryptDecryptRoundTrip 验证加密-解密往返一致性。
func TestAESGCMTokenEncryptor_EncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	enc, err := NewAESGCMTokenEncryptor(key)
	require.NoError(t, err)

	ctx := context.Background()
	tenantID := uuid.New()
	attemptID := uuid.New()
	plaintext := "tok_test_abc123"

	ciphertext, err := enc.EncryptToken(ctx, tenantID, attemptID, plaintext)
	require.NoError(t, err)
	assert.NotEmpty(t, ciphertext)
	assert.NotEqual(t, plaintext, string(ciphertext))

	decrypted, err := enc.DecryptToken(ctx, tenantID, attemptID, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

// TestAESGCMTokenEncryptor_TamperDetection 验证密文篡改后被检测。
func TestAESGCMTokenEncryptor_TamperDetection(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	enc, err := NewAESGCMTokenEncryptor(key)
	require.NoError(t, err)

	ctx := context.Background()
	tenantID := uuid.New()
	attemptID := uuid.New()

	ciphertext, err := enc.EncryptToken(ctx, tenantID, attemptID, "secret_token")
	require.NoError(t, err)

	// 篡改密文
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0xFF

	_, err = enc.DecryptToken(ctx, tenantID, attemptID, tampered)
	assert.Error(t, err)
}

// TestAESGCMTokenEncryptor_AADBinding 验证 AAD 绑定——不同 tenant/attempt 无法解密。
func TestAESGCMTokenEncryptor_AADBinding(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	enc, err := NewAESGCMTokenEncryptor(key)
	require.NoError(t, err)

	ctx := context.Background()
	tenantID := uuid.New()
	attemptID := uuid.New()

	ciphertext, err := enc.EncryptToken(ctx, tenantID, attemptID, "secret_token")
	require.NoError(t, err)

	// 使用不同的 attemptID 解密——应失败
	otherAttemptID := uuid.New()
	_, err = enc.DecryptToken(ctx, tenantID, otherAttemptID, ciphertext)
	assert.Error(t, err)
}

// TestAESGCMTokenEncryptor_EmptyKey 验证空 key 返回错误（fail-closed）。
func TestAESGCMTokenEncryptor_EmptyKey(t *testing.T) {
	_, err := NewAESGCMTokenEncryptor(nil)
	assert.Error(t, err)

	_, err = NewAESGCMTokenEncryptor([]byte{})
	assert.Error(t, err)
}

// TestAESGCMTokenEncryptor_WrongKeyLength 验证非 32 字节 key 返回错误。
func TestAESGCMTokenEncryptor_WrongKeyLength(t *testing.T) {
	_, err := NewAESGCMTokenEncryptor(make([]byte, 16))
	assert.Error(t, err)

	_, err = NewAESGCMTokenEncryptor(make([]byte, 64))
	assert.Error(t, err)
}

// TestNewTokenEncryptorFromMasterKey_Empty 验证空 master key 返回错误。
func TestNewTokenEncryptorFromMasterKey_Empty(t *testing.T) {
	_, err := NewTokenEncryptorFromMasterKey("")
	assert.Error(t, err)
}

// TestNewTokenEncryptorFromMasterKey_Valid 验证合法 base64 key 成功构造。
func TestNewTokenEncryptorFromMasterKey_Valid(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	encoded := base64.StdEncoding.EncodeToString(key)

	enc, err := NewTokenEncryptorFromMasterKey(encoded)
	require.NoError(t, err)
	assert.NotNil(t, enc)
}

// TestNewTokenEncryptorFromKMS_NilKMS 验证 nil KMS 返回错误。
func TestNewTokenEncryptorFromKMS_NilKMS(t *testing.T) {
	_, err := NewTokenEncryptorFromKMS(context.Background(), nil)
	assert.Error(t, err)
}

// TestNewTokenEncryptor_ProductionMode 验证 production 模式路由到 KMS。
func TestNewTokenEncryptor_ProductionMode(t *testing.T) {
	// production 模式 + nil KMS → 应返回错误
	_, err := NewTokenEncryptor(context.Background(), "production", nil, "")
	assert.Error(t, err)
}

// TestNewTokenEncryptor_LocalMode 验证 local 模式路由到 master key。
func TestNewTokenEncryptor_LocalMode(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	encoded := base64.StdEncoding.EncodeToString(key)

	enc, err := NewTokenEncryptor(context.Background(), "local", nil, encoded)
	require.NoError(t, err)
	assert.NotNil(t, enc)
}
