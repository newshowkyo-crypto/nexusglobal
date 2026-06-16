package adapter

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
)

const (
	testStripeSecretKey = "sk_test_1234567890abcdefghijklmnop"
	testWebhookSecret   = "whsec_test_secret_1234567890"
)

// mockStripeServer 模拟 Stripe API 服务器
type mockStripeServer struct {
	t            *testing.T
	server       *httptest.Server
	handler      func(w http.ResponseWriter, r *http.Request, body string)
	delay        time.Duration
	mu           sync.Mutex
	requestCount int
	requests     []capturedRequest
}

type capturedRequest struct {
	Method  string
	Path    string
	Body    string
	Headers http.Header
}

func newMockStripeServer(t *testing.T) *mockStripeServer {
	m := &mockStripeServer{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.requestCount++
		m.requests = append(m.requests, capturedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Body:    string(body),
			Headers: r.Header,
		})
		m.mu.Unlock()

		// 应用延迟
		if m.delay > 0 {
			time.Sleep(m.delay)
		}

		m.handler(w, r, string(body))
	})
	m.server = httptest.NewServer(mux)
	return m
}

func (m *mockStripeServer) close() {
	m.server.Close()
}

func (m *mockStripeServer) URL() string {
	return m.server.URL
}

func (m *mockStripeServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *mockStripeServer) Requests() []capturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]capturedRequest{}, m.requests...)
}

// TestStripeClient_NewStripeClient 验证客户端创建
func TestStripeClient_NewStripeClient(t *testing.T) {
	t.Run("ValidConfig", func(t *testing.T) {
		client, err := NewStripeClient(StripeConfig{
			SecretKey:     testStripeSecretKey,
			WebhookSecret: testWebhookSecret,
			Timeout:       30 * time.Second,
		})
		require.NoError(t, err)
		assert.NotNil(t, client)
	})

	t.Run("MissingSecretKey", func(t *testing.T) {
		client, err := NewStripeClient(StripeConfig{
			SecretKey: "",
		})
		assert.Error(t, err)
		assert.Nil(t, client)
	})
}

// TestStripeClient_Authorize_ContractSuccess 测试授权成功
func TestStripeClient_Authorize_ContractSuccess(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		// 验证请求头
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer "+testStripeSecretKey)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		// 验证请求参数
		assert.Contains(t, body, "amount=")
		assert.Contains(t, body, "currency=")
		assert.Contains(t, body, "payment_method=")
		assert.Contains(t, body, "capture_method=manual")

		// 返回成功响应
		resp := stripePaymentIntent{
			ID:     "pi_" + uuid.New().String()[:14],
			Object: "payment_intent",
			Amount: 10000, // $100.00
			Status: "requires_capture",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_visa",
		IdempotencyKey:     "idem_123",
	}

	resp, err := client.Authorize(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, domain.PaymentStatusAuthorized, resp.Status)
	assert.NotEmpty(t, resp.ProviderRef)
	assert.Equal(t, 1, server.RequestCount())
}

// TestStripeClient_Authorize_RequiresAction 测试需要 3DS 操作
func TestStripeClient_Authorize_RequiresAction(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		resp := stripePaymentIntent{
			ID:     "pi_" + uuid.New().String()[:14],
			Object: "payment_intent",
			Amount: 10000,
			Status: "requires_action",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_3ds",
	}

	resp, err := client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, IsRequiresAction(err))
	assert.NotNil(t, resp)
	assert.Equal(t, domain.PaymentStatusPending, resp.Status)
}

// TestStripeClient_Authorize_MissingToken 测试缺少 token
func TestStripeClient_Authorize_MissingToken(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		t.Error("should not call server when token is missing")
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:    "order_123",
		MerchantID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:     decimal.NewFromFloat(100.00),
		Currency:   "USD",
		// 故意不设置 PaymentMethodToken
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrPaymentMethodTokenRequired))
	assert.Equal(t, 0, server.RequestCount())
}

// TestStripeClient_Authorize_CardDeclined 测试卡片拒绝
func TestStripeClient_Authorize_CardDeclined(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(http.StatusPaymentRequired)
		// 返回包含 Error 字段的结构，模拟真实 Stripe API 响应
		resp := map[string]interface{}{
			"error": map[string]interface{}{
				"type":    "card_error",
				"code":    "card_declined",
				"message": "Your card was declined",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_declined",
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, IsExplicitDecline(err))
	assert.False(t, IsUnknownExternalState(err))
}

// TestStripeClient_Authorize_Timeout 测试超时
func TestStripeClient_Authorize_Timeout(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	// 模拟超时：延迟大于客户端 timeout
	server.delay = 2 * time.Second

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   100 * time.Millisecond, // 100ms timeout
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_visa",
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, IsUnknownExternalState(err), "timeout should map to ErrUnknownExternalState")
}

// TestStripeClient_Capture_Success 测试捕获成功
func TestStripeClient_Capture_Success(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	providerRef := "pi_test1234567890"

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/payment_intents/"+providerRef+"/capture")

		resp := stripePaymentIntent{
			ID:     providerRef,
			Object: "payment_intent",
			Amount: 10000,
			Status: "succeeded",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	resp, err := client.Capture(context.Background(), domain.CaptureRequest{
		ProviderRef:    providerRef,
		Amount:         decimal.NewFromFloat(100.00),
		IdempotencyKey: "capture_123",
	})

	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusCaptured, resp.Status)
	assert.Equal(t, providerRef, resp.ProviderRef)
}

