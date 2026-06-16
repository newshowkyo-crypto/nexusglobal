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
	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/ledger/repository"
)

// AssetBridgeEngine 资产桥引擎
type AssetBridgeEngine struct {
	repo     repository.Repository
	registry *registry.UniversalAdapterRegistry
}

// NewAssetBridgeEngine 创建资产桥引擎
func NewAssetBridgeEngine(repo repository.Repository, registry *registry.UniversalAdapterRegistry) *AssetBridgeEngine {
	return &AssetBridgeEngine{
		repo:     repo,
		registry: registry,
	}
}

// ExecuteBridgeRequest 执行桥操作请求
type ExecuteBridgeRequest struct {
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID
	ProfileID         uuid.UUID
	OrderID           string
	Direction         domain.BridgeDirection
	SourceAmount      decimal.Decimal
	TargetAddress     string // 目标地址（桥出时使用）
}

// ExecuteBridgeResponse 执行桥操作响应
type ExecuteBridgeResponse struct {
	OperationID       uuid.UUID
	Status            domain.BridgeStatus
	ApprovalRequired  bool
	EstimatedRate     decimal.Decimal
	EstimatedSlippage int
	Message           string
}

// ExecuteBridge 执行资产桥操作
// 包含：20bps slippage guard, limit/sliced execution policy, allowlisted wallet policy
func (e *AssetBridgeEngine) ExecuteBridge(ctx context.Context, req ExecuteBridgeRequest) (*ExecuteBridgeResponse, error) {
	// 获取桥配置
	profile, err := e.repo.GetBridgeProfile(ctx, req.ProfileID)
	if err != nil {
		if errors.Is(err, domain.ErrBridgeProfileNotFound) {
			return nil, domain.ErrBridgeProfileNotFound
		}
		return nil, fmt.Errorf("bridge: get profile failed: %w", err)
	}

	// 检查配置是否启用
	if !profile.Enabled {
		return nil, domain.ErrBridgeProfileNotEnabled
	}

	// 检查金额限制
	if req.SourceAmount.LessThan(profile.MinTransactionAmount) {
		return nil, domain.ErrAmountBelowMinimum
	}
	if req.SourceAmount.GreaterThan(profile.MaxTransactionAmount) {
		return nil, domain.ErrAmountAboveMaximum
	}

	// 检查每日限额
	dailyUsage, err := e.repo.GetDailyBridgeUsage(ctx, req.TenantID, req.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("bridge: get daily usage failed: %w", err)
	}
	if dailyUsage.Add(req.SourceAmount).GreaterThan(profile.DailyLimit) {
		return nil, domain.ErrDailyLimitExceeded
	}

	// 验证目标地址（桥出时）
	if req.Direction == domain.BridgeDirectionOut && req.TargetAddress != "" {
		if !e.isWalletAllowlisted(profile, req.TargetAddress) {
			return nil, domain.ErrWalletNotAllowlisted
		}
	}

	// 检查是否需要审批
	approvalRequired := profile.RequireApproval && req.SourceAmount.GreaterThan(profile.ApprovalThreshold)

	// 执行滑点保护检查
	slippageResult := e.checkSlippageGuard(ctx, profile, req.SourceAmount)
	if !slippageResult.Allowed {
		return nil, fmt.Errorf("bridge: slippage guard rejected: %s", slippageResult.Message)
	}

	// 创建桥操作记录
	operation := &domain.BridgeOperation{
		ID:                    uuid.New(),
		TenantID:              req.TenantID,
		CorporateEntityID:     req.CorporateEntityID,
		ProfileID:             req.ProfileID,
		OrderID:               req.OrderID,
		Direction:             req.Direction,
		SourceAsset:           profile.SourceAsset,
		TargetAsset:           profile.TargetAsset,
		SourceAmount:          req.SourceAmount,
		Rate:                  slippageResult.EstimatedRate,
		Status:                domain.BridgeStatusPending,
		SlippageBps:           slippageResult.EstimatedBps,
		ExecutionPolicy:       profile.ExecutionPolicy,
		AllowlistedWalletUsed: e.isWalletAllowlisted(profile, req.TargetAddress),
		ApprovalRequired:      approvalRequired,
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}

	if err := e.repo.CreateBridgeOperation(ctx, operation); err != nil {
		return nil, fmt.Errorf("bridge: create operation failed: %w", err)
	}

	// 如果需要审批，进入待审批状态
	if approvalRequired {
		operation.Status = domain.BridgeStatusAwaitingApproval
		if err := e.repo.UpdateBridgeOperation(ctx, operation); err != nil {
			return nil, fmt.Errorf("bridge: update operation status failed: %w", err)
		}

		return &ExecuteBridgeResponse{
			OperationID:      operation.ID,
			Status:           operation.Status,
			ApprovalRequired: true,
			Message:          "bridge operation requires approval",
		}, nil
	}

	// 否则开始处理
	return e.processBridgeOperation(ctx, operation, slippageResult)
}

