package provider

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StripeWebhookVerifier Stripe webhook 验签器
type StripeWebhookVerifier struct {
	webhookSecret string
}

// NewStripeWebhookVerifier 创建 Stripe webhook 验签器
func NewStripeWebhookVerifier(webhookSecret string) *StripeWebhookVerifier {
	return &StripeWebhookVerifier{
		webhookSecret: webhookSecret,
	}
}

// Verify 验证 Stripe webhook 签名
// Stripe-Signature 格式: t=timestamp,v1=signature
func (v *StripeWebhookVerifier) Verify(payload []byte, signature string) error {
	if v.webhookSecret == "" {
		return errors.New("stripe: webhook secret is required")
	}
	if signature == "" {
		return errors.New("stripe: signature is required")
	}

	// 解析签名头
	var timestamp string
	var sig string
	for _, part := range strings.Split(signature, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			sig = kv[1]
		}
	}

	if timestamp == "" || sig == "" {
		return errors.New("stripe: invalid signature format")
	}

	// 验证时间戳 tolerance (5 分钟)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("stripe: invalid timestamp")
	}

	now := time.Now().Unix()
	if now-ts > 300 || ts-now > 300 {
		return errors.New("stripe: signature timestamp expired")
	}

	// 计算签名
	signedPayload := timestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(v.webhookSecret))
	mac.Write([]byte(signedPayload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return errors.New("stripe: signature mismatch")
	}

	return nil
}

// StripeEvent Stripe webhook 事件
type StripeEvent struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Created int64           `json:"created"`
	Data    json.RawMessage `json:"data"`
}

// StripeEventType Stripe 事件类型
type StripeEventType string

const (
	StripeEventPaymentIntentSucceeded StripeEventType = "payment_intent.succeeded"
	StripeEventPaymentIntentFailed    StripeEventType = "payment_intent.payment_failed"
	StripeEventChargeRefunded         StripeEventType = "charge.refunded"
	StripeEventRefundSucceeded        StripeEventType = "refund.succeeded"
	StripeEventDisputeCreated         StripeEventType = "charge.dispute.created"
	StripeEventDisputeClosed          StripeEventType = "charge.dispute.closed"
)

// StripePaymentIntent Stripe PaymentIntent 对象
type StripePaymentIntent struct {
	ID            string            `json:"id"`
	Object        string            `json:"object"`
	Amount        int64             `json:"amount"`
	Currency      string            `json:"currency"`
	Status        string            `json:"status"`
	PaymentMethod string            `json:"payment_method"`
	Description   string            `json:"description"`
	Metadata      map[string]string `json:"metadata"`
}

// StripeCharge Stripe Charge 对象
type StripeCharge struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	PaymentIntent string `json:"payment_intent"`
	Dispute       string `json:"dispute"`
	Refund        string `json:"refund"`
	Outcome       *struct {
		NetworkStatus string `json:"network_status"`
		RiskLevel     string `json:"risk_level"`
	} `json:"outcome"`
}

// StripeDispute Stripe Dispute 对象
type StripeDispute struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	Charge        string `json:"charge"`
	PaymentIntent string `json:"payment_intent"`
}

// StripeRefund Stripe Refund 对象
type StripeRefund struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	PaymentIntent string `json:"payment_intent"`
	Charge        string `json:"charge"`
	Reason        string `json:"reason"`
}

// InternalEventType 内部事件类型
type InternalEventType string

const (
	InternalEventPaymentCaptured    InternalEventType = "payment_captured"
	InternalEventPaymentFailed      InternalEventType = "payment_failed"
	InternalEventRefundConfirmed    InternalEventType = "refund_confirmed"
	InternalEventChargebackReceived InternalEventType = "chargeback_received"
	InternalEventChargebackReversed InternalEventType = "chargeback_reversed"
)

// ProviderWebhookEvent 提供商 webhook 事件
type ProviderWebhookEvent struct {
	EventID        string
	EventType      string
	InternalType   InternalEventType
	ProviderRef    string
	OrderID        string
	MerchantID     uuid.UUID
	Amount         int64
	Currency       string
	OccurredAt     time.Time
	RawPayloadHash string
}

