package adapter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// StripeClient Stripe API 客户端
type StripeClient struct {
	secretKey     string
	webhookSecret string
	baseURL       string
	httpClient    *http.Client
	apiVersion    string
}

// StripeConfig Stripe 配置
type StripeConfig struct {
	SecretKey     string // Stripe secret key (sk_test_... 或 sk_live_...)
	WebhookSecret string // Webhook signing secret (whsec_...)
	BaseURL       string // API base URL，默认为 https://api.stripe.com
	Timeout       time.Duration
	APIVersion    string // API 版本，默认 2024-04-10
}

// NewStripeClient 创建 Stripe 客户端
func NewStripeClient(cfg StripeConfig) (*StripeClient, error) {
	if cfg.SecretKey == "" {
		return nil, errors.New("stripe: secret key is required")
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.stripe.com"
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = "2024-04-10"
	}

	return &StripeClient{
		secretKey:     cfg.SecretKey,
		webhookSecret: cfg.WebhookSecret,
		baseURL:       baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
		apiVersion: apiVersion,
	}, nil
}

// Authorize 授权（创建 PaymentIntent，capture_method=manual）
func (c *StripeClient) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	// 验证 payment_method_token
	if req.PaymentMethodToken == "" {
		return nil, ErrPaymentMethodTokenRequired
	}

	// 构建请求
	params := map[string]string{
		"amount":              strconv.Itoa(amountToCents(req.Amount)),
		"currency":            strings.ToLower(req.Currency),
		"payment_method":      req.PaymentMethodToken,
		"capture_method":      "manual", // 先授权，后捕获
		"confirmation_method": "automatic",
		"confirm":             "true", // 不 confirm 只会停在 requires_confirmation，后续 Capture 必然失败
		"return_url":          "",     // 3DS 完成后跳转 URL
	}

	if req.IdempotencyKey != "" {
		params["idempotency_key"] = req.IdempotencyKey
	}

	if req.MerchantID != uuid.Nil {
		params["metadata[merchant_id]"] = req.MerchantID.String()
	}
	if req.OrderID != "" {
		params["metadata[order_id]"] = req.OrderID
	}

	body, _, err := c.post(ctx, "/v1/payment_intents", params)
	if err != nil {
		return nil, err
	}

	var piResp stripePaymentIntent
	if err := json.Unmarshal(body, &piResp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal payment intent: %v", ErrUnknownExternalState, err)
	}

	// 处理错误响应
	if piResp.Error != nil {
		return nil, mapStripeError(piResp.Error)
	}

	// 检查状态
	switch piResp.Status {
	case "requires_action", "requires_source_action":
		return &domain.AuthorizeResult{
			TransactionID:    uuid.New(),
			ProviderRef:      piResp.ID,
			Status:           domain.PaymentStatusPending,
			AuthorizedAmount: req.Amount,
		}, ErrPaymentRequiresAction

	case "requires_confirmation", "requires_payment_method":
		return &domain.AuthorizeResult{
			TransactionID:    uuid.New(),
			ProviderRef:      piResp.ID,
			Status:           domain.PaymentStatusAuthorized,
			AuthorizedAmount: req.Amount,
		}, nil

	case "requires_capture":
		return &domain.AuthorizeResult{
			TransactionID:    uuid.New(),
			ProviderRef:      piResp.ID,
			Status:           domain.PaymentStatusAuthorized,
			AuthorizedAmount: req.Amount,
		}, nil

	default:
		return nil, fmt.Errorf("%w: unexpected status: %s", ErrUnknownExternalState, piResp.Status)
	}
}

// Capture 捕获授权
func (c *StripeClient) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("stripe: provider_ref is required for capture")
	}

	params := map[string]string{}
	if !req.Amount.IsZero() {
		params["amount_to_capture"] = strconv.Itoa(amountToCents(req.Amount))
	}

	if req.IdempotencyKey != "" {
		params["idempotency_key"] = req.IdempotencyKey
	}

	path := fmt.Sprintf("/v1/payment_intents/%s/capture", req.ProviderRef)
	body, _, err := c.post(ctx, path, params)
	if err != nil {
		return nil, err
	}

	var piResp stripePaymentIntent
	if err := json.Unmarshal(body, &piResp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal payment intent: %v", ErrUnknownExternalState, err)
	}

	if piResp.Error != nil {
		return nil, mapStripeError(piResp.Error)
	}

	if piResp.Status == "succeeded" {
		return &domain.CaptureResult{
			ProviderRef:    piResp.ID,
			Status:         domain.PaymentStatusCaptured,
			CapturedAmount: centsToDecimal(piResp.Amount),
		}, nil
	}

	return nil, fmt.Errorf("%w: capture failed, status: %s", ErrUnknownExternalState, piResp.Status)
}

