package checkout

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ============================================================================
// P0-6: AES-256-GCM Payload 加密测试
//
// 替换 XOR encryptPayload/decryptPayload，使用正式 AEAD 加密。
// ============================================================================

// TestWebhookPayload_AESGCMRoundTrip 验证 AES-256-GCM 加密/解密往返。
func TestWebhookPayload_AESGCMRoundTrip(t *testing.T) {
	// 32 字节密钥（AES-256）
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	enc, err := NewAESGCMPayloadEncryptor(key)
	if err != nil {
		t.Fatalf("create encryptor: %v", err)
	}

	// 测试多种 payload
	testPayloads := [][]byte{
		[]byte(`{"event":"payment.captured","amount":100}`),
		[]byte(`{}`),
		[]byte(`{"sensitive":"card_number_4111111111111111","cvv":"123"}`),
		make([]byte, 0),    // 空 payload
		make([]byte, 1024), // 1KB payload
	}

	for i, plaintext := range testPayloads {
		ciphertext, err := enc.Encrypt(plaintext)
		if err != nil {
			t.Errorf("payload %d: encrypt failed: %v", i, err)
			continue
		}

		decrypted, err := enc.Decrypt(ciphertext)
		if err != nil {
			t.Errorf("payload %d: decrypt failed: %v", i, err)
			continue
		}

		if !bytes.Equal(decrypted, plaintext) {
			t.Errorf("payload %d: decrypted != plaintext\ngot:  %x\nwant: %x", i, decrypted, plaintext)
		}
	}
}

// TestWebhookPayload_UniqueNonce 验证每次加密生成唯一 nonce。
// 即使加密相同 plaintext，密文也必须不同（语义安全）。
func TestWebhookPayload_UniqueNonce(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	enc, err := NewAESGCMPayloadEncryptor(key)
	if err != nil {
		t.Fatalf("create encryptor: %v", err)
	}

	plaintext := []byte(`{"event":"test","data":"same_payload"}`)

	// 加密相同 payload 100 次
	const numEncryptions = 100
	ciphertexts := make(map[string]bool, numEncryptions)
	nonces := make(map[string]bool, numEncryptions)

	for i := 0; i < numEncryptions; i++ {
		ciphertext, err := enc.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("encryption %d failed: %v", i, err)
		}

		// 密文必须唯一
		ctStr := fmt.Sprintf("%x", ciphertext)
		if ciphertexts[ctStr] {
			t.Fatalf("encryption %d: ciphertext is not unique — same output for same plaintext", i)
		}
		ciphertexts[ctStr] = true

		// 提取 nonce（header 3 bytes + nonce 12 bytes）
		if len(ciphertext) < 15 {
			t.Fatalf("encryption %d: ciphertext too short", i)
		}
		nonce := ciphertext[3:15]
		nonceStr := fmt.Sprintf("%x", nonce)
		if nonces[nonceStr] {
			t.Fatalf("encryption %d: nonce is not unique — same nonce reused", i)
		}
		nonces[nonceStr] = true
	}

	if len(ciphertexts) != numEncryptions {
		t.Errorf("expected %d unique ciphertexts, got %d", numEncryptions, len(ciphertexts))
	}
	if len(nonces) != numEncryptions {
		t.Errorf("expected %d unique nonces, got %d", numEncryptions, len(nonces))
	}
}

// TestWebhookPayload_TamperRejected 验证密文篡改被拒绝。
// AES-256-GCM 提供完整性保护，任何字节篡改应导致解密失败。
func TestWebhookPayload_TamperRejected(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	enc, err := NewAESGCMPayloadEncryptor(key)
	if err != nil {
		t.Fatalf("create encryptor: %v", err)
	}

	plaintext := []byte(`{"event":"payment.captured","amount":100,"currency":"USD"}`)
	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// 1. 篡改 ciphertext 部分（header 之后的数据）
	tamperedCT := make([]byte, len(ciphertext))
	copy(tamperedCT, ciphertext)
	// 篡改最后一个字节（tag 区域）
	tamperedCT[len(tamperedCT)-1] ^= 0xFF
	_, err = enc.Decrypt(tamperedCT)
	if err == nil {
		t.Error("tampered ciphertext (last byte) should be rejected")
	}

	// 2. 篡改 nonce
	tamperedNonce := make([]byte, len(ciphertext))
	copy(tamperedNonce, ciphertext)
	tamperedNonce[5] ^= 0x01 // nonce 区域
	_, err = enc.Decrypt(tamperedNonce)
	if err == nil {
		t.Error("tampered nonce should be rejected")
	}

	// 3. 篡改 version
	tamperedVersion := make([]byte, len(ciphertext))
	copy(tamperedVersion, ciphertext)
	tamperedVersion[0] = 0x02 // 不同版本
	_, err = enc.Decrypt(tamperedVersion)
	if err == nil {
		t.Error("tampered version should be rejected")
	}

	// 4. 篡改 algorithm
	tamperedAlgo := make([]byte, len(ciphertext))
	copy(tamperedAlgo, ciphertext)
	tamperedAlgo[1] = 0x02 // 不同算法
	_, err = enc.Decrypt(tamperedAlgo)
	if err == nil {
		t.Error("tampered algorithm should be rejected")
	}

	// 5. 截断密文
	truncated := ciphertext[:len(ciphertext)-1]
	_, err = enc.Decrypt(truncated)
	if err == nil {
		t.Error("truncated ciphertext should be rejected")
	}

	// 6. 原始密文应能正常解密
	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("original ciphertext should decrypt successfully: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Error("original ciphertext should decrypt to original plaintext")
	}
}