// ParseStripeEvent 解析 Stripe 事件
func ParseStripeEvent(payload []byte) (*StripeEvent, error) {
	var event StripeEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("stripe: parse event: %w", err)
	}
	if event.ID == "" {
		return nil, errors.New("stripe: missing event id")
	}
	return &event, nil
}

// MapStripeEventToInternal 映射 Stripe 事件到内部事件
func MapStripeEventToInternal(event *StripeEvent, payloadHash string) (*ProviderWebhookEvent, error) {
	if event == nil {
		return nil, errors.New("stripe: event is nil")
	}

	evt := &ProviderWebhookEvent{
		EventID:        event.ID,
		EventType:      string(event.Type),
		OccurredAt:     time.Unix(event.Created, 0),
		RawPayloadHash: payloadHash,
	}

	// 根据事件类型映射
	switch StripeEventType(event.Type) {
	case StripeEventPaymentIntentSucceeded:
		evt.InternalType = InternalEventPaymentCaptured
		// 解析 PaymentIntent
		var obj struct {
			Object struct {
				ID       string            `json:"id"`
				Amount   int64             `json:"amount"`
				Currency string            `json:"currency"`
				Metadata map[string]string `json:"metadata"`
			} `json:"object"`
		}
		if err := json.Unmarshal(event.Data, &obj); err == nil {
			evt.ProviderRef = obj.Object.ID
			evt.Amount = obj.Object.Amount
			evt.Currency = obj.Object.Currency
			if obj.Object.Metadata != nil {
				evt.OrderID = obj.Object.Metadata["order_id"]
				if mid := obj.Object.Metadata["merchant_id"]; mid != "" {
					evt.MerchantID, _ = uuid.Parse(mid)
				}
			}
		}

	case StripeEventPaymentIntentFailed:
		evt.InternalType = InternalEventPaymentFailed
		var obj struct {
			Object struct {
				ID       string            `json:"id"`
				Amount   int64             `json:"amount"`
				Currency string            `json:"currency"`
				Metadata map[string]string `json:"metadata"`
			} `json:"object"`
		}
		if err := json.Unmarshal(event.Data, &obj); err == nil {
			evt.ProviderRef = obj.Object.ID
			evt.Amount = obj.Object.Amount
			evt.Currency = obj.Object.Currency
			if obj.Object.Metadata != nil {
				evt.OrderID = obj.Object.Metadata["order_id"]
			}
		}

	case StripeEventRefundSucceeded, StripeEventChargeRefunded:
		evt.InternalType = InternalEventRefundConfirmed
		var obj struct {
			Object StripeRefund `json:"object"`
		}
		if err := json.Unmarshal(event.Data, &obj); err == nil {
			evt.ProviderRef = obj.Object.Charge
			evt.Amount = obj.Object.Amount
			evt.Currency = obj.Object.Currency
		}

	case StripeEventDisputeCreated:
		evt.InternalType = InternalEventChargebackReceived
		var obj struct {
			Object StripeDispute `json:"object"`
		}
		if err := json.Unmarshal(event.Data, &obj); err == nil {
			evt.ProviderRef = obj.Object.Charge
			evt.Amount = obj.Object.Amount
			evt.Currency = obj.Object.Currency
		}

	case StripeEventDisputeClosed:
		var obj struct {
			Object StripeDispute `json:"object"`
		}
		if err := json.Unmarshal(event.Data, &obj); err == nil {
			evt.ProviderRef = obj.Object.Charge
			evt.Amount = obj.Object.Amount
			evt.Currency = obj.Object.Currency
			// 根据 dispute status 判断是 reversed 还是其他
			if obj.Object.Status == "won" {
				evt.InternalType = InternalEventChargebackReversed
			}
		}

	default:
		return nil, fmt.Errorf("stripe: unknown event type: %s", event.Type)
	}

	return evt, nil
}

// IsIdempotentEvent 判断事件是否可幂等处理
func IsIdempotentEvent(internalType InternalEventType) bool {
	switch internalType {
	case InternalEventPaymentCaptured,
		InternalEventPaymentFailed,
		InternalEventRefundConfirmed,
		InternalEventChargebackReceived,
		InternalEventChargebackReversed:
		return true
	default:
		return false
	}
}