// Refund 退款
func (c *StripeClient) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("stripe: provider_ref is required for refund")
	}

	params := map[string]string{
		"payment_intent": req.ProviderRef,
	}

	if !req.Amount.IsZero() {
		params["amount"] = strconv.Itoa(amountToCents(req.Amount))
	}

	if req.Reason != "" {
		params["reason"] = mapRefundReason(req.Reason)
	}

	if req.IdempotencyKey != "" {
		params["idempotency_key"] = req.IdempotencyKey
	}

	body, _, err := c.post(ctx, "/v1/refunds", params)
	if err != nil {
		return nil, err
	}

	var refundResp stripeRefund
	if err := json.Unmarshal(body, &refundResp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal refund: %v", ErrUnknownExternalState, err)
	}

	if refundResp.Error != nil {
		return nil, mapStripeError(refundResp.Error)
	}

	return &domain.RefundResult{
		ProviderRef:    refundResp.ID,
		Status:         mapStripeRefundStatus(refundResp.Status),
		RefundedAmount: centsToDecimal(refundResp.Amount),
	}, nil
}

// QueryPayment 查询支付状态 - Legacy interface
func (c *StripeClient) QueryPayment(ctx context.Context, req domain.LegacyQueryRequest) (*domain.LegacyQueryResponse, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("stripe: provider_ref is required for query")
	}

	path := fmt.Sprintf("/v1/payment_intents/%s", req.ProviderRef)
	body, _, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}

	var piResp stripePaymentIntent
	if err := json.Unmarshal(body, &piResp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal payment intent: %v", ErrUnknownExternalState, err)
	}

	if piResp.Error != nil {
		return nil, mapStripeError(piResp.Error)
	}

	return &domain.LegacyQueryResponse{
		ProviderRef: piResp.ID,
		Status:      mapStripeStatus(piResp.Status),
		Amount:      centsToDecimal(piResp.Amount),
	}, nil
}

// VerifyWebhook 验证 webhook 签名
func (c *StripeClient) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	if c.webhookSecret == "" {
		return nil, ErrWebhookSignatureInvalid
	}
	if err := verifyStripeSignature(req.Payload, req.Signature, c.webhookSecret); err != nil {
		return nil, err
	}
	return &domain.VerifiedWebhookEvent{
		EventID:     "stripe-webhook",
		EventType:   "webhook.event",
		ProviderRef: "stripe-ref",
		Payload:     req.Payload,
	}, nil
}

// post 发送 POST 请求
func (c *StripeClient) post(ctx context.Context, path string, params map[string]string) ([]byte, int, error) {
	return c.doRequest(ctx, http.MethodPost, path, params)
}

// get 发送 GET 请求
func (c *StripeClient) get(ctx context.Context, path string) ([]byte, int, error) {
	return c.doRequest(ctx, http.MethodGet, path, nil)
}

// doRequest 执行 HTTP 请求
func (c *StripeClient) doRequest(ctx context.Context, method, path string, params map[string]string) ([]byte, int, error) {
	var body io.Reader = http.NoBody
	if method == http.MethodPost && params != nil {
		values := url.Values{}
		for k, v := range params {
			values.Set(k, v)
		}
		body = bytes.NewBufferString(values.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, 0, err
	}

	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Stripe-Version", c.apiVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// 超时或网络错误
		return nil, 0, ErrProviderTimeout
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, ErrUnknownExternalState
	}

	return respBody, resp.StatusCode, nil
}

// mapStripeError 映射 Stripe 错误到统一错误
func mapStripeError(e *stripeError) error {
	if e == nil {
		return nil
	}

	switch e.Type {
	case "card_error":
		switch e.Code {
		case "card_declined", "insufficient_funds", "expired_card", "incorrect_cvc", "incorrect_number":
			return ErrPaymentDeclined
		case "authentication_required":
			return ErrPaymentRequiresAction
		}
	case "rate_limit_error":
		return ErrProviderRateLimited
	case "invalid_request_error":
		return ErrProviderInvalidRequest
	case "authentication_error":
		return ErrProviderAuthFailed
	}

	// 5xx 或未知错误
	if e.HTTPStatus >= 500 {
		return ErrUnknownExternalState
	}

	return fmt.Errorf("%w: %s: %s", ErrUnknownExternalState, e.Type, e.Message)
}