// TestStripeClient_Refund_Success 测试退款成功
func TestStripeClient_Refund_Success(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/refunds")
		assert.Contains(t, body, "payment_intent=pi_test123")

		resp := stripeRefund{
			ID:       "re_test123",
			Object:   "refund",
			Amount:   5000,
			Currency: "usd",
			Status:   "succeeded",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	resp, err := client.Refund(context.Background(), domain.RefundRequest{
		ProviderRef:    "pi_test123",
		Amount:         decimal.NewFromFloat(50.00),
		Reason:         "requested_by_customer",
		IdempotencyKey: "refund_123",
	})

	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusRefunded, resp.Status)
}

// TestStripeClient_Query_Success 测试查询成功
func TestStripeClient_Query_Success(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	providerRef := "pi_test1234567890"

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/payment_intents/"+providerRef)

		resp := stripePaymentIntent{
			ID:     providerRef,
			Object: "payment_intent",
			Amount: 10000,
			Status: "succeeded",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	resp, err := client.QueryPayment(context.Background(), domain.LegacyQueryRequest{
		ProviderRef: providerRef,
	})

	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusCaptured, resp.Status)
}

// TestStripeClient_VerifyWebhook_ValidSignature 测试有效签名验证
func TestStripeClient_VerifyWebhook_ValidSignature(t *testing.T) {
	client, err := NewStripeClient(StripeConfig{
		SecretKey:     testStripeSecretKey,
		WebhookSecret: testWebhookSecret,
		Timeout:       10 * time.Second,
	})
	require.NoError(t, err)

	payload := []byte(`{"id":"evt_test","type":"payment_intent.succeeded"}`)

	// 生成有效签名
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signedPayload := timestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(testWebhookSecret))
	mac.Write([]byte(signedPayload))
	signature := fmt.Sprintf("t=%s,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))

	_, err = client.VerifyWebhook(context.Background(), domain.VerifyWebhookRequest{
		Payload:   payload,
		Signature: signature,
	})
	assert.NoError(t, err)
}

// TestStripeClient_VerifyWebhook_InvalidSignature 测试无效签名验证
func TestStripeClient_VerifyWebhook_InvalidSignature(t *testing.T) {
	client, err := NewStripeClient(StripeConfig{
		SecretKey:     testStripeSecretKey,
		WebhookSecret: testWebhookSecret,
		Timeout:       10 * time.Second,
	})
	require.NoError(t, err)

	payload := []byte(`{"id":"evt_test","type":"payment_intent.succeeded"}`)

	// 使用错误的签名
	_, err = client.VerifyWebhook(context.Background(), domain.VerifyWebhookRequest{
		Payload:   payload,
		Signature: "t=123,v1=invalid",
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrWebhookSignatureInvalid))
}

// TestStripeClient_VerifyWebhook_ExpiredTimestamp 测试过期时间戳
func TestStripeClient_VerifyWebhook_ExpiredTimestamp(t *testing.T) {
	client, err := NewStripeClient(StripeConfig{
		SecretKey:     testStripeSecretKey,
		WebhookSecret: testWebhookSecret,
		Timeout:       10 * time.Second,
	})
	require.NoError(t, err)

	payload := []byte(`{"id":"evt_test","type":"payment_intent.succeeded"}`)

	// 使用过期的时间戳（10分钟前）
	oldTimestamp := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	signedPayload := oldTimestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(testWebhookSecret))
	mac.Write([]byte(signedPayload))
	signature := fmt.Sprintf("t=%s,v1=%s", oldTimestamp, hex.EncodeToString(mac.Sum(nil)))

	_, err = client.VerifyWebhook(context.Background(), domain.VerifyWebhookRequest{
		Payload:   payload,
		Signature: signature,
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrWebhookSignatureInvalid))
}

// TestStripeClient_500Error 测试 500 服务器错误
func TestStripeClient_500Error(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"type":"api_error","message":"Internal server error"}}`))
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_visa",
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, IsUnknownExternalState(err), "500 should map to ErrUnknownExternalState")
}

// TestStripeClient_429RateLimit 测试限流
func TestStripeClient_429RateLimit(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(http.StatusTooManyRequests)
		// 返回包含 Error 字段的结构，模拟真实 Stripe API 响应
		resp := map[string]interface{}{
			"error": map[string]interface{}{
				"type":    "rate_limit_error",
				"message": "Too many requests",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_visa",
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrProviderRateLimited))
}

// TestStripeAcquirer_Interface 实现验证
func TestStripeAcquirer_Interface(t *testing.T) {
	server := newMockStripeServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		resp := stripePaymentIntent{
			ID:     "pi_test123",
			Object: "payment_intent",
			Amount: 10000,
			Status: "requires_capture",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewStripeClient(StripeConfig{
		SecretKey: testStripeSecretKey,
		BaseURL:   server.URL(),
		Timeout:   10 * time.Second,
	})
	require.NoError(t, err)

	acquirer := NewStripeAcquirer(client)

	// 验证实现了 AcquirerClient 接口
	var _ domain.AcquirerClient = acquirer

	// 测试 Authorize
	resp, err := acquirer.Authorize(context.Background(), domain.AuthorizeRequest{
		OrderID:            "order_test",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "pm_test",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusAuthorized, resp.Status)
}
