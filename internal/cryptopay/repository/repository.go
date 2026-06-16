package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/cryptopay/domain"
)

// Repository 加密货币支付仓库接口
type Repository interface {
	// CreateDepositIntent 创建存款意图
	CreateDepositIntent(ctx context.Context, intent *domain.DepositIntent) error

	// GetDepositIntentByID 根据ID获取存款意图
	GetDepositIntentByID(ctx context.Context, id uuid.UUID) (*domain.DepositIntent, error)

	// GetDepositIntentByProviderRef 根据提供商引用获取存款意图
	GetDepositIntentByProviderRef(ctx context.Context, providerRef string) (*domain.DepositIntent, error)

	// GetDepositIntentByAddress 根据存款地址获取存款意图
	GetDepositIntentByAddress(ctx context.Context, address string) (*domain.DepositIntent, error)

	// UpdateDepositIntent 更新存款意图
	UpdateDepositIntent(ctx context.Context, intent *domain.DepositIntent) error

	// GetPendingDepositIntents 获取待处理的存款意图
	GetPendingDepositIntents(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.DepositIntent, error)

	// GetExpiredDepositIntents 获取已过期的存款意图
	GetExpiredDepositIntents(ctx context.Context, limit int) ([]domain.DepositIntent, error)

	// DeleteDepositIntent 删除存款意图
	DeleteDepositIntent(ctx context.Context, id uuid.UUID) error

	// GetDepositIntentsByTenant 获取租户的存款意图列表
	GetDepositIntentsByTenant(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]domain.DepositIntent, error)

	// GetDepositIntentCountByState 获取指定状态的存款意图数量
	GetDepositIntentCountByState(ctx context.Context, tenantID uuid.UUID, state domain.DepositState) (int, error)

	// MarkAsReconcileMismatch 标记为对账不匹配
	MarkAsReconcileMismatch(ctx context.Context, intentID uuid.UUID, reason string) error

	// ConfirmDepositIntent 确认存款意图
	ConfirmDepositIntent(ctx context.Context, intentID uuid.UUID, receivedAmount string, confirmations int, transactionRef string) error

	// CreateAddressPoolEntry 创建地址池条目
	CreateAddressPoolEntry(ctx context.Context, entry *domain.DepositAddressPool) error

	// GetAvailableAddress 获取可用地址
	GetAvailableAddress(ctx context.Context, tenantID uuid.UUID, assetCode string, network domain.NetworkType) (*domain.DepositAddressPool, error)

	// AllocateAddress 分配地址
	AllocateAddress(ctx context.Context, addressID uuid.UUID, intentID uuid.UUID) error

	// ReleaseAddress 释放地址
	ReleaseAddress(ctx context.Context, addressID uuid.UUID) error

	// MarkAddressUsed 标记地址已使用
	MarkAddressUsed(ctx context.Context, addressID uuid.UUID) error

	// CreateChainTransaction 创建链上交易记录
	CreateChainTransaction(ctx context.Context, tx *domain.ChainTransaction) error

	// GetChainTransactionByHash 根据交易哈希获取交易
	GetChainTransactionByHash(ctx context.Context, txHash string) (*domain.ChainTransaction, error)

	// UpdateChainTransaction 更新链上交易
	UpdateChainTransaction(ctx context.Context, tx *domain.ChainTransaction) error

	// GetUnconfirmedTransactions 获取未确认的交易
	GetUnconfirmedTransactions(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.ChainTransaction, error)
}
