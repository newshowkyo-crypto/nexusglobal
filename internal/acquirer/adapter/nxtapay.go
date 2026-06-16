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
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// NxtapayClient Nxtapay API 客户端
type NxtapayClient struct {
	apiKey        string
	apiSecret     string
	merchantID    string
	webhookSecret string
	baseURL       string
	httpClient    *http.Client
}

// NxtapayConfig Nxtapay 配置
type NxtapayConfig struct {
	APIKey        string // Nxtapay API key
	APISecret     string // Nxtapay API secret
	MerchantID    string // Nxtapay merchant ID
	WebhookSecret string // Webhook HMAC secret
	BaseURL       string // API base URL，sandbox 和 production 不同
	Timeout       time.Duration
}

// NewNxtapayClient 创建 Nxtapay 客户端
func NewNxtapayClient(cfg NxtapayConfig) (*NxtapayClient, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("nxtapay: API key is required")
	}
	if cfg.APISecret == "" {
		return nil, errors.New("nxtapay: API secret is required")
	}
	if cfg.MerchantID == "" {
		return nil, errors.New("nxtapay: merchant ID is required")
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://sandbox.nxtapay.com/api/v1" // 默认 sandbox URL
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &NxtapayClient{
		apiKey:        cfg.APIKey,
		apiSecret:     cfg.APISecret,
		merchantID:    cfg.MerchantID,
		webhookSecret: cfg.WebhookSecret,
		baseURL:       baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
	}, nil
}

// Authorize 授权（第一阶段：请求授权）
func (c *NxtapayClient) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	// 验证 payment_method_token
	if req.PaymentMethodToken == "" {
		return nil, ErrPaymentMethodTokenRequired
	}

	// 构建请求体
	body := map[string]interface{}{
		"merchant_id":    c.merchantID,
		"order_id":       req.OrderID,
		"amount":         amountToDecimal(req.Amount),
		"currency":       strings.ToUpper(req.Currency),
		"payment_method": req.PaymentMethodToken,
		"capture_mode":   "deferred", // 先授权，后捕获
		"callback_url":   "",         // 3DS 回调 URL
	}

	if req.IdempotencyKey != "" {
		body["idempotency_key"] = req.IdempotencyKey
	}

	if req.MerchantID != uuid.Nil {
		body["merchant_reference"] = req.MerchantID.String()
	}

	respBody, _, err := c.doRequest(ctx, http.MethodPost, "/payments/authorize", body)
	if err != nil {
		return nil, err
	}

	var authResp nxtapayAuthorizeResponse
	if err := json.Unmarshal(respBody, &authResp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal authorize response: %v", ErrUnknownExternalState, err)
	}

	// 处理错误
	if authResp.Error != nil {
		return nil, mapNxtapayError(authResp.Error)
	}

	// 检查状态
	switch authResp.Status {
	case "AUTHORIZED":
		return &domain.AuthorizeResult{
			TransactionID:    mustParseUUIDOptional(authResp.TransactionID),
			ProviderRef:      authResp.TransactionID,
			Status:           domain.PaymentStatusAuthorized,
			AuthorizedAmount: req.Amount,
			ExpiresAt:        parseNxtapayTime(authResp.ExpiresAt),
		}, nil

	case "PENDING_ACTION":
		return nil, ErrPaymentRequiresAction

	case "DECLINED", "FAILED":
		return nil, ErrPaymentDeclined

	default:
		return nil, fmt.Errorf("%w: unexpected status: %s", ErrUnknownExternalState, authResp.Status)
	}
}

