package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// Bank Collection Adapter - 银行收款适配器
// 支持 Dynamic Virtual Account 和 Static Virtual Account + Unique Reference
// ============================================================================

// BankCollectionAdapter 银行收款适配器
type BankCollectionAdapter struct {
	providerCode    string
	apiKey          string
	apiSecret       string
	webhookSecret   string
	baseURL         string
	httpClient      *http.Client
	supportedAssets []string
	config          map[string]any
}

// BankCollectionConfig 银行收款配置
type BankCollectionConfig struct {
	ProviderCode  string
	APIKey        string
	APISecret     string
	WebhookSecret string
	BaseURL       string
	Timeout       time.Duration
	Config        map[string]any
}

// NewBankCollectionAdapter 创建银行收款适配器
func NewBankCollectionAdapter(providerCode string, apiKey string, apiSecret string, webhookSecret string, config map[string]any) (domain.AdapterClient, error) {
	if apiKey == "" {
		return nil, domain.ErrCredentialMissing
	}

	timeout := 30 * time.Second
	if t, ok := config["timeout"].(time.Duration); ok {
		timeout = t
	}

	baseURL := ""
	if u, ok := config["base_url"].(string); ok {
		baseURL = u
	}

	// 根据不同 provider 设置默认 base URL
	if baseURL == "" {
		switch strings.ToLower(providerCode) {
		case "airwallex":
			baseURL = "https://api.airwallex.com"
		case "payoneer":
			baseURL = "https://api.payoneer.com"
		default:
			baseURL = "https://api.mock-bank-collection.local"
		}
	}

	return &BankCollectionAdapter{
		providerCode:  providerCode,
		apiKey:        apiKey,
		apiSecret:     apiSecret,
		webhookSecret: webhookSecret,
		baseURL:       baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
		supportedAssets: []string{"USD", "EUR", "GBP", "CNY"},
		config:          config,
	}, nil
}

// GetAdapterType 返回适配器类型
func (b *BankCollectionAdapter) GetAdapterType() domain.AdapterType {
	return domain.AdapterTypeBankCollection
}

// SupportsAsset 检查是否支持指定资产
func (b *BankCollectionAdapter) SupportsAsset(assetCode string) bool {
	for _, a := range b.supportedAssets {
		if a == assetCode {
			return true
		}
	}
	return false
}

// SupportsNetwork 检查是否支持指定网络（银行不使用网络概念）
func (b *BankCollectionAdapter) SupportsNetwork(network string) bool {
	return false
}

// ============================================================================
// InboundAdapter 接口实现
// ============================================================================

