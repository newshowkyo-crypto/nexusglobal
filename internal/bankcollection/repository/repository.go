package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/bankcollection/domain"
)

// Repository 银行收款仓库接口
type Repository interface {
	// CreateInboundIntent 创建入金意图
	CreateInboundIntent(ctx context.Context, intent *domain.InboundIntent) error

	// GetInboundIntentByID 根据ID获取入金意图
	GetInboundIntentByID(ctx context.Context, id uuid.UUID) (*domain.InboundIntent, error)

	// GetInboundIntentByProviderRef 根据提供商引用获取入金意图
	GetInboundIntentByProviderRef(ctx context.Context, providerRef string) (*domain.InboundIntent, error)

	// GetInboundIntentByPaymentReference 根据支付引用获取入金意图
	GetInboundIntentByPaymentReference(ctx context.Context, paymentReference string) (*domain.InboundIntent, error)

	// UpdateInboundIntent 更新入金意图
	UpdateInboundIntent(ctx context.Context, intent *domain.InboundIntent) error

	// GetPendingIntents 获取待处理的入金意图
	GetPendingIntents(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.InboundIntent, error)

	// GetExpiredIntents 获取已过期的入金意图
	GetExpiredIntents(ctx context.Context, limit int) ([]domain.InboundIntent, error)

	// DeleteInboundIntent 删除入金意图
	DeleteInboundIntent(ctx context.Context, id uuid.UUID) error

	// GetInboundIntentsByTenant 获取租户的入金意图列表
	GetInboundIntentsByTenant(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]domain.InboundIntent, error)

	// GetInboundIntentCountByState 获取指定状态的入金意图数量
	GetInboundIntentCountByState(ctx context.Context, tenantID uuid.UUID, state domain.InboundIntentState) (int, error)

	// MarkAsReconcileMismatch 标记为对账不匹配
	MarkAsReconcileMismatch(ctx context.Context, intentID uuid.UUID, reason string) error

	// ConfirmIntent 确认入金意图
	ConfirmIntent(ctx context.Context, intentID uuid.UUID, receivedAmount string, transactionRef string) error
}
