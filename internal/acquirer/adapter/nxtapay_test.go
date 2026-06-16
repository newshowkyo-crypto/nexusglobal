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
	"strings"
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
	testNxtapayAPIKey        = "nx_test_api_key_1234567890"
	testNxtapayAPISecret     = "nx_test_secret_abcdef1234567890"
	testNxtapayMerchantID    = "MERCHANT_001"
	testNxtapayWebhookSecret = "whsec_nxtapay_test_secret"
)

// mockNxtapayServer 模拟 Nxtapay API 服务器
type mockNxtapayServer struct {
	t            *testing.T
	server       *httptest.Server
	handler      func(w http.ResponseWriter, r *http.Request, body string)
	delay        time.Duration
	mu           sync.Mutex
	requestCount int
	requests     []capturedNxtapayRequest
}

type capturedNxtapayRequest struct {
	Method  string
	Path    string
	Body    string
	Headers map[string]string
}

func newMockNxtapayServer(t *testing.T) *mockNxtapayServer {
	m := &mockNxtapayServer{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		m.mu.Lock()
		m.requestCount++
		headers := make(map[string]string)
		for k, v := range r.Header {
			if len(v) > 0 {
				headers[k] = v[0]
			}
		}
		m.requests = append(m.requests, capturedNxtapayRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Body:    string(body),
			Headers: headers,
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

func (m *mockNxtapayServer) close() {
	m.server.Close()
}

func (m *mockNxtapayServer) URL() string {
	return m.server.URL
}

func (m *mockNxtapayServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *mockNxtapayServer) Requests() []capturedNxtapayRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]capturedNxtapayRequest{}, m.requests...)
}

// TestNxtapayClient_NewNxtapayClient 验证客户端创建
func TestNxtapayClient_NewNxtapayClient(t *testing.T) {
	t.Run("ValidConfig", func(t *testing.T) {
		client, err := NewNxtapayClient(NxtapayConfig{
			APIKey:        testNxtapayAPIKey,
			APISecret:     testNxtapayAPISecret,
			MerchantID:    testNxtapayMerchantID,
			WebhookSecret: testNxtapayWebhookSecret,
			Timeout:       30 * time.Second,
		})
		require.NoError(t, err)
		assert.NotNil(t, client)
	})

	t.Run("MissingAPIKey", func(t *testing.T) {
		client, err := NewNxtapayClient(NxtapayConfig{
			APIKey:     "",
			APISecret:  testNxtapayAPISecret,
			MerchantID: testNxtapayMerchantID,
		})
		assert.Error(t, err)
		assert.Nil(t, client)
	})

	t.Run("MissingAPISecret", func(t *testing.T) {
		client, err := NewNxtapayClient(NxtapayConfig{
			APIKey:     testNxtapayAPIKey,
			APISecret:  "",
			MerchantID: testNxtapayMerchantID,
		})
		assert.Error(t, err)
		assert.Nil(t, client)
	})

	t.Run("MissingMerchantID", func(t *testing.T) {
		client, err := NewNxtapayClient(NxtapayConfig{
			APIKey:     testNxtapayAPIKey,
			APISecret:  testNxtapayAPISecret,
			MerchantID: "",
		})
		assert.Error(t, err)
		assert.Nil(t, client)
	})
}

// TestSignNxtapayRequest_SignatureVectors 签名向量测试
func TestSignNxtapayRequest_SignatureVectors(t *testing.T) {
	secret := "test_secret_key"
	method := "POST"
	path := "/api/v1/payments/authorize"
	timestamp := "1710000000"
	nonce := "nonce123"
	bodyHash := hashSHA256(`{"amount":"100.00"}`)

	sig1 := SignNxtapayRequest(secret, method, path, timestamp, nonce, bodyHash)
	sig2 := SignNxtapayRequest(secret, method, path, timestamp, nonce, bodyHash)

	// 相同输入应生成相同签名
	assert.Equal(t, sig1, sig2)

	// 改变 body hash 应改变签名
	sig3 := SignNxtapayRequest(secret, method, path, timestamp, nonce, "different_hash")
	assert.NotEqual(t, sig1, sig3)

	// 改变 timestamp 应改变签名
	sig4 := SignNxtapayRequest(secret, method, path, "1710000001", nonce, bodyHash)
	assert.NotEqual(t, sig1, sig4)

	// 改变 secret 应改变签名
	sig5 := SignNxtapayRequest("different_secret", method, path, timestamp, nonce, bodyHash)
	assert.NotEqual(t, sig1, sig5)
}

