package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	adapterdomain "github.com/nexusglobal/mamr/internal/adapter/domain"
	"github.com/nexusglobal/mamr/internal/adapter/registry"
	"github.com/nexusglobal/mamr/internal/bankcollection/domain"
	"github.com/nexusglobal/mamr/internal/bankcollection/repository"
)

// BankCollectionService 银行收款服务
type BankCollectionService struct {
	repo     repository.Repository
	registry *registry.UniversalAdapterRegistry
}

// NewBankCollectionService 创建银行收款服务
func NewBankCollectionService(repo repository.Repository, registry *registry.UniversalAdapterRegistry) *BankCollectionService {
	return &BankCollectionService{
		repo:     repo,
		registry: registry,
	}
}

// CreateInboundIntentRequest 创建入金意图请求
type CreateInboundIntentRequest struct {
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID
	MerchantID        *uuid.UUID
	OrderID           string
	AssetCode         string
	Amount            decimal.Decimal
	BuyerRef          string
	ChannelID         uuid.UUID
}

// CreateInboundIntentResponse 创建入金意图响应
type CreateInboundIntentResponse struct {
	IntentID         uuid.UUID
	ProviderRef      string
	State            domain.InboundIntentState
	VirtualAccount   *domain.VirtualAccountAllocation
	ExpiresAt        time.Time
	PaymentReference string
}

// CreateInboundIntent 创建入金意图
// 外部银行 API 调用不在 DB transaction 内
func (s *BankCollectionService) CreateInboundIntent(ctx context.Context, req CreateInboundIntentRequest) (*CreateInboundIntentResponse, error) {
	// 获取适配器 bundle
	bundle, err := s.registry.Get(req.ChannelID)
	if err != nil {
		return nil, fmt.Errorf("bank collection: get adapter failed: %w", err)
	}

	// 检查适配器类型
	if bundle.AdapterType != adapterdomain.AdapterTypeBankCollection {
		return nil, errors.New("bank collection: invalid adapter type")
	}

	// 检查资产支持
	assetSupported := false
	for _, asset := range bundle.SupportedAssets {
		if asset == req.AssetCode {
			assetSupported = true
			break
		}
	}
	if !assetSupported {
		return nil, errors.New("bank collection: asset not supported")
	}

	// 获取入金适配器
	inboundAdapter, ok := bundle.Client.(adapterdomain.InboundAdapter)
	if !ok {
		return nil, errors.New("bank collection: adapter does not support inbound operations")
	}

	// 生成唯一支付引用
	paymentReference := generatePaymentReference()

	// 创建入金意图请求（不在 DB transaction 内）
	adapterReq := adapterdomain.InboundIntentRequest{
		OrderID:        req.OrderID,
		TenantID:       req.TenantID,
		MerchantID:     req.MerchantID,
		AssetCode:      req.AssetCode,
		Amount:         req.Amount,
		BuyerRef:       req.BuyerRef,
		IdempotencyKey: req.OrderID + "-" + paymentReference,
		Metadata:       map[string]any{"payment_reference": paymentReference},
	}

	// 调用外部适配器（不在 DB transaction 内）
	adapterResp, err := inboundAdapter.CreateInboundIntent(ctx, adapterReq)
	if err != nil {
		if errors.Is(err, adapterdomain.ErrUnknownExternalState) {
			return nil, domain.ErrUnknownExternalState
		}
		if errors.Is(err, adapterdomain.ErrApprovalRequired) {
			return nil, domain.ErrProviderUnavailable
		}
		return nil, fmt.Errorf("bank collection: create inbound intent failed: %w", err)
	}

	// 创建入金意图记录
	intent := &domain.InboundIntent{
		ID:                 uuid.New(),
		TenantID:           req.TenantID,
		CorporateEntityID:  req.CorporateEntityID,
		MerchantID:         req.MerchantID,
		OrderID:            req.OrderID,
		AssetCode:          req.AssetCode,
		ExpectedAmount:     req.Amount,
		BuyerRef:           req.BuyerRef,
		PaymentReference:   paymentReference,
		VirtualAccountType: domain.VirtualAccountTypeDynamic,
		ProviderRef:        adapterResp.ProviderRef,
		State:              domain.IntentStateCreated,
		ExpiresAt:          time.Now().Add(24 * time.Hour),
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	// 保存到数据库
	if err := s.repo.CreateInboundIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("bank collection: save intent failed: %w", err)
	}

	// 构建响应
	resp := &CreateInboundIntentResponse{
		IntentID:         intent.ID,
		ProviderRef:      adapterResp.ProviderRef,
		State:            intent.State,
		ExpiresAt:        intent.ExpiresAt,
		PaymentReference: paymentReference,
	}

	// 如果有虚拟账户信息
	if adapterResp.VirtualAccount != nil {
		resp.VirtualAccount = &domain.VirtualAccountAllocation{
			AccountNumber:    adapterResp.VirtualAccount.AccountNumber,
			RoutingNumber:    adapterResp.VirtualAccount.RoutingNumber,
			IBAN:             adapterResp.VirtualAccount.IBAN,
			BankName:         adapterResp.VirtualAccount.BankName,
			BankAddress:      adapterResp.VirtualAccount.BankAddress,
			PaymentReference: adapterResp.VirtualAccount.PaymentReference,
		}
	}

	return resp, nil
}

