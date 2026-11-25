// Package crypto — envelope encryption（SEC-001 P0 修复）。
//
// 密文信封格式 v2（自包含，可跨进程/跨调用/跨 key version 解密）：
//
//	"NXE2" magic (4B) | wrappedLen (uint16 BE, 2B) | wrappedDEK | nonce | ciphertext+tag
//
// Encrypt 流程：KMS 生成随机 DEK（返回明文 + KEK 包装形式）→ 本地 AES-256-GCM 加密 →
// 输出 [wrapped DEK || 密文]。Decrypt 流程：解析信封 → KMS 解包 DEK → 本地解密。
//
// 旧 v1 原始格式（nonce||ct，无信封）仅在使用确定性派生 KMS（MockKMS）时可通过
// StableDEK 回退解密；VaultKMS 的 v1 数据因旧实现 DEK 不可复现而无法解密，
// Decrypt 对此显式报错（fail closed），不做静默兜底。
package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// envelopeMagic v2 信封魔数。随机旧密文以该 4 字节开头的概率约 1/2^32，
// 且误判时 unwrap/open 认证失败会回退 legacy 路径，不会产生错误明文。
var envelopeMagic = [4]byte{'N', 'X', 'E', '2'}

const (
	envelopeHeaderLen = 4 + 2 // magic + wrappedLen
	maxWrappedDEKLen  = 4096  // Vault transit ciphertext 远小于此；防御性上限
)

type Meta struct {
	TenantID   string
	RecordID   string
	FieldName  string
	KeyVersion string
}

// KMS 是数据密钥（DEK）的信封加密后端。
// 实现必须保证：同一 wrappedDEK 经 UnwrapDEK 恢复出与 GenerateDEK 返回时
// 完全相同的明文 DEK；wrappedDEK 随密文持久化，因此跨进程可解密。
type KMS interface {
	// GenerateDEK 生成新的随机 32 字节数据密钥，并返回其 KEK 包装形式。
	GenerateDEK(ctx context.Context, tenantID string) (plaintextDEK, wrappedDEK []byte, err error)
	// UnwrapDEK 从包装形式恢复明文 DEK。
	UnwrapDEK(ctx context.Context, tenantID string, wrappedDEK []byte) ([]byte, error)
}

// StableDEKSource 提供确定性派生密钥（同 purpose 恒定）。
// 仅 Mock/local KMS 实现；Vault 等不导出密钥材质的后端必须拒绝。
type StableDEKSource interface {
	StableDEK(ctx context.Context, purpose string) ([]byte, error)
}

type MockKMS struct {
	masterKey []byte
}

