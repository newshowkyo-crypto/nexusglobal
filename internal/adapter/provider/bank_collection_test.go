package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
)

// sha256Sum 计算 SHA256 哈希并返回十六进制字符串
func sha256Sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// hmacSign 计算 HMAC-SHA256 签名
func hmacSign(secret string, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestBankCollectionAdapter_New(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := map[string]any{
		"base_url": server.URL,
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)
	require.NotNil(t, adapter)
	assert.Equal(t, domain.AdapterTypeBankCollection, adapter.GetAdapterType())
}

func TestBankCollectionAdapter_SupportsAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := map[string]any{
		"base_url": server.URL,
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	assert.True(t, adapter.SupportsAsset("USD"))
	assert.True(t, adapter.SupportsAsset("EUR"))
	assert.False(t, adapter.SupportsAsset("BTC"))
}

func TestBankCollectionAdapter_CreateInboundIntent_Success(t *testing.T) {
	intentID := uuid.New()
	providerRef := "BC-REF-" + uuid.New().String()[:8]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求方法
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/inbound-intents", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-api-key", r.Header.Get("X-API-Key"))

		// 解析请求体
		var req map[string]any
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "test-order-123", req["order_id"])
		assert.Equal(t, "USD", req["currency"])

		// 返回成功响应
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"intent_id":         intentID.String(),
			"provider_ref":      providerRef,
			"status":            "CREATED",
			"account_number":    "1234567890",
			"routing_number":    "021000021",
			"bank_name":         "Test Bank",
			"bank_address":      "123 Main St",
			"payment_reference": "REF-" + uuid.New().String()[:8],
			"expires_at":        time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
	}))
	defer server.Close()

	config := map[string]any{
		"base_url": server.URL,
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	inboundAdapter, ok := adapter.(domain.InboundAdapter)
	require.True(t, ok)

	req := domain.InboundIntentRequest{
		OrderID:        "test-order-123",
		AssetCode:      "USD",
		Amount:         decimalPtr("100.00"),
		BuyerRef:       "test-buyer",
		IdempotencyKey: "test-idempotency-key",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := inboundAdapter.CreateInboundIntent(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, intentID, resp.IntentID)
	assert.Equal(t, providerRef, resp.ProviderRef)
	assert.Equal(t, domain.InboundStatusCreated, resp.Status)
	assert.NotNil(t, resp.VirtualAccount)
	assert.Equal(t, "1234567890", resp.VirtualAccount.AccountNumber)
}

func TestBankCollectionAdapter_CreateInboundIntent_Failure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/inbound-intents", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    "INVALID_AMOUNT",
				"message": "Amount must be positive",
			},
		})
	}))
	defer server.Close()

	config := map[string]any{
		"base_url": server.URL,
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	inboundAdapter, ok := adapter.(domain.InboundAdapter)
	require.True(t, ok)

	req := domain.InboundIntentRequest{
		OrderID:        "test-order-fail",
		AssetCode:      "USD",
		Amount:         decimalPtr("100.00"),
		BuyerRef:       "test-buyer",
		IdempotencyKey: "test-idempotency-fail",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = inboundAdapter.CreateInboundIntent(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INVALID_AMOUNT")
}

func TestBankCollectionAdapter_CreateInboundIntent_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟超时
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := map[string]any{
		"base_url": server.URL,
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	inboundAdapter, ok := adapter.(domain.InboundAdapter)
	require.True(t, ok)

	req := domain.InboundIntentRequest{
		OrderID:        "test-order-timeout",
		AssetCode:      "USD",
		Amount:         decimalPtr("100.00"),
		BuyerRef:       "test-buyer",
		IdempotencyKey: "test-idempotency-timeout",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = inboundAdapter.CreateInboundIntent(ctx, req)
	require.Error(t, err)
	// timeout 必须映射到 UNKNOWN_EXTERNAL_STATE
	assert.Equal(t, domain.ErrUnknownExternalState, err)
}

func TestBankCollectionAdapter_QueryInboundIntent_Success(t *testing.T) {
	providerRef := "BC-REF-QUERY"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求方法
		assert.Equal(t, "GET", r.Method)
		assert.True(t, strings.HasPrefix(r.URL.Path, "/api/v1/inbound-intents/"))
		assert.Equal(t, "test-api-key", r.Header.Get("X-API-Key"))

		// 返回成功响应
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"intent_id":        uuid.New().String(),
			"provider_ref":     providerRef,
			"status":           "CONFIRMED",
			"received_amount":  "100.00",
			"confirmed_amount": "100.00",
			"transaction_ref":  "TXN-" + uuid.New().String()[:8],
			"updated_at":       time.Now().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	config := map[string]any{
		"base_url": server.URL,
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	inboundAdapter, ok := adapter.(domain.InboundAdapter)
	require.True(t, ok)

	queryReq := domain.QueryInboundIntentRequest{
		ProviderRef: providerRef,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	queryResp, err := inboundAdapter.QueryInboundIntent(ctx, queryReq)
	require.NoError(t, err)
	require.NotNil(t, queryResp)

	assert.Equal(t, providerRef, queryResp.ProviderRef)
	assert.Equal(t, domain.InboundStatusConfirmed, queryResp.Status)
	assert.True(t, queryResp.ConfirmedAmount.Equal(decimalPtr("100.00")))
}

func TestBankCollectionAdapter_QueryInboundIntent_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := map[string]any{
		"base_url": server.URL,
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	inboundAdapter, ok := adapter.(domain.InboundAdapter)
	require.True(t, ok)

	queryReq := domain.QueryInboundIntentRequest{
		ProviderRef: "BC-REF-TIMEOUT",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = inboundAdapter.QueryInboundIntent(ctx, queryReq)
	require.Error(t, err)
	assert.Equal(t, domain.ErrUnknownExternalState, err)
}

func TestBankCollectionAdapter_VerifyWebhook_Success(t *testing.T) {
	config := map[string]any{
		"base_url": "http://localhost",
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	inboundAdapter, ok := adapter.(domain.InboundAdapter)
	require.True(t, ok)

	// 构造有效的 webhook payload
	eventID := uuid.New().String()
	intentID := uuid.New().String()
	providerRef := "BC-REF-WH"
	timestamp := time.Now().Unix()

	payload := map[string]any{
		"event_id":        eventID,
		"event_type":      "payment_received",
		"intent_id":       intentID,
		"provider_ref":    providerRef,
		"amount":          "100.00",
		"currency":        "USD",
		"status":          "CONFIRMED",
		"transaction_ref": "TXN-123",
		"timestamp":       time.Now().Format(time.RFC3339),
	}

	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)

	// 计算签名
	payloadHash := sha256Sum(payloadBytes)
	message := fmt.Sprintf("%d.%s", timestamp, payloadHash)
	signature := hmacSign("test-webhook-secret", message)
	sigHeader := fmt.Sprintf("t=%d,v1=%s", timestamp, signature)

	ctx := context.Background()

	event, err := inboundAdapter.VerifyInboundWebhook(ctx, payloadBytes, sigHeader)
	require.NoError(t, err)
	require.NotNil(t, event)

	assert.Equal(t, eventID, event.EventID)
	assert.Equal(t, "payment_received", event.EventType)
	assert.Equal(t, providerRef, event.ProviderRef)
	assert.Equal(t, domain.InboundStatusConfirmed, event.Status)
}

func TestBankCollectionAdapter_VerifyWebhook_InvalidSignature(t *testing.T) {
	config := map[string]any{
		"base_url": "http://localhost",
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	inboundAdapter, ok := adapter.(domain.InboundAdapter)
	require.True(t, ok)

	payload := []byte(`{"event": "payment_received"}`)
	signature := "invalid-signature"

	ctx := context.Background()

	_, err = inboundAdapter.VerifyInboundWebhook(ctx, payload, signature)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature verification failed")
}

func TestBankCollectionAdapter_AssetNotSupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := map[string]any{
		"base_url": server.URL,
	}

	adapter, err := NewBankCollectionAdapter("mock_bank_collection", "test-api-key", "test-secret", "test-webhook-secret", config)
	require.NoError(t, err)

	inboundAdapter, ok := adapter.(domain.InboundAdapter)
	require.True(t, ok)

	req := domain.InboundIntentRequest{
		OrderID:        "test-order-btc",
		AssetCode:      "BTC", // 不支持的资产
		Amount:         decimalPtr("1.00"),
		BuyerRef:       "test-buyer",
		IdempotencyKey: "test-idempotency-btc",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = inboundAdapter.CreateInboundIntent(ctx, req)
	assert.Error(t, err)
	assert.Equal(t, domain.ErrAssetNotSupported, err)
}

// decimalPtr 辅助函数
func decimalPtr(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}