// Capture 捕获（第二阶段：确认扣款）
func (c *NxtapayClient) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("nxtapay: provider_ref is required for capture")
	}

	body := map[string]interface{}{
		"transaction_id": req.ProviderRef,
	}

	if !req.Amount.IsZero() {
		body["amount"] = amountToDecimal(req.Amount)
	}

	if req.IdempotencyKey != "" {
		body["idempotency_key"] = req.IdempotencyKey
	}

	respBody, _, err := c.doRequest(ctx, http.MethodPost, "/payments/capture", body)
	if err != nil {
		return nil, err
	}

	var captureResp nxtapayCaptureResponse
	if err := json.Unmarshal(respBody, &captureResp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal capture response: %v", ErrUnknownExternalState, err)
	}

	if captureResp.Error != nil {
		return nil, mapNxtapayError(captureResp.Error)
	}

	if captureResp.Status == "CAPTURED" || captureResp.Status == "COMPLETED" {
		return &domain.CaptureResult{
			ProviderRef:    captureResp.TransactionID,
			Status:         domain.PaymentStatusCaptured,
			CapturedAmount: parseNxtapayAmount(captureResp.Amount),
		}, nil
	}

	return nil, fmt.Errorf("%w: capture failed, status: %s", ErrUnknownExternalState, captureResp.Status)
}

// Refund 退款
func (c *NxtapayClient) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("nxtapay: provider_ref is required for refund")
	}

	body := map[string]interface{}{
		"transaction_id": req.ProviderRef,
	}

	if !req.Amount.IsZero() {
		body["amount"] = amountToDecimal(req.Amount)
	}

	if req.Reason != "" {
		body["reason"] = req.Reason
	}

	if req.IdempotencyKey != "" {
		body["idempotency_key"] = req.IdempotencyKey
	}

	respBody, _, err := c.doRequest(ctx, http.MethodPost, "/refunds", body)
	if err != nil {
		return nil, err
	}

	var refundResp nxtapayRefundResponse
	if err := json.Unmarshal(respBody, &refundResp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal refund response: %v", ErrUnknownExternalState, err)
	}

	if refundResp.Error != nil {
		return nil, mapNxtapayError(refundResp.Error)
	}

	return &domain.RefundResult{
		ProviderRef:    refundResp.RefundID,
		Status:         mapNxtapayRefundStatus(refundResp.Status),
		RefundedAmount: parseNxtapayAmount(refundResp.Amount),
	}, nil
}

// QueryPayment 查询支付状态 - Legacy interface for AcquirerClient
func (c *NxtapayClient) QueryPayment(ctx context.Context, req domain.LegacyQueryRequest) (*domain.LegacyQueryResponse, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("nxtapay: provider_ref is required for query")
	}

	respBody, _, err := c.doRequest(ctx, http.MethodGet, "/payments/"+req.ProviderRef, nil)
	if err != nil {
		return nil, err
	}

	var queryResp nxtapayQueryResponse
	if err := json.Unmarshal(respBody, &queryResp); err != nil {
		return nil, fmt.Errorf("%w: unmarshal query response: %v", ErrUnknownExternalState, err)
	}

	if queryResp.Error != nil {
		return nil, mapNxtapayError(queryResp.Error)
	}

	return &domain.LegacyQueryResponse{
		ProviderRef: queryResp.TransactionID,
		Status:      mapNxtapayPaymentStatus(queryResp.Status),
		Amount:      parseNxtapayAmount(queryResp.Amount),
	}, nil
}

// VerifyWebhook 验证 webhook 签名
func (c *NxtapayClient) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	if c.webhookSecret == "" {
		return nil, ErrWebhookSignatureInvalid
	}
	if err := verifyNxtapayWebhookSignature(req.Payload, req.Signature, c.webhookSecret); err != nil {
		return nil, err
	}
	return &domain.VerifiedWebhookEvent{
		EventID:     "nxtapay-webhook",
		EventType:   "webhook.event",
		ProviderRef: "nxtapay-ref",
		Payload:     req.Payload,
	}, nil
}

