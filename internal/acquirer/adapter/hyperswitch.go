package adapter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// Hyperswitch（开源支付编排层）适配器
//
// Hyperswitch 作为聚合编排层放在我们与底层 PSP 之间：
//   - 我们只对接一套 /payments、/refunds API，具体 PSP 由 Hyperswitch 路由。
//   - 授权（manual capture）→ 捕获 → 退款 三段与本系统状态机对齐。
//   - webhook 使用 x-webhook-signature / x-webhook-timestamp 验签。
//
// 文档：https://docs.hyperswitch.io
// ============================================================================

// HyperswitchConfig Hyperswitch 客户端配置。
type HyperswitchConfig struct {
	APIKey        string        // api-key，必填
	WebhookSecret string        // webhook 验签密钥
	BaseURL       string        // 默认 https://sandbox.hyperswitch.io
	ProfileID     string        // 多 profile 部署时必填（X-Profile-Id）
	Timeout       time.Duration // 默认 30s
}

// HyperswitchClient Hyperswitch REST 客户端。
type HyperswitchClient struct {
	apiKey        string
	webhookSecret string
	baseURL       string
	profileID     string
	httpClient    *http.Client
}

// NewHyperswitchClient 创建 Hyperswitch 客户端。
func NewHyperswitchClient(cfg HyperswitchConfig) (*HyperswitchClient, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("hyperswitch: api key is required")
	}
	baseURL := strings.TrimSuffix(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://sandbox.hyperswitch.io"
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &HyperswitchClient{
		apiKey:        cfg.APIKey,
		webhookSecret: cfg.WebhookSecret,
		baseURL:       baseURL,
		profileID:     cfg.ProfileID,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
	}, nil
}

// Authorize 创建支付（manual capture，先授权后捕获）。
func (c *HyperswitchClient) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	if req.PaymentMethodToken == "" {
		return nil, ErrPaymentMethodTokenRequired
	}
	if req.Currency == "" {
		return nil, fmt.Errorf("%w: currency is required", ErrProviderInvalidRequest)
	}

	body := map[string]any{
		"amount":            amountToCents(req.Amount),
		"currency":          strings.ToUpper(req.Currency),
		"confirm":           true,
		"capture_method":    "manual",
		"payment_method":    "card",
		"payment_method_id": req.PaymentMethodToken,
		"metadata": map[string]any{
			"order_id": req.OrderID,
		},
	}
	if req.MerchantID != uuid.Nil {
		body["metadata"].(map[string]any)["merchant_id"] = req.MerchantID.String()
	}
	if req.BuyerIP != "" {
		body["browser_info"] = map[string]any{"ip_address": req.BuyerIP}
	}

	raw, status, err := c.doJSON(ctx, http.MethodPost, "/payments", body, req.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	var resp hyperswitchPayment
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal payment: %v", ErrUnknownExternalState, err)
	}
	if resp.Error != nil {
		return nil, mapHyperswitchError(resp.Error, status)
	}
	ref := resp.ref()
	if ref == "" {
		return nil, fmt.Errorf("%w: payment id missing", ErrUnknownExternalState)
	}

	switch resp.Status {
	case "succeeded", "requires_capture":
		return &domain.AuthorizeResult{
			TransactionID:    uuid.Nil,
			ProviderRef:      ref,
			Status:           domain.PaymentStatusAuthorized,
			AuthorizedAmount: req.Amount,
		}, nil
	case "requires_customer_action", "requires_capture_action":
		return &domain.AuthorizeResult{
			TransactionID:    uuid.Nil,
			ProviderRef:      ref,
			Status:           domain.PaymentStatusPending,
			AuthorizedAmount: req.Amount,
		}, ErrPaymentRequiresAction
	case "processing":
		return &domain.AuthorizeResult{
			TransactionID:    uuid.Nil,
			ProviderRef:      ref,
			Status:           domain.PaymentStatusPending,
			AuthorizedAmount: req.Amount,
		}, nil
	case "requires_payment_method", "failed", "cancelled", "expired":
		return nil, ErrPaymentDeclined
	default:
		return nil, fmt.Errorf("%w: unexpected status %q", ErrUnknownExternalState, resp.Status)
	}
}

