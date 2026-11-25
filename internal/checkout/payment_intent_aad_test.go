package checkout

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
)

// TestR5P01_AADTokenEncryptionConsistency 验证 Payment Intent 加密 token 的 AAD ID 一致性（R5 P0-1）。
//
// 缺陷背景：修复前 createPaymentIntentInternal 使用 attemptIDForEnc := uuid.New() 作为
// 加密 AAD，而落库的 attempt 是另一个 uuid.New() 结果。解密时以落库 attempt.ID 作为 AAD，
// AAD mismatch 导致解密永远失败。
//
// 回归断言：
//  1. ProcessPaymentIntent 入库后，payment_method_token_encrypted 非空。
//  2. payment_method_token 明文列为空。
//  3. 用落库的 attempt ID 解密必须成功且等于原始 token（修复前此断言必然失败）。
func TestR5P01_AADTokenEncryptionConsistency(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	// 32 字节测试密钥（仅用于测试，不打印）
	testKey := []byte("0123456789abcdef0123456789abcdef")
	masterKeyB64 := base64.StdEncoding.EncodeToString(testKey)

	enc, err := NewTokenEncryptorFromMasterKey(masterKeyB64)
	if err != nil {
		t.Fatalf("construct token encryptor failed: %v", err)
	}

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	service.SetTokenEncryptor(enc)

	const rawToken = "tok_aad_consistency_secret"

	result, err := service.ProcessPaymentIntent(context.Background(), ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     "idem_aad_consistency_001",
		PayloadHash:        "hash_aad_consistency_001",
		MerchantOrderID:    "order_aad_consistency",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: rawToken,
	})
	if err != nil {
		t.Fatalf("process payment intent failed: %v", err)
	}

	attemptID, err := uuid.Parse(result.PaymentAttemptID)
	if err != nil {
		t.Fatalf("parse attempt id failed: %v", err)
	}

	// 以 DB 为真相读取存储结果
	var plaintextToken string
	var encryptedToken []byte
	err = pool.QueryRow(context.Background(), `
		SELECT payment_method_token, payment_method_token_encrypted
		FROM payment_attempts WHERE id = $1
	`, attemptID).Scan(&plaintextToken, &encryptedToken)
	if err != nil {
		t.Fatalf("query attempt failed: %v", err)
	}

	if len(encryptedToken) == 0 {
		t.Fatal("payment_method_token_encrypted must not be empty")
	}
	if plaintextToken != "" {
		t.Errorf("payment_method_token plaintext column must be empty, got non-empty value")
	}

	// 核心断言：用落库 attempt ID 作为 AAD 解密必须成功。
	// 修复前加密 AAD 是另一个随机 UUID，此断言必然失败。
	decrypted, decErr := enc.DecryptToken(context.Background(), merchantID, attemptID, encryptedToken)
	if decErr != nil {
		t.Fatalf("decrypt with stored attempt ID failed (AAD mismatch): %v", decErr)
	}
	if decrypted != rawToken {
		t.Errorf("decrypted token mismatch: got different value than original")
	}
}
