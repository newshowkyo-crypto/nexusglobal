package checkout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ============================================================================
// Webhook 统一 Receiver 测试
// ============================================================================

// newTestWebhookReceiver 创建带 AES-GCM payload 加密器的测试 receiver。
// P0-6: 无 encryptor 的 receiver fail-closed 拒绝存储，因此断言事件落库的
// 测试必须通过此构造器注入加密器。
func newTestWebhookReceiver(pool *pgxpool.Pool) *UnifiedWebhookReceiver {
	enc, err := NewAESGCMPayloadEncryptor(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		panic(err)
	}
	return NewUnifiedWebhookReceiverWithEncryptor(pool, []byte("test_signing_key"), enc, nil)
}

// TestWebhook_Deduplication 验证重复事件不重复处理。
//
// RED-10: 幂等键 UNIQUE，重复事件不重复入账。
func TestWebhook_Deduplication(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	signingKey := []byte("test_signing_key")
	receiver := newTestWebhookReceiver(pool)

	// 创建 webhook receiver HTTP handler
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/unified/{provider_type}/{provider_id}", receiver.HandleWebhook)
	server := httptest.NewServer(mux)
	defer server.Close()

	// 构造事件
	attemptID := uuid.New()
	event := WebhookEventPayload{
		EventType:       "payment.captured",
		ProviderEventID: "evt_dup_001",
		AttemptID:       attemptID.String(),
		Status:          "captured",
		ProviderRef:     "ref_001",
	}
	payload, _ := json.Marshal(event)
	signature := computeMockHMAC(payload, signingKey)

	// 发送第一次
	url := server.URL + "/webhook/unified/test/test_provider"
	req1, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Signature", signature)
	req1.Header.Set("X-Provider-Event-Id", "evt_dup_001")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp1.StatusCode)
	}

	// 发送第二次（重复）
	req2, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Signature", signature)
	req2.Header.Set("X-Provider-Event-Id", "evt_dup_001")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for duplicate, got %d", resp2.StatusCode)
	}

	var resp2Body map[string]interface{}
	json.NewDecoder(resp2.Body).Decode(&resp2Body)
	if resp2Body["duplicated"] != true {
		t.Error("duplicate event should be marked as duplicated")
	}

	// RED-10: DB 中应该只有 1 个事件
	var eventCount int
	err = pool.QueryRow(context.Background(), `
		SELECT count(*) FROM unified_webhook_events WHERE provider_id = 'test_provider'
	`).Scan(&eventCount)
	if err != nil {
		t.Fatalf("query count failed: %v", err)
	}
	if eventCount != 1 {
		t.Errorf("RED-10 VIOLATION: expected 1 event, got %d", eventCount)
	}
}

// TestWebhook_InvalidSignature 验证签名验证。
func TestWebhook_InvalidSignature(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	signingKey := []byte("test_signing_key")
	receiver := NewUnifiedWebhookReceiver(pool, signingKey, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/unified/{provider_type}/{provider_id}", receiver.HandleWebhook)
	server := httptest.NewServer(mux)
	defer server.Close()

	event := WebhookEventPayload{
		EventType:       "payment.captured",
		ProviderEventID: "evt_invalid_sig",
		Status:          "captured",
	}
	payload, _ := json.Marshal(event)

	url := server.URL + "/webhook/unified/test/test_provider"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", "invalid_signature")
	req.Header.Set("X-Provider-Event-Id", "evt_invalid_sig")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid signature, got %d", resp.StatusCode)
	}
}

