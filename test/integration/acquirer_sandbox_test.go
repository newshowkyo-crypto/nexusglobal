package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/acquirer/adapter"
	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/shopspring/decimal"
)

// TestStripeContract_AuthorizeCaptureRefund 用本地 httptest 模拟 Stripe API，
// 覆盖 Authorize → Capture → Refund 的请求构造与状态映射。
// 默认即可跑、不出网；provider id 保持真实形态（pi_/re_ 前缀）。
func TestStripeContract_AuthorizeCaptureRefund(t *testing.T) {
	const (
		providerRef = "pi_test_123"
		refundRef   = "re_test_456"
	)

	secret := envOr("STRIPE_SANDBOX_SECRET", "sk_test_sandbox_dummy")

	var mu sync.Mutex
	var paths []string
	record := func(p string) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, p)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r.Method + " " + r.URL.Path)

		if got, want := r.Header.Get("Authorization"), "Bearer "+secret; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/payment_intents":
			expectForm(t, r, "amount", "10000")
			expectForm(t, r, "currency", "usd")
			expectForm(t, r, "capture_method", "manual")
			expectForm(t, r, "payment_method", "pm_card_visa")
			// 必须立即 confirm，否则 PaymentIntent 停在 requires_confirmation，后续 Capture 必失败
			expectForm(t, r, "confirm", "true")
			writeJSON(t, w, map[string]interface{}{
				"id": providerRef, "object": "payment_intent",
				"amount": 10000, "currency": "usd", "status": "requires_capture",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/payment_intents/"+providerRef+"/capture":
			expectForm(t, r, "amount_to_capture", "10000")
			writeJSON(t, w, map[string]interface{}{
				"id": providerRef, "object": "payment_intent",
				"amount": 10000, "currency": "usd", "status": "succeeded",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
			expectForm(t, r, "payment_intent", providerRef)
			expectForm(t, r, "amount", "5000")
			expectForm(t, r, "reason", "requested_by_customer")
			writeJSON(t, w, map[string]interface{}{
				"id": refundRef, "object": "refund",
				"amount": 5000, "currency": "usd", "status": "succeeded",
			})

		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := adapter.NewStripeClient(adapter.StripeConfig{
		SecretKey: secret,
		BaseURL:   srv.URL,
	})
	if err != nil {
		t.Fatalf("Failed to create Stripe client: %v", err)
	}

	ctx := context.Background()
	testChannelID := uuid.New()

	authResult, err := client.Authorize(ctx, domain.AuthorizeRequest{
		OrderID:            "test-order-" + uuid.New().String(),
		MerchantID:         uuid.New(),
		ChannelID:          testChannelID,
		Amount:             decimal.NewFromInt(100),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_visa",
		IdempotencyKey:     uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}
	if authResult.Status != domain.PaymentStatusAuthorized {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusAuthorized, authResult.Status)
	}
	if authResult.ProviderRef != providerRef {
		t.Errorf("ProviderRef = %q, want %q", authResult.ProviderRef, providerRef)
	}
	// TransactionID 是本系统内部的 UUID（不再由 provider id 反解），ProviderRef 才是渠道侧引用
	if authResult.TransactionID == uuid.Nil {
		t.Error("TransactionID should not be empty")
	}

	captureResult, err := client.Capture(ctx, domain.CaptureRequest{
		ChannelID:      testChannelID,
		ProviderRef:    authResult.ProviderRef,
		Amount:         decimal.NewFromInt(100),
		IdempotencyKey: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("Capture failed: %v", err)
	}
	if captureResult.Status != domain.PaymentStatusCaptured {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusCaptured, captureResult.Status)
	}
	if captureResult.ProviderRef != providerRef {
		t.Errorf("Capture ProviderRef = %q, want %q", captureResult.ProviderRef, providerRef)
	}
	if !captureResult.CapturedAmount.Equal(decimal.NewFromInt(100)) {
		t.Errorf("CapturedAmount = %s, want 100", captureResult.CapturedAmount.String())
	}

	refundResult, err := client.Refund(ctx, domain.RefundRequest{
		ChannelID:      testChannelID,
		ProviderRef:    authResult.ProviderRef,
		Amount:         decimal.NewFromInt(50),
		Reason:         "requested_by_customer",
		IdempotencyKey: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}
	if refundResult.Status != domain.PaymentStatusRefunded {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusRefunded, refundResult.Status)
	}
	if refundResult.ProviderRef != refundRef {
		t.Errorf("Refund ProviderRef = %q, want %q", refundResult.ProviderRef, refundRef)
	}
	if !refundResult.RefundedAmount.Equal(decimal.NewFromInt(50)) {
		t.Errorf("RefundedAmount = %s, want 50", refundResult.RefundedAmount.String())
	}

	mu.Lock()
	calls := append([]string(nil), paths...)
	mu.Unlock()
	if len(calls) != 3 {
		t.Errorf("expected 3 API calls (authorize/capture/refund), got %d: %v", len(calls), calls)
	}

	t.Logf("Stripe contract test PASSED: Auth=%s, Capture=%s, Refund=%s",
		authResult.ProviderRef, captureResult.ProviderRef, refundResult.ProviderRef)
}

func TestStripeSandbox_WebhookSignature(t *testing.T) {
	// Webhook 验签只做本地 HMAC 比对、不发网络请求，因此不依赖真实沙箱凭证：
	// 未配置环境变量时用固定假凭证，保证用例默认就能跑；配置了则用真实值。
	stripeSecret := envOr("STRIPE_SANDBOX_SECRET", "sk_test_sandbox_dummy")
	webhookSecret := envOr("STRIPE_SANDBOX_WEBHOOK_SECRET", "whsec_sandbox_dummy")

	ctx := context.Background()
	client, err := adapter.NewStripeClient(adapter.StripeConfig{
		SecretKey:     stripeSecret,
		WebhookSecret: webhookSecret,
	})
	if err != nil {
		t.Fatalf("Failed to create Stripe client: %v", err)
	}

	testCases := []struct {
		name       string
		payload    []byte
		signature  string
		expectFail bool
	}{
		{
			name:       "empty payload should fail",
			payload:    []byte{},
			signature:  "",
			expectFail: true,
		},
		{
			name:       "invalid signature should fail",
			payload:    []byte(`{"id": "evt_test", "type": "payment_intent.succeeded"}`),
			signature:  "t=12345,v1=invalid_signature",
			expectFail: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := domain.VerifyWebhookRequest{
				ChannelID: uuid.New(),
				Payload:   tc.payload,
				Signature: tc.signature,
			}

			_, err := client.VerifyWebhook(ctx, req)
			if tc.expectFail && err == nil {
				t.Error("Expected verification to fail, but it succeeded")
			}
			if !tc.expectFail && err != nil {
				t.Errorf("Expected verification to succeed, but got error: %v", err)
			}
		})
	}

	t.Log("Stripe webhook signature test PASSED")
}

// TestNxtapayContract_AuthorizeCaptureRefund 用本地 httptest 模拟 Nxtapay API，
// 覆盖 Authorize → Capture → Refund 的请求构造与状态映射。
// 默认即可跑、不出网。
func TestNxtapayContract_AuthorizeCaptureRefund(t *testing.T) {
	const (
		txnID    = "nx_tx_777"
		refundID = "nx_rf_888"
	)

	apiKey := envOr("NXTAPAY_SANDBOX_API_KEY", "nxtapay_test_key")
	apiSecret := envOr("NXTAPAY_SANDBOX_API_SECRET", "nxtapay_test_secret")
	merchantID := envOr("NXTAPAY_SANDBOX_MERCHANT_ID", "nxtapay_test_merchant")

	var mu sync.Mutex
	var paths []string
	record := func(p string) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, p)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r.Method + " " + r.URL.Path)

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/payments/authorize":
			expectJSON(t, body, "merchant_id", merchantID)
			expectJSON(t, body, "amount", "100.00")
			expectJSON(t, body, "currency", "USD")
			expectJSON(t, body, "payment_method", "pm_card_visa")
			expectJSON(t, body, "capture_mode", "deferred")
			writeJSON(t, w, map[string]interface{}{
				"transaction_id": txnID,
				"status":         "AUTHORIZED",
				"expires_at":     "2030-01-01T00:00:00Z",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/payments/capture":
			expectJSON(t, body, "transaction_id", txnID)
			expectJSON(t, body, "amount", "100.00")
			writeJSON(t, w, map[string]interface{}{
				"transaction_id": txnID,
				"status":         "CAPTURED",
				"amount":         "100.00",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/refunds":
			expectJSON(t, body, "transaction_id", txnID)
			expectJSON(t, body, "amount", "50.00")
			expectJSON(t, body, "reason", "requested_by_customer")
			writeJSON(t, w, map[string]interface{}{
				"refund_id": refundID,
				"status":    "COMPLETED",
				"amount":    "50.00",
			})

		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := adapter.NewNxtapayClient(adapter.NxtapayConfig{
		APIKey:     apiKey,
		APISecret:  apiSecret,
		MerchantID: merchantID,
		BaseURL:    srv.URL,
	})
	if err != nil {
		t.Fatalf("Failed to create Nxtapay client: %v", err)
	}

	ctx := context.Background()
	testChannelID := uuid.New()

	authResult, err := client.Authorize(ctx, domain.AuthorizeRequest{
		OrderID:            "test-order-" + uuid.New().String(),
		MerchantID:         uuid.New(),
		ChannelID:          testChannelID,
		Amount:             decimal.NewFromInt(100),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_visa",
		IdempotencyKey:     uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}
	if authResult.Status != domain.PaymentStatusAuthorized {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusAuthorized, authResult.Status)
	}
	if authResult.ProviderRef != txnID {
		t.Errorf("ProviderRef = %q, want %q", authResult.ProviderRef, txnID)
	}
	// TransactionID 是本系统内部的 UUID，ProviderRef 才是渠道侧 transaction_id
	if authResult.TransactionID == uuid.Nil {
		t.Error("TransactionID should not be empty")
	}

	captureResult, err := client.Capture(ctx, domain.CaptureRequest{
		ChannelID:      testChannelID,
		ProviderRef:    authResult.ProviderRef,
		Amount:         decimal.NewFromInt(100),
		IdempotencyKey: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("Capture failed: %v", err)
	}
	if captureResult.Status != domain.PaymentStatusCaptured {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusCaptured, captureResult.Status)
	}
	if !captureResult.CapturedAmount.Equal(decimal.NewFromInt(100)) {
		t.Errorf("CapturedAmount = %s, want 100", captureResult.CapturedAmount.String())
	}

	refundResult, err := client.Refund(ctx, domain.RefundRequest{
		ChannelID:      testChannelID,
		ProviderRef:    authResult.ProviderRef,
		Amount:         decimal.NewFromInt(50),
		Reason:         "requested_by_customer",
		IdempotencyKey: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}
	if refundResult.Status != domain.PaymentStatusRefunded {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusRefunded, refundResult.Status)
	}
	if refundResult.ProviderRef != refundID {
		t.Errorf("Refund ProviderRef = %q, want %q", refundResult.ProviderRef, refundID)
	}
	if !refundResult.RefundedAmount.Equal(decimal.NewFromInt(50)) {
		t.Errorf("RefundedAmount = %s, want 50", refundResult.RefundedAmount.String())
	}

	mu.Lock()
	calls := append([]string(nil), paths...)
	mu.Unlock()
	if len(calls) != 3 {
		t.Errorf("expected 3 API calls (authorize/capture/refund), got %d: %v", len(calls), calls)
	}

	t.Logf("Nxtapay contract test PASSED: Auth=%s, Capture=%s, Refund=%s",
		authResult.ProviderRef, captureResult.ProviderRef, refundResult.ProviderRef)
}

func TestNxtapaySandbox_WebhookSignature(t *testing.T) {
	// 同 Stripe：验签是本地 HMAC，NxtapayClient 只是要求这几个字段非空，
	// 未配置真实沙箱时用固定假凭证，保证用例默认可跑。
	apiKey := envOr("NXTAPAY_SANDBOX_API_KEY", "nxtapay_test_key")
	apiSecret := envOr("NXTAPAY_SANDBOX_API_SECRET", "nxtapay_test_secret")
	merchantID := envOr("NXTAPAY_SANDBOX_MERCHANT_ID", "nxtapay_test_merchant")
	webhookSecret := envOr("NXTAPAY_SANDBOX_WEBHOOK_SECRET", "nxtapay_test_webhook_secret")

	ctx := context.Background()
	client, err := adapter.NewNxtapayClient(adapter.NxtapayConfig{
		APIKey:        apiKey,
		APISecret:     apiSecret,
		MerchantID:    merchantID,
		WebhookSecret: webhookSecret,
	})
	if err != nil {
		t.Fatalf("Failed to create Nxtapay client: %v", err)
	}

	testCases := []struct {
		name       string
		payload    []byte
		signature  string
		expectFail bool
	}{
		{
			name:       "empty payload should fail",
			payload:    []byte{},
			signature:  "",
			expectFail: true,
		},
		{
			name:       "invalid signature should fail",
			payload:    []byte(`{"transaction_id": "txn_test", "status": "success"}`),
			signature:  "invalid_signature",
			expectFail: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := domain.VerifyWebhookRequest{
				ChannelID: uuid.New(),
				Payload:   tc.payload,
				Signature: tc.signature,
			}

			_, err := client.VerifyWebhook(ctx, req)
			if tc.expectFail && err == nil {
				t.Error("Expected verification to fail, but it succeeded")
			}
			if !tc.expectFail && err != nil {
				t.Errorf("Expected verification to succeed, but got error: %v", err)
			}
		})
	}

	t.Log("Nxtapay webhook signature test PASSED")
}

// TestCredentialNotExposed 校验凭证不会通过 client 的对外可见面泄露：
// 既不能被 json.Marshal 序列化出来，也不能出现在任何导出字段上。
func TestCredentialNotExposed(t *testing.T) {
	secret := envOr("STRIPE_SANDBOX_SECRET", "sk_test_credential_leak_guard")

	client, err := adapter.NewStripeClient(adapter.StripeConfig{SecretKey: secret})
	if err != nil {
		t.Fatalf("Failed to create Stripe client: %v", err)
	}
	if client == nil {
		t.Fatal("Client should not be nil")
	}

	encoded, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("marshal client: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Errorf("secret leaked via json.Marshal: %s", encoded)
	}

	v := reflect.ValueOf(client).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if f.PkgPath != "" {
			// 未导出字段不属于对外契约，允许持有凭证
			continue
		}
		if v.Field(i).Kind() == reflect.String && strings.Contains(v.Field(i).String(), secret) {
			t.Errorf("secret leaked via exported field %s", f.Name)
		}
	}

	t.Log("Credential exposure check PASSED: credential is not reachable via exported fields or JSON")
}

// envOr 返回环境变量值；未设置或为空时回落 fallback。
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// expectForm 断言表单字段值；断言失败只记录错误，不中断 handler。
func expectForm(t *testing.T, r *http.Request, key, want string) {
	t.Helper()
	if got := r.FormValue(key); got != want {
		t.Errorf("%s = %q, want %q (path %s)", key, got, want, r.URL.Path)
	}
}

// expectJSON 断言 JSON 请求体字段值。
func expectJSON(t *testing.T, body map[string]interface{}, key string, want interface{}) {
	t.Helper()
	got, ok := body[key]
	if !ok {
		t.Errorf("missing field %q", key)
		return
	}
	if got != want {
		t.Errorf("%s = %v, want %v", key, got, want)
	}
}

// writeJSON 写出 JSON 响应。
func writeJSON(t *testing.T, w http.ResponseWriter, v interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("write json: %v", err)
	}
}