// QueryInboundIntentRequest 查询入金意图请求
type QueryInboundIntentRequest struct {
	IntentID    uuid.UUID
	ProviderRef string
}

// QueryInboundIntent 查询入金意图状态
func (s *BankCollectionService) QueryInboundIntent(ctx context.Context, req QueryInboundIntentRequest) (*domain.InboundIntent, error) {
	var intent *domain.InboundIntent
	var err error

	if req.IntentID != uuid.Nil {
		intent, err = s.repo.GetInboundIntentByID(ctx, req.IntentID)
	} else if req.ProviderRef != "" {
		intent, err = s.repo.GetInboundIntentByProviderRef(ctx, req.ProviderRef)
	} else {
		return nil, errors.New("bank collection: either intent_id or provider_ref is required")
	}

	if err != nil {
		if errors.Is(err, domain.ErrIntentNotFound) {
			return nil, domain.ErrIntentNotFound
		}
		return nil, fmt.Errorf("bank collection: query intent failed: %w", err)
	}

	return intent, nil
}

// VerifyInboundWebhookRequest 验证 webhook 请求
type VerifyInboundWebhookRequest struct {
	ChannelID uuid.UUID
	Payload   []byte
	Signature string
	Timestamp time.Time
}

// VerifyInboundWebhook 验证入金 webhook
// 验签失败不得入账
func (s *BankCollectionService) VerifyInboundWebhook(ctx context.Context, req VerifyInboundWebhookRequest) (*domain.InboundWebhookPayload, error) {
	// 获取适配器 bundle
	bundle, err := s.registry.Get(req.ChannelID)
	if err != nil {
		return nil, fmt.Errorf("bank collection: get adapter failed: %w", err)
	}

	// 获取入金适配器
	inboundAdapter, ok := bundle.Client.(adapterdomain.InboundAdapter)
	if !ok {
		return nil, errors.New("bank collection: adapter does not support inbound operations")
	}

	// 验证 webhook 签名（外部调用不在 DB transaction 内）
	event, err := inboundAdapter.VerifyInboundWebhook(ctx, req.Payload, req.Signature)
	if err != nil {
		if errors.Is(err, adapterdomain.ErrWebhookSignatureInvalid) {
			return nil, domain.ErrSignatureInvalid
		}
		return nil, fmt.Errorf("bank collection: webhook verification failed: %w", err)
	}

	// 解析 webhook payload
	payload := &domain.InboundWebhookPayload{
		EventID:          event.EventID,
		EventType:        event.EventType,
		ProviderRef:      event.ProviderRef,
		Amount:           event.Amount,
		AssetCode:        event.AssetCode,
		TransactionRef:   event.TransactionRef,
		Timestamp:        event.Timestamp,
		RawPayload:       event.RawPayload,
		PaymentReference: event.TransactionRef, // 使用 transaction_ref 作为支付引用
	}

	return payload, nil
}

// HandleWebhookEventRequest 处理 webhook 事件请求
type HandleWebhookEventRequest struct {
	Payload *domain.InboundWebhookPayload
}