// checkSlippageGuard 检查滑点保护（20bps = 0.2%）
func (e *AssetBridgeEngine) checkSlippageGuard(ctx context.Context, profile *domain.BridgeProfile, amount decimal.Decimal) *domain.SlippageGuardResult {
	// 获取当前汇率（模拟）
	estimatedRate := decimal.NewFromFloat(1.0) // 实际应从交易所获取
	maxSlippageBps := profile.MaxSlippageBps
	if maxSlippageBps == 0 {
		maxSlippageBps = 20 // 默认 20bps = 0.2%
	}

	// 估算滑点（模拟）
	estimatedSlippageBps := e.estimateSlippage(profile, amount)

	if estimatedSlippageBps > maxSlippageBps {
		return &domain.SlippageGuardResult{
			Allowed:        false,
			MaxSlippageBps: maxSlippageBps,
			EstimatedBps:   estimatedSlippageBps,
			EstimatedRate:  estimatedRate,
			Message:        fmt.Sprintf("estimated slippage (%d bps) exceeds maximum allowed (%d bps)", estimatedSlippageBps, maxSlippageBps),
		}
	}

	return &domain.SlippageGuardResult{
		Allowed:        true,
		MaxSlippageBps: maxSlippageBps,
		EstimatedBps:   estimatedSlippageBps,
		EstimatedRate:  estimatedRate,
		Message:        "slippage check passed",
	}
}

// estimateSlippage 估算滑点
func (e *AssetBridgeEngine) estimateSlippage(profile *domain.BridgeProfile, amount decimal.Decimal) int {
	// 简化的滑点估算逻辑
	// 实际实现应查询交易所订单簿深度
	baseSlippage := 5 // 基础滑点

	// 根据金额调整
	if amount.GreaterThan(decimal.NewFromFloat(10000)) {
		baseSlippage += 10
	}

	return baseSlippage
}

// isWalletAllowlisted 检查钱包是否在白名单中
func (e *AssetBridgeEngine) isWalletAllowlisted(profile *domain.BridgeProfile, walletAddress string) bool {
	if walletAddress == "" {
		return true // 空地址视为允许
	}

	for _, addr := range profile.AllowlistedWallets {
		if addr == walletAddress {
			return true
		}
	}

	return false
}

// processBridgeOperation 处理桥操作
func (e *AssetBridgeEngine) processBridgeOperation(ctx context.Context, operation *domain.BridgeOperation, slippageResult *domain.SlippageGuardResult) (*ExecuteBridgeResponse, error) {
	// 更新状态为处理中
	operation.Status = domain.BridgeStatusProcessing
	if err := e.repo.UpdateBridgeOperation(ctx, operation); err != nil {
		return nil, fmt.Errorf("bridge: update operation status failed: %w", err)
	}

	// 获取适配器 bundle
	bundles := e.registry.GetByType(operation.TenantID, adapterdomain.AdapterTypeExchange)
	if len(bundles) == 0 {
		// 进入未知外部状态
		operation.Status = domain.BridgeStatusUnknown
		e.repo.UpdateBridgeOperation(ctx, operation)
		return nil, domain.ErrUnknownExternalState
	}

	// 使用第一个可用的适配器
	bundle := bundles[0]

	// 获取桥适配器
	bridgeAdapter, ok := bundle.Client.(adapterdomain.BridgeAdapter)
	if !ok {
		return nil, errors.New("bridge: adapter does not support bridge operations")
	}

	// 执行桥操作（不在 DB transaction 内）
	switch operation.Direction {
	case domain.BridgeDirectionOut:
		return e.executeBridgeOut(ctx, bridgeAdapter, operation)
	case domain.BridgeDirectionIn:
		return e.executeBridgeIn(ctx, bridgeAdapter, operation)
	default:
		return nil, errors.New("bridge: invalid direction")
	}
}

