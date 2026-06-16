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
	"github.com/nexusglobal/mamr/internal/cryptopay/domain"
	"github.com/nexusglobal/mamr/internal/cryptopay/repository"
)

// CryptoPayService 加密货币支付服务
type CryptoPayService struct {
	repo     repository.Repository
	registry *registry.UniversalAdapterRegistry
}

// NewCryptoPayService 创建加密货币支付服务
func NewCryptoPayService(repo repository.Repository, registry *registry.UniversalAdapterRegistry) *CryptoPayService {
	return &CryptoPayService{
		repo:     repo,
		registry: registry,
	}
}

// CreateDepositIntentRequest 创建存款意图请求
type CreateDepositIntentRequest struct {
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID
	MerchantID        *uuid.UUID
	OrderID           string
	AssetCode         string
	Network           string
	Amount            decimal.Decimal
	ChannelID         uuid.UUID
}

// CreateDepositIntentResponse 创建存款意图响应
type CreateDepositIntentResponse struct {
	IntentID              uuid.UUID
	ProviderRef           string
	State                 domain.DepositState
	DepositAddress        string
	MemoTag               string
	Network               string
	ConfirmationsRequired int
	ExpiresAt             time.Time
}

// CreateDepositIntent 创建存款意图
// 非白名单 asset/network 拒绝
func (s *CryptoPayService) CreateDepositIntent(ctx context.Context, req CreateDepositIntentRequest) (*CreateDepositIntentResponse, error) {
	// 获取适配器 bundle
	bundle, err := s.registry.Get(req.ChannelID)
	if err != nil {
		return nil, fmt.Errorf("crypto pay: get adapter failed: %w", err)
	}

	// 检查适配器类型
	if bundle.AdapterType != adapterdomain.AdapterTypeCryptoPay {
		return nil, errors.New("crypto pay: invalid adapter type")
	}

	// 检查资产支持（白名单验证）
	assetSupported := false
	for _, asset := range bundle.SupportedAssets {
		if asset == req.AssetCode {
			assetSupported = true
			break
		}
	}
	if !assetSupported {
		return nil, domain.ErrAssetNotSupported
	}

	// 检查网络支持（白名单验证）
	networkSupported := false
	for _, network := range bundle.SupportedNetworks {
		if network == req.Network {
			networkSupported = true
			break
		}
	}
	if !networkSupported {
		return nil, domain.ErrNetworkNotSupported
	}

	// 获取入金适配器
	inboundAdapter, ok := bundle.Client.(adapterdomain.InboundAdapter)
	if !ok {
		return nil, errors.New("crypto pay: adapter does not support inbound operations")
	}

	// 创建入金意图请求（不在 DB transaction 内）
	adapterReq := adapterdomain.InboundIntentRequest{
		OrderID:        req.OrderID,
		TenantID:       req.TenantID,
		MerchantID:     req.MerchantID,
		AssetCode:      req.AssetCode,
		Network:        req.Network,
		Amount:         req.Amount,
		IdempotencyKey: req.OrderID + "-crypto",
	}

	// 调用外部适配器（不在 DB transaction 内）
	adapterResp, err := inboundAdapter.CreateInboundIntent(ctx, adapterReq)
	if err != nil {
		if errors.Is(err, adapterdomain.ErrUnknownExternalState) {
			return nil, domain.ErrUnknownExternalState
		}
		if errors.Is(err, adapterdomain.ErrAssetNotSupported) {
			return nil, domain.ErrAssetNotSupported
		}
		if errors.Is(err, adapterdomain.ErrNetworkNotSupported) {
			return nil, domain.ErrNetworkNotSupported
		}
		return nil, fmt.Errorf("crypto pay: create deposit intent failed: %w", err)
	}

	// 解析网络类型
	networkType := domain.NetworkType(req.Network)

	// 创建存款意图记录
	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		TenantID:              req.TenantID,
		CorporateEntityID:     req.CorporateEntityID,
		MerchantID:            req.MerchantID,
		OrderID:               req.OrderID,
		AssetCode:             req.AssetCode,
		Network:               networkType,
		ExpectedAmount:        req.Amount,
		DepositAddress:        "",
		MemoTag:               "",
		ConfirmationsRequired: 6, // 默认需要 6 个确认
		ProviderRef:           adapterResp.ProviderRef,
		State:                 domain.DepositStateCreated,
		ExpiresAt:             time.Now().Add(24 * time.Hour),
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}

	// 如果返回了存款地址信息
	if adapterResp.DepositAddress != nil {
		intent.DepositAddress = adapterResp.DepositAddress.Address
		intent.MemoTag = adapterResp.DepositAddress.MemoTag
		if adapterResp.DepositAddress.ConfirmationsRequired > 0 {
			intent.ConfirmationsRequired = adapterResp.DepositAddress.ConfirmationsRequired
		}
	}

	// 保存到数据库
	if err := s.repo.CreateDepositIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("crypto pay: save intent failed: %w", err)
	}

	return &CreateDepositIntentResponse{
		IntentID:              intent.ID,
		ProviderRef:           adapterResp.ProviderRef,
		State:                 intent.State,
		DepositAddress:        intent.DepositAddress,
		MemoTag:               intent.MemoTag,
		Network:               req.Network,
		ConfirmationsRequired: intent.ConfirmationsRequired,
		ExpiresAt:             intent.ExpiresAt,
	}, nil
}

