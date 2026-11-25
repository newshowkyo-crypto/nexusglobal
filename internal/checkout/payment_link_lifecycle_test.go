package checkout

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedWebhookProviderCode 查询 fixture 商户通道绑定的 provider_code
// （webhook URL 的 provider_id 必须与之一致才能通过 SEC-008 归属校验）。
func seedWebhookProviderCode(ctx context.Context, t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID) string {
	t.Helper()
	var providerCode string
	err := pool.QueryRow(ctx, `
		SELECT p.provider_code
		FROM tenant_acquirer_channel c
		JOIN payment_provider p ON p.id = c.provider_id
		WHERE c.merchant_id = $1
		LIMIT 1
	`, merchantID).Scan(&providerCode)
	if err != nil {
		t.Fatalf("query provider code failed: %v", err)
	}
	return providerCode
}

// mustTestPayloadEncryptor 构造测试用 webhook payload AES-GCM 加密器。
func mustTestPayloadEncryptor(t *testing.T) PayloadEncryptor {
	t.Helper()
	enc, err := NewAESGCMPayloadEncryptor(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("construct payload encryptor failed: %v", err)
	}
	return enc
}

// PAY-009: PAYMENT_LINK attempt 捕获成功时，同一事务内将链接标记为 PAID。
func TestWebhookCapture_MarksPaymentLinkPaid(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(ctx, t, pool, merchantID))
	enc := newTestTokenEncryptor(t)
	service.SetTokenEncryptor(enc)

	paymentLinkSvc := NewPaymentLinkServiceWithEncryptor(pool, enc)
	paymentLinkSvc.SetChannelRegistry(service.channelRegistry)

	result, err := paymentLinkSvc.CreatePaymentLink(ctx, CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "mark_paid_order",
		AmountMinor:     3000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	confirm, err := paymentLinkSvc.ConfirmPayment(ctx, result.Token, "tok_test")
	if err != nil {
		t.Fatalf("confirm payment failed: %v", err)
	}

	// 构造带 per-provider 验签 + encryptor 的 receiver（与生产一致）
	providerCode := seedWebhookProviderCode(ctx, t, pool, merchantID)
	signingKey := []byte("test_signing_key_16_bytes_long_enough")
	receiver := NewUnifiedWebhookReceiverWithEncryptor(pool, signingKey, mustTestPayloadEncryptor(t), nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/unified/{provider_type}/{provider_id}", receiver.HandleWebhook)
	server := httptest.NewServer(mux)
	defer server.Close()

	event := WebhookEventPayload{
		EventType:       "payment.captured",
		ProviderEventID: "evt_mark_paid_" + uuid.New().String(),
		AttemptID:       confirm.PaymentAttemptID,
		Status:          "captured",
		ProviderRef:     "ref_mark_paid",
	}
	payload, _ := json.Marshal(event)
	signature := computeMockHMAC(payload, signingKey)

	url := server.URL + "/webhook/unified/test/" + providerCode
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", signature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook request failed: %v", err)
	}
	var respBody map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&respBody)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, respBody)
	}

	ingressID, _ := respBody["ingress_id"].(string)
	if ingressID == "" {
		t.Fatal("expected ingress_id in response")
	}
	eventID, err := uuid.Parse(ingressID)
	if err != nil {
		t.Fatalf("parse ingress id failed: %v", err)
	}

	// 事件已入库并被 outbox 异步消费——测试直接驱动一次 processEvent
	if err := receiver.processEvent(ctx, eventID); err != nil {
		t.Fatalf("processEvent failed: %v", err)
	}

	// attempt 必须 CAPTURED
	attemptID, _ := uuid.Parse(confirm.PaymentAttemptID)
	attempt, err := NewPaymentAttemptRepository(pool).GetPaymentAttempt(ctx, attemptID)
	if err != nil {
		t.Fatalf("get attempt failed: %v", err)
	}
	if attempt.Status != AttemptCaptured {
		t.Errorf("expected attempt CAPTURED, got %s", attempt.Status)
	}

	// PAY-009: 链接必须 PAID
	linkID, _ := uuid.Parse(result.PaymentLinkID)
	var linkStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM payment_links WHERE id = $1`, linkID).Scan(&linkStatus); err != nil {
		t.Fatalf("query link failed: %v", err)
	}
	if linkStatus != "PAID" {
		t.Errorf("PAY-009 VIOLATION: expected link PAID, got %s", linkStatus)
	}
}

// PAY-010: rotate 单事务——旧链接 REVOKED、新链接 ACTIVE 且 rotated_from_id
// 正确、旧链接未决 attempt CANCELED。
func TestRotate_SingleTransaction_Outcome(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(ctx, t, pool, merchantID))
	enc := newTestTokenEncryptor(t)
	service.SetTokenEncryptor(enc)

	paymentLinkSvc := NewPaymentLinkServiceWithEncryptor(pool, enc)
	paymentLinkSvc.SetChannelRegistry(service.channelRegistry)

	result, err := paymentLinkSvc.CreatePaymentLink(ctx, CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "rotate_order",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	newResult, err := paymentLinkSvc.Rotate(ctx, result.Token)
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}

	oldLinkID, _ := uuid.Parse(result.PaymentLinkID)
	newLinkID, _ := uuid.Parse(newResult.PaymentLinkID)

	var oldStatus, newStatus string
	var rotatedFrom uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT status FROM payment_links WHERE id = $1`, oldLinkID).Scan(&oldStatus); err != nil {
		t.Fatalf("query old link failed: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status, rotated_from_id FROM payment_links WHERE id = $1`, newLinkID).Scan(&newStatus, &rotatedFrom); err != nil {
		t.Fatalf("query new link failed: %v", err)
	}

	if oldStatus != "REVOKED" {
		t.Errorf("expected old link REVOKED, got %s", oldStatus)
	}
	if newStatus != "ACTIVE" {
		t.Errorf("expected new link ACTIVE, got %s", newStatus)
	}
	if rotatedFrom != oldLinkID {
		t.Errorf("expected rotated_from_id = old link, got %s", rotatedFrom)
	}

	// 旧链接未决 attempt 必须已被取消
	var pendingCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM payment_attempts
		WHERE entry_channel = 'PAYMENT_LINK' AND entry_ref = $1::text
		  AND status IN ('REQUIRES_ACTION', 'PROCESSING', 'AUTHORIZED')
	`, oldLinkID.String()).Scan(&pendingCount); err != nil {
		t.Fatalf("query attempts failed: %v", err)
	}
	if pendingCount != 0 {
		t.Errorf("expected 0 pending attempts on old link, got %d", pendingCount)
	}

	// 新 token 可解析，旧 token 不可解析
	if _, err := paymentLinkSvc.ResolveByToken(ctx, newResult.Token); err != nil {
		t.Errorf("new token should resolve, got %v", err)
	}
	if _, err := paymentLinkSvc.ResolveByToken(ctx, result.Token); err == nil {
		t.Error("old token should NOT resolve after rotate")
	}

	// 对已吊销链接再次 rotate → ErrPaymentLinkNotFound
	if _, err := paymentLinkSvc.Rotate(ctx, result.Token); err == nil {
		t.Error("rotating a revoked link should fail")
	}
}
