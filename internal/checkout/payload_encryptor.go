package checkout

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// ============================================================================
// P0-6: PayloadEncryptor 接口与 AES-256-GCM 实现
//
// 替换 webhook_receiver.go 中的 XOR encryptPayload/decryptPayload。
// XOR 不是真正的加密，仅是混淆。AES-256-GCM 提供机密性 + 完整性 + 认证。
//
// ciphertext 格式（固定头部 + AES-GCM 输出）：
//   ┌─────────┬───────────┬────────┬─────────┬──────────────┬──────┐
//   │ version │ algorithm │ key_id │  nonce  │  ciphertext  │ tag  │
//   │  1 byte │  1 byte   │ 1 byte │ 12 byte │  variable    │ 16B  │
//   └─────────┴───────────┴────────┴─────────┴──────────────┴──────┘
//
// version:   0x01（当前版本）
// algorithm: 0x01（AES-256-GCM）
// key_id:    0x01（当前密钥 ID，支持密钥轮换）
// nonce:     12 字节随机 nonce（每条记录唯一）
// ciphertext: AES-256-GCM 加密的 plaintext
// tag:       16 字节 GCM 认证标签（由 gcm.Seal 自动附加）
// ============================================================================

// PayloadEncryptor 是 webhook payload 加密/解密的抽象接口。
//
// 实现：
//   - AESGCMPayloadEncryptor: 本地/mock 使用 AES-256-GCM
//   - 生产环境可注入 KMS/Envelope Encryption 实现
type PayloadEncryptor interface {
	// Encrypt 加密 plaintext，返回包含版本头/nonce/ciphertext/tag 的字节串。
	// 每次调用生成唯一随机 nonce，保证语义安全。
	Encrypt(plaintext []byte) (ciphertext []byte, err error)

	// Decrypt 解密 Encrypt 的输出，验证认证标签后返回 plaintext。
	// 篡改任何字节（包括 nonce/ciphertext/tag）将返回错误。
	Decrypt(ciphertext []byte) (plaintext []byte, err error)
}

// ciphertext 格式常量
const (
	cipherVersion   byte = 0x01
	cipherAlgorithm byte = 0x01                                            // AES-256-GCM
	cipherKeyID     byte = 0x01                                            // 当前密钥 ID
	cipherHeaderLen      = 3                                               // version + algorithm + key_id
	cipherNonceLen       = 12                                              // AES-GCM 标准 nonce 长度
	cipherTagLen         = 16                                              // AES-GCM 标签长度
	cipherMinLen         = cipherHeaderLen + cipherNonceLen + cipherTagLen // 最小密文长度（空 plaintext）
)

// ErrEncryptionKeyMissing 表示加密密钥缺失，fail closed（拒绝明文回退）。
var ErrEncryptionKeyMissing = errors.New("ENCRYPTION_KEY_MISSING: payload encryptor key is missing, fail closed")

// ErrCipherTextTooShort 表示密文长度不足，无法包含头部+nonce+tag。
var ErrCipherTextTooShort = errors.New("CIPHERTEXT_TOO_SHORT: ciphertext does not contain header+nonce+tag")

// ErrCipherVersionMismatch 表示密文版本不匹配。
var ErrCipherVersionMismatch = errors.New("CIPHERTEXT_VERSION_MISMATCH: unsupported ciphertext version")

// ErrCipherAlgorithmMismatch 表示加密算法不匹配。
var ErrCipherAlgorithmMismatch = errors.New("CIPHERTEXT_ALGORITHM_MISMATCH: unsupported algorithm")

// AESGCMPayloadEncryptor 使用 AES-256-GCM 加密 payload。
//
// 密钥要求：
//   - 32 字节（AES-256）
//   - 密钥缺失时 Encrypt/Decrypt 返回 ErrEncryptionKeyMissing（fail closed）
//   - 禁止明文回退
type AESGCMPayloadEncryptor struct {
	gcm cipher.AEAD
	key []byte
}

