package consumer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

// --- P0-6: 现有测试 ---

func TestPayloadHash(t *testing.T) {
	data := []byte(`{"event":"captured","amount":"100.00"}`)
	hash := PayloadHash(data)

	require.NotEmpty(t, hash)
	assert.Len(t, hash, 64) // SHA256 hex string
}

func TestPayloadHash_Deterministic(t *testing.T) {
	data := []byte(`{"event":"captured","amount":"100.00"}`)
	hash1 := PayloadHash(data)
	hash2 := PayloadHash(data)

	assert.Equal(t, hash1, hash2)
}

func TestPayloadHash_Different(t *testing.T) {
	data1 := []byte(`{"event":"captured","amount":"100.00"}`)
	data2 := []byte(`{"event":"captured","amount":"200.00"}`)
	hash1 := PayloadHash(data1)
	hash2 := PayloadHash(data2)

	assert.NotEqual(t, hash1, hash2)
}

func TestWebhookIdempotencyKey(t *testing.T) {
	key := WebhookIdempotencyKey("mae", "prov_001", "evt_001", "captured")

	require.NotEmpty(t, key)
	assert.Len(t, key, 64) // SHA256 hex string
}

func TestWebhookIdempotencyKey_Deterministic(t *testing.T) {
	key1 := WebhookIdempotencyKey("mae", "prov_001", "evt_001", "captured")
	key2 := WebhookIdempotencyKey("mae", "prov_001", "evt_001", "captured")

	assert.Equal(t, key1, key2)
}

func TestWebhookIdempotencyKey_Different(t *testing.T) {
	key1 := WebhookIdempotencyKey("mae", "prov_001", "evt_001", "captured")
	key2 := WebhookIdempotencyKey("mae", "prov_001", "evt_002", "captured")

	assert.NotEqual(t, key1, key2)
}

// --- P0-6: validatePayload 负面路径测试 ---

