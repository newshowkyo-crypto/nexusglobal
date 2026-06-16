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

// NxtapayWebhookVerifier Nxtapay webhook 验签器
type NxtapayWebhookVerifier struct {
	webhookSecret string
}

// NewNxtapayWebhookVerifier 创建 Nxtapay webhook 验签器
func NewNxtapayWebhookVerifier(webhookSecret string) *NxtapayWebhookVerifier {
	return &NxtapayWebhookVerifier{
		webhookSecret: webhookSecret,
	}
}

// Verify 验证 Nxtapay webhook 签名
// Nxtapay-Signature 格式: ts=timestamp,nonce=nonce,hash=hash
func (v *NxtapayWebhookVerifier) Verify(payload []byte, signature string) error {
	if v.webhookSecret == "" {
		return errors.New("nxtapay: webhook secret is required")
	}
	if signature == "" {
		return errors.New("nxtapay: signature is required")
	}

	// 解析签名头
	var timestamp, nonce, hash string
	for _, part := range strings.Split(signature, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "ts":
			timestamp = kv[1]
		case "nonce":
			nonce = kv[1]
		case "hash":
			hash = kv[1]
		}
	}

	if timestamp == "" || nonce == "" || hash == "" {
		return errors.New("nxtapay: invalid signature format")
	}

	// 验证时间戳 tolerance (5 分钟)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("nxtapay: invalid timestamp")
	}

	now := time.Now().Unix()
	if now-ts > 300 || ts-now > 300 {
		return errors.New("nxtapay: signature timestamp expired")
	}

	// 计算期望签名
	payloadHash := sha256.Sum256(payload)
	message := timestamp + "." + nonce + "." + hex.EncodeToString(payloadHash[:])
	mac := hmac.New(sha256.New, []byte(v.webhookSecret))
	mac.Write([]byte(message))
	expectedHash := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(hash), []byte(expectedHash)) {
		return errors.New("nxtapay: signature mismatch")
	}

	return nil
}

// NxtapayWebhookEvent Nxtapay webhook 事件
type NxtapayWebhookEvent struct {
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	Timestamp     int64           `json:"timestamp"`
	MerchantID    string          `json:"merchant_id"`
	TransactionID string          `json:"transaction_id,omitempty"`
	OrderID       string          `json:"order_id,omitempty"`
	Amount        json.Number     `json:"amount,omitempty"`
	Currency      string          `json:"currency,omitempty"`
	Status        string          `json:"status,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
}

// NxtapayEventType Nxtapay 事件类型
type NxtapayEventType string

const (
	NxtapayEventPaymentCaptured NxtapayEventType = "payment.captured"
	NxtapayEventPaymentDeclined NxtapayEventType = "payment.declined"
	NxtapayEventPaymentPending  NxtapayEventType = "payment.pending"
	NxtapayEventRefundCompleted NxtapayEventType = "refund.completed"
	NxtapayEventDisputeReceived NxtapayEventType = "dispute.received"
	NxtapayEventDisputeResolved NxtapayEventType = "dispute.resolved"
)

// NxtapayPaymentData Nxtapay 支付数据
type NxtapayPaymentData struct {
	TransactionID string `json:"transaction_id"`
	OrderID       string `json:"order_id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
}

// NxtapayRefundData Nxtapay 退款数据
type NxtapayRefundData struct {
	RefundID      string `json:"refund_id"`
	TransactionID string `json:"transaction_id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
}

// NxtapayDisputeData Nxtapay Dispute 数据
type NxtapayDisputeData struct {
	DisputeID     string `json:"dispute_id"`
	TransactionID string `json:"transaction_id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Reason        string `json:"reason"`
	Status        string `json:"status"`
}

// ParseNxtapayEvent 解析 Nxtapay 事件
func ParseNxtapayEvent(payload []byte) (*NxtapayWebhookEvent, error) {
	var event NxtapayWebhookEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("nxtapay: parse event: %w", err)
	}
	if event.EventID == "" {
		return nil, errors.New("nxtapay: missing event id")
	}
	return &event, nil
}

// MapNxtapayEventToInternal 映射 Nxtapay 事件到内部事件
func MapNxtapayEventToInternal(event *NxtapayWebhookEvent, payloadHash string) (*ProviderWebhookEvent, error) {
	if event == nil {
		return nil, errors.New("nxtapay: event is nil")
	}

	evt := &ProviderWebhookEvent{
		EventID:        event.EventID,
		EventType:      string(event.EventType),
		OccurredAt:     time.Unix(event.Timestamp, 0),
		RawPayloadHash: payloadHash,
		OrderID:        event.OrderID,
		Currency:       event.Currency,
	}

	if event.MerchantID != "" {
		evt.MerchantID, _ = uuid.Parse(event.MerchantID)
	}

	// 解析金额
	if amtStr := event.Amount.String(); amtStr != "" && amtStr != "0" {
		amountFloat, _ := event.Amount.Float64()
		evt.Amount = int64(amountFloat * 100) // 假设是元，转分为
	}

	// 根据事件类型映射
	switch NxtapayEventType(event.EventType) {
	case NxtapayEventPaymentCaptured:
		evt.InternalType = InternalEventPaymentCaptured
		evt.ProviderRef = event.TransactionID
		if event.Data != nil {
			var data NxtapayPaymentData
			if json.Unmarshal(event.Data, &data) == nil {
				evt.ProviderRef = data.TransactionID
				evt.OrderID = data.OrderID
			}
		}

	case NxtapayEventPaymentDeclined:
		evt.InternalType = InternalEventPaymentFailed
		evt.ProviderRef = event.TransactionID

	case NxtapayEventRefundCompleted:
		evt.InternalType = InternalEventRefundConfirmed
		if event.Data != nil {
			var data NxtapayRefundData
			if json.Unmarshal(event.Data, &data) == nil {
				evt.ProviderRef = data.TransactionID
			}
		}

	case NxtapayEventDisputeReceived:
		evt.InternalType = InternalEventChargebackReceived
		if event.Data != nil {
			var data NxtapayDisputeData
			if json.Unmarshal(event.Data, &data) == nil {
				evt.ProviderRef = data.TransactionID
			}
		}

	case NxtapayEventDisputeResolved:
		evt.InternalType = InternalEventChargebackReversed
		if event.Data != nil {
			var data NxtapayDisputeData
			if json.Unmarshal(event.Data, &data) == nil && data.Status == "won" {
				evt.InternalType = InternalEventChargebackReversed
			}
		}

	default:
		return nil, fmt.Errorf("nxtapay: unknown event type: %s", event.EventType)
	}

	return evt, nil
}

// ComputeNxtapayPayloadHash 计算 Nxtapay payload 哈希
func ComputeNxtapayPayloadHash(payload []byte) string {
	h := sha256.Sum256(payload)
	return hex.EncodeToString(h[:])
}
