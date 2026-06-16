package checkout

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/pipeline"
)

var (
	// ErrChannelNotFound 通道未找到
	ErrChannelNotFound = errors.New("acquirer gateway: channel not found")
	// ErrChannelDisabled 通道已禁用
	ErrChannelDisabled = errors.New("acquirer gateway: channel disabled")
	// ErrRawCardNotAllowed 真实模式禁止 raw card
	ErrRawCardNotAllowed = errors.New("acquirer gateway: raw card not allowed in real mode")
	// ErrMerchantIDInvalid merchant ID 无效
	ErrMerchantIDInvalid = errors.New("acquirer gateway: invalid merchant_id")
	// ErrCrossGroupOperation 跨组操作不允许
	ErrCrossGroupOperation = errors.New("acquirer gateway: cross group operation not allowed")
	// ErrGroupFrozen 隔离组已冻结
	ErrGroupFrozen = errors.New("acquirer gateway: isolation group frozen")
)

// AcquirerGateway 通过 ChannelRegistry 获取 AcquirerClient
type AcquirerGateway struct {
	registry    *registry.ChannelRegistry
	pipelineSvc *pipeline.Service
	isReal      bool
}

// NewAcquirerGateway 创建基于 ChannelRegistry 的支付网关
func NewAcquirerGateway(reg *registry.ChannelRegistry, pipelineSvc *pipeline.Service, isReal bool) *AcquirerGateway {
	return &AcquirerGateway{
		registry:    reg,
		pipelineSvc: pipelineSvc,
		isReal:      isReal,
	}
}

// Charge 调用 AcquirerClient.Authorize -> Capture
func (g *AcquirerGateway) Charge(ctx context.Context, req PaymentRequest) (*PaymentResult, error) {
	// Real 模式必须拒绝 raw PAN/CVV
	if g.isReal {
		if req.CardNumber != "" || req.CardCVV != "" {
			return nil, ErrRawCardNotAllowed
		}
		// Real 模式必须要求 payment_method_token
		if req.PaymentMethodToken == "" {
			return nil, errors.New("acquirer gateway: payment_method_token required in real mode")
		}
	}

	// 解析 merchant ID（防止 panic）
	var merchantUUID uuid.UUID
	var err error
	if req.MerchantID != "" {
		merchantUUID, err = uuid.Parse(req.MerchantID)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMerchantIDInvalid, err)
		}
	} else {
		// 使用默认 merchant ID 用于演示
		merchantUUID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	}

	// 获取 channel ID（从 OrderID 解析或使用默认 channel）
	channelID := g.channelIDFromOrder(req.OrderID)

	// 从 registry 获取 client bundle
	bundle, err := g.registry.Get(channelID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelNotFound) {
			return nil, ErrChannelNotFound
		}
		return nil, fmt.Errorf("acquirer gateway: get channel: %w", err)
	}

	// 检查通道是否激活
	if !bundle.IsActive {
		return nil, ErrChannelDisabled
	}

	// 检查管道隔离组状态（如果配置了）
	if g.pipelineSvc != nil && bundle.IsolationGroupID != nil {
		// 检查组是否冻结或风险冻结
		if err := g.pipelineSvc.CheckGroupStatus(ctx, bundle.IsolationGroupID); err != nil {
			if errors.Is(err, pipeline.ErrGroupFrozen) || errors.Is(err, pipeline.ErrGroupRiskHold) {
				return nil, ErrGroupFrozen
			}
			return nil, fmt.Errorf("acquirer gateway: check group status: %w", err)
		}
	}

	// 获取 AcquirerClient（必须存在，registry 已确保）
	client, ok := bundle.Client.(domain.AcquirerClient)
	if !ok {
		return nil, errors.New("acquirer gateway: invalid client type")
	}

	// 构建 Authorize 请求
	authReq := domain.AuthorizeRequest{
		OrderID:            req.OrderID,
		MerchantID:         merchantUUID,
		Amount:             req.Amount,
		Currency:           req.Currency,
		BuyerIP:            req.BuyerIP,
		CardBIN:            req.CardBIN,
		PaymentMethodToken: req.PaymentMethodToken,
	}

	// Authorize
	authResp, err := client.Authorize(ctx, authReq)
	if err != nil {
		// 检查是否是超时错误
		if errors.Is(err, domain.ErrProviderTimeout) {
			return nil, ErrPaymentTimeout
		}
		if errors.Is(err, domain.ErrExplicitFailure) {
			return nil, ErrPaymentFailed
		}
		return nil, fmt.Errorf("acquirer gateway: authorize: %w", err)
	}

	// 根据响应状态处理
	switch authResp.Status {
	case domain.PaymentStatusAuthorized:
		// 成功，进行 Capture
		captureResp, err := client.Capture(ctx, domain.CaptureRequest{
			ProviderRef: authResp.ProviderRef,
			Amount:      req.Amount,
		})
		if err != nil {
			return nil, fmt.Errorf("acquirer gateway: capture: %w", err)
		}
		return &PaymentResult{
			TransactionID: authResp.TransactionID.String(),
			Status:        string(captureResp.Status),
			ProviderRef:   captureResp.ProviderRef,
		}, nil

	case domain.PaymentStatusFailed:
		return nil, ErrPaymentFailed

	default:
		return nil, fmt.Errorf("acquirer gateway: unexpected auth status: %s", authResp.Status)
	}
}

// channelIDFromOrder 从 OrderID 解析 channel ID
// 约定：order:<channel_code>:<id> 格式，或使用默认 channel
func (g *AcquirerGateway) channelIDFromOrder(orderID string) uuid.UUID {
	// 默认使用第一个 active channel
	if g.registry == nil {
		return uuid.Nil
	}

	channels := g.registry.ListAll()
	if len(channels) == 0 {
		return uuid.Nil
	}

	// 查找匹配的 channel
	for _, ch := range channels {
		if ch.IsActive {
			// 简单匹配：order 包含 channel code
			if len(orderID) > len(ch.ChannelCode) && orderID[:len(ch.ChannelCode)] == ch.ChannelCode {
				return ch.ChannelID
			}
		}
	}

	// 返回第一个 active channel
	return channels[0].ChannelID
}

// extractCardBIN 从卡号提取 BIN（前6位）
func extractCardBIN(cardNumber string) string {
	if len(cardNumber) < 6 {
		return ""
	}
	return cardNumber[:6]
}
