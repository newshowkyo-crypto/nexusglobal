package riskprofile

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Service 商户风险配置服务
type Service struct {
	repo  Repository
	clock func() time.Time
}

// NewService 创建服务实例
func NewService(repo Repository, clock func() time.Time) *Service {
	return &Service{
		repo:  repo,
		clock: clock,
	}
}

// GetRiskProfile 获取商户风险配置
func (s *Service) GetRiskProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error) {
	return s.repo.GetByMerchantID(ctx, merchantID)
}

// CreateRiskProfile 创建商户风险配置
func (s *Service) CreateRiskProfile(ctx context.Context, merchantID uuid.UUID, rollingReserveRateBps, disputeReserveRateBps, releaseDays int) error {
	profile := &MerchantRiskProfile{
		ID:                    uuid.New(),
		MerchantID:            merchantID,
		RollingReserveRateBps: rollingReserveRateBps,
		DisputeReserveRateBps: disputeReserveRateBps,
		ReserveReleaseDays:    releaseDays,
		Status:                RiskProfileStatusActive,
		CreatedAt:             s.clock(),
		UpdatedAt:             s.clock(),
	}
	return s.repo.Create(ctx, profile)
}

// SplitCaptureAmount 拆分capture金额到准备金和可结算部分
func (s *Service) SplitCaptureAmount(ctx context.Context, merchantID uuid.UUID, totalAmount int64) (reserveAmount, settledAmount int64, err error) {
	profile, err := s.repo.GetByMerchantID(ctx, merchantID)
	if err != nil {
		return 0, 0, err
	}

	if !profile.IsActive() {
		return 0, totalAmount, nil
	}

	// 计算滚动准备金
	rollingReserve := decimal.NewFromInt(totalAmount).
		Mul(decimal.NewFromInt(int64(profile.RollingReserveRateBps))).
		Div(decimal.NewFromInt(10000))

	// 计算争议准备金
	disputeReserve := decimal.NewFromInt(totalAmount).
		Mul(decimal.NewFromInt(int64(profile.DisputeReserveRateBps))).
		Div(decimal.NewFromInt(10000))

	totalReserve := rollingReserve.Add(disputeReserve)
	settled := decimal.NewFromInt(totalAmount).Sub(totalReserve)

	return totalReserve.IntPart(), settled.IntPart(), nil
}

// CreateReserveEntries 创建准备金条目
func (s *Service) CreateReserveEntries(ctx context.Context, merchantID, transactionID uuid.UUID, totalAmount int64) error {
	profile, err := s.repo.GetByMerchantID(ctx, merchantID)
	if err != nil {
		return err
	}

	if !profile.IsActive() {
		return nil
	}

	releaseDate := s.clock().Add(time.Duration(profile.ReserveReleaseDays) * 24 * time.Hour)

	// 创建滚动准备金条目
	rollingAmount := decimal.NewFromInt(totalAmount).
		Mul(decimal.NewFromInt(int64(profile.RollingReserveRateBps))).
		Div(decimal.NewFromInt(10000)).IntPart()

	if rollingAmount > 0 {
		err = s.repo.CreateReserveEntry(ctx, &RollingReserveEntry{
			ID:            uuid.New(),
			MerchantID:    merchantID,
			TransactionID: transactionID,
			Amount:        rollingAmount,
			ReserveType:   ReserveTypeRolling,
			ReleaseDate:   releaseDate,
			Status:        ReserveStatusHeld,
			CreatedAt:     s.clock(),
			UpdatedAt:     s.clock(),
		})
		if err != nil {
			return err
		}
	}

	// 创建争议准备金条目
	disputeAmount := decimal.NewFromInt(totalAmount).
		Mul(decimal.NewFromInt(int64(profile.DisputeReserveRateBps))).
		Div(decimal.NewFromInt(10000)).IntPart()

	if disputeAmount > 0 {
		err = s.repo.CreateReserveEntry(ctx, &RollingReserveEntry{
			ID:            uuid.New(),
			MerchantID:    merchantID,
			TransactionID: transactionID,
			Amount:        disputeAmount,
			ReserveType:   ReserveTypeDispute,
			ReleaseDate:   releaseDate,
			Status:        ReserveStatusHeld,
			CreatedAt:     s.clock(),
			UpdatedAt:     s.clock(),
		})
	}

	return err
}

// ReleaseReserve 释放准备金
func (s *Service) ReleaseReserve(ctx context.Context, id uuid.UUID, isRiskHold bool) error {
	if isRiskHold {
		return ErrReserveInRiskHold
	}

	entry, err := s.repo.GetReserveEntryByID(ctx, id)
	if err != nil {
		return err
	}

	if entry.Status != ReserveStatusHeld {
		return ErrReserveAlreadyReleased
	}

	if s.clock().Before(entry.ReleaseDate) {
		return ErrReserveNotReady
	}

	return s.repo.UpdateReserveStatus(ctx, id, ReserveStatusReleased)
}

// GetReadyToRelease 获取到期可释放的准备金
func (s *Service) GetReadyToRelease(ctx context.Context) ([]*RollingReserveEntry, error) {
	return s.repo.GetReadyToRelease(ctx, s.clock())
}

// IsActive 检查配置是否活跃
func (p *MerchantRiskProfile) IsActive() bool {
	return p.Status == RiskProfileStatusActive
}

// IsInRiskHold 检查商户是否处于风险冻结状态
func (p *MerchantRiskProfile) IsInRiskHold() bool {
	return p.Status == RiskProfileStatusRiskHold
}