// TestSignNxtapayRequest_ManualVerification 手动验证签名算法
func TestSignNxtapayRequest_ManualVerification(t *testing.T) {
	secret := "my_secret"
	method := "POST"
	path := "/api/v1/payments/authorize"
	timestamp := "1710000000"
	nonce := "abc123"
	bodyHash := hashSHA256(`{"test":"data"}`)

	// 手动计算期望签名
	message := method + path + timestamp + nonce + bodyHash
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	// 验证 SignNxtapayRequest 生成的签名一致
	actualSig := SignNxtapayRequest(secret, method, path, timestamp, nonce, bodyHash)
	assert.Equal(t, expectedSig, actualSig)
}

// TestNxtapayClient_Authorize_Success 测试授权成功
func TestNxtapayClient_Authorize_Success(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		// 验证签名头
		assert.NotEmpty(t, r.Header.Get("X-Nxtapay-Key"))
		assert.NotEmpty(t, r.Header.Get("X-Nxtapay-Timestamp"))
		assert.NotEmpty(t, r.Header.Get("X-Nxtapay-Nonce"))
		assert.NotEmpty(t, r.Header.Get("X-Nxtapay-Signature"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// 解析请求体
		var reqBody map[string]interface{}
		json.Unmarshal([]byte(body), &reqBody)
		assert.Equal(t, testNxtapayMerchantID, reqBody["merchant_id"])
		assert.Equal(t, "deferred", reqBody["capture_mode"])

		// 返回成功响应
		resp := nxtapayAuthorizeResponse{
			TransactionID: "nx_tx_" + uuid.New().String()[:12],
			Status:        "AUTHORIZED",
			ExpiresAt:     time.Now().Add(30 * time.Minute).Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "nx_pm_token_visa",
		IdempotencyKey:     "idem_nx_123",
	}

	resp, err := client.Authorize(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, domain.PaymentStatusAuthorized, resp.Status)
	assert.NotEmpty(t, resp.ProviderRef)
	assert.Equal(t, 1, server.RequestCount())
}

// TestNxtapayClient_Authorize_Declined 测试授权被拒绝
func TestNxtapayClient_Authorize_Declined(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(http.StatusBadRequest)
		resp := nxtapayAuthorizeResponse{
			Error: &nxtapayError{
				Code:       "DECLINED",
				Message:    "Payment was declined",
				HTTPStatus: http.StatusBadRequest,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "nx_pm_declined",
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, IsExplicitDecline(err))
	assert.False(t, IsUnknownExternalState(err))
}

// TestNxtapayClient_Authorize_MissingToken 测试缺少 token
func TestNxtapayClient_Authorize_MissingToken(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		t.Error("should not call server when token is missing")
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
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

// TestNxtapayClient_Authorize_Timeout 测试超时
func TestNxtapayClient_Authorize_Timeout(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	// 模拟超时：延迟大于客户端 timeout
	server.delay = 2 * time.Second

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    100 * time.Millisecond, // 100ms timeout
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "nx_pm_token",
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, IsUnknownExternalState(err), "timeout should map to ErrUnknownExternalState")
}

// TestNxtapayClient_Capture_Success 测试捕获成功
func TestNxtapayClient_Capture_Success(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	providerRef := "nx_tx_test123"

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/payments/capture")

		var reqBody map[string]interface{}
		json.Unmarshal([]byte(body), &reqBody)
		assert.Equal(t, providerRef, reqBody["transaction_id"])

		resp := nxtapayCaptureResponse{
			TransactionID: providerRef,
			Status:        "CAPTURED",
			Amount:        "100.00",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	resp, err := client.Capture(context.Background(), domain.CaptureRequest{
		ProviderRef:    providerRef,
		Amount:         decimal.NewFromFloat(100.00),
		IdempotencyKey: "capture_nx_123",
	})

	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusCaptured, resp.Status)
	assert.Equal(t, providerRef, resp.ProviderRef)
}

// TestNxtapayClient_Refund_Success 测试退款成功
func TestNxtapayClient_Refund_Success(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/refunds")

		var reqBody map[string]interface{}
		json.Unmarshal([]byte(body), &reqBody)
		assert.Equal(t, "nx_tx_refund_test", reqBody["transaction_id"])

		resp := nxtapayRefundResponse{
			RefundID: "nx_refund_123",
			Status:   "COMPLETED",
			Amount:   "50.00",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	resp, err := client.Refund(context.Background(), domain.RefundRequest{
		ProviderRef:    "nx_tx_refund_test",
		Amount:         decimal.NewFromFloat(50.00),
		Reason:         "requested_by_customer",
		IdempotencyKey: "refund_nx_123",
	})

	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusRefunded, resp.Status)
}

// TestNxtapayClient_Query_Success 测试查询成功
func TestNxtapayClient_Query_Success(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	providerRef := "nx_tx_query_test"

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, providerRef)

		resp := nxtapayQueryResponse{
			TransactionID: providerRef,
			OrderID:       "order_123",
			Status:        "CAPTURED",
			Amount:        "100.00",
			Currency:      "USD",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	resp, err := client.QueryPayment(context.Background(), domain.LegacyQueryRequest{
		ProviderRef: providerRef,
	})

	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusCaptured, resp.Status)
}

// TestNxtapayClient_500Error 测试 500 服务器错误
func TestNxtapayClient_500Error(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(http.StatusInternalServerError)
		resp := nxtapayError{
			Code:       "500",
			Message:    "Internal server error",
			HTTPStatus: http.StatusInternalServerError,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"error": resp})
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "nx_pm_token",
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, IsUnknownExternalState(err), "500 should map to ErrUnknownExternalState")
}