// CreateInboundIntent 创建入金意图（分配虚拟账户）
func (b *BankCollectionAdapter) CreateInboundIntent(ctx context.Context, req domain.InboundIntentRequest) (*domain.InboundIntentResponse, error) {
	// 验证资产支持
	if !b.SupportsAsset(req.AssetCode) {
		return nil, domain.ErrAssetNotSupported
	}

	// 构建请求
	intentReq := map[string]any{
		"order_id":        req.OrderID,
		"amount":          req.Amount.String(),
		"currency":        req.AssetCode,
		"buyer_ref":       req.BuyerRef,
		"idempotency_key": req.IdempotencyKey,
		"mode":            "dynamic", // 动态虚拟账户模式
	}

	// 如果配置为静态账户模式
	if mode, ok := b.config["account_mode"].(string); ok && mode == "static" {
		intentReq["mode"] = "static"
		intentReq["static_account_id"] = b.config["static_account_id"]
	}

	// 调用外部 API（不在 DB transaction 内）
	respBody, statusCode, err := b.doRequest(ctx, "POST", "/api/v1/inbound-intents", intentReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		IntentID      string `json:"intent_id"`
		ProviderRef   string `json:"provider_ref"`
		Status        string `json:"status"`
		AccountNumber string `json:"account_number"`
		RoutingNumber string `json:"routing_number"`
		IBAN          string `json:"iban"`
		BankName      string `json:"bank_name"`
		BankAddress   string `json:"bank_address"`
		PaymentRef    string `json:"payment_reference"`
		ExpiresAt     string `json:"expires_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("bank collection: failed to parse response: %w", err)
	}

	// 检查状态码
	if statusCode >= 400 {
		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Code != "" {
			return nil, fmt.Errorf("bank collection: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("bank collection: request failed with status %d", statusCode)
	}

	intentID := uuid.New()
	if resp.IntentID != "" {
		intentID, _ = uuid.Parse(resp.IntentID)
	}

	// 转换状态
	status := domain.InboundStatusCreated
	switch resp.Status {
	case "WAITING_FUNDS":
		status = domain.InboundStatusWaitingFunds
	case "CREATED":
		status = domain.InboundStatusCreated
	}

	// 构建虚拟账户详情
	virtualAccount := &domain.VirtualAccountDetails{
		AccountNumber:    resp.AccountNumber,
		RoutingNumber:    resp.RoutingNumber,
		IBAN:             resp.IBAN,
		BankName:         resp.BankName,
		BankAddress:      resp.BankAddress,
		PaymentReference: resp.PaymentRef,
	}

	var expiresAt *time.Time
	if resp.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, resp.ExpiresAt)
		if err == nil {
			expiresAt = &t
		}
	}

	return &domain.InboundIntentResponse{
		IntentID:       intentID,
		ProviderRef:    resp.ProviderRef,
		Status:         status,
		VirtualAccount: virtualAccount,
		ExpiresAt:      expiresAt,
		CreatedAt:      time.Now(),
	}, nil
}

// QueryInboundIntent 查询入金意图状态
func (b *BankCollectionAdapter) QueryInboundIntent(ctx context.Context, req domain.QueryInboundIntentRequest) (*domain.InboundIntentStatusResponse, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("bank collection: provider_ref required")
	}

	// 调用外部 API
	respBody, statusCode, err := b.doRequest(ctx, "GET", fmt.Sprintf("/api/v1/inbound-intents/%s", req.ProviderRef), nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		IntentID        string `json:"intent_id"`
		ProviderRef     string `json:"provider_ref"`
		Status          string `json:"status"`
		ReceivedAmount  string `json:"received_amount"`
		ConfirmedAmount string `json:"confirmed_amount"`
		TransactionRef  string `json:"transaction_ref"`
		UpdatedAt       string `json:"updated_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("bank collection: failed to parse response: %w", err)
	}

	// 检查状态码
	if statusCode >= 400 {
		return nil, fmt.Errorf("bank collection: query failed with status %d", statusCode)
	}

	// 转换状态
	status := domain.InboundStatusCreated
	switch resp.Status {
	case "WAITING_FUNDS":
		status = domain.InboundStatusWaitingFunds
	case "PARTIALLY_MATCHED":
		status = domain.InboundStatusPartiallyMatched
	case "CONFIRMED":
		status = domain.InboundStatusConfirmed
	case "RECONCILE_MISMATCH":
		status = domain.InboundStatusReconcileMismatch
	case "EXPIRED":
		status = domain.InboundStatusExpired
	case "FAILED":
		status = domain.InboundStatusFailed
	}

	receivedAmount, _ := decimal.NewFromString(resp.ReceivedAmount)
	confirmedAmount, _ := decimal.NewFromString(resp.ConfirmedAmount)

	return &domain.InboundIntentStatusResponse{
		IntentID:        req.IntentID,
		ProviderRef:     resp.ProviderRef,
		Status:          status,
		ReceivedAmount:  receivedAmount,
		ConfirmedAmount: confirmedAmount,
		TransactionRef:  resp.TransactionRef,
		UpdatedAt:       time.Now(),
	}, nil
}

