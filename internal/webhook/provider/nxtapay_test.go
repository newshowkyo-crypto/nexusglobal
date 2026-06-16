package provider

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testNxtapayWebhookSecret = "whsec_test_nxtapay_secret_1234567890"

// TestNxtapayWebhookVerifier_New 创建验签器
func TestNxtapayWebhookVerifier_New(t *testing.T) {
	verifier := NewNxtapayWebhookVerifier(testNxtapayWebhookSecret)
	require.NotNil(t, verifier)
}

// TestNxtapayWebhookVerifier_Verify_ValidSignature 测试有效签名
func TestNxtapayWebhookVerifier_Verify_ValidSignature(t *testing.T) {
	verifier := NewNxtapayWebhookVerifier(testNxtapayWebhookSecret)

	payload := []byte(`{"event_id":"evt_nx_123","event_type":"payment.captured","timestamp":1710000000}`)

	// 生成有效签名
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := "nonce_abc123"
	payloadHash := sha256.Sum256(payload)
	payloadHashHex := hex.EncodeToString(payloadHash[:])

	message := timestamp + "." + nonce + "." + payloadHashHex
	mac := hmac.New(sha256.New, []byte(testNxtapayWebhookSecret))
	mac.Write([]byte(message))
	signature := hex.EncodeToString(mac.Sum(nil))

	sigHeader := fmt.Sprintf("ts=%s,nonce=%s,hash=%s", timestamp, nonce, signature)

	err := verifier.Verify(payload, sigHeader)
	assert.NoError(t, err)
}

// TestNxtapayWebhookVerifier_Verify_MissingSecret 测试缺少 secret
func TestNxtapayWebhookVerifier_Verify_MissingSecret(t *testing.T) {
	verifier := NewNxtapayWebhookVerifier("")
	err := verifier.Verify([]byte("test"), "ts=123,nonce=abc,hash=xyz")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "webhook secret is required")
}