// HandleWebhookEvent 处理 webhook 事件
// 金额/币种/reference 不匹配必须进入 RECONCILE_MISMATCH
func (s *BankCollectionService) HandleWebhookEvent(ctx context.Context, req HandleWebhookEventRequest) error {
	payload := req.Payload

	// 根据支付引用查找入金意图
	intent, err := s.repo.GetInboundIntentByPaymentReference(ctx, payload.PaymentReference)
	if err != nil {
		if errors.Is(err, domain.ErrIntentNotFound) {
			// 无法找到对应的入金意图，记录日志并返回
			return fmt.Errorf("bank collection: no intent found for reference %s", payload.PaymentReference)
		}
		return fmt.Errorf("bank collection: get intent failed: %w", err)
	}

	// 检查意图状态
	if intent.State.IsTerminalState() {
		return errors.New("bank collection: intent is already in terminal state")
	}

	// 检查是否过期
	if time.Now().After(intent.ExpiresAt) {
		intent.State = domain.IntentStateExpired
		if err := s.repo.UpdateInboundIntent(ctx, intent); err != nil {
			return fmt.Errorf("bank collection: update intent state failed: %w", err)
		}
		return domain.ErrIntentExpired
	}

	// 执行对账
	reconciliation := s.reconcileIntent(intent, payload)
	if !reconciliation.Matched {
		// 对账不匹配，进入 RECONCILE_MISMATCH 状态
		intent.State = domain.IntentStateReconcileMismatch
		intent.LastError = fmt.Sprintf("reconciliation mismatch: %s", reconciliation.MismatchType)
		if err := s.repo.UpdateInboundIntent(ctx, intent); err != nil {
			return fmt.Errorf("bank collection: update intent to mismatch failed: %w", err)
		}
		return domain.ErrReconciliationFailed
	}

	// 对账成功，更新状态
	intent.ReceivedAmount = intent.ReceivedAmount.Add(payload.Amount)

	if intent.ReceivedAmount.GreaterThanOrEqual(intent.ExpectedAmount) {
		// 全额到账
		intent.State = domain.IntentStateConfirmed
		intent.ConfirmedAmount = intent.ExpectedAmount
	} else {
		// 部分到账
		intent.State = domain.IntentStatePartiallyMatched
	}

	if err := s.repo.UpdateInboundIntent(ctx, intent); err != nil {
		return fmt.Errorf("bank collection: update intent state failed: %w", err)
	}

	return nil
}

// reconcileIntent 执行对账
func (s *BankCollectionService) reconcileIntent(intent *domain.InboundIntent, payload *domain.InboundWebhookPayload) domain.ReconciliationResult {
	result := domain.ReconciliationResult{
		IntentID:          intent.ID,
		ExpectedAmount:    intent.ExpectedAmount,
		ExpectedReference: intent.PaymentReference,
		ReceivedAmount:    payload.Amount,
		ReceivedReference: payload.PaymentReference,
	}

	// 检查引用匹配
	if payload.PaymentReference != intent.PaymentReference {
		result.Matched = false
		result.MismatchType = "REFERENCE_MISMATCH"
		return result
	}

	// 检查币种匹配
	if payload.AssetCode != intent.AssetCode {
		result.Matched = false
		result.MismatchType = "CURRENCY_MISMATCH"
		return result
	}

	// 检查金额（允许小额差异，如银行费用）
	// 差异在 0.1% 以内视为匹配
	allowedDiff := intent.ExpectedAmount.Mul(decimal.NewFromFloat(0.001))
	diff := payload.Amount.Sub(intent.ExpectedAmount).Abs()
	if diff.GreaterThan(allowedDiff) {
		result.Matched = false
		result.MismatchType = "AMOUNT_MISMATCH"
		return result
	}

	result.Matched = true
	return result
}

// ExpireIntents 处理过期意图
func (s *BankCollectionService) ExpireIntents(ctx context.Context) error {
	intents, err := s.repo.GetExpiredIntents(ctx, 100)
	if err != nil {
		return fmt.Errorf("bank collection: get expired intents failed: %w", err)
	}

	for _, intent := range intents {
		if intent.State.IsTerminalState() {
			continue
		}

		intent.State = domain.IntentStateExpired
		if err := s.repo.UpdateInboundIntent(ctx, &intent); err != nil {
			return fmt.Errorf("bank collection: expire intent %s failed: %w", intent.ID, err)
		}
	}

	return nil
}

// generatePaymentReference 生成唯一支付引用
func generatePaymentReference() string {
	return fmt.Sprintf("REF-%d-%s", time.Now().Unix(), uuid.New().String()[:8])
}