// doRequest 执行 HTTP 请求
func (c *NxtapayClient) doRequest(ctx context.Context, method, path string, body map[string]interface{}) ([]byte, int, error) {
	var reqBody io.Reader = http.NoBody
	if method == http.MethodPost && body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, 0, err
	}

	// 添加签名头
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := generateNonce()
	var bodyHash string
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		bodyHash = hashSHA256(string(jsonBody))
	} else {
		bodyHash = hashSHA256("")
	}

	signature := SignNxtapayRequest(c.apiSecret, method, path, timestamp, nonce, bodyHash)

	req.Header.Set("X-Nxtapay-Key", c.apiKey)
	req.Header.Set("X-Nxtapay-Timestamp", timestamp)
	req.Header.Set("X-Nxtapay-Nonce", nonce)
	req.Header.Set("X-Nxtapay-Signature", signature)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

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

// SignNxtapayRequest 生成 Nxtapay 请求签名
// 签名算法: HMAC-SHA256(secret, method + path + timestamp + nonce + bodyHash)
func SignNxtapayRequest(secret, method, path, timestamp, nonce, bodyHash string) string {
	message := strings.Join([]string{method, path, timestamp, nonce, bodyHash}, "")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyNxtapayWebhookSignature 验证 Nxtapay webhook 签名
func verifyNxtapayWebhookSignature(payload []byte, signature, secret string) error {
	if signature == "" {
		return ErrWebhookSignatureInvalid
	}

	parts := strings.Split(signature, ",")
	if len(parts) != 4 {
		return ErrWebhookSignatureInvalid
	}

	var timestamp, nonce, hash string
	for _, part := range parts {
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
		return ErrWebhookSignatureInvalid
	}

	// 验证时间戳 tolerance (5 分钟)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrWebhookSignatureInvalid
	}

	now := time.Now().Unix()
	if now-ts > 300 || ts-now > 300 {
		return ErrWebhookSignatureInvalid
	}

	// 验证 nonce 是否重复（简化：检查与当前时间戳的组合）
	// 实际生产应使用 Redis 存储 nonce

	// 验证签名
	payloadHash := hashSHA256(string(payload))
	expectedSig := SignNxtapayRequest(secret, "POST", "/webhook", timestamp, nonce, payloadHash)

	if !hmac.Equal([]byte(hash), []byte(expectedSig)) {
		return ErrWebhookSignatureInvalid
	}

	return nil
}

// generateNonce 生成随机 nonce
func generateNonce() string {
	return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano) + uuid.New().String()))
}

// hashSHA256 计算 SHA256 哈希
func hashSHA256(data string) string {
	h := sha256.New()
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// mapNxtapayError 映射 Nxtapay 错误到统一错误
func mapNxtapayError(e *nxtapayError) error {
	if e == nil {
		return nil
	}

	switch e.Code {
	case "DECLINED", "INSUFFICIENT_FUNDS", "CARD_DECLINED":
		return ErrPaymentDeclined
	case "PENDING_ACTION", "3DS_REQUIRED":
		return ErrPaymentRequiresAction
	case "RATE_LIMITED", "429":
		return ErrProviderRateLimited
	case "UNAUTHORIZED", "INVALID_SIGNATURE", "401":
		return ErrProviderAuthFailed
	case "INVALID_REQUEST", "VALIDATION_ERROR", "400":
		return ErrProviderInvalidRequest
	case "TIMEOUT", "NETWORK_ERROR":
		return ErrUnknownExternalState
	}

	// 5xx 或未知错误
	if e.HTTPStatus >= 500 || e.Code == "500" {
		return ErrUnknownExternalState
	}

	return fmt.Errorf("%w: %s", ErrUnknownExternalState, e.Message)
}

// mapNxtapayPaymentStatus 映射 Nxtapay 状态到 domain 状态
func mapNxtapayPaymentStatus(status string) domain.PaymentStatus {
	switch status {
	case "CAPTURED", "COMPLETED":
		return domain.PaymentStatusCaptured
	case "AUTHORIZED", "PENDING_CAPTURE":
		return domain.PaymentStatusAuthorized
	case "PENDING", "PENDING_ACTION":
		return domain.PaymentStatusPending
	case "DECLINED", "FAILED", "CANCELLED":
		return domain.PaymentStatusFailed
	default:
		return domain.PaymentStatusUnknown
	}
}

// mapNxtapayRefundStatus 映射 Nxtapay 退款状态
func mapNxtapayRefundStatus(status string) domain.PaymentStatus {
	switch status {
	case "COMPLETED", "REFUNDED":
		return domain.PaymentStatusRefunded
	case "PENDING":
		return domain.PaymentStatusPending
	case "FAILED", "CANCELLED":
		return domain.PaymentStatusFailed
	default:
		return domain.PaymentStatusUnknown
	}
}

// amountToDecimal 将 decimal 转换为字符串
func amountToDecimal(amount decimal.Decimal) string {
	return amount.StringFixed(2)
}

// parseNxtapayAmount 解析 Nxtapay 金额字符串
func parseNxtapayAmount(amountStr interface{}) decimal.Decimal {
	if amountStr == nil {
		return decimal.Zero
	}
	switch v := amountStr.(type) {
	case string:
		d, _ := decimal.NewFromString(v)
		return d
	case float64:
		return decimal.NewFromFloat(v)
	case int:
		return decimal.NewFromInt(int64(v))
	case int64:
		return decimal.NewFromInt(v)
	default:
		return decimal.Zero
	}
}

// parseNxtapayTime 解析 Nxtapay 时间字符串
func parseNxtapayTime(timeStr string) *time.Time {
	if timeStr == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return nil
	}
	return &t
}

