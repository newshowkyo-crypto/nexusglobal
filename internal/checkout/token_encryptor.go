package checkout

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

// ============================================================================
// P0-3: TokenEncryptor 装配函数
//
// API 与 Worker 共用同一套 TokenEncryptor 装配逻辑。
// Production 使用 KMS 获取专用 payment-token DEK，在启动阶段完成 Encryptor 初始化。
// Local/mock 使用合法的 MASTER_KEY_BASE64 构造 AES-GCM Encryptor。
// 密钥缺失、Base64 非法或长度不足时启动失败。
// 禁止 NoopTokenEncryptor 和明文 fallback。
// ============================================================================

// AESGCMTokenEncryptor 使用 AES-256-GCM 加密/解密 payment_method_token。
// 实现 TokenEncryptor 接口。
type AESGCMTokenEncryptor struct {
	gcm cipher.AEAD
}

// NewAESGCMTokenEncryptor 创建基于 AES-256-GCM 的 TokenEncryptor。
// key 必须为 32 字节（AES-256）。
func NewAESGCMTokenEncryptor(key []byte) (*AESGCMTokenEncryptor, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("token encryptor: key is empty, fail closed")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("token encryptor: AES-256-GCM key must be 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("token encryptor: create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("token encryptor: create GCM: %w", err)
	}

	return &AESGCMTokenEncryptor{gcm: gcm}, nil
}

// EncryptToken 加密 plaintext token，返回 nonce+ciphertext+tag。
func (e *AESGCMTokenEncryptor) EncryptToken(ctx context.Context, tenantID uuid.UUID, attemptID uuid.UUID, plaintextToken string) ([]byte, error) {
	if e == nil || e.gcm == nil {
		return nil, fmt.Errorf("token encryptor: not initialized, fail closed")
	}

	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("token encryptor: generate nonce: %w", err)
	}

	// AAD = tenantID || attemptID（绑定 token 到特定 attempt，防止跨 attempt 重放）
	aad := append(tenantID[:], attemptID[:]...)

	gcmCiphertext := e.gcm.Seal(nil, nonce, []byte(plaintextToken), aad)

	// 组装：nonce + gcmCiphertext(plaintext + tag)
	result := make([]byte, 0, len(nonce)+len(gcmCiphertext))
	result = append(result, nonce...)
	result = append(result, gcmCiphertext...)

	return result, nil
}

// DecryptToken 解密 EncryptToken 的输出。
func (e *AESGCMTokenEncryptor) DecryptToken(ctx context.Context, tenantID uuid.UUID, attemptID uuid.UUID, ciphertext []byte) (string, error) {
	if e == nil || e.gcm == nil {
		return "", fmt.Errorf("token encryptor: not initialized, fail closed")
	}

	nonceSize := e.gcm.NonceSize()
	if len(ciphertext) < nonceSize+e.gcm.Overhead() {
		return "", fmt.Errorf("token encryptor: ciphertext too short")
	}

	nonce := ciphertext[:nonceSize]
	gcmCiphertext := ciphertext[nonceSize:]

	aad := append(tenantID[:], attemptID[:]...)

	plaintext, err := e.gcm.Open(nil, nonce, gcmCiphertext, aad)
	if err != nil {
		return "", fmt.Errorf("token encryptor: decrypt (tamper detected): %w", err)
	}

	return string(plaintext), nil
}

// NewTokenEncryptorFromKMS 通过 KMS 获取 payment-token DEK 并构造 AES-GCM TokenEncryptor。
// Production 模式必须使用此函数。
//
// SEC-001: payment-token 需要跨进程恒定的稳定密钥，因此只接受支持确定性派生的
// KMS（crypto.StableDEKSource，即 MockKMS）。Vault transit 不导出密钥材质，
// 无法提供稳定 DEK；此时显式 fail closed 并说明，而不是像旧实现那样每次调用
// 拿到不同密钥导致已加密 token 不可恢复。真实模式下的稳定 token DEK 需要
// 持久化 wrapped DEK 存储（Backlog PAY-014）。
func NewTokenEncryptorFromKMS(ctx context.Context, kms crypto.KMS) (TokenEncryptor, error) {
	if kms == nil {
		return nil, errors.New("token encryptor: KMS is nil, fail closed")
	}

	src, ok := kms.(crypto.StableDEKSource)
	if !ok {
		return nil, errors.New("token encryptor: KMS does not support stable DEK derivation " +
			"(payment-token DEK requires a deterministic KMS backend; persistent wrapped-DEK storage not yet implemented)")
	}

	dek, err := src.StableDEK(ctx, "payment-token")
	if err != nil {
		return nil, fmt.Errorf("token encryptor: KMS DEK derivation failed: %w", err)
	}

	if len(dek) < 32 {
		return nil, fmt.Errorf("token encryptor: KMS DEK too short (%d bytes), need 32", len(dek))
	}

	enc, err := NewAESGCMTokenEncryptor(dek[:32])
	if err != nil {
		return nil, fmt.Errorf("token encryptor: create AES-GCM encryptor: %w", err)
	}

	return enc, nil
}

// NewTokenEncryptorFromMasterKey 通过 MASTER_KEY_BASE64 构造 AES-GCM TokenEncryptor。
// Local/mock 模式使用此函数。
func NewTokenEncryptorFromMasterKey(masterKeyBase64 string) (TokenEncryptor, error) {
	if masterKeyBase64 == "" {
		return nil, errors.New("token encryptor: MASTER_KEY_BASE64 is empty, fail closed")
	}

	key, err := base64.StdEncoding.DecodeString(masterKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("token encryptor: decode master key base64: %w", err)
	}

	if len(key) < 32 {
		return nil, fmt.Errorf("token encryptor: master key must be at least 32 bytes, got %d", len(key))
	}

	enc, err := NewAESGCMTokenEncryptor(key[:32])
	if err != nil {
		return nil, fmt.Errorf("token encryptor: create AES-GCM encryptor: %w", err)
	}

	return enc, nil
}

// NewTokenEncryptor 统一装配函数，根据环境选择 KMS 或 MASTER_KEY。
//
// Production/real 模式：使用 KMS 获取 payment-token DEK。
// Local/mock 模式：使用 MASTER_KEY_BASE64 构造 AES-GCM。
// 密钥缺失或非法时返回 error，调用方必须 fail-closed。
func NewTokenEncryptor(ctx context.Context, appEnv string, kms crypto.KMS, masterKeyBase64 string) (TokenEncryptor, error) {
	if appEnv == "production" {
		return NewTokenEncryptorFromKMS(ctx, kms)
	}
	return NewTokenEncryptorFromMasterKey(masterKeyBase64)
}
