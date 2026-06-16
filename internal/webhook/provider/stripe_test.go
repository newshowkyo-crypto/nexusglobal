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

const testStripeWebhookSecret = "whsec_test_stripe_secret_1234567890"

// TestStripeWebhookVerifier_New 创建验签器
func TestStripeWebhookVerifier_New(t *testing.T) {
	verifier := NewStripeWebhookVerifier(testStripeWebhookSecret)
	require.NotNil(t, verifier)
}

// TestStripeWebhookVerifier_Verify_ValidSignature 测试有效签名
func TestStripeWebhookVerifier_Verify_ValidSignature(t *testing.T) {
	verifier := NewStripeWebhookVerifier(testStripeWebhookSecret)

	payload := []byte(`{"id":"evt_test_123","type":"payment_intent.succeeded","created":1710000000,"data":{}}`)

	// 生成有效签名
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signedPayload := timestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(testStripeWebhookSecret))
	mac.Write([]byte(signedPayload))
	signature := hex.EncodeToString(mac.Sum(nil))
	sigHeader := fmt.Sprintf("t=%s,v1=%s", timestamp, signature)

	err := verifier.Verify(payload, sigHeader)
	assert.NoError(t, err)
}

// TestStripeWebhookVerifier_Verify_MissingSecret 测试缺少 secret
func TestStripeWebhookVerifier_Verify_MissingSecret(t *testing.T) {
	verifier := NewStripeWebhookVerifier("")
	err := verifier.Verify([]byte("test"), "sig")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "webhook secret is required")
}