// TestNxtapayClient_RateLimit 测试限流
func TestNxtapayClient_RateLimit(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(http.StatusTooManyRequests)
		resp := nxtapayError{
			Code:       "RATE_LIMITED",
			Message:    "Too many requests",
			HTTPStatus: http.StatusTooManyRequests,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"error": resp})
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	req := domain.AuthorizeRequest{
		OrderID:            "order_123",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "nx_pm_token",
	}

	_, err = client.Authorize(context.Background(), req)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrProviderRateLimited))
}

// TestNxtapayClient_VerifyWebhook_ValidSignature 测试有效签名验证
func TestNxtapayClient_VerifyWebhook_ValidSignature(t *testing.T) {
	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:        testNxtapayAPIKey,
		APISecret:     testNxtapayAPISecret,
		MerchantID:    testNxtapayMerchantID,
		WebhookSecret: testNxtapayWebhookSecret,
		Timeout:       10 * time.Second,
	})
	require.NoError(t, err)

	payload := []byte(`{"event":"payment.captured","transaction_id":"nx_tx_123"}`)

	// 生成有效签名
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := "nonce123"
	payloadHash := hashSHA256(string(payload))
	signature := SignNxtapayRequest(testNxtapayWebhookSecret, "POST", "/webhook", timestamp, nonce, payloadHash)
	// 签名头包含 4 个部分：ts, nonce, hash, 和额外的字段
	sigHeader := fmt.Sprintf("ts=%s, nonce=%s, hash=%s, v=1", timestamp, nonce, signature)

	_, err = client.VerifyWebhook(context.Background(), domain.VerifyWebhookRequest{
		Payload:   payload,
		Signature: sigHeader,
	})
	assert.NoError(t, err)
}