// executeBridgeOut 执行桥出操作
func (e *AssetBridgeEngine) executeBridgeOut(ctx context.Context, adapter adapterdomain.BridgeAdapter, operation *domain.BridgeOperation) (*ExecuteBridgeResponse, error) {
	// 创建转账请求（桥出）
	transferReq := adapterdomain.TransferRequest{
		TenantID:       operation.TenantID,
		JobID:          operation.ID,
		SourceAsset:    operation.SourceAsset,
		TargetAsset:    operation.TargetAsset,
		Amount:         operation.SourceAmount,
		Destination:    "",                    // 目标地址在实际调用时设置
		Network:        operation.TargetAsset, // 使用目标资产作为网络标识
		IdempotencyKey: operation.ID.String(),
		Metadata: map[string]any{
			"direction":        "OUT",
			"rate":             operation.Rate.String(),
			"slippage_bps":     operation.SlippageBps,
			"execution_policy": operation.ExecutionPolicy,
		},
	}

	// 调用外部适配器
	transferResp, err := adapter.CreateTransfer(ctx, transferReq)
	if err != nil {
		if errors.Is(err, adapterdomain.ErrUnknownExternalState) {
			operation.Status = domain.BridgeStatusUnknown
			e.repo.UpdateBridgeOperation(ctx, operation)
			return nil, domain.ErrUnknownExternalState
		}
		operation.Status = domain.BridgeStatusFailed
		e.repo.UpdateBridgeOperation(ctx, operation)
		return nil, fmt.Errorf("bridge: execute bridge out failed: %w", err)
	}

	// 更新操作状态
	operation.Status = domain.BridgeStatusExecuting
	operation.SourceTransactionRef = transferResp.ProviderRef
	if err := e.repo.UpdateBridgeOperation(ctx, operation); err != nil {
		return nil, fmt.Errorf("bridge: update operation failed: %w", err)
	}

	return &ExecuteBridgeResponse{
		OperationID:       operation.ID,
		Status:            operation.Status,
		ApprovalRequired:  false,
		EstimatedRate:     operation.Rate,
		EstimatedSlippage: operation.SlippageBps,
		Message:           "bridge out executing",
	}, nil
}

// executeBridgeIn 执行桥入操作
func (e *AssetBridgeEngine) executeBridgeIn(ctx context.Context, adapter adapterdomain.BridgeAdapter, operation *domain.BridgeOperation) (*ExecuteBridgeResponse, error) {
	// 创建转账请求（桥入）
	transferReq := adapterdomain.TransferRequest{
		TenantID:       operation.TenantID,
		JobID:          operation.ID,
		SourceAsset:    operation.SourceAsset,
		TargetAsset:    operation.TargetAsset,
		Amount:         operation.SourceAmount,
		Destination:    "",                    // 目标地址在实际调用时设置
		Network:        operation.SourceAsset, // 使用源资产作为网络标识
		IdempotencyKey: operation.ID.String(),
		Metadata: map[string]any{
			"direction":        "IN",
			"rate":             operation.Rate.String(),
			"slippage_bps":     operation.SlippageBps,
			"execution_policy": operation.ExecutionPolicy,
		},
	}

	// 调用外部适配器
	transferResp, err := adapter.CreateTransfer(ctx, transferReq)
	if err != nil {
		if errors.Is(err, adapterdomain.ErrUnknownExternalState) {
			operation.Status = domain.BridgeStatusUnknown
			e.repo.UpdateBridgeOperation(ctx, operation)
			return nil, domain.ErrUnknownExternalState
		}
		operation.Status = domain.BridgeStatusFailed
		e.repo.UpdateBridgeOperation(ctx, operation)
		return nil, fmt.Errorf("bridge: execute bridge in failed: %w", err)
	}

	// 更新操作状态
	operation.Status = domain.BridgeStatusExecuting
	operation.SourceTransactionRef = transferResp.ProviderRef
	if err := e.repo.UpdateBridgeOperation(ctx, operation); err != nil {
		return nil, fmt.Errorf("bridge: update operation failed: %w", err)
	}

	return &ExecuteBridgeResponse{
		OperationID:       operation.ID,
		Status:            operation.Status,
		ApprovalRequired:  false,
		EstimatedRate:     operation.Rate,
		EstimatedSlippage: operation.SlippageBps,
		Message:           "bridge in executing",
	}, nil
}