// TestWebhookReceiver_MissingEncryptionKeyFailClosed 验证加密密钥缺失时 fail closed。
// 禁止明文回退：不存储未加密的 payload。
func TestWebhookReceiver_MissingEncryptionKeyFailClosed(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	// 使用有效的 signingKey（用于 HMAC 签名验证），但不注入 encryptor（模拟加密密钥缺失）
	signingKey := []byte("test_signing_key_for_hmac_validation")
	receiver := NewUnifiedWebhookReceiverWithEncryptor(pool, signingKey, nil, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/unified/{provider_type}/{provider_id}", receiver.HandleWebhook)
	server := httptest.NewServer(mux)
	defer server.Close()

	event := WebhookEventPayload{
		EventType:       "payment.captured",
		ProviderEventID: "evt_no_key_001",
		Status:          "captured",
	}
	payload, _ := json.Marshal(event)
	signature := computeMockHMAC(payload, signingKey)

	url := server.URL + "/webhook/unified/test/no_key_provider"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", signature)
	req.Header.Set("X-Provider-Event-Id", "evt_no_key_001")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// 必须返回 500（fail closed），不能存储明文
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 (fail closed) when encryption key missing, got %d", resp.StatusCode)
	}

	// 验证 DB 中没有存储任何事件（明文回退被拒绝）
	var eventCount int
	err = pool.QueryRow(context.Background(), `
		SELECT count(*) FROM unified_webhook_events WHERE provider_id = 'no_key_provider'
	`).Scan(&eventCount)
	if err != nil {
		t.Fatalf("query count failed: %v", err)
	}
	if eventCount != 0 {
		t.Errorf("fail closed violated: expected 0 events stored, got %d (plaintext fallback must be rejected)", eventCount)
	}
}