// Capture 捕获已授权支付。
func (c *HyperswitchClient) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	if req.ProviderRef == "" {
		return nil, fmt.Errorf("%w: provider_ref is required", ErrProviderInvalidRequest)
	}
	body := map[string]any{}
	if !req.Amount.IsZero() {
		body["amount_to_capture"] = amountToCents(req.Amount)
	}

	raw, status, err := c.doJSON(ctx, http.MethodPost,
		"/payments/"+req.ProviderRef+"/capture", body, req.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	var resp hyperswitchPayment
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal capture: %v", ErrUnknownExternalState, err)
	}
	if resp.Error != nil {
		return nil, mapHyperswitchError(resp.Error, status)
	}

	switch resp.Status {
	case "succeeded", "partially_captured":
		captured := req.Amount
		if resp.Amount > 0 {
			captured = centsToDecimal(resp.Amount)
		}
		return &domain.CaptureResult{
			ProviderRef:    req.ProviderRef,
			Status:         domain.PaymentStatusCaptured,
			CapturedAmount: captured,
		}, nil
	case "processing":
		return nil, fmt.Errorf("%w: capture processing", ErrUnknownExternalState)
	default:
		return nil, fmt.Errorf("%w: capture failed with status %q", ErrUnknownExternalState, resp.Status)
	}
}

// Refund 退款。
func (c *HyperswitchClient) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	if req.ProviderRef == "" {
		return nil, fmt.Errorf("%w: provider_ref is required", ErrProviderInvalidRequest)
	}
	body := map[string]any{"payment_id": req.ProviderRef}
	if !req.Amount.IsZero() {
		body["amount"] = amountToCents(req.Amount)
	}
	if req.Reason != "" {
		body["reason"] = req.Reason
	}

	raw, status, err := c.doJSON(ctx, http.MethodPost, "/refunds", body, req.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	var resp hyperswitchRefund
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal refund: %v", ErrUnknownExternalState, err)
	}
	if resp.Error != nil {
		return nil, mapHyperswitchError(resp.Error, status)
	}

	ref := resp.RefundID
	if ref == "" {
		ref = resp.ID
	}
	refunded := req.Amount
	if resp.Amount > 0 {
		refunded = centsToDecimal(resp.Amount)
	}

	switch resp.Status {
	case "succeeded":
		return &domain.RefundResult{ProviderRef: ref, Status: domain.PaymentStatusRefunded, RefundedAmount: refunded}, nil
	case "pending", "processing", "review":
		return &domain.RefundResult{ProviderRef: ref, Status: domain.PaymentStatusPending, RefundedAmount: refunded}, nil
	case "failed", "cancelled":
		return &domain.RefundResult{ProviderRef: ref, Status: domain.PaymentStatusFailed, RefundedAmount: refunded}, nil
	default:
		return nil, fmt.Errorf("%w: unexpected refund status %q", ErrUnknownExternalState, resp.Status)
	}
}

// VerifyWebhook 验签并解析事件。
//
// Hyperswitch 签名：x-webhook-signature = HMAC-SHA512(secret, "{timestamp}.{body}")，
// 兼容 base64 与 hex 两种编码；无 timestamp 时退化为对 body 签名。
func (c *HyperswitchClient) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	if c.webhookSecret == "" {
		return nil, ErrWebhookSignatureInvalid
	}
	if !c.validSignature(req.Payload, req.Signature) {
		return nil, ErrWebhookSignatureInvalid
	}

	var evt hyperswitchWebhook
	if err := json.Unmarshal(req.Payload, &evt); err != nil {
		return nil, fmt.Errorf("%w: unmarshal webhook: %v", ErrUnknownExternalState, err)
	}

	ref := evt.contentPaymentID()
	eventID := evt.EventID
	if eventID == "" {
		eventID = evt.EventType + ":" + ref
	}
	eventType := evt.EventType
	if eventType == "" {
		eventType = "webhook.event"
	}

	return &domain.VerifiedWebhookEvent{
		EventID:     eventID,
		EventType:   eventType,
		ProviderRef: ref,
		Payload:     req.Payload,
	}, nil
}

// validSignature 恒时比较签名；接受 base64 与 hex 两种表示。
func (c *HyperswitchClient) validSignature(payload []byte, signature string) bool {
	if signature == "" {
		return false
	}
	timestamp, sig := splitSignature(signature)
	candidates := [][]byte{payload}
	if timestamp != "" {
		candidates = [][]byte{[]byte(timestamp + "." + string(payload))}
	}

	for _, signed := range candidates {
		mac := hmac.New(sha512.New, []byte(c.webhookSecret))
		mac.Write(signed)
		sum := mac.Sum(nil)
		if hmac.Equal([]byte(sig), []byte(hex.EncodeToString(sum))) ||
			hmac.Equal([]byte(sig), []byte(base64.StdEncoding.EncodeToString(sum))) {
			return true
		}
	}
	return false
}