// TestWebhook_MissingSignature 验证缺少签名被拒绝。
func TestWebhook_MissingSignature(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	receiver := NewUnifiedWebhookReceiver(pool, []byte("key"), nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/unified/{provider_type}/{provider_id}", receiver.HandleWebhook)
	server := httptest.NewServer(mux)
	defer server.Close()

	url := server.URL + "/webhook/unified/test/test_provider"
	req, _ := http.NewRequest("POST", url, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing signature, got %d", resp.StatusCode)
	}
}

// TestWebhook_ConcurrentSameEvent 验证并发相同事件只处理一次。
//
// RED-10: 并发相同事件不重复入账。
func TestWebhook_ConcurrentSameEvent(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	signingKey := []byte("test_signing_key")
	receiver := newTestWebhookReceiver(pool)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/unified/{provider_type}/{provider_id}", receiver.HandleWebhook)
	server := httptest.NewServer(mux)
	defer server.Close()

	event := WebhookEventPayload{
		EventType:       "payment.captured",
		ProviderEventID: "evt_concurrent_001",
		Status:          "captured",
		ProviderRef:     "ref_concurrent",
	}
	payload, _ := json.Marshal(event)
	signature := computeMockHMAC(payload, signingKey)

	url := server.URL + "/webhook/unified/test/concurrent_provider"

	const numGoroutines = 50
	var wg sync.WaitGroup
	var successCount int64
	var mu sync.Mutex

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Signature", signature)
			req.Header.Set("X-Provider-Event-Id", "evt_concurrent_001")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// 所有请求都应成功（重复返回 200）
	if successCount != numGoroutines {
		t.Errorf("expected %d successes, got %d", numGoroutines, successCount)
	}

	// RED-10: DB 中应该只有 1 个事件
	var eventCount int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM unified_webhook_events WHERE provider_id = 'concurrent_provider'
	`).Scan(&eventCount)
	if err != nil {
		t.Fatalf("query count failed: %v", err)
	}
	if eventCount != 1 {
		t.Errorf("RED-10 VIOLATION: expected 1 event, got %d", eventCount)
	}
}

// TestWebhook_PayloadEncrypted 验证 payload 加密存储。
//
// RED-15: 敏感字段加密。
// P0-6: 使用 AES-256-GCM 替代 XOR。
func TestWebhook_PayloadEncrypted(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	signingKey := []byte("test_signing_key_16_bytes_long_enough")
	// P0-6: payload 加密器必须配置，否则 receiver fail-closed 拒绝存储
	enc, err := NewAESGCMPayloadEncryptor(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("construct payload encryptor failed: %v", err)
	}
	receiver := NewUnifiedWebhookReceiverWithEncryptor(pool, signingKey, enc, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/unified/{provider_type}/{provider_id}", receiver.HandleWebhook)
	server := httptest.NewServer(mux)
	defer server.Close()

	event := WebhookEventPayload{
		EventType:       "payment.captured",
		ProviderEventID: "evt_encrypted_001",
		Status:          "captured",
		ProviderRef:     "ref_secret_data_should_not_be_plaintext",
	}
	payload, _ := json.Marshal(event)
	signature := computeMockHMAC(payload, signingKey)

	url := server.URL + "/webhook/unified/test/encrypted_provider"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", signature)
	req.Header.Set("X-Provider-Event-Id", "evt_encrypted_001")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// 查询 DB 中的 payload_encrypted
	var encryptedPayload []byte
	err = pool.QueryRow(context.Background(), `
		SELECT payload_encrypted FROM unified_webhook_events WHERE provider_id = 'encrypted_provider'
	`).Scan(&encryptedPayload)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	// RED-15: payload 应该被加密（不等于明文）
	if bytes.Equal(encryptedPayload, payload) {
		t.Error("RED-15 VIOLATION: payload should be encrypted, not plaintext")
	}

	// P0-6: 使用 AES-256-GCM 解密后应该等于原始 payload
	encryptor := receiver.Encryptor()
	if encryptor == nil {
		t.Fatal("encryptor should not be nil")
	}
	decrypted, err := encryptor.Decrypt(encryptedPayload)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if !bytes.Equal(decrypted, payload) {
		t.Error("decrypted payload should match original")
	}
}

// ============================================================================
// Mock Provider 测试
// ============================================================================

// TestMockProvider_SuccessScenario 验证成功场景。
func TestMockProvider_SuccessScenario(t *testing.T) {
	mp := NewMockProvider(MockScenarioSuccess, "", nil)
	defer mp.Close()

	req := MockChargeRequest{
		OrderID:            "order_001",
		AttemptID:          uuid.New().String(),
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test",
	}
	payload, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", mp.URL(), bytes.NewReader(payload))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result MockChargeResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Status != "captured" {
		t.Errorf("expected captured, got %s", result.Status)
	}
	if mp.CallCount() != 1 {
		t.Errorf("expected 1 call, got %d", mp.CallCount())
	}
}

// TestMockProvider_FailureScenario 验证拒绝场景。
func TestMockProvider_FailureScenario(t *testing.T) {
	mp := NewMockProvider(MockScenarioFailure, "", nil)
	defer mp.Close()

	req := MockChargeRequest{
		OrderID:   "order_fail",
		AttemptID: uuid.New().String(),
	}
	payload, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", mp.URL(), bytes.NewReader(payload))
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result MockChargeResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Status != "failed" {
		t.Errorf("expected failed, got %s", result.Status)
	}
	if result.FailureCode != "CARD_DECLINED" {
		t.Errorf("expected CARD_DECLINED, got %s", result.FailureCode)
	}
}

// TestMockProvider_3DSRedirectScenario 验证 3DS redirect 场景。
func TestMockProvider_3DSRedirectScenario(t *testing.T) {
	mp := NewMockProvider(MockScenario3DSRedirect, "", nil)
	defer mp.Close()

	attemptID := uuid.New().String()
	req := MockChargeRequest{
		OrderID:   "order_3ds",
		AttemptID: attemptID,
	}
	payload, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", mp.URL(), bytes.NewReader(payload))
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result MockChargeResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Status != "requires_action" {
		t.Errorf("expected requires_action, got %s", result.Status)
	}
	if result.ActionType != "3DS_CHALLENGE" {
		t.Errorf("expected 3DS_CHALLENGE, got %s", result.ActionType)
	}
	if result.RedirectURL == "" {
		t.Error("redirect URL should not be empty")
	}
}

// TestMockProvider_TimeoutScenario 验证 timeout 场景。
func TestMockProvider_TimeoutScenario(t *testing.T) {
	mp := NewMockProvider(MockScenarioTimeout, "", nil)
	defer mp.Close()

	// 设置 mock timeout 大于客户端 timeout，让客户端先超时
	mp.SetTimeoutDelay(500 * time.Millisecond)

	req := MockChargeRequest{
		OrderID:   "order_timeout",
		AttemptID: uuid.New().String(),
	}
	payload, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", mp.URL(), bytes.NewReader(payload))
	// 设置短超时，让客户端先超时
	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, err := client.Do(httpReq)
	if err == nil {
		t.Error("expected timeout error")
	}
}

// TestMockProvider_DuplicateWebhookScenario 验证重复 webhook 场景。
func TestMockProvider_DuplicateWebhookScenario(t *testing.T) {
	// 创建一个简单的 webhook receiver 来接收 webhook
	var webhookCount int64
	webhookMu := &sync.Mutex{}
	webhookHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		webhookMu.Lock()
		webhookCount++
		webhookMu.Unlock()
	})
	webhookServer := httptest.NewServer(webhookHandler)
	defer webhookServer.Close()

	mp := NewMockProvider(MockScenarioDuplicateWebhook, webhookServer.URL, []byte("wh_secret"))
	defer mp.Close()
	mp.SetWebhookDelay(50 * time.Millisecond)

	req := MockChargeRequest{
		OrderID:   "order_dup_wh",
		AttemptID: uuid.New().String(),
	}
	payload, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", mp.URL(), bytes.NewReader(payload))
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// 等待 webhook 发送
	time.Sleep(500 * time.Millisecond)

	// 应该发送 2 个 webhook
	if mp.WebhooksSent() != 2 {
		t.Errorf("expected 2 webhooks, got %d", mp.WebhooksSent())
	}

	// 两个 webhook 应该有相同的 provider_event_id
	events := mp.GetWebhookEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

// TestMockProvider_OutOfOrderWebhookScenario 验证乱序 webhook 场景。
func TestMockProvider_OutOfOrderWebhookScenario(t *testing.T) {
	// 创建 webhook receiver
	webhookHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	webhookServer := httptest.NewServer(webhookHandler)
	defer webhookServer.Close()

	mp := NewMockProvider(MockScenarioOutOfOrderWebhook, webhookServer.URL, []byte("wh_secret"))
	defer mp.Close()
	mp.SetWebhookDelay(50 * time.Millisecond)

	req := MockChargeRequest{
		OrderID:   "order_ooo_wh",
		AttemptID: uuid.New().String(),
	}
	payload, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", mp.URL(), bytes.NewReader(payload))
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// 等待 webhook 发送
	time.Sleep(500 * time.Millisecond)

	// 应该发送 2 个 webhook
	if mp.WebhooksSent() != 2 {
		t.Errorf("expected 2 webhooks, got %d", mp.WebhooksSent())
	}

	// 验证乱序：第一个 sequence=2，第二个 sequence=1
	events := mp.GetWebhookEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Sequence != 2 {
		t.Errorf("expected first event sequence=2, got %d", events[0].Sequence)
	}
	if events[1].Sequence != 1 {
		t.Errorf("expected second event sequence=1, got %d", events[1].Sequence)
	}
}

// TestMockProvider_LateWebhookScenario 验证晚到 webhook 场景。
func TestMockProvider_LateWebhookScenario(t *testing.T) {
	// 创建 webhook receiver
	webhookHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	webhookServer := httptest.NewServer(webhookHandler)
	defer webhookServer.Close()

	mp := NewMockProvider(MockScenarioLateWebhook, webhookServer.URL, []byte("wh_secret"))
	defer mp.Close()
	mp.SetLateWebhookDelay(200 * time.Millisecond)

	req := MockChargeRequest{
		OrderID:   "order_late_wh",
		AttemptID: uuid.New().String(),
	}
	payload, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", mp.URL(), bytes.NewReader(payload))
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result MockChargeResponse
	json.NewDecoder(resp.Body).Decode(&result)

	// 初始响应应该是 processing
	if result.Status != "processing" {
		t.Errorf("expected processing, got %s", result.Status)
	}

	// 等待晚到 webhook
	time.Sleep(500 * time.Millisecond)

	// 应该发送 1 个 webhook
	if mp.WebhooksSent() != 1 {
		t.Errorf("expected 1 webhook, got %d", mp.WebhooksSent())
	}

	events := mp.GetWebhookEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Status != "captured" {
		t.Errorf("expected captured webhook, got %s", events[0].Status)
	}
}

// Ensure fmt is used
var _ = fmt.Sprintf