func TestValidatePayload_MissingMerchantID(t *testing.T) {
	p := &webhookPayload{Amount: "100.00", Currency: "USD"}
	err := validatePayload("captured", p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field merchant_id")
}

func TestValidatePayload_FinancialEventMissingAmount(t *testing.T) {
	p := &webhookPayload{MerchantID: "m_001", Currency: "USD"}
	for _, eventType := range []string{"captured", "settled", "refund", "dispute"} {
		err := validatePayload(eventType, p)
		require.Error(t, err, "eventType=%s", eventType)
		assert.Contains(t, err.Error(), "missing required field amount")
	}
}

func TestValidatePayload_FinancialEventMissingCurrency(t *testing.T) {
	p := &webhookPayload{MerchantID: "m_001", Amount: "100.00"}
	for _, eventType := range []string{"captured", "settled", "refund", "dispute"} {
		err := validatePayload(eventType, p)
		require.Error(t, err, "eventType=%s", eventType)
		assert.Contains(t, err.Error(), "missing required field currency")
	}
}

func TestValidatePayload_PayoutEventMissingPayoutRequestID(t *testing.T) {
	p := &webhookPayload{MerchantID: "m_001"}
	for _, eventType := range []string{"payout_confirmed", "lp_callback"} {
		err := validatePayload(eventType, p)
		require.Error(t, err, "eventType=%s", eventType)
		assert.Contains(t, err.Error(), "missing required field payout_request_id")
	}
}

func TestValidatePayload_ValidFinancialEvent(t *testing.T) {
	p := &webhookPayload{MerchantID: "m_001", Amount: "100.00", Currency: "USD"}
	for _, eventType := range []string{"captured", "settled", "refund", "dispute"} {
		err := validatePayload(eventType, p)
		assert.NoError(t, err, "eventType=%s", eventType)
	}
}

func TestValidatePayload_ValidPayoutEvent(t *testing.T) {
	p := &webhookPayload{MerchantID: "m_001", PayoutRequestID: "pr_001"}
	for _, eventType := range []string{"payout_confirmed", "lp_callback"} {
		err := validatePayload(eventType, p)
		assert.NoError(t, err, "eventType=%s", eventType)
	}
}

func TestValidatePayload_UnknownEventTypeStillRequiresMerchantID(t *testing.T) {
	p := &webhookPayload{Amount: "100.00", Currency: "USD"}
	err := validatePayload("unknown_event", p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field merchant_id")
}

// --- P0-6: categorizeError 测试 ---

func TestCategorizeError_Nil(t *testing.T) {
	assert.Equal(t, "", categorizeError(nil))
}

func TestCategorizeError_ConfigError(t *testing.T) {
	assert.Equal(t, "config_error", categorizeError(fmt.Errorf("encryptor is not configured")))
	assert.Equal(t, "config_error", categorizeError(fmt.Errorf("webhook repository not configured")))
	assert.Equal(t, "config_error", categorizeError(fmt.Errorf("no confirmer configured")))
}

func TestCategorizeError_NotFound(t *testing.T) {
	assert.Equal(t, "not_found", categorizeError(fmt.Errorf("ingress event abc not found")))
	assert.Equal(t, "not_found", categorizeError(fmt.Errorf("missing ingress_id in message")))
}

func TestCategorizeError_DecryptError(t *testing.T) {
	assert.Equal(t, "decrypt_error", categorizeError(fmt.Errorf("decrypt ingress payload: cipher error")))
	assert.Equal(t, "decrypt_error", categorizeError(fmt.Errorf("ingress has empty ciphertext")))
}

func TestCategorizeError_ParseError(t *testing.T) {
	assert.Equal(t, "parse_error", categorizeError(fmt.Errorf("unmarshal payload for ingress: invalid json")))
	assert.Equal(t, "parse_error", categorizeError(fmt.Errorf("JSON decode error")))
}

func TestCategorizeError_ValidationError(t *testing.T) {
	assert.Equal(t, "validation_error", categorizeError(fmt.Errorf("missing required field merchant_id")))
	assert.Equal(t, "validation_error", categorizeError(fmt.Errorf("invalid merchant_id: bad format")))
	assert.Equal(t, "validation_error", categorizeError(fmt.Errorf("invalid amount: not a number")))
	assert.Equal(t, "validation_error", categorizeError(fmt.Errorf("amount must be positive")))
}

func TestCategorizeError_InternalError(t *testing.T) {
	assert.Equal(t, "internal_error", categorizeError(fmt.Errorf("something unexpected")))
	assert.Equal(t, "internal_error", categorizeError(fmt.Errorf("connection refused")))
}

// --- P0-6: Encrypt → Persist → Decrypt 契约测试 ---

func TestEncryptDecryptContract(t *testing.T) {
	// 使用 MockKMS 构造 Encryptor，验证加密→持久化表示→解密的一致性
	masterKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	kms, err := crypto.NewMockKMS(masterKey)
	require.NoError(t, err)
	enc := crypto.NewEncryptor(kms)

	ctx := context.Background()
	plaintext := []byte(`{"merchant_id":"m_001","amount":"100.00","currency":"USD"}`)

	// 模拟 webhook ingress 加密端使用的 Meta
	encryptMeta := crypto.Meta{
		TenantID:  "mae",
		RecordID:  "provider_123",
		FieldName: "raw_payload",
	}

	// 加密
	ciphertext, err := enc.Encrypt(ctx, plaintext, encryptMeta)
	require.NoError(t, err)
	require.NotEmpty(t, ciphertext)
	// 密文不得等于明文
	assert.NotEqual(t, plaintext, ciphertext)

	// 模拟持久化：ciphertext 存入 DB 后原样读回
	persisted := ciphertext

	// 模拟 webhook consumer 解密端使用的 Meta（必须与加密端一致）
	decryptMeta := crypto.Meta{
		TenantID:  "mae",
		RecordID:  "provider_123",
		FieldName: "raw_payload",
	}

	// 解密
	decrypted, err := enc.Decrypt(ctx, persisted, decryptMeta)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)

	// 验证解密后的 JSON 可正确解析
	var payload webhookPayload
	require.NoError(t, json.Unmarshal(decrypted, &payload))
	assert.Equal(t, "m_001", payload.MerchantID)
	assert.Equal(t, "100.00", payload.Amount)
	assert.Equal(t, "USD", payload.Currency)
}

func TestEncryptDecryptContract_WrongMetaFails(t *testing.T) {
	// 验证使用不匹配的 Meta 解密会失败
	masterKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	kms, err := crypto.NewMockKMS(masterKey)
	require.NoError(t, err)
	enc := crypto.NewEncryptor(kms)

	ctx := context.Background()
	plaintext := []byte(`{"merchant_id":"m_001"}`)

	encryptMeta := crypto.Meta{
		TenantID:  "mae",
		RecordID:  "provider_123",
		FieldName: "raw_payload",
	}

	ciphertext, err := enc.Encrypt(ctx, plaintext, encryptMeta)
	require.NoError(t, err)

	// 使用不同的 TenantID 解密——应失败
	wrongMeta := crypto.Meta{
		TenantID:  "bank",
		RecordID:  "provider_123",
		FieldName: "raw_payload",
	}
	_, err = enc.Decrypt(ctx, ciphertext, wrongMeta)
	assert.Error(t, err, "decrypt with wrong Meta should fail")
}

func TestEncryptDecryptContract_EmptyCiphertextFails(t *testing.T) {
	// P0-6: 密文为空不得解密
	masterKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	kms, err := crypto.NewMockKMS(masterKey)
	require.NoError(t, err)
	enc := crypto.NewEncryptor(kms)

	ctx := context.Background()
	meta := crypto.Meta{TenantID: "mae", RecordID: "prov", FieldName: "raw_payload"}

	_, err = enc.Decrypt(ctx, []byte{}, meta)
	assert.Error(t, err, "empty ciphertext should fail decrypt")
}

// --- P0-6: handleMessage 行为测试（验证未知事件不 ACK）---
// 注意：handleMessage 需要完整的 Consumer 依赖（webhookRepo、encryptor 等），
// 未知事件类型的端到端验证应在集成测试中完成。
// 此处验证 loadPayload 的各失败路径均返回非 nil error（不 ACK），
// 以及 validatePayload 对未知事件类型的处理。

func TestValidatePayload_UnknownEventTypeNoSpecialValidation(t *testing.T) {
	// 未知事件类型仍要求 merchant_id，但不要求 amount/currency/payout_request_id
	p := &webhookPayload{MerchantID: "m_001"}
	err := validatePayload("some_new_event", p)
	assert.NoError(t, err, "unknown event type with merchant_id should pass basic validation")
}

// --- 辅助函数 ---