// TestNxtapayWebhookVerifier_Verify_MissingSignature 测试缺少签名
func TestNxtapayWebhookVerifier_Verify_MissingSignature(t *testing.T) {
	verifier := NewNxtapayWebhookVerifier(testNxtapayWebhookSecret)
	err := verifier.Verify([]byte("test"), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature is required")
}

// TestNxtapayWebhookVerifier_Verify_InvalidFormat 测试无效格式
func TestNxtapayWebhookVerifier_Verify_InvalidFormat(t *testing.T) {
	verifier := NewNxtapayWebhookVerifier(testNxtapayWebhookSecret)

	// 无效格式
	err := verifier.Verify([]byte("test"), "invalid_format")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid signature format")
}

// TestNxtapayWebhookVerifier_Verify_MissingFields 测试缺少字段
func TestNxtapayWebhookVerifier_Verify_MissingFields(t *testing.T) {
	verifier := NewNxtapayWebhookVerifier(testNxtapayWebhookSecret)

	// 缺少 nonce
	err := verifier.Verify([]byte("test"), "ts=123,hash=xyz")
	assert.Error(t, err)

	// 缺少 hash
	err = verifier.Verify([]byte("test"), "ts=123,nonce=abc")
	assert.Error(t, err)
}

// TestNxtapayWebhookVerifier_Verify_ExpiredTimestamp 测试过期时间戳
func TestNxtapayWebhookVerifier_Verify_ExpiredTimestamp(t *testing.T) {
	verifier := NewNxtapayWebhookVerifier(testNxtapayWebhookSecret)

	payload := []byte(`{"event_id":"evt_nx_123","event_type":"payment.captured"}`)

	// 使用过期的时间戳（10分钟前）
	oldTimestamp := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	nonce := "nonce_abc123"
	payloadHash := sha256.Sum256(payload)
	payloadHashHex := hex.EncodeToString(payloadHash[:])

	message := oldTimestamp + "." + nonce + "." + payloadHashHex
	mac := hmac.New(sha256.New, []byte(testNxtapayWebhookSecret))
	mac.Write([]byte(message))
	signature := hex.EncodeToString(mac.Sum(nil))

	sigHeader := fmt.Sprintf("ts=%s,nonce=%s,hash=%s", oldTimestamp, nonce, signature)

	err := verifier.Verify(payload, sigHeader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp expired")
}

// TestNxtapayWebhookVerifier_Verify_MismatchSignature 测试签名不匹配
func TestNxtapayWebhookVerifier_Verify_MismatchSignature(t *testing.T) {
	verifier := NewNxtapayWebhookVerifier(testNxtapayWebhookSecret)

	payload := []byte(`{"event_id":"evt_nx_123","event_type":"payment.captured"}`)

	// 使用正确的时间戳但错误的签名
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	sigHeader := fmt.Sprintf("ts=%s,nonce=nonce123,hash=%s", timestamp, "wrong_signature_here_0000000000000000000000000000000000000000")

	err := verifier.Verify(payload, sigHeader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature mismatch")
}

// TestParseNxtapayEvent_Valid 测试解析有效事件
func TestParseNxtapayEvent_Valid(t *testing.T) {
	payload := []byte(`{
		"event_id": "evt_nx_123",
		"event_type": "payment.captured",
		"timestamp": 1710000000,
		"merchant_id": "MERCHANT_001",
		"transaction_id": "nx_tx_123",
		"order_id": "order_abc",
		"amount": "100.00",
		"currency": "USD"
	}`)

	event, err := ParseNxtapayEvent(payload)
	require.NoError(t, err)
	require.NotNil(t, event)
	assert.Equal(t, "evt_nx_123", event.EventID)
	assert.Equal(t, "payment.captured", event.EventType)
	assert.Equal(t, int64(1710000000), event.Timestamp)
	assert.Equal(t, "nx_tx_123", event.TransactionID)
	assert.Equal(t, "order_abc", event.OrderID)
}

// TestParseNxtapayEvent_InvalidJSON 测试无效 JSON
func TestParseNxtapayEvent_InvalidJSON(t *testing.T) {
	payload := []byte(`{invalid json}`)

	event, err := ParseNxtapayEvent(payload)
	assert.Error(t, err)
	assert.Nil(t, event)
}

// TestParseNxtapayEvent_MissingID 测试缺少 ID
func TestParseNxtapayEvent_MissingID(t *testing.T) {
	payload := []byte(`{"event_type": "payment.captured", "timestamp": 1710000000}`)

	event, err := ParseNxtapayEvent(payload)
	assert.Error(t, err)
	assert.Nil(t, event)
}

// TestMapNxtapayEventToInternal_PaymentCaptured 测试映射支付成功
func TestMapNxtapayEventToInternal_PaymentCaptured(t *testing.T) {
	payload := []byte(`{
		"event_id": "evt_nx_captured",
		"event_type": "payment.captured",
		"timestamp": 1710000000,
		"merchant_id": "00000000-0000-0000-0000-000000000001",
		"transaction_id": "nx_tx_captured",
		"order_id": "order_nx_123",
		"amount": "150.50",
		"currency": "EUR"
	}`)

	event, err := ParseNxtapayEvent(payload)
	require.NoError(t, err)

	payloadHash := "hash_nx_123"
	evt, err := MapNxtapayEventToInternal(event, payloadHash)
	require.NoError(t, err)

	assert.Equal(t, "evt_nx_captured", evt.EventID)
	assert.Equal(t, "payment.captured", evt.EventType)
	assert.Equal(t, InternalEventPaymentCaptured, evt.InternalType)
	assert.Equal(t, "nx_tx_captured", evt.ProviderRef)
	assert.Equal(t, "order_nx_123", evt.OrderID)
	assert.Equal(t, "EUR", evt.Currency)
	assert.Equal(t, payloadHash, evt.RawPayloadHash)
}

// TestMapNxtapayEventToInternal_PaymentDeclined 测试映射支付失败
func TestMapNxtapayEventToInternal_PaymentDeclined(t *testing.T) {
	payload := []byte(`{
		"event_id": "evt_nx_declined",
		"event_type": "payment.declined",
		"timestamp": 1710000001,
		"transaction_id": "nx_tx_declined",
		"amount": "50.00",
		"currency": "USD"
	}`)

	event, err := ParseNxtapayEvent(payload)
	require.NoError(t, err)

	evt, err := MapNxtapayEventToInternal(event, "hash_declined")
	require.NoError(t, err)

	assert.Equal(t, InternalEventPaymentFailed, evt.InternalType)
	assert.Equal(t, "nx_tx_declined", evt.ProviderRef)
}

// TestMapNxtapayEventToInternal_Refund 测试映射退款
func TestMapNxtapayEventToInternal_Refund(t *testing.T) {
	payload := []byte(`{
		"event_id": "evt_nx_refund",
		"event_type": "refund.completed",
		"timestamp": 1710000002,
		"transaction_id": "nx_tx_refund",
		"amount": "25.00",
		"currency": "USD",
		"data": {
			"refund_id": "nx_refund_123",
			"transaction_id": "nx_tx_original",
			"amount": "25.00",
			"status": "completed"
		}
	}`)

	event, err := ParseNxtapayEvent(payload)
	require.NoError(t, err)

	evt, err := MapNxtapayEventToInternal(event, "hash_refund")
	require.NoError(t, err)

	assert.Equal(t, InternalEventRefundConfirmed, evt.InternalType)
	assert.Equal(t, "nx_tx_original", evt.ProviderRef)
}

// TestMapNxtapayEventToInternal_Dispute 测试映射 Dispute
func TestMapNxtapayEventToInternal_Dispute(t *testing.T) {
	payload := []byte(`{
		"event_id": "evt_nx_dispute",
		"event_type": "dispute.received",
		"timestamp": 1710000003,
		"transaction_id": "nx_tx_disputed",
		"amount": "200.00",
		"currency": "USD",
		"data": {
			"dispute_id": "nx_dp_123",
			"transaction_id": "nx_tx_disputed",
			"amount": "200.00",
			"reason": "fraudulent",
			"status": "open"
		}
	}`)

	event, err := ParseNxtapayEvent(payload)
	require.NoError(t, err)

	evt, err := MapNxtapayEventToInternal(event, "hash_dispute")
	require.NoError(t, err)

	assert.Equal(t, InternalEventChargebackReceived, evt.InternalType)
	assert.Equal(t, "nx_tx_disputed", evt.ProviderRef)
}

// TestMapNxtapayEventToInternal_DisputeWon 测试映射 Dispute Won
func TestMapNxtapayEventToInternal_DisputeWon(t *testing.T) {
	payload := []byte(`{
		"event_id": "evt_nx_dispute_won",
		"event_type": "dispute.resolved",
		"timestamp": 1710000004,
		"transaction_id": "nx_tx_won",
		"amount": "200.00",
		"currency": "USD",
		"data": {
			"dispute_id": "nx_dp_won",
			"transaction_id": "nx_tx_won",
			"status": "won"
		}
	}`)

	event, err := ParseNxtapayEvent(payload)
	require.NoError(t, err)

	evt, err := MapNxtapayEventToInternal(event, "hash_won")
	require.NoError(t, err)

	assert.Equal(t, InternalEventChargebackReversed, evt.InternalType)
}

// TestMapNxtapayEventToInternal_UnknownType 测试未知事件类型
func TestMapNxtapayEventToInternal_UnknownType(t *testing.T) {
	payload := []byte(`{
		"event_id": "evt_nx_unknown",
		"event_type": "customer.created",
		"timestamp": 1710000005
	}`)

	event, err := ParseNxtapayEvent(payload)
	require.NoError(t, err)

	_, err = MapNxtapayEventToInternal(event, "hash_unknown")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown event type")
}

// TestComputeNxtapayPayloadHash 测试 payload hash 计算
func TestComputeNxtapayPayloadHash(t *testing.T) {
	payload1 := []byte(`{"event_id":"test"}`)
	payload2 := []byte(`{"event_id":"test"}`)
	payload3 := []byte(`{"event_id":"different"}`)

	hash1 := ComputeNxtapayPayloadHash(payload1)
	hash2 := ComputeNxtapayPayloadHash(payload2)
	hash3 := ComputeNxtapayPayloadHash(payload3)

	// 相同 payload 应该生成相同 hash
	assert.Equal(t, hash1, hash2)

	// 不同 payload 应该生成不同 hash
	assert.NotEqual(t, hash1, hash3)

	// hash 长度应该是 64 (SHA256 hex)
	assert.Len(t, hash1, 64)
}

// TestNxtapayEventJSON 完整的 JSON 序列化/反序列化测试
func TestNxtapayEventJSON(t *testing.T) {
	original := NxtapayWebhookEvent{
		EventID:       "evt_nx_full_test",
		EventType:     "payment.captured",
		Timestamp:     1710000000,
		MerchantID:    "MERCHANT_001",
		TransactionID: "nx_tx_full",
		OrderID:       "order_full",
		Amount:        json.Number("999.99"),
		Currency:      "GBP",
		Status:        "completed",
		Data:          json.RawMessage(`{"extra":"data"}`),
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var parsed NxtapayWebhookEvent
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, original.EventID, parsed.EventID)
	assert.Equal(t, original.EventType, parsed.EventType)
	assert.Equal(t, original.Timestamp, parsed.Timestamp)
	assert.Equal(t, original.TransactionID, parsed.TransactionID)
	assert.Equal(t, original.OrderID, parsed.OrderID)
	assert.Equal(t, original.Currency, parsed.Currency)
}