// mapStripeStatus 映射 Stripe 状态到 domain 状态
func mapStripeStatus(status string) domain.PaymentStatus {
	switch status {
	case "succeeded":
		return domain.PaymentStatusCaptured
	case "requires_capture":
		return domain.PaymentStatusAuthorized
	case "requires_confirmation", "requires_payment_method":
		return domain.PaymentStatusPending
	case "canceled":
		return domain.PaymentStatusFailed
	default:
		return domain.PaymentStatusUnknown
	}
}

// mapStripeRefundStatus 映射 Stripe 退款状态
func mapStripeRefundStatus(status string) domain.PaymentStatus {
	switch status {
	case "succeeded":
		return domain.PaymentStatusRefunded
	case "pending":
		return domain.PaymentStatusPending
	case "failed", "canceled":
		return domain.PaymentStatusFailed
	default:
		return domain.PaymentStatusUnknown
	}
}

// mapRefundReason 映射退款原因
func mapRefundReason(reason string) string {
	switch reason {
	case "duplicate", "fraudulent", "requested_by_customer":
		return reason
	default:
		return "requested_by_customer"
	}
}

// amountToCents 将 decimal 转换为分
func amountToCents(amount decimal.Decimal) int {
	return int(amount.Mul(decimal.NewFromInt(100)).IntPart())
}

// centsToDecimal 将分转换为 decimal
func centsToDecimal(cents int64) decimal.Decimal {
	return decimal.NewFromInt(cents).Div(decimal.NewFromInt(100))
}

// verifyStripeSignature 验证 Stripe webhook 签名
// Stripe-Signature 格式: t=timestamp,v1=signature
func verifyStripeSignature(payload []byte, signature, secret string) error {
	if signature == "" {
		return ErrWebhookSignatureInvalid
	}

	// 解析签名头
	var timestamp string
	var sig string
	for _, part := range strings.Split(signature, ",") {
		kv := strings.SplitN(part, "=", 2)
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
		return ErrWebhookSignatureInvalid
	}

	// 验证时间戳 tolerance (5 分钟)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrWebhookSignatureInvalid
	}

	now := time.Now().Unix()
	if now-ts > 300 || ts-now > 300 {
		// 时间戳超过 5 分钟 tolerance
		return ErrWebhookSignatureInvalid
	}

	// 计算签名
	signedPayload := timestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return ErrWebhookSignatureInvalid
	}

	return nil
}

// Stripe API 响应结构

type stripePaymentIntent struct {
	ID               string       `json:"id"`
	Object           string       `json:"object"`
	Amount           int64        `json:"amount"`
	Currency         string       `json:"currency"`
	Status           string       `json:"status"`
	Error            *stripeError `json:"error,omitempty"`
	LastPaymentError *stripeError `json:"last_payment_error,omitempty"`
}

type stripeRefund struct {
	ID       string       `json:"id"`
	Object   string       `json:"object"`
	Amount   int64        `json:"amount"`
	Currency string       `json:"currency"`
	Status   string       `json:"status"`
	Error    *stripeError `json:"error,omitempty"`
}

type stripeError struct {
	Type       string `json:"type"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status"`
	DocURL     string `json:"doc_url"`
	Param      string `json:"param"`
	RequestID  string `json:"request_id"`
}

// StripeAcquirer 实现 domain.AcquirerClient 接口的 Stripe 适配器
type StripeAcquirer struct {
	client *StripeClient
}

// NewStripeAcquirer 创建 Stripe 收单适配器
func NewStripeAcquirer(client *StripeClient) *StripeAcquirer {
	return &StripeAcquirer{client: client}
}

// Authorize 授权
func (s *StripeAcquirer) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	return s.client.Authorize(ctx, req)
}

// Capture 捕获
func (s *StripeAcquirer) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	return s.client.Capture(ctx, req)
}

// Refund 退款
func (s *StripeAcquirer) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	return s.client.Refund(ctx, req)
}

// QueryPayment 查询 - Legacy interface
func (s *StripeAcquirer) QueryPayment(ctx context.Context, req domain.LegacyQueryRequest) (*domain.LegacyQueryResponse, error) {
	return s.client.QueryPayment(ctx, req)
}

// VerifyWebhook 验签
func (s *StripeAcquirer) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	return s.client.VerifyWebhook(ctx, req)
}