// VerifyInboundWebhook 验证入金 webhook
func (b *BankCollectionAdapter) VerifyInboundWebhook(ctx context.Context, payload []byte, signature string) (*domain.InboundWebhookEvent, error) {
	// 验证签名
	if b.webhookSecret != "" {
		if !b.verifySignature(payload, signature) {
			return nil, errors.New("bank collection: webhook signature verification failed")
		}
	}

	// 解析事件
	var event struct {
		EventID        string `json:"event_id"`
		EventType      string `json:"event_type"`
		IntentID       string `json:"intent_id"`
		ProviderRef    string `json:"provider_ref"`
		Amount         string `json:"amount"`
		Currency       string `json:"currency"`
		Status         string `json:"status"`
		TransactionRef string `json:"transaction_ref"`
		Timestamp      string `json:"timestamp"`
	}

	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("bank collection: failed to parse webhook: %w", err)
	}

	// 验证必填字段
	if event.EventID == "" || event.ProviderRef == "" {
		return nil, errors.New("bank collection: webhook missing required fields")
	}

	// 转换状态
	status := domain.InboundStatusCreated
	switch event.Status {
	case "CONFIRMED":
		status = domain.InboundStatusConfirmed
	case "PARTIALLY_MATCHED":
		status = domain.InboundStatusPartiallyMatched
	case "RECONCILE_MISMATCH":
		status = domain.InboundStatusReconcileMismatch
	case "FAILED":
		status = domain.InboundStatusFailed
	}

	amount, _ := decimal.NewFromString(event.Amount)

	timestamp := time.Now()
	if event.Timestamp != "" {
		t, err := time.Parse(time.RFC3339, event.Timestamp)
		if err == nil {
			timestamp = t
		}
	}

	intentID := uuid.Nil
	if event.IntentID != "" {
		intentID, _ = uuid.Parse(event.IntentID)
	}

	return &domain.InboundWebhookEvent{
		EventID:        event.EventID,
		EventType:      event.EventType,
		IntentID:       intentID,
		ProviderRef:    event.ProviderRef,
		Amount:         amount,
		AssetCode:      event.Currency,
		Status:         status,
		TransactionRef: event.TransactionRef,
		Timestamp:      timestamp,
		RawPayload:     payload,
	}, nil
}

// ============================================================================
// 内部方法
// ============================================================================

// doRequest 执行 HTTP 请求
// 不在 DB transaction 内调用
func (b *BankCollectionAdapter) doRequest(ctx context.Context, method string, path string, body map[string]any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = strings.NewReader(string(jsonBody))
	}

	url := b.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", b.apiKey)

	// 签名请求
	if b.apiSecret != "" {
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		signature := b.signRequest(method, path, timestamp, body)
		req.Header.Set("X-Timestamp", timestamp)
		req.Header.Set("X-Signature", signature)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	return respBody, resp.StatusCode, nil
}

// signRequest 签名请求
func (b *BankCollectionAdapter) signRequest(method string, path string, timestamp string, body map[string]any) string {
	// 计算请求体 hash
	var bodyHash string
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		h := sha256.Sum256(jsonBody)
		bodyHash = hex.EncodeToString(h[:])
	} else {
		bodyHash = ""
	}

	// 构建签名消息
	message := strings.Join([]string{method, path, timestamp, bodyHash}, "")

	// HMAC-SHA256 签名
	mac := hmac.New(sha256.New, []byte(b.apiSecret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifySignature 验证 webhook 签名
func (b *BankCollectionAdapter) verifySignature(payload []byte, signature string) bool {
	// 解析签名头
	// 格式: t=timestamp,v1=signature
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
		return false
	}

	// 验证时间戳（5分钟容忍）
	var ts time.Time
	// 尝试解析为 Unix timestamp
	var tsUnix int64
	if _, err := fmt.Sscanf(timestamp, "%d", &tsUnix); err == nil {
		ts = time.Unix(tsUnix, 0)
	} else {
		// 尝试 RFC3339 格式
		ts, err = time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return false
		}
	}

	// 检查时间戳是否在合理范围内（防止重放攻击）
	if time.Since(ts) > 5*time.Minute || time.Until(ts) > 1*time.Minute {
		return false
	}

	// 计算期望签名
	payloadHash := sha256.Sum256(payload)
	message := timestamp + "." + hex.EncodeToString(payloadHash[:])
	mac := hmac.New(sha256.New, []byte(b.webhookSecret))
	mac.Write([]byte(message))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(sig), []byte(expectedSig))
}