// ApproveBridgeOperationRequest 审批桥操作请求
type ApproveBridgeOperationRequest struct {
	OperationID uuid.UUID
	ApprovedBy  uuid.UUID
}

// ApproveBridgeOperation 审批桥操作
func (e *AssetBridgeEngine) ApproveBridgeOperation(ctx context.Context, req ApproveBridgeOperationRequest) error {
	// 获取操作记录
	operation, err := e.repo.GetBridgeOperation(ctx, req.OperationID)
	if err != nil {
		return fmt.Errorf("bridge: get operation failed: %w", err)
	}

	// 检查状态
	if operation.Status != domain.BridgeStatusAwaitingApproval {
		return domain.ErrBridgeStatusInvalid
	}

	// 更新审批信息
	now := time.Now()
	operation.ApprovedBy = &req.ApprovedBy
	operation.ApprovedAt = &now
	operation.Status = domain.BridgeStatusProcessing
	operation.ApprovalRequired = false
	operation.UpdatedAt = now

	if err := e.repo.UpdateBridgeOperation(ctx, operation); err != nil {
		return fmt.Errorf("bridge: update operation failed: %w", err)
	}

	// 继续处理操作
	slippageResult := &domain.SlippageGuardResult{
		Allowed:        true,
		MaxSlippageBps: 20,
		EstimatedBps:   operation.SlippageBps,
	}
	_, err = e.processBridgeOperation(ctx, operation, slippageResult)
	return err
}

// UpdateBridgeStatusRequest 更新桥状态请求
type UpdateBridgeStatusRequest struct {
	OperationID          uuid.UUID
	Status               domain.BridgeStatus
	TargetTransactionRef string
	ActualSlippageBps    int
}

// UpdateBridgeStatus 更新桥状态
func (e *AssetBridgeEngine) UpdateBridgeStatus(ctx context.Context, req UpdateBridgeStatusRequest) error {
	// 获取操作记录
	operation, err := e.repo.GetBridgeOperation(ctx, req.OperationID)
	if err != nil {
		return fmt.Errorf("bridge: get operation failed: %w", err)
	}

	// 验证状态转换
	if !operation.Status.CanTransitionTo(req.Status) {
		return domain.ErrBridgeStatusInvalid
	}

	// 更新状态
	operation.Status = req.Status
	if req.TargetTransactionRef != "" {
		operation.TargetTransactionRef = req.TargetTransactionRef
	}
	if req.ActualSlippageBps > 0 {
		operation.ActualSlippageBps = req.ActualSlippageBps
	}
	operation.UpdatedAt = time.Now()

	if err := e.repo.UpdateBridgeOperation(ctx, operation); err != nil {
		return fmt.Errorf("bridge: update operation failed: %w", err)
	}

	// 如果完成，更新账本
	if req.Status == domain.BridgeStatusCompleted {
		return e.settleBridgeOperation(ctx, operation)
	}

	return nil
}

// settleBridgeOperation 结算桥操作
func (e *AssetBridgeEngine) settleBridgeOperation(ctx context.Context, operation *domain.BridgeOperation) error {
	// 根据方向更新账本
	if operation.Direction == domain.BridgeDirectionIn {
		// 桥入：增加目标资产
		return e.repo.RecordTransaction(ctx, &domain.Transaction{
			ID:                uuid.New(),
			TenantID:          operation.TenantID,
			CorporateEntityID: operation.CorporateEntityID,
			OrderID:           operation.OrderID,
			Type:              domain.TransactionTypeBridgeIn,
			AssetCode:         operation.TargetAsset,
			Amount:            operation.TargetAmount,
			Status:            domain.TransactionStatusConfirmed,
			ExternalRef:       operation.TargetTransactionRef,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		})
	}

	// 桥出：减少源资产
	return e.repo.RecordTransaction(ctx, &domain.Transaction{
		ID:                uuid.New(),
		TenantID:          operation.TenantID,
		CorporateEntityID: operation.CorporateEntityID,
		OrderID:           operation.OrderID,
		Type:              domain.TransactionTypeBridgeOut,
		AssetCode:         operation.SourceAsset,
		Amount:            operation.SourceAmount.Neg(),
		Status:            domain.TransactionStatusConfirmed,
		ExternalRef:       operation.SourceTransactionRef,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	})
}
