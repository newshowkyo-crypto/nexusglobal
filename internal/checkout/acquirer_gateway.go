package checkout

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/pipeline"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
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
	// P0-2: failClosedSvc 用于 ExecutionCheck（Provider dispatch 前）。
	// 若为 nil，回退到进程内缓存检查（IsSystemLockedDown）。
	failClosedSvc *failclosed.Service
}

// NewAcquirerGateway 创建基于 ChannelRegistry 的支付网关
func NewAcquirerGateway(reg *registry.ChannelRegistry, pipelineSvc *pipeline.Service, isReal bool) *AcquirerGateway {
	return &AcquirerGateway{
		registry:    reg,
		pipelineSvc: pipelineSvc,
		isReal:      isReal,
	}
}

// NewAcquirerGatewayWithFailClosed 创建带 Fail-Closed 服务注入的 AcquirerGateway（P0-2）。
//
// 生产环境应使用此构造函数，确保 Provider dispatch 前经过 ExecutionCheck。
// failClosedSvc != nil 时，Charge 会在调用 client.Authorize 前调用 ExecutionCheck(ctx)，
// 封锁或 DB 错误时返回 ErrSystemLockdownFailClosed。
func NewAcquirerGatewayWithFailClosed(reg *registry.ChannelRegistry, pipelineSvc *pipeline.Service, isReal bool, failClosedSvc *failclosed.Service) *AcquirerGateway {
	return &AcquirerGateway{
		registry:      reg,
		pipelineSvc:   pipelineSvc,
		isReal:        isReal,
		failClosedSvc: failClosedSvc,
	}
}

// SetFailClosedService 注入 Fail-Closed 服务（用于已构造的 AcquirerGateway）。
func (g *AcquirerGateway) SetFailClosedService(svc *failclosed.Service) {
	g.failClosedSvc = svc
}

// SetPipelineService 注入 Pipeline 隔离服务（P0-3）。
//
// 用于组合根在构造 AcquirerGateway 后注入 PipelineService，
// 确保 frozen/RISK_HOLD/DISABLED 隔离组能阻断实际路由。
// 不允许回退到 NoopRepository（G05 fail-closed）。
func (g *AcquirerGateway) SetPipelineService(svc *pipeline.Service) {
	g.pipelineSvc = svc
}

// PipelineService 返回注入的 Pipeline 服务（可能为 nil，用于测试）。
func (g *AcquirerGateway) PipelineService() *pipeline.Service {
	return g.pipelineSvc
}

// executionCheck 是 Provider dispatch 前的统一封锁检查（P0-2）。
// 若 failClosedSvc 为 nil，回退到进程内缓存检查（向后兼容）。
// 否则调用 ExecutionCheck(ctx)，封锁或 DB 错误时返回 ErrSystemLockdownFailClosed。
func (g *AcquirerGateway) executionCheck(ctx context.Context) error {
	if g.failClosedSvc != nil {
		return g.failClosedSvc.ExecutionCheck(ctx)
	}
	// 回退：仅查进程内缓存（旧路径）
	if failclosed.IsSystemLockedDown() {
		return ErrSystemLockdownFailClosed
	}
	return nil
}

// Charge 调用 AcquirerClient.Authorize -> Capture
//
// P0-3: 只接受明确 AcquirerChannelID。uuid.Nil/未找到/通道不属于merchant/币种不匹配/通道禁用/配置歧义
// 一律 fail-closed，绝不调用 Provider。
func (g *AcquirerGateway) Charge(ctx context.Context, req PaymentRequest) (*PaymentResult, error) {
	// P0-2: Provider dispatch 前强一致封锁检查。
	// ExecutionCheck 查询 DB 持久状态，DB 错误时 fail-closed。
	if err := g.executionCheck(ctx); err != nil {
		return nil, ErrSystemLockdownFailClosed
	}

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

	// P0-3: 只接受明确 AcquirerChannelID，uuid.Nil → fail-closed
	if req.AcquirerChannelID == uuid.Nil {
		return nil, ErrChannelNotFound
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
		// P0-3: 无 merchant ID → fail-closed，不再使用默认回退
		return nil, ErrMerchantIDInvalid
	}

	// 从 registry 获取 client bundle
	bundle, err := g.registry.Get(req.AcquirerChannelID)
	if err != nil {
		// P0-3: 通道未找到 → fail-closed
		return nil, ErrChannelNotFound
	}

	// 检查通道是否激活
	if !bundle.IsActive {
		return nil, ErrChannelDisabled
	}

	// P0-3: 校验 merchant 归属 — req.MerchantID 必须与 bundle 的 MerchantID 一致
	if bundle.MerchantID != merchantUUID {
		return nil, ErrChannelNotFound // fail-closed：跨商户错路由
	}

	// P0-3: 校验币种 — 通道必须支持请求的币种
	// 从 bundle.Config 检查 currencies 配置
	if bundle.Config != nil {
		if currencies, ok := bundle.Config["currencies"].([]interface{}); ok {
			currencyMatched := false
			for _, c := range currencies {
				if fmt.Sprintf("%v", c) == req.Currency {
					currencyMatched = true
					break
				}
			}
			if !currencyMatched {
				return nil, ErrChannelNotFound // fail-closed：币种不匹配
			}
		}
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
	// B3: 传递 ProviderIdempotencyKey 到底层 AcquirerClient
	authReq := domain.AuthorizeRequest{
		OrderID:            req.OrderID,
		MerchantID:         merchantUUID,
		ChannelID:          req.AcquirerChannelID,
		Amount:             req.Amount,
		Currency:           req.Currency,
		BuyerIP:            req.BuyerIP,
		CardBIN:            req.CardBIN,
		PaymentMethodToken: req.PaymentMethodToken,
		IdempotencyKey:     req.ProviderIdempotencyKey,
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
		// B3: Capture key 使用稳定派生值：provider_idempotency_key + ":capture"
		captureKey := req.ProviderIdempotencyKey + ":capture"
		captureResp, err := client.Capture(ctx, domain.CaptureRequest{
			ChannelID:      req.AcquirerChannelID,
			ProviderRef:    authResp.ProviderRef,
			Amount:         req.Amount,
			IdempotencyKey: captureKey,
		})
		if err != nil {
			return nil, fmt.Errorf("acquirer gateway: capture: %w", err)
		}
		// TransactionID 语义为渠道侧交易单号（Stripe pi_* / Nxtapay nx_tx_*），
		// 与 MockProvider 的 txn_* 一致；不是本系统内部 UUID（内部关联用 attempt ID）。
		return &PaymentResult{
			TransactionID: captureResp.ProviderRef,
			Status:        string(captureResp.Status),
			ProviderRef:   captureResp.ProviderRef,
		}, nil

	case domain.PaymentStatusFailed:
		return nil, ErrPaymentFailed

	default:
		return nil, fmt.Errorf("acquirer gateway: unexpected auth status: %s", authResp.Status)
	}
}

// extractCardBIN 从卡号提取 BIN（前6位）
func extractCardBIN(cardNumber string) string {
	if len(cardNumber) < 6 {
		return ""
	}
	return cardNumber[:6]
}