func NewMockKMS(masterKeyBase64 string) (*MockKMS, error) {
	key, err := base64.StdEncoding.DecodeString(masterKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("crypto: invalid master key base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("crypto: master key must be 32 bytes, got %d", len(key))
	}
	return &MockKMS{masterKey: key}, nil
}

// StableDEK 确定性派生（masterKey XOR purpose）。
// 同时作为 legacy v1 密文的解密路径：旧 MockKMS.DEK 语义完全一致。
func (m *MockKMS) StableDEK(_ context.Context, purpose string) ([]byte, error) {
	dek := make([]byte, 32)
	copy(dek, m.masterKey)
	for i, b := range []byte(purpose) {
		dek[i%32] ^= b
	}
	return dek, nil
}

// GenerateDEK 生成随机 DEK，并用 masterKey 作为 KEK 以 AES-256-GCM 包装，
// AAD 绑定 tenant，防止 wrapped DEK 被跨租户移植。
func (m *MockKMS) GenerateDEK(_ context.Context, tenantID string) ([]byte, []byte, error) {
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, nil, fmt.Errorf("crypto: generate DEK: %w", err)
	}
	block, err := aes.NewCipher(m.masterKey)
	if err != nil {
		return nil, nil, fmt.Errorf("crypto: kek cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("crypto: kek gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("crypto: kek nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, dek, []byte("nexus-dek|"+tenantID))
	wrapped := make([]byte, 0, len(nonce)+len(sealed))
	wrapped = append(wrapped, nonce...)
	wrapped = append(wrapped, sealed...)
	return dek, wrapped, nil
}

// UnwrapDEK 恢复 MockKMS 包装的 DEK。
func (m *MockKMS) UnwrapDEK(_ context.Context, tenantID string, wrappedDEK []byte) ([]byte, error) {
	block, err := aes.NewCipher(m.masterKey)
	if err != nil {
		return nil, fmt.Errorf("crypto: kek cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: kek gcm: %w", err)
	}
	if len(wrappedDEK) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("crypto: wrapped DEK too short")
	}
	nonce := wrappedDEK[:gcm.NonceSize()]
	sealed := wrappedDEK[gcm.NonceSize():]
	dek, err := gcm.Open(nil, nonce, sealed, []byte("nexus-dek|"+tenantID))
	if err != nil {
		return nil, fmt.Errorf("crypto: unwrap DEK: %w", err)
	}
	return dek, nil
}

type Encryptor struct {
	kms KMS
}

func NewEncryptor(kms KMS) *Encryptor {
	return &Encryptor{kms: kms}
}

func (e *Encryptor) Encrypt(ctx context.Context, plaintext []byte, meta Meta) ([]byte, error) {
	dek, wrapped, err := e.kms.GenerateDEK(ctx, meta.TenantID)
	if err != nil {
		return nil, fmt.Errorf("crypto: generate DEK: %w", err)
	}
	if len(dek) != 32 {
		return nil, fmt.Errorf("crypto: DEK must be 32 bytes, got %d", len(dek))
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}

	aad := buildAAD(meta.TenantID, meta.RecordID, meta.FieldName, meta.KeyVersion)
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)

	out := make([]byte, 0, envelopeHeaderLen+2+len(wrapped)+len(nonce)+len(ciphertext))
	out = append(out, envelopeMagic[:]...)
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(wrapped)))
	out = append(out, lenBuf[:]...)
	out = append(out, wrapped...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

func (e *Encryptor) Decrypt(ctx context.Context, data []byte, meta Meta) ([]byte, error) {
	if hasEnvelopeMagic(data) {
		return e.decryptEnvelope(ctx, data, meta)
	}
	return e.decryptLegacy(ctx, data, meta)
}

func (e *Encryptor) decryptEnvelope(ctx context.Context, data []byte, meta Meta) ([]byte, error) {
	if len(data) < envelopeHeaderLen {
		return nil, errors.New("crypto: envelope too short")
	}
	wrappedLen := int(binary.BigEndian.Uint16(data[4:6]))
	if wrappedLen <= 0 || wrappedLen > maxWrappedDEKLen {
		return nil, fmt.Errorf("crypto: invalid wrapped DEK length %d", wrappedLen)
	}
	total := envelopeHeaderLen + wrappedLen
	if len(data) < total {
		return nil, errors.New("crypto: envelope truncated")
	}
	wrapped := data[envelopeHeaderLen:total]
	sealed := data[total:]

	dek, err := e.kms.UnwrapDEK(ctx, meta.TenantID, wrapped)
	if err != nil {
		return nil, err
	}
	return openGCM(dek, sealed, meta)
}

// decryptLegacy 处理 v1 原始密文（nonce||ct，确定性派生 DEK）。
// 仅当 KMS 支持 StableDEK（MockKMS）时可行；VaultKMS 的 v1 数据不可恢复，
// 显式报错而不是静默失败。
func (e *Encryptor) decryptLegacy(ctx context.Context, data []byte, meta Meta) ([]byte, error) {
	src, ok := e.kms.(StableDEKSource)
	if !ok {
		return nil, errors.New("crypto: legacy v1 ciphertext requires a deterministic KMS; " +
			"data encrypted before envelope encryption is not recoverable with this KMS backend")
	}
	dek, err := src.StableDEK(ctx, meta.TenantID)
	if err != nil {
		return nil, fmt.Errorf("crypto: derive legacy DEK: %w", err)
	}
	return openGCM(dek, data, meta)
}

func openGCM(dek, data []byte, meta Meta) ([]byte, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize+gcm.Overhead() {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce := data[:nonceSize]
	ciphertext := data[nonceSize:]
	aad := buildAAD(meta.TenantID, meta.RecordID, meta.FieldName, meta.KeyVersion)
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypt: %w", err)
	}
	return plaintext, nil
}

func hasEnvelopeMagic(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return data[0] == envelopeMagic[0] && data[1] == envelopeMagic[1] &&
		data[2] == envelopeMagic[2] && data[3] == envelopeMagic[3]
}

func buildAAD(tenantID, recordID, fieldName, keyVersion string) []byte {
	return []byte(tenantID + "|" + recordID + "|" + fieldName + "|" + keyVersion)
}
