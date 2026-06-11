package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

type Meta struct {
	TenantID   string
	RecordID   string
	FieldName  string
	KeyVersion string
}

type KMS interface {
	DEK(ctx context.Context, tenantID string) ([]byte, error)
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

func (m *MockKMS) DEK(_ context.Context, tenantID string) ([]byte, error) {
	dek := make([]byte, 32)
	copy(dek, m.masterKey)
	for i, b := range []byte(tenantID) {
		dek[i%32] ^= b
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
	dek, err := e.kms.DEK(ctx, meta.TenantID)
	if err != nil {
		return nil, fmt.Errorf("crypto: derive DEK: %w", err)
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}

	aad := buildAAD(meta.TenantID, meta.RecordID, meta.FieldName, meta.KeyVersion)
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)

	result := make([]byte, len(nonce)+len(ciphertext))
	copy(result, nonce)
	copy(result[len(nonce):], ciphertext)

	return result, nil
}

func (e *Encryptor) Decrypt(ctx context.Context, data []byte, meta Meta) ([]byte, error) {
	dek, err := e.kms.DEK(ctx, meta.TenantID)
	if err != nil {
		return nil, fmt.Errorf("crypto: derive DEK: %w", err)
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new GCM: %w", err)
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

func buildAAD(tenantID, recordID, fieldName, keyVersion string) []byte {
	return []byte(tenantID + "|" + recordID + "|" + fieldName + "|" + keyVersion)
}