// NewAESGCMPayloadEncryptor 创建 AES-256-GCM payload 加密器。
//
// key 必须为 32 字节。若 key 为空或长度不为 32，返回错误。
// 调用方应在启动时调用此函数，密钥缺失时拒绝启动。
func NewAESGCMPayloadEncryptor(key []byte) (*AESGCMPayloadEncryptor, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("%w: key is empty", ErrEncryptionKeyMissing)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("AES-256-GCM key must be 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	// 保留 key 副本防止外部修改
	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	return &AESGCMPayloadEncryptor{gcm: gcm, key: keyCopy}, nil
}

// Encrypt 加密 plaintext，返回 version+algorithm+key_id+nonce+ciphertext+tag。
//
// 每次调用生成唯一随机 nonce（12 字节），保证语义安全。
// 即使加密相同 plaintext，输出也不同。
func (e *AESGCMPayloadEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	if e == nil || e.gcm == nil {
		return nil, fmt.Errorf("%w: encryptor not initialized", ErrEncryptionKeyMissing)
	}

	// 生成随机 nonce（每条记录唯一）
	nonce := make([]byte, cipherNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// AES-256-GCM 加密（Seal 自动附加 16 字节 tag）
	// AAD 为空（webhook payload 自包含完整性）
	gcmCiphertext := e.gcm.Seal(nil, nonce, plaintext, nil)

	// 组装最终密文：header(3) + nonce(12) + gcmCiphertext(plaintext_len + tag_16)
	result := make([]byte, 0, cipherHeaderLen+cipherNonceLen+len(gcmCiphertext))
	result = append(result, cipherVersion)
	result = append(result, cipherAlgorithm)
	result = append(result, cipherKeyID)
	result = append(result, nonce...)
	result = append(result, gcmCiphertext...)

	return result, nil
}

// Decrypt 解密 Encrypt 的输出。
//
// 验证版本头、算法标识，然后使用 AES-256-GCM 解密并验证 tag。
// 任何字节篡改（nonce/ciphertext/tag）将返回错误。
func (e *AESGCMPayloadEncryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	if e == nil || e.gcm == nil {
		return nil, fmt.Errorf("%w: encryptor not initialized", ErrEncryptionKeyMissing)
	}
	if len(ciphertext) < cipherMinLen {
		return nil, fmt.Errorf("%w: got %d bytes, need at least %d",
			ErrCipherTextTooShort, len(ciphertext), cipherMinLen)
	}

	// 验证版本头
	if ciphertext[0] != cipherVersion {
		return nil, fmt.Errorf("%w: expected 0x%02x, got 0x%02x",
			ErrCipherVersionMismatch, cipherVersion, ciphertext[0])
	}
	// 验证算法
	if ciphertext[1] != cipherAlgorithm {
		return nil, fmt.Errorf("%w: expected 0x%02x, got 0x%02x",
			ErrCipherAlgorithmMismatch, cipherAlgorithm, ciphertext[1])
	}
	// key_id 当前仅支持 0x01，未来支持密钥轮换时需查找对应 key
	if ciphertext[2] != cipherKeyID {
		return nil, fmt.Errorf("unsupported key_id: 0x%02x", ciphertext[2])
	}

	// 提取 nonce 和 gcmCiphertext
	nonce := ciphertext[cipherHeaderLen : cipherHeaderLen+cipherNonceLen]
	gcmCiphertext := ciphertext[cipherHeaderLen+cipherNonceLen:]

	// AES-256-GCM 解密并验证 tag
	plaintext, err := e.gcm.Open(nil, nonce, gcmCiphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("AES-256-GCM decrypt (tamper detected): %w", err)
	}

	return plaintext, nil
}

// NoopPayloadEncryptor 是不加密的空实现，仅用于测试。
// 生产环境禁止使用。
type NoopPayloadEncryptor struct{}

// Encrypt 直接返回 plaintext（不加密）。
func (NoopPayloadEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	return plaintext, nil
}

// Decrypt 直接返回 ciphertext（不解密）。
func (NoopPayloadEncryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	return ciphertext, nil
}
