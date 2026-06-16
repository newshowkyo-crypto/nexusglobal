package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/entity/domain"
)

// Repository 法人实体仓储接口
type Repository interface {
	CreateCorporateEntity(ctx context.Context, tx pgx.Tx, entity *domain.CorporateEntity) error
	GetCorporateEntityByID(ctx context.Context, id uuid.UUID) (*domain.CorporateEntity, error)
	GetCorporateEntitiesByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.CorporateEntity, error)
	UpdateCorporateEntity(ctx context.Context, tx pgx.Tx, entity *domain.CorporateEntity) error
	CreateEntityRiskProfile(ctx context.Context, tx pgx.Tx, profile *domain.EntityRiskProfile) error
	GetEntityRiskProfile(ctx context.Context, corporateEntityID uuid.UUID) (*domain.EntityRiskProfile, error)
	UpdateEntityRiskProfile(ctx context.Context, tx pgx.Tx, profile *domain.EntityRiskProfile) error
	CreateEntityAssetLimit(ctx context.Context, tx pgx.Tx, limit *domain.EntityAssetLimit) error
	GetEntityAssetLimit(ctx context.Context, corporateEntityID uuid.UUID, assetCode string) (*domain.EntityAssetLimit, error)
	UpdateEntityAssetLimit(ctx context.Context, tx pgx.Tx, limit *domain.EntityAssetLimit) error
	UpdateDailyFlow(ctx context.Context, tx pgx.Tx, corporateEntityID uuid.UUID, assetCode string, inflow, outflow decimal.Decimal) error
	GetDailyFlow(ctx context.Context, corporateEntityID uuid.UUID, assetCode string) (*DailyFlow, error)
}

// DailyFlow 每日流水
type DailyFlow struct {
	TotalInflow  decimal.Decimal
	TotalOutflow decimal.Decimal
}