// TestNxtapayClient_VerifyWebhook_InvalidSignature 测试无效签名验证
func TestNxtapayClient_VerifyWebhook_InvalidSignature(t *testing.T) {
	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:        testNxtapayAPIKey,
		APISecret:     testNxtapayAPISecret,
		MerchantID:    testNxtapayMerchantID,
		WebhookSecret: testNxtapayWebhookSecret,
	})
	require.NoError(t, err)

	payload := []byte(`{"event":"payment.success","data":{"transaction_id":"test_tx_001"}}`)
	_, err = client.VerifyWebhook(context.Background(), domain.VerifyWebhookRequest{
		Payload:   payload,
		Signature: "ts=123, nonce=abc, hash=invalid",
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrWebhookSignatureInvalid))
}

// TestNxtapayClient_VerifyWebhook_ExpiredTimestamp 测试过期时间戳
func TestNxtapayClient_VerifyWebhook_ExpiredTimestamp(t *testing.T) {
	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:        testNxtapayAPIKey,
		APISecret:     testNxtapayAPISecret,
		MerchantID:    testNxtapayMerchantID,
		WebhookSecret: testNxtapayWebhookSecret,
		Timeout:       10 * time.Second,
	})
	require.NoError(t, err)

	payload := []byte(`{"event":"payment.captured","transaction_id":"nx_tx_123"}`)

	// 使用过期的时间戳（10分钟前）
	oldTimestamp := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	nonce := "nonce123"
	payloadHash := hashSHA256(string(payload))
	signature := SignNxtapayRequest(testNxtapayWebhookSecret, "POST", "/webhook", oldTimestamp, nonce, payloadHash)
	sigHeader := fmt.Sprintf("ts=%s, nonce=%s, hash=%s", oldTimestamp, nonce, signature)

	_, err = client.VerifyWebhook(context.Background(), domain.VerifyWebhookRequest{
		Payload:   payload,
		Signature: sigHeader,
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrWebhookSignatureInvalid))
}

// TestNxtapayAcquirer_Interface 实现验证
func TestNxtapayAcquirer_Interface(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		resp := nxtapayAuthorizeResponse{
			TransactionID: "nx_tx_interface_test",
			Status:        "AUTHORIZED",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	acquirer := NewNxtapayAcquirer(client)

	// 验证实现了 AcquirerClient 接口
	var _ domain.AcquirerClient = acquirer

	// 测试 Authorize
	resp, err := acquirer.Authorize(context.Background(), domain.AuthorizeRequest{
		OrderID:            "order_test",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "nx_pm_test",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusAuthorized, resp.Status)
}

// TestNxtapayClient_IdempotencyKey 测试幂等键
func TestNxtapayClient_IdempotencyKey(t *testing.T) {
	server := newMockNxtapayServer(t)
	defer server.close()

	idempotencyKey := "idem_nx_unique_12345"

	server.handler = func(w http.ResponseWriter, r *http.Request, body string) {
		// 验证请求体包含 idempotency_key
		assert.Contains(t, body, idempotencyKey)

		resp := nxtapayAuthorizeResponse{
			TransactionID: "nx_tx_idem_test",
			Status:        "AUTHORIZED",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, err := NewNxtapayClient(NxtapayConfig{
		APIKey:     testNxtapayAPIKey,
		APISecret:  testNxtapayAPISecret,
		MerchantID: testNxtapayMerchantID,
		BaseURL:    server.URL(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)

	// 第一次请求
	_, err = client.Authorize(context.Background(), domain.AuthorizeRequest{
		OrderID:            "order_idem",
		MerchantID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Amount:             decimal.NewFromFloat(100.00),
		Currency:           "USD",
		PaymentMethodToken: "nx_pm_test",
		IdempotencyKey:     idempotencyKey,
	})
	require.NoError(t, err)
}

// Helper function for error import
var _ = strings.TrimSpace