// QueryDepositIntentRequest 查询存款意图请求
type QueryDepositIntentRequest struct {
	IntentID    uuid.UUID
	ProviderRef string
	Address     string
}

// QueryDepositIntent 查询存款意图状态
func (s *CryptoPayService) QueryDepositIntent(ctx context.Context, req QueryDepositIntentRequest) (*domain.DepositIntent, error) {
	var intent *domain.DepositIntent
	var err error

	if req.IntentID != uuid.Nil {
		intent, err = s.repo.GetDepositIntentByID(ctx, req.IntentID)
	} else if req.ProviderRef != "" {
		intent, err = s.repo.GetDepositIntentByProviderRef(ctx, req.ProviderRef)
	} else if req.Address != "" {
		intent, err = s.repo.GetDepositIntentByAddress(ctx, req.Address)
	} else {
		return nil, errors.New("crypto pay: either intent_id, provider_ref, or address is required")
	}

	if err != nil {
		if errors.Is(err, domain.ErrDepositIntentNotFound) {
			return nil, domain.ErrDepositIntentNotFound
		}
		return nil, fmt.Errorf("crypto pay: query intent failed: %w", err)
	}

	return intent, nil
}

// HandleChainTransactionRequest 处理链上交易请求
type HandleChainTransactionRequest struct {
	TransactionHash string
	BlockHash       string
	BlockNumber     int64
	FromAddress     string
	ToAddress       string
	Amount          decimal.Decimal
	AssetCode       string
	Network         string
	Confirmations   int
	MemoTag         string
	Timestamp       time.Time
}

