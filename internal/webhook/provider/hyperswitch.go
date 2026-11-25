package provider

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// Hyperswitch webhook 验签与事件映射（P3）
//
// Hyperswitch 会把自己从底层 PSP 收到的事件统一转发给我们，
// 因此这里只需处理 Hyperswitch 自己的事件命名，不再关心底层 PSP 差异。
// ============================================================================

// HyperswitchWebhookVerifier Hyperswitch webhook 验签器。
type HyperswitchWebhookVerifier struct {
	webhookSecret string
}

func NewHyperswitchWebhookVerifier(webhookSecret string) *HyperswitchWebhookVerifier {
	return &HyperswitchWebhookVerifier{webhookSecret: webhookSecret}
}

// Verify 校验签名。
//
// 支持两种形态：
//   - "t=<ts>,v1=<hex-or-base64>"（HMAC-SHA512 作用于 "{ts}.{body}"）
//   - 裸签名（HMAC-SHA512 作用于 body）
func (v *HyperswitchWebhookVerifier) Verify(payload []byte, signature string) error {
	if v.webhookSecret == "" {
		return errors.New("hyperswitch: webhook secret is required")
	}
	if signature == "" {
		return errors.New("hyperswitch: signature is required")
	}

	timestamp, sig := parseHyperswitchSignature(signature)
	signed := payload
	if timestamp != "" {
		signed = []byte(timestamp + "." + string(payload))
	}

	mac := hmac.New(sha512.New, []byte(v.webhookSecret))
	mac.Write(signed)
	sum := mac.Sum(nil)

	if hmac.Equal([]byte(sig), []byte(hex.EncodeToString(sum))) ||
		hmac.Equal([]byte(sig), []byte(base64.StdEncoding.EncodeToString(sum))) {
		return nil
	}
	return errors.New("hyperswitch: signature mismatch")
}

func parseHyperswitchSignature(signature string) (timestamp, sig string) {
	sig = signature
	for _, part := range strings.Split(signature, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t", "timestamp":
			timestamp = kv[1]
		case "v1", "signature":
			sig = kv[1]
		}
	}
	return timestamp, sig
}

// Hyperswitch 事件类型。
const (
	HyperswitchEventPaymentSucceeded = "payment_succeeded"
	HyperswitchEventPaymentFailed    = "payment_failed"
	HyperswitchEventRefundSucceeded  = "refund_succeeded"
	HyperswitchEventRefundFailed     = "refund_failed"
	HyperswitchEventDisputeOpened    = "dispute_opened"
	HyperswitchEventDisputeExpired   = "dispute_expired"
	HyperswitchEventDisputeWon       = "dispute_won"
	HyperswitchEventDisputeCancelled = "dispute_cancelled"
)

// HyperswitchEvent Hyperswitch webhook 事件。
type HyperswitchEvent struct {
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	Content   struct {
		PaymentID string            `json:"payment_id"`
		RefundID  string            `json:"refund_id"`
		Amount    int64             `json:"amount"`
		Currency  string            `json:"currency"`
		Metadata  map[string]string `json:"metadata"`
		Object    struct {
			PaymentID string            `json:"payment_id"`
			RefundID  string            `json:"refund_id"`
			Amount    int64             `json:"amount"`
			Currency  string            `json:"currency"`
			Metadata  map[string]string `json:"metadata"`
		} `json:"object"`
	} `json:"content"`
	CreatedAt int64 `json:"created_at"`
}

// ParseHyperswitchEvent 解析 Hyperswitch 事件。
func ParseHyperswitchEvent(payload []byte) (*HyperswitchEvent, error) {
	var event HyperswitchEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("hyperswitch: parse event: %w", err)
	}
	if event.EventID == "" {
		return nil, errors.New("hyperswitch: missing event id")
	}
	return &event, nil
}

// MapHyperswitchEventToInternal 映射 Hyperswitch 事件到内部事件。
func MapHyperswitchEventToInternal(event *HyperswitchEvent, payloadHash string) (*ProviderWebhookEvent, error) {
	if event == nil {
		return nil, errors.New("hyperswitch: event is nil")
	}

	evt := &ProviderWebhookEvent{
		EventID:        event.EventID,
		EventType:      event.EventType,
		RawPayloadHash: payloadHash,
		OccurredAt:     time.Now().UTC(),
	}
	if event.CreatedAt > 0 {
		evt.OccurredAt = time.Unix(event.CreatedAt, 0).UTC()
	}

	paymentID := event.Content.PaymentID
	if paymentID == "" {
		paymentID = event.Content.Object.PaymentID
	}
	refundID := event.Content.RefundID
	if refundID == "" {
		refundID = event.Content.Object.RefundID
	}
	amount := event.Content.Amount
	if amount == 0 {
		amount = event.Content.Object.Amount
	}
	currency := event.Content.Currency
	if currency == "" {
		currency = event.Content.Object.Currency
	}
	meta := event.Content.Metadata
	if meta == nil {
		meta = event.Content.Object.Metadata
	}

	evt.OrderID = meta["order_id"]
	if mid := meta["merchant_id"]; mid != "" {
		evt.MerchantID, _ = uuid.Parse(mid)
	}
	evt.Amount = amount
	evt.Currency = strings.ToUpper(currency)

	switch event.EventType {
	case HyperswitchEventPaymentSucceeded:
		evt.InternalType = InternalEventPaymentCaptured
		evt.ProviderRef = paymentID
	case HyperswitchEventPaymentFailed:
		evt.InternalType = InternalEventPaymentFailed
		evt.ProviderRef = paymentID
	case HyperswitchEventRefundSucceeded:
		evt.InternalType = InternalEventRefundConfirmed
		evt.ProviderRef = refundID
		if evt.ProviderRef == "" {
			evt.ProviderRef = paymentID
		}
	case HyperswitchEventRefundFailed:
		evt.InternalType = InternalEventRefundConfirmed
		evt.ProviderRef = refundID
		if evt.ProviderRef == "" {
			evt.ProviderRef = paymentID
		}
	case HyperswitchEventDisputeOpened, HyperswitchEventDisputeExpired:
		evt.InternalType = InternalEventChargebackReceived
		evt.ProviderRef = paymentID
	case HyperswitchEventDisputeWon, HyperswitchEventDisputeCancelled:
		evt.InternalType = InternalEventChargebackReversed
		evt.ProviderRef = paymentID
	default:
		return nil, fmt.Errorf("hyperswitch: unknown event type: %s", event.EventType)
	}

	return evt, nil
}