// TestStripeWebhookVerifier_Verify_MissingSignature 测试缺少签名
func TestStripeWebhookVerifier_Verify_MissingSignature(t *testing.T) {
	verifier := NewStripeWebhookVerifier(testStripeWebhookSecret)
	err := verifier.Verify([]byte("test"), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature is required")
}

// TestStripeWebhookVerifier_Verify_InvalidFormat 测试无效格式
func TestStripeWebhookVerifier_Verify_InvalidFormat(t *testing.T) {
	verifier := NewStripeWebhookVerifier(testStripeWebhookSecret)

	// 无效格式
	err := verifier.Verify([]byte("test"), "invalid_format")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid signature format")
}

// TestStripeWebhookVerifier_Verify_ExpiredTimestamp 测试过期时间戳
func TestStripeWebhookVerifier_Verify_ExpiredTimestamp(t *testing.T) {
	verifier := NewStripeWebhookVerifier(testStripeWebhookSecret)

	payload := []byte(`{"id":"evt_test_123","type":"payment_intent.succeeded"}`)

	// 使用过期的时间戳（10分钟前）
	oldTimestamp := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	signedPayload := oldTimestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(testStripeWebhookSecret))
	mac.Write([]byte(signedPayload))
	signature := hex.EncodeToString(mac.Sum(nil))
	sigHeader := fmt.Sprintf("t=%s,v1=%s", oldTimestamp, signature)

	err := verifier.Verify(payload, sigHeader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp expired")
}

// TestStripeWebhookVerifier_Verify_MismatchSignature 测试签名不匹配
func TestStripeWebhookVerifier_Verify_MismatchSignature(t *testing.T) {
	verifier := NewStripeWebhookVerifier(testStripeWebhookSecret)

	payload := []byte(`{"id":"evt_test_123","type":"payment_intent.succeeded"}`)

	// 使用正确的时间戳但错误的签名
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	sigHeader := fmt.Sprintf("t=%s,v1=%s", timestamp, "wrong_signature_here")

	err := verifier.Verify(payload, sigHeader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature mismatch")
}

// TestParseStripeEvent_Valid 测试解析有效事件
func TestParseStripeEvent_Valid(t *testing.T) {
	payload := []byte(`{
		"id": "evt_test_123",
		"type": "payment_intent.succeeded",
		"created": 1710000000,
		"data": {"object": {"id": "pi_test", "amount": 1000}}
	}`)

	event, err := ParseStripeEvent(payload)
	require.NoError(t, err)
	require.NotNil(t, event)
	assert.Equal(t, "evt_test_123", event.ID)
	assert.Equal(t, "payment_intent.succeeded", event.Type)
	assert.Equal(t, int64(1710000000), event.Created)
}

// TestParseStripeEvent_InvalidJSON 测试无效 JSON
func TestParseStripeEvent_InvalidJSON(t *testing.T) {
	payload := []byte(`{invalid json}`)

	event, err := ParseStripeEvent(payload)
	assert.Error(t, err)
	assert.Nil(t, event)
}

// TestParseStripeEvent_MissingID 测试缺少 ID
func TestParseStripeEvent_MissingID(t *testing.T) {
	payload := []byte(`{"type": "payment_intent.succeeded", "created": 1710000000}`)

	event, err := ParseStripeEvent(payload)
	assert.Error(t, err)
	assert.Nil(t, event)
}

// TestMapStripeEventToInternal_PaymentSucceeded 测试映射支付成功
func TestMapStripeEventToInternal_PaymentSucceeded(t *testing.T) {
	payload := []byte(`{
		"id": "evt_123",
		"type": "payment_intent.succeeded",
		"created": 1710000000,
		"data": {
			"object": {
				"id": "pi_test_123",
				"amount": 10000,
				"currency": "usd",
				"metadata": {"order_id": "order_abc", "merchant_id": "00000000-0000-0000-0000-000000000001"}
			}
		}
	}`)

	event, err := ParseStripeEvent(payload)
	require.NoError(t, err)

	payloadHash := "hash_abc123"
	evt, err := MapStripeEventToInternal(event, payloadHash)
	require.NoError(t, err)

	assert.Equal(t, "evt_123", evt.EventID)
	assert.Equal(t, "payment_intent.succeeded", evt.EventType)
	assert.Equal(t, InternalEventPaymentCaptured, evt.InternalType)
	assert.Equal(t, "pi_test_123", evt.ProviderRef)
	assert.Equal(t, "order_abc", evt.OrderID)
	assert.Equal(t, int64(10000), evt.Amount)
	assert.Equal(t, "usd", evt.Currency)
	assert.Equal(t, payloadHash, evt.RawPayloadHash)
}

// TestMapStripeEventToInternal_PaymentFailed 测试映射支付失败
func TestMapStripeEventToInternal_PaymentFailed(t *testing.T) {
	payload := []byte(`{
		"id": "evt_456",
		"type": "payment_intent.payment_failed",
		"created": 1710000001,
		"data": {
			"object": {
				"id": "pi_failed",
				"amount": 5000,
				"currency": "eur"
			}
		}
	}`)

	event, err := ParseStripeEvent(payload)
	require.NoError(t, err)

	evt, err := MapStripeEventToInternal(event, "hash_def")
	require.NoError(t, err)

	assert.Equal(t, InternalEventPaymentFailed, evt.InternalType)
	assert.Equal(t, "pi_failed", evt.ProviderRef)
}

// TestMapStripeEventToInternal_Refund 测试映射退款
func TestMapStripeEventToInternal_Refund(t *testing.T) {
	payload := []byte(`{
		"id": "evt_refund_123",
		"type": "refund.succeeded",
		"created": 1710000002,
		"data": {
			"object": {
				"id": "re_123",
				"amount": 3000,
				"currency": "usd",
				"charge": "ch_original",
				"payment_intent": "pi_original"
			}
		}
	}`)

	event, err := ParseStripeEvent(payload)
	require.NoError(t, err)

	evt, err := MapStripeEventToInternal(event, "hash_ghi")
	require.NoError(t, err)

	assert.Equal(t, InternalEventRefundConfirmed, evt.InternalType)
	assert.Equal(t, "ch_original", evt.ProviderRef)
	assert.Equal(t, int64(3000), evt.Amount)
}

// TestMapStripeEventToInternal_Dispute 测试映射 Dispute
func TestMapStripeEventToInternal_Dispute(t *testing.T) {
	payload := []byte(`{
		"id": "evt_dispute_123",
		"type": "charge.dispute.created",
		"created": 1710000003,
		"data": {
			"object": {
				"id": "dp_123",
				"amount": 10000,
				"currency": "usd",
				"status": "needs_response",
				"reason": "fraudulent",
				"charge": "ch_disputed",
				"payment_intent": "pi_disputed"
			}
		}
	}`)

	event, err := ParseStripeEvent(payload)
	require.NoError(t, err)

	evt, err := MapStripeEventToInternal(event, "hash_jkl")
	require.NoError(t, err)

	assert.Equal(t, InternalEventChargebackReceived, evt.InternalType)
	assert.Equal(t, "ch_disputed", evt.ProviderRef)
	assert.Equal(t, int64(10000), evt.Amount)
}

// TestMapStripeEventToInternal_DisputeWon 测试映射 Dispute Won
func TestMapStripeEventToInternal_DisputeWon(t *testing.T) {
	payload := []byte(`{
		"id": "evt_dispute_won",
		"type": "charge.dispute.closed",
		"created": 1710000004,
		"data": {
			"object": {
				"id": "dp_won",
				"amount": 10000,
				"currency": "usd",
				"status": "won",
				"charge": "ch_won"
			}
		}
	}`)

	event, err := ParseStripeEvent(payload)
	require.NoError(t, err)

	evt, err := MapStripeEventToInternal(event, "hash_mno")
	require.NoError(t, err)

	assert.Equal(t, InternalEventChargebackReversed, evt.InternalType)
}

// TestMapStripeEventToInternal_UnknownType 测试未知事件类型
func TestMapStripeEventToInternal_UnknownType(t *testing.T) {
	payload := []byte(`{
		"id": "evt_unknown",
		"type": "customer.created",
		"created": 1710000005,
		"data": {}
	}`)

	event, err := ParseStripeEvent(payload)
	require.NoError(t, err)

	_, err = MapStripeEventToInternal(event, "hash")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown event type")
}

// TestIsIdempotentEvent 测试幂等事件判断
func TestIsIdempotentEvent(t *testing.T) {
	testCases := []struct {
		eventType InternalEventType
		expected  bool
	}{
		{InternalEventPaymentCaptured, true},
		{InternalEventPaymentFailed, true},
		{InternalEventRefundConfirmed, true},
		{InternalEventChargebackReceived, true},
		{InternalEventChargebackReversed, true},
		{InternalEventType("unknown"), false},
	}

	for _, tc := range testCases {
		result := IsIdempotentEvent(tc.eventType)
		assert.Equal(t, tc.expected, result, "event type: %s", tc.eventType)
	}
}

// TestStripeEventJSON 完整的 JSON 序列化/反序列化测试
func TestStripeEventJSON(t *testing.T) {
	original := StripeEvent{
		ID:      "evt_full_test",
		Type:    "payment_intent.succeeded",
		Created: 1710000000,
		Data:    json.RawMessage(`{"object":{"id":"pi_full","amount":20000,"currency":"gbp"}}`),
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var parsed StripeEvent
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, original.ID, parsed.ID)
	assert.Equal(t, original.Type, parsed.Type)
	assert.Equal(t, original.Created, parsed.Created)
}