// HandleChainTransaction 处理链上交易
// confirmations 不足不得确认
// wrong network / amount mismatch / memo mismatch 不得确认
func (s *CryptoPayService) HandleChainTransaction(ctx context.Context, req HandleChainTransactionRequest) error {
	// 根据收款地址查找存款意图
	intent, err := s.repo.GetDepositIntentByAddress(ctx, req.ToAddress)
	if err != nil {
		if errors.Is(err, domain.ErrDepositIntentNotFound) {
			return fmt.Errorf("crypto pay: no intent found for address %s", req.ToAddress)
		}
		return fmt.Errorf("crypto pay: get intent failed: %w", err)
	}

	// 检查意图状态
	if intent.State.IsTerminalState() {
		return errors.New("crypto pay: intent is already in terminal state")
	}

	// 检查是否过期
	if time.Now().After(intent.ExpiresAt) {
		intent.State = domain.DepositStateExpired
		if err := s.repo.UpdateDepositIntent(ctx, intent); err != nil {
			return fmt.Errorf("crypto pay: update intent state failed: %w", err)
		}
		return domain.ErrIntentExpired
	}

	// 检查网络匹配
	if string(intent.Network) != req.Network {
		intent.State = domain.DepositStateReconcileMismatch
		intent.LastError = "network mismatch"
		if err := s.repo.UpdateDepositIntent(ctx, intent); err != nil {
			return fmt.Errorf("crypto pay: update intent to mismatch failed: %w", err)
		}
		return domain.ErrNetworkMismatch
	}

	// 检查 memo/tag 匹配（如果有）
	if intent.MemoTag != "" && req.MemoTag != intent.MemoTag {
		intent.State = domain.DepositStateReconcileMismatch
		intent.LastError = "memo/tag mismatch"
		if err := s.repo.UpdateDepositIntent(ctx, intent); err != nil {
			return fmt.Errorf("crypto pay: update intent to mismatch failed: %w", err)
		}
		return domain.ErrMemoMismatch
	}

	// 检查金额匹配（允许小额差异）
	allowedDiff := intent.ExpectedAmount.Mul(decimal.NewFromFloat(0.001))
	diff := req.Amount.Sub(intent.ExpectedAmount).Abs()
	if diff.GreaterThan(allowedDiff) {
		intent.State = domain.DepositStateReconcileMismatch
		intent.LastError = "amount mismatch"
		if err := s.repo.UpdateDepositIntent(ctx, intent); err != nil {
			return fmt.Errorf("crypto pay: update intent to mismatch failed: %w", err)
		}
		return domain.ErrAmountMismatch
	}

	// 更新确认数
	intent.Confirmations = req.Confirmations
	intent.ReceivedAmount = req.Amount

	// 检查确认数是否足够
	if req.Confirmations >= intent.ConfirmationsRequired {
		// 确认数足够，完成存款
		intent.State = domain.DepositStateConfirmed
		intent.ConfirmedAmount = req.Amount
		intent.TransactionRef = req.TransactionHash
	} else {
		// 确认数不足，继续等待
		intent.State = domain.DepositStateConfirming
	}

	if err := s.repo.UpdateDepositIntent(ctx, intent); err != nil {
		return fmt.Errorf("crypto pay: update intent state failed: %w", err)
	}

	// 创建链上交易记录
	tx := &domain.ChainTransaction{
		ID:              uuid.New(),
		TenantID:        intent.TenantID,
		IntentID:        intent.ID,
		TransactionHash: req.TransactionHash,
		BlockHash:       req.BlockHash,
		BlockNumber:     req.BlockNumber,
		FromAddress:     req.FromAddress,
		ToAddress:       req.ToAddress,
		Amount:          req.Amount,
		AssetCode:       req.AssetCode,
		Network:         domain.NetworkType(req.Network),
		Confirmations:   req.Confirmations,
		Status:          "PENDING",
		MemoTag:         req.MemoTag,
		Timestamp:       req.Timestamp,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if req.Confirmations >= intent.ConfirmationsRequired {
		tx.Status = "CONFIRMED"
	}

	if err := s.repo.CreateChainTransaction(ctx, tx); err != nil {
		return fmt.Errorf("crypto pay: create chain transaction failed: %w", err)
	}

	return nil
}

// GetPendingDepositIntents 获取待处理的存款意图（供监听器使用）
func (s *CryptoPayService) GetPendingDepositIntents(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.DepositIntent, error) {
	return s.repo.GetPendingDepositIntents(ctx, tenantID, limit)
}

// UpdateDepositIntent 更新存款意图（供监听器使用）
func (s *CryptoPayService) UpdateDepositIntent(ctx context.Context, intent *domain.DepositIntent) error {
	return s.repo.UpdateDepositIntent(ctx, intent)
}

// GetDepositIntentByProviderRef 根据提供商引用获取存款意图（供监听器使用）
func (s *CryptoPayService) GetDepositIntentByProviderRef(ctx context.Context, providerRef string) (*domain.DepositIntent, error) {
	return s.repo.GetDepositIntentByProviderRef(ctx, providerRef)
}
func (s *CryptoPayService) ExpireDepositIntents(ctx context.Context) error {
	intents, err := s.repo.GetExpiredDepositIntents(ctx, 100)
	if err != nil {
		return fmt.Errorf("crypto pay: get expired intents failed: %w", err)
	}

	for _, intent := range intents {
		if intent.State.IsTerminalState() {
			continue
		}

		intent.State = domain.DepositStateExpired
		if err := s.repo.UpdateDepositIntent(ctx, &intent); err != nil {
			return fmt.Errorf("crypto pay: expire intent %s failed: %w", intent.ID, err)
		}
	}

	return nil
}