// TestWebhookLogs_DoNotContainSensitivePayload 验证日志中不包含原始敏感 payload。
// P0-6: 数据库和日志禁止输出 webhook 原始敏感 payload。
func TestWebhookLogs_DoNotContainSensitivePayload(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	signingKey := make([]byte, 32)
	for i := range signingKey {
		signingKey[i] = byte(i)
	}
	// P0-6: 注入 payload 加密器（无 encryptor 的 receiver fail-closed 拒绝存储）
	payloadEnc, err := NewAESGCMPayloadEncryptor(bytes.Repeat([]byte("w"), 32))
	if err != nil {
		t.Fatalf("construct payload encryptor failed: %v", err)
	}
	receiver := NewUnifiedWebhookReceiverWithEncryptor(pool, signingKey, payloadEnc, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/unified/{provider_type}/{provider_id}", receiver.HandleWebhook)
	server := httptest.NewServer(mux)
	defer server.Close()

	// 包含敏感数据的 payload
	sensitiveData := "4111111111111111" // 卡号
	event := WebhookEventPayload{
		EventType:       "payment.captured",
		ProviderEventID: "evt_sensitive_001",
		Status:          "captured",
		ProviderRef:     "ref_contains_" + sensitiveData,
	}
	payload, _ := json.Marshal(event)
	signature := computeMockHMAC(payload, signingKey)

	url := server.URL + "/webhook/unified/test/sensitive_provider"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", signature)
	req.Header.Set("X-Provider-Event-Id", "evt_sensitive_001")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// 1. 验证 DB 中的 payload_encrypted 不包含明文敏感数据
	var encryptedPayload []byte
	err = pool.QueryRow(context.Background(), `
		SELECT payload_encrypted FROM unified_webhook_events WHERE provider_id = 'sensitive_provider'
	`).Scan(&encryptedPayload)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	encryptedStr := fmt.Sprintf("%x", encryptedPayload)
	if strings.Contains(encryptedStr, sensitiveData) {
		t.Error("P0-6 VIOLATION: encrypted payload contains plaintext sensitive data (card number)")
	}

	// 2. 验证明文 payload 不在 DB 中（以任何形式）
	var payloadHash string
	_ = pool.QueryRow(context.Background(), `
		SELECT payload_hash FROM unified_webhook_events WHERE provider_id = 'sensitive_provider'
	`).Scan(&payloadHash)
	// payload_hash 是哈希值，不包含明文
	if strings.Contains(payloadHash, sensitiveData) {
		t.Error("P0-6 VIOLATION: payload_hash contains plaintext sensitive data")
	}

	// 3. 验证解密后能获取原始数据（加密是可逆的，但 DB 中存储的是密文）
	encryptor := receiver.Encryptor()
	if encryptor == nil {
		t.Fatal("encryptor should not be nil")
	}
	decrypted, err := encryptor.Decrypt(encryptedPayload)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if !bytes.Contains(decrypted, []byte(sensitiveData)) {
		t.Error("decrypted payload should contain original sensitive data (for authorized decryption only)")
	}
}

// TestWebhookPayload_CiphertextFormat 验证密文格式符合规范：
// version(1) + algorithm(1) + key_id(1) + nonce(12) + ciphertext + tag(16)
func TestWebhookPayload_CiphertextFormat(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	enc, err := NewAESGCMPayloadEncryptor(key)
	if err != nil {
		t.Fatalf("create encryptor: %v", err)
	}

	plaintext := []byte(`{"test":"format"}`)
	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// 1. 验证最小长度：header(3) + nonce(12) + tag(16) = 31 字节（空 plaintext）
	minLen := cipherHeaderLen + cipherNonceLen + cipherTagLen
	if len(ciphertext) < minLen {
		t.Errorf("ciphertext too short: got %d, need at least %d", len(ciphertext), minLen)
	}

	// 2. 验证 version
	if ciphertext[0] != 0x01 {
		t.Errorf("version: expected 0x01, got 0x%02x", ciphertext[0])
	}

	// 3. 验证 algorithm
	if ciphertext[1] != 0x01 {
		t.Errorf("algorithm: expected 0x01 (AES-256-GCM), got 0x%02x", ciphertext[1])
	}

	// 4. 验证 key_id
	if ciphertext[2] != 0x01 {
		t.Errorf("key_id: expected 0x01, got 0x%02x", ciphertext[2])
	}

	// 5. 验证 nonce 长度（12 字节，位于 header 之后）
	nonce := ciphertext[cipherHeaderLen : cipherHeaderLen+cipherNonceLen]
	if len(nonce) != cipherNonceLen {
		t.Errorf("nonce length: expected %d, got %d", cipherNonceLen, len(nonce))
	}

	// 6. 验证 tag 长度（16 字节，位于密文末尾）
	// AES-GCM 的 Seal 输出 = plaintext_len + tag_len
	// 所以 ciphertext 长度 = header(3) + nonce(12) + plaintext_len + tag(16)
	expectedLen := cipherHeaderLen + cipherNonceLen + len(plaintext) + cipherTagLen
	if len(ciphertext) != expectedLen {
		t.Errorf("ciphertext length: expected %d (header+nonce+plaintext+tag), got %d", expectedLen, len(ciphertext))
	}
}

// TestWebhookPayload_InvalidKeyRejected 验证无效密钥被拒绝。
func TestWebhookPayload_InvalidKeyRejected(t *testing.T) {
	// 1. 空密钥
	_, err := NewAESGCMPayloadEncryptor([]byte{})
	if err == nil {
		t.Error("empty key should be rejected")
	}

	// 2. 短密钥（16 字节，AES-128）
	_, err = NewAESGCMPayloadEncryptor(make([]byte, 16))
	if err == nil {
		t.Error("16-byte key should be rejected (need 32 for AES-256)")
	}

	// 3. 长密钥（64 字节）
	_, err = NewAESGCMPayloadEncryptor(make([]byte, 64))
	if err == nil {
		t.Error("64-byte key should be rejected (need exactly 32 for AES-256)")
	}

	// 4. 正确密钥（32 字节）
	enc, err := NewAESGCMPayloadEncryptor(make([]byte, 32))
	if err != nil {
		t.Errorf("32-byte key should be accepted: %v", err)
	}
	if enc == nil {
		t.Error("encryptor should not be nil for valid key")
	}
}

// TestWebhookPayload_EncryptorInterface 验证 AESGCMPayloadEncryptor 实现 PayloadEncryptor 接口。
func TestWebhookPayload_EncryptorInterface(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	enc, err := NewAESGCMPayloadEncryptor(key)
	if err != nil {
		t.Fatalf("create encryptor: %v", err)
	}

	// 编译期接口检查
	var _ PayloadEncryptor = enc

	// 运行时接口检查
	plaintext := []byte(`{"interface":"test"}`)
	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Error("round-trip failed")
	}
}

// 确保 uuid 包被引用（用于未来扩展）
var _ = uuid.New