// splitSignature 解析 "t=...,v1=..." 形式的签名头；否则整串视为签名。
func splitSignature(signature string) (timestamp, sig string) {
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

func (c *HyperswitchClient) doJSON(ctx context.Context, method, path string, body any, idempotencyKey string) ([]byte, int, error) {
	var payload io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: marshal request: %v", ErrProviderInvalidRequest, err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("api-key", c.apiKey)
	if c.profileID != "" {
		req.Header.Set("X-Profile-Id", c.profileID)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, ErrProviderTimeout
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, ErrUnknownExternalState
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return raw, resp.StatusCode, ErrProviderRateLimited
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return raw, resp.StatusCode, ErrProviderAuthFailed
	}
	if resp.StatusCode >= 500 {
		return raw, resp.StatusCode, ErrUnknownExternalState
	}
	if resp.StatusCode >= 400 {
		var parsed struct {
			Error *hyperswitchError `json:"error"`
		}
		if json.Unmarshal(raw, &parsed) == nil && parsed.Error != nil {
			return raw, resp.StatusCode, mapHyperswitchError(parsed.Error, resp.StatusCode)
		}
		return raw, resp.StatusCode, ErrProviderInvalidRequest
	}
	return raw, resp.StatusCode, nil
}

// mapHyperswitchError 把 Hyperswitch 错误映射为统一错误。
func mapHyperswitchError(e *hyperswitchError, httpStatus int) error {
	if e == nil {
		return nil
	}
	code := strings.ToLower(e.Code)
	msg := strings.ToLower(e.Message)

	switch {
	case code == "card_declined", code == "insufficient_funds", code == "expired_card",
		strings.Contains(msg, "declined"), strings.Contains(msg, "insufficient funds"):
		return ErrPaymentDeclined
	case code == "authentication_required", strings.Contains(msg, "authentication"):
		return ErrPaymentRequiresAction
	case httpStatus == http.StatusTooManyRequests:
		return ErrProviderRateLimited
	case httpStatus == http.StatusUnauthorized || httpStatus == http.StatusForbidden:
		return ErrProviderAuthFailed
	case httpStatus >= 500:
		return ErrUnknownExternalState
	}
	return fmt.Errorf("%w: %s: %s", ErrProviderInvalidRequest, e.Code, e.Message)
}

// HyperswitchAcquirer 实现 domain.AcquirerClient。
type HyperswitchAcquirer struct {
	client *HyperswitchClient
}

// NewHyperswitchAcquirer 创建 Hyperswitch 收单适配器。
func NewHyperswitchAcquirer(client *HyperswitchClient) *HyperswitchAcquirer {
	return &HyperswitchAcquirer{client: client}
}

func (h *HyperswitchAcquirer) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	return h.client.Authorize(ctx, req)
}

func (h *HyperswitchAcquirer) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	return h.client.Capture(ctx, req)
}

func (h *HyperswitchAcquirer) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	return h.client.Refund(ctx, req)
}

func (h *HyperswitchAcquirer) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	return h.client.VerifyWebhook(ctx, req)
}

// ---------------------------------------------------------------------------
// Hyperswitch 响应结构
// ---------------------------------------------------------------------------

type hyperswitchPayment struct {
	ID           string            `json:"id"`
	PaymentID    string            `json:"payment_id"`
	Status       string            `json:"status"`
	Amount       int64             `json:"amount"`
	Currency     string            `json:"currency"`
	ClientSecret string            `json:"client_secret"`
	Error        *hyperswitchError `json:"error"`
}

// ref 兼容新旧两种响应字段。
func (p hyperswitchPayment) ref() string {
	if p.PaymentID != "" {
		return p.PaymentID
	}
	return p.ID
}

type hyperswitchRefund struct {
	ID       string            `json:"id"`
	RefundID string            `json:"refund_id"`
	Status   string            `json:"status"`
	Amount   int64             `json:"amount"`
	Error    *hyperswitchError `json:"error"`
}

type hyperswitchError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

type hyperswitchWebhook struct {
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	Content   struct {
		PaymentID string `json:"payment_id"`
		RefundID  string `json:"refund_id"`
		Object    struct {
			PaymentID string `json:"payment_id"`
			RefundID  string `json:"refund_id"`
		} `json:"object"`
	} `json:"content"`
}

func (w hyperswitchWebhook) contentPaymentID() string {
	if w.Content.PaymentID != "" {
		return w.Content.PaymentID
	}
	if w.Content.Object.PaymentID != "" {
		return w.Content.Object.PaymentID
	}
	if w.Content.RefundID != "" {
		return w.Content.RefundID
	}
	return w.Content.Object.RefundID
}
