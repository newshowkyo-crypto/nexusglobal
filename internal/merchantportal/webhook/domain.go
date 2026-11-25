// Package webhook 实现商户出站回调（A2）。
//
// 设计：
//   - 事件与业务同事务写入 merchant_webhook_delivery（Outbox 语义的落地表）。
//   - 投递按 (endpoint, event_type, event_id) 唯一，重复事件不会重复通知。
//   - 签名 HMAC-SHA256(timestamp + "." + body)，secret 只存密文。
//   - 失败按退避重试，超过上限进入 DEAD，可人工 replay。
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// 事件类型。
const (
	EventPaymentSucceeded    = "payment.succeeded"
	EventPaymentFailed       = "payment.failed"
	EventRefundSucceeded     = "refund.succeeded"
	EventRefundFailed        = "refund.failed"
	EventRefundUnknown       = "refund.unknown"
	EventSettlementCompleted = "settlement.completed"
)

// 投递状态。
const (
	StatusPending    = "PENDING"
	StatusDelivering = "DELIVERING"
	StatusSucceeded  = "SUCCEEDED"
	StatusFailed     = "FAILED"
	StatusDead       = "DEAD"
)

// MaxAttempts 最大投递次数，超过后进入 DEAD。
const MaxAttempts = 8

// Endpoint 商户回调端点。
type Endpoint struct {
	ID           uuid.UUID `json:"id"`
	TenantID     uuid.UUID `json:"tenant_id"`
	MerchantID   uuid.UUID `json:"merchant_id"`
	URL          string    `json:"url"`
	EventTypes   []string  `json:"event_types"`
	Description  string    `json:"description"`
	Status       string    `json:"status"`
	SecretMasked string    `json:"secret_masked"`
	CreatedAt    time.Time `json:"created_at"`
}

// Delivery 一条待投递事件。
type Delivery struct {
	ID            uuid.UUID
	EndpointID    uuid.UUID
	MerchantID    uuid.UUID
	EventType     string
	EventID       string
	Payload       []byte
	Status        string
	AttemptCount  int
	NextAttemptAt time.Time
}

// Sign 生成签名：hex(HMAC-SHA256(secret, "{timestamp}.{body}"))。
func Sign(secret []byte, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// MaskSecret 只保留首尾各 4 位，用于展示。
func MaskSecret(secret string) string {
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "****" + secret[len(secret)-4:]
}

// Backoff 返回第 attempt 次失败后的下次重试延迟。
// attempt 从 1 开始：1m, 5m, 15m, 1h, 6h, 12h, 24h。
func Backoff(attempt int) time.Duration {
	schedule := []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
		1 * time.Hour,
		6 * time.Hour,
		12 * time.Hour,
		24 * time.Hour,
	}
	if attempt <= 0 {
		return schedule[0]
	}
	if attempt > len(schedule) {
		return schedule[len(schedule)-1]
	}
	return schedule[attempt-1]
}