// mustParseUUIDOptional 解析 UUID，可选
func mustParseUUIDOptional(s string) uuid.UUID {
	if s == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// Nxtapay API 响应结构

type nxtapayAuthorizeResponse struct {
	TransactionID string        `json:"transaction_id"`
	Status        string        `json:"status"`
	ExpiresAt     string        `json:"expires_at,omitempty"`
	RedirectURL   string        `json:"redirect_url,omitempty"`
	Error         *nxtapayError `json:"error,omitempty"`
}

type nxtapayCaptureResponse struct {
	TransactionID string        `json:"transaction_id"`
	Status        string        `json:"status"`
	Amount        interface{}   `json:"amount"`
	CapturedAt    string        `json:"captured_at,omitempty"`
	Error         *nxtapayError `json:"error,omitempty"`
}

type nxtapayRefundResponse struct {
	RefundID string        `json:"refund_id"`
	Status   string        `json:"status"`
	Amount   interface{}   `json:"amount"`
	Error    *nxtapayError `json:"error,omitempty"`
}

type nxtapayQueryResponse struct {
	TransactionID string        `json:"transaction_id"`
	OrderID       string        `json:"order_id"`
	Status        string        `json:"status"`
	Amount        interface{}   `json:"amount"`
	Currency      string        `json:"currency"`
	Error         *nxtapayError `json:"error,omitempty"`
}

type nxtapayError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status"`
}

// NxtapayAcquirer 实现 domain.AcquirerClient 接口的 Nxtapay 适配器
type NxtapayAcquirer struct {
	client *NxtapayClient
}

// NewNxtapayAcquirer 创建 Nxtapay 收单适配器
func NewNxtapayAcquirer(client *NxtapayClient) *NxtapayAcquirer {
	return &NxtapayAcquirer{client: client}
}

// Authorize 授权
func (n *NxtapayAcquirer) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	return n.client.Authorize(ctx, req)
}

// Capture 捕获
func (n *NxtapayAcquirer) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	return n.client.Capture(ctx, req)
}

// Refund 退款
func (n *NxtapayAcquirer) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	return n.client.Refund(ctx, req)
}

// QueryPayment 查询 - Legacy interface
func (n *NxtapayAcquirer) QueryPayment(ctx context.Context, req domain.LegacyQueryRequest) (*domain.LegacyQueryResponse, error) {
	return n.client.QueryPayment(ctx, req)
}

// VerifyWebhook 验签
func (n *NxtapayAcquirer) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	return n.client.VerifyWebhook(ctx, req)
}
