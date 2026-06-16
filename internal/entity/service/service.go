package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/entity/domain"
	"github.com/nexusglobal/mamr/internal/entity/repository"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

// Service 法人实体服务
type Service struct {
	repo repository.Repository
	txm  *postgres.TxManager
}

// New 创建法人实体服务实例
func New(repo repository.Repository, txm *postgres.TxManager) *Service {
	return &Service{
		repo: repo,
		txm:  txm,
	}
}

// CreateCorporateEntityRequest 创建法人实体请求
type CreateCorporateEntityRequest struct {
	TenantID     uuid.UUID
	Name         string
	LegalName    string
	TaxID        string
	Country      string
	Jurisdiction string
	Metadata     map[string]any
}

// CreateCorporateEntity 创建法人实体
func (s *Service) CreateCorporateEntity(ctx context.Context, req CreateCorporateEntityRequest) (*domain.CorporateEntity, error) {
	var entity *domain.CorporateEntity
	err := s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		entity = &domain.CorporateEntity{
			ID:           uuid.New(),
			TenantID:     req.TenantID,
			Name:         req.Name,
			LegalName:    req.LegalName,
			TaxID:        req.TaxID,
			Country:      req.Country,
			Jurisdiction: req.Jurisdiction,
			Status:       domain.EntityStatusActive,
			Metadata:     req.Metadata,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}

		if err := s.repo.CreateCorporateEntity(ctx, tx, entity); err != nil {
			return fmt.Errorf("entity: create corporate entity failed: %w", err)
		}

		// 创建默认风险配置
		profile := &domain.EntityRiskProfile{
			ID:                         uuid.New(),
			CorporateEntityID:          entity.ID,
			MaxDailyTransactionAmount:  decimal.NewFromInt(1000000),
			MaxSingleTransactionAmount: decimal.NewFromInt(100000),
			RiskScore:                  0,
			CreatedAt:                  time.Now(),
			UpdatedAt:                  time.Now(),
		}
		if err := s.repo.CreateEntityRiskProfile(ctx, tx, profile); err != nil {
			return fmt.Errorf("entity: create risk profile failed: %w", err)
		}

		return nil
	})

	return entity, err
}

// GetCorporateEntityByID 获取法人实体
func (s *Service) GetCorporateEntityByID(ctx context.Context, id uuid.UUID) (*domain.CorporateEntity, error) {
	entity, err := s.repo.GetCorporateEntityByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("entity: get corporate entity failed: %w", err)
	}
	return entity, nil
}

// ValidateEntityLimit 验证实体限额
func (s *Service) ValidateEntityLimit(ctx context.Context, corporateEntityID uuid.UUID, assetCode string, amount decimal.Decimal, isInflow bool) error {
	entity, err := s.repo.GetCorporateEntityByID(ctx, corporateEntityID)
	if err != nil {
		return fmt.Errorf("entity: get corporate entity failed: %w", err)
	}

	if entity.Status != domain.EntityStatusActive {
		return domain.ErrEntityNotActive
	}

	limit, err := s.repo.GetEntityAssetLimit(ctx, corporateEntityID, assetCode)
	if err != nil {
		return fmt.Errorf("entity: get asset limit failed: %w", err)
	}

	if limit == nil {
		return nil // 没有配置限额，允许通过
	}

	// 检查单笔交易限额（从风险配置获取）
	profile, err := s.repo.GetEntityRiskProfile(ctx, corporateEntityID)
	if err != nil {
		return fmt.Errorf("entity: get risk profile failed: %w", err)
	}

	if profile != nil && amount.GreaterThan(profile.MaxSingleTransactionAmount) {
		return domain.ErrInsufficientLimit
	}

	// 检查每日流水限额
	flow, err := s.repo.GetDailyFlow(ctx, corporateEntityID, assetCode)
	if err != nil {
		return fmt.Errorf("entity: get daily flow failed: %w", err)
	}

	if isInflow {
		if flow.TotalInflow.Add(amount).GreaterThan(limit.MaxDailyInflow) {
			return domain.ErrInsufficientLimit
		}
	} else {
		if flow.TotalOutflow.Add(amount).GreaterThan(limit.MaxDailyOutflow) {
			return domain.ErrInsufficientLimit
		}
	}

	return nil
}

// UpdateDailyFlow 更新每日流水
func (s *Service) UpdateDailyFlow(ctx context.Context, corporateEntityID uuid.UUID, assetCode string, inflow, outflow decimal.Decimal) error {
	err := s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return s.repo.UpdateDailyFlow(ctx, tx, corporateEntityID, assetCode, inflow, outflow)
	})
	if err != nil {
		return fmt.Errorf("entity: update daily flow failed: %w", err)
	}
	return nil
}
