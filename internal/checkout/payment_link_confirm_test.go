package checkout

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/failclosed"
)

// ============================================================================
// C1: Payment Link Confirm Payment 测试
//
// POST /pay/{token}/confirm — 让用户通过 Payment Link 提交支付方法后
// 真正进入支付生命周期。
// ============================================================================

// TestConfirmPaymentLink_Success 验证有效 token + payment_method_token
// 返回 200 + payment_attempt_id + status REQUIRES_ACTION。
func TestConfirmPaymentLink_Success(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	pool := getTestPool(t)
	defer pool.Close()

	// P0-C1: 使用带 Encryptor 的 PaymentLinkService，禁止明文 token 存储
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
	paymentLinkSvc := NewPaymentLinkServiceWithEncryptor(pool, enc)
	paymentLinkSvc.SetChannelRegistry(service.channelRegistry)
	handler := NewCheckoutResolverHandler(service, paymentLinkSvc)

	// 创建 Payment Link（非 single_use，便于直接 confirm）
	result, err := paymentLinkSvc.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_confirm_success",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       false,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	// 构造 POST /pay/{token}/confirm 请求
	reqBody, _ := json.Marshal(map[string]string{
		"payment_method_token": "tok_test_12345",
	})

	r := chi.NewRouter()
	r.Post("/pay/{token}/confirm", handler.ConfirmPaymentLink)

	req := httptest.NewRequest(http.MethodPost, "/pay/"+result.Token+"/confirm", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	if resp["payment_attempt_id"] == nil || resp["payment_attempt_id"] == "" {
		t.Error("expected non-empty payment_attempt_id in response")
	}

	if resp["status"] != "REQUIRES_ACTION" {
		t.Errorf("expected status REQUIRES_ACTION, got %v", resp["status"])
	}

	// 验证 DB 中 payment_attempt 的 payment_method_token 已更新
	attemptIDStr, _ := resp["payment_attempt_id"].(string)
	attemptID, _ := uuid.Parse(attemptIDStr)
	attemptRepo := NewPaymentAttemptRepository(pool)
	attempt, err := attemptRepo.GetPaymentAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatalf("get payment attempt failed: %v", err)
	}

	// P0-C1: 有 encryptor 时加密存储，明文列必须为空
	if attempt.PaymentMethodToken != "" {
		t.Errorf("expected empty payment_method_token (encrypted storage), got %q", attempt.PaymentMethodToken)
	}
	// 直接查 DB 验证加密列非空
	var encryptedTokenCol []byte
	err = pool.QueryRow(context.Background(), `
		SELECT payment_method_token_encrypted FROM payment_attempts WHERE id = $1
	`, attemptID).Scan(&encryptedTokenCol)
	if err != nil {
		t.Fatalf("query encrypted token failed: %v", err)
	}
	if len(encryptedTokenCol) == 0 {
		t.Error("expected non-empty payment_method_token_encrypted in DB")
	}

	// 验证 Outbox 中有 PAYMENT_DISPATCH_REQUESTED 事件
	var outboxCount int
	err = pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM transactional_outbox
		WHERE aggregate_id = $1 AND event_type = 'PAYMENT_DISPATCH_REQUESTED'
	`, attemptID).Scan(&outboxCount)
	if err != nil {
		t.Fatalf("query outbox failed: %v", err)
	}
	if outboxCount != 1 {
		t.Errorf("expected 1 PAYMENT_DISPATCH_REQUESTED outbox entry, got %d", outboxCount)
	}

	// 验证 Outbox payload 不含 payment_method_token
	var payload []byte
	err = pool.QueryRow(context.Background(), `
		SELECT payload FROM transactional_outbox
		WHERE aggregate_id = $1 AND event_type = 'PAYMENT_DISPATCH_REQUESTED'
	`, attemptID).Scan(&payload)
	if err != nil {
		t.Fatalf("query outbox payload failed: %v", err)
	}
	if bytes.Contains(payload, []byte("tok_test_12345")) {
		t.Error("outbox payload contains payment_method_token — security violation")
	}
}

// TestConfirmPaymentLink_InvalidToken 验证无效 token 返回 404 + error 字段。
// G09: 不泄露记录存在性。
func TestConfirmPaymentLink_InvalidToken(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	paymentLinkSvc := NewPaymentLinkService(pool)
	paymentLinkSvc.SetChannelRegistry(service.channelRegistry)
	handler := NewCheckoutResolverHandler(service, paymentLinkSvc)

	reqBody, _ := json.Marshal(map[string]string{
		"payment_method_token": "tok_test_12345",
	})

	r := chi.NewRouter()
	r.Post("/pay/{token}/confirm", handler.ConfirmPaymentLink)

	// 使用无效 token（64 hex chars 但不存在）
	invalidToken := "0000000000000000000000000000000000000000000000000000000000000000"
	req := httptest.NewRequest(http.MethodPost, "/pay/"+invalidToken+"/confirm", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	if resp["error"] == "" {
		t.Error("expected error field in response")
	}
}

// TestConfirmPaymentLink_ExpiredLink 验证过期链接返回 404（不泄露存在性）。
// G09: 过期链接和无效链接返回相同的错误，不泄露记录存在性。
func TestConfirmPaymentLink_ExpiredLink(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	paymentLinkSvc := NewPaymentLinkService(pool)
	paymentLinkSvc.SetChannelRegistry(service.channelRegistry)
	handler := NewCheckoutResolverHandler(service, paymentLinkSvc)

	// 创建已过期的 Payment Link
	expiredAt := time.Now().Add(-1 * time.Hour)
	result, err := paymentLinkSvc.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_confirm_expired",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       false,
		ExpiresAt:       &expiredAt,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	reqBody, _ := json.Marshal(map[string]string{
		"payment_method_token": "tok_test_12345",
	})

	r := chi.NewRouter()
	r.Post("/pay/{token}/confirm", handler.ConfirmPaymentLink)

	req := httptest.NewRequest(http.MethodPost, "/pay/"+result.Token+"/confirm", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	// G09: 过期链接返回 404（不泄露存在性），不是 410
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404 for expired link (not leaking existence), got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	if resp["error"] == "" {
		t.Error("expected error field in response")
	}

	// 确保响应中不包含 "expired" 或 "EXPIRED" 字样（不泄露存在性）
	if resp["error"] == "PAYMENT_LINK_EXPIRED" {
		t.Error("error code should not reveal that the link existed but expired")
	}
}

// TestConfirmPaymentLink_Lockdown 验证封锁状态返回 503。
// P0-2: 消费入口在封锁时必须拒绝。
func TestConfirmPaymentLink_Lockdown(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	pool := getTestPool(t)
	defer pool.Close()

	// 构造带 failClosedSvc 的 CheckoutService
	failClosedSvc := failclosed.NewService(pool, nil, nil)
	service := NewServiceWithFailClosed(pool, nil, nil, nil, failClosedSvc)
	paymentLinkSvc := NewPaymentLinkService(pool)
	handler := NewCheckoutResolverHandler(service, paymentLinkSvc)

	// 激活封锁
	failclosed.ActivateSystemLockdown()
	defer failclosed.ClearSystemLockdownForTest()

	reqBody, _ := json.Marshal(map[string]string{
		"payment_method_token": "tok_test_12345",
	})

	r := chi.NewRouter()
	r.Post("/pay/{token}/confirm", handler.ConfirmPaymentLink)

	// 使用任意 token（封锁检查在 token 校验之前）
	req := httptest.NewRequest(http.MethodPost, "/pay/anytoken/confirm", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 during lockdown, got %d: %s", rec.Code, rec.Body.String())
	}
}
