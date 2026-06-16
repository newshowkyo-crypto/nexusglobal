package riskprofile

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Repository 商户风险配置仓储接口
type Repository interface {
	// GetByMerchantID 根据商户ID获取风险配置
	GetByMerchantID(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error)

	// Create 创建风险配置
	Create(ctx context.Context, profile *MerchantRiskProfile) error

	// Update 更新风险配置
	Update(ctx context.Context, profile *MerchantRiskProfile) error

	// CreateReserveEntry 创建准备金条目
	CreateReserveEntry(ctx context.Context, entry *RollingReserveEntry) error

	// GetReserveEntryByID 根据ID获取准备金条目
	GetReserveEntryByID(ctx context.Context, id uuid.UUID) (*RollingReserveEntry, error)

	// GetReadyToRelease 获取到期可释放的准备金
	GetReadyToRelease(ctx context.Context, now time.Time) ([]*RollingReserveEntry, error)

	// UpdateReserveStatus 更新准备金状态
	UpdateReserveStatus(ctx context.Context, id uuid.UUID, status ReserveStatus) error

	// GetReservesByMerchant 获取商户的准备金列表
	GetReservesByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*RollingReserveEntry, error)
}
