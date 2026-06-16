package provider

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/acquirer/adapter"
	acquirerdomain "github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/adapter/domain"
)

// ============================================================================
// Card Acquirer Adapter - 兼容现有 acquirer adapter
// ============================================================================

// CardAcquirerAdapter 卡网收单适配器
// 包装现有 acquirer.AcquirerClient，实现通用 AdapterClient 接口
type CardAcquirerAdapter struct {
	providerCode    string
	acquirerClient  acquirerdomain.AcquirerClient
	webhookSecret   string
	supportedAssets []string
	config          map[string]any
}

// NewMockCardAcquirer 创建 Mock 卡网适配器
func NewMockCardAcquirer(webhookSecret string, config map[string]any) (domain.AdapterClient, error) {
	mockClient := adapter.NewMockAcquirer(webhookSecret)

	return &CardAcquirerAdapter{
		providerCode:    "mock",
		acquirerClient:  mockClient,
		webhookSecret:   webhookSecret,
		supportedAssets: []string{"USD", "EUR", "GBP"},
		config:          config,
	}, nil
}

// NewStripeCardAcquirer 创建 Stripe 卡网适配器
func NewStripeCardAcquirer(secretKey string, webhookSecret string, config map[string]any) (domain.AdapterClient, error) {
	if secretKey == "" {
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

	stripeClient, err := adapter.NewStripeClient(adapter.StripeConfig{
		SecretKey:     secretKey,
		WebhookSecret: webhookSecret,
		BaseURL:       baseURL,
		Timeout:       timeout,
	})
	if err != nil {
		return nil, err
	}

	return &CardAcquirerAdapter{
		providerCode:    "stripe",
		acquirerClient:  stripeClient,
		webhookSecret:   webhookSecret,
		supportedAssets: []string{"USD", "EUR", "GBP", "CAD", "AUD"},
		config:          config,
	}, nil
}

// NewNxtapayCardAcquirer 创建 Nxtapay 卡网适配器
func NewNxtapayCardAcquirer(apiKey string, apiSecret string, webhookSecret string, config map[string]any) (domain.AdapterClient, error) {
	if apiKey == "" || apiSecret == "" {
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

	nxtapayClient, err := adapter.NewNxtapayClient(adapter.NxtapayConfig{
		APIKey:        apiKey,
		APISecret:     apiSecret,
		WebhookSecret: webhookSecret,
		BaseURL:       baseURL,
		Timeout:       timeout,
	})
	if err != nil {
		return nil, err
	}

	return &CardAcquirerAdapter{
		providerCode:    "nxtapay",
		acquirerClient:  nxtapayClient,
		webhookSecret:   webhookSecret,
		supportedAssets: []string{"USD", "EUR", "GBP"},
		config:          config,
	}, nil
}

// GetAdapterType 返回适配器类型
func (c *CardAcquirerAdapter) GetAdapterType() domain.AdapterType {
	return domain.AdapterTypeCardAcquirer
}

// SupportsAsset 检查是否支持指定资产
func (c *CardAcquirerAdapter) SupportsAsset(assetCode string) bool {
	for _, a := range c.supportedAssets {
		if a == assetCode {
			return true
		}
	}
	return false
}

// SupportsNetwork 检查是否支持指定网络（卡网不适用）
func (c *CardAcquirerAdapter) SupportsNetwork(network string) bool {
	return false // 卡网不使用网络概念
}

// ============================================================================
// InboundAdapter 接口实现
// ============================================================================

// CreateInboundIntent 创建入金意图（卡网授权）
func (c *CardAcquirerAdapter) CreateInboundIntent(ctx context.Context, req domain.InboundIntentRequest) (*domain.InboundIntentResponse, error) {
	// 验证必须使用 payment_method_token（real mode 约束）
	if req.PaymentMethodToken == "" && c.providerCode != "mock" {
		return nil, errors.New("card acquirer: payment_method_token required for real mode")
	}

	// 转换为 acquirer 请求
	authReq := acquirerdomain.AuthorizeRequest{
		OrderID:            req.OrderID,
		MerchantID:         uuid.Nil, // 从 req.MerchantID 获取
		Amount:             req.Amount,
		Currency:           req.AssetCode,
		PaymentMethodToken: req.PaymentMethodToken,
		IdempotencyKey:     req.IdempotencyKey,
	}

	if req.MerchantID != nil {
		authReq.MerchantID = *req.MerchantID
	}

	// 调用现有 acquirer client
	authResp, err := c.acquirerClient.Authorize(ctx, authReq)
	if err != nil {
		// 错误映射
		if errors.Is(err, adapter.ErrPaymentDeclined) {
			return nil, domain.ErrApprovalRequired // 拒绝需要人工复核
		}
		if errors.Is(err, adapter.ErrUnknownExternalState) {
			return nil, domain.ErrUnknownExternalState
		}
		if errors.Is(err, acquirerdomain.ErrProviderTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		if errors.Is(err, acquirerdomain.ErrExplicitFailure) {
			return nil, domain.ErrApprovalRequired
		}
		return nil, err
	}

	// 转换响应
	status := domain.InboundStatusCreated
	if authResp.Status == acquirerdomain.PaymentStatusAuthorized {
		status = domain.InboundStatusWaitingFunds // 等待 capture
	}

	intentID := uuid.New()

	return &domain.InboundIntentResponse{
		IntentID:    intentID,
		ProviderRef: authResp.ProviderRef,
		Status:      status,
		CreatedAt:   time.Now(),
	}, nil
}

// QueryInboundIntent 查询入金意图状态
func (c *CardAcquirerAdapter) QueryInboundIntent(ctx context.Context, req domain.QueryInboundIntentRequest) (*domain.InboundIntentStatusResponse, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("card acquirer: provider_ref required")
	}

	// QueryPayment 已从 AcquirerClient 接口移除
	// 返回不支持错误
	return nil, errors.New("card acquirer: QueryInboundIntent not supported in tier-based mode")
}

// VerifyInboundWebhook 验证入金 webhook
func (c *CardAcquirerAdapter) VerifyInboundWebhook(ctx context.Context, payload []byte, signature string) (*domain.InboundWebhookEvent, error) {
	// 使用新的 VerifyWebhook 接口
	webhookReq := acquirerdomain.VerifyWebhookRequest{
		Payload:   payload,
		Signature: signature,
	}
	verifiedEvent, err := c.acquirerClient.VerifyWebhook(ctx, webhookReq)
	if err != nil {
		if errors.Is(err, adapter.ErrWebhookSignatureInvalid) {
			return nil, domain.ErrWebhookSignatureInvalid
		}
		return nil, err
	}

	// 解析 webhook payload（简化实现，实际应从具体 provider 解析）
	// 这里返回一个基本的事件结构
	inboundEvent := &domain.InboundWebhookEvent{
		EventID:     verifiedEvent.EventID,
		EventType:   verifiedEvent.EventType,
		ProviderRef: verifiedEvent.ProviderRef,
		AssetCode:   "USD",
		Timestamp:   time.Now(),
		RawPayload:  payload,
		Status:      domain.InboundStatusCreated,
	}

	return inboundEvent, nil
}

// ============================================================================
// Capture / Refund 方法（卡网特有）
// ============================================================================

// Capture 捕获（确认扣款）
func (c *CardAcquirerAdapter) Capture(ctx context.Context, providerRef string, idempotencyKey string) error {
	_, err := c.acquirerClient.Capture(ctx, acquirerdomain.CaptureRequest{
		ProviderRef:    providerRef,
		IdempotencyKey: idempotencyKey,
	})
	return err
}

// Refund 退款
func (c *CardAcquirerAdapter) Refund(ctx context.Context, providerRef string, amount decimal.Decimal, idempotencyKey string) error {
	_, err := c.acquirerClient.Refund(ctx, acquirerdomain.RefundRequest{
		ProviderRef:    providerRef,
		Amount:         amount,
		IdempotencyKey: idempotencyKey,
	})
	return err
}
