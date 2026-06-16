package issuing

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type FraudGuard interface {
	CheckMerchantIssuingAllowed(ctx context.Context, merchantID uuid.UUID) error
	RecordAuthEvent(ctx context.Context, event CardAuthEvent) error
	CheckCardDeclineThreshold(ctx context.Context, cardID uuid.UUID) error
	CheckMerchantFraudRate(ctx context.Context, merchantID uuid.UUID) error
}

type fraudGuard struct {
	repo           FraudGuardRepository
	maxDeclines    int
	windowDuration time.Duration
	maxFraudBps    int
}

type FraudGuardRepository interface {
	GetMerchantRiskProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error)
	GetCardDeclineStats(ctx context.Context, cardID uuid.UUID, windowStart time.Time) (int, error)
	UpdateCardDeclineStats(ctx context.Context, cardID uuid.UUID, event CardAuthEvent) error
	UpdateMerchantFraudRate(ctx context.Context, merchantID uuid.UUID, event CardAuthEvent) error
}

type MerchantRiskProfile struct {
	MerchantID       uuid.UUID
	IsIssuingAllowed bool
	FraudRateBps     int
	LastUpdatedAt    time.Time
}

func NewFraudGuard(repo FraudGuardRepository, maxDeclines int, windowDuration time.Duration, maxFraudBps int) FraudGuard {
	return &fraudGuard{
		repo:           repo,
		maxDeclines:    maxDeclines,
		windowDuration: windowDuration,
		maxFraudBps:    maxFraudBps,
	}
}

func (f *fraudGuard) CheckMerchantIssuingAllowed(ctx context.Context, merchantID uuid.UUID) error {
	profile, err := f.repo.GetMerchantRiskProfile(ctx, merchantID)
	if err != nil {
		return err
	}
	if !profile.IsIssuingAllowed {
		return ErrMerchantIssuingNotAllowed
	}
	return nil
}

func (f *fraudGuard) RecordAuthEvent(ctx context.Context, event CardAuthEvent) error {
	if err := f.repo.UpdateCardDeclineStats(ctx, event.CardID, event); err != nil {
		return err
	}
	if err := f.repo.UpdateMerchantFraudRate(ctx, event.MerchantID, event); err != nil {
		return err
	}
	return nil
}

func (f *fraudGuard) CheckCardDeclineThreshold(ctx context.Context, cardID uuid.UUID) error {
	windowStart := time.Now().Add(-f.windowDuration)
	count, err := f.repo.GetCardDeclineStats(ctx, cardID, windowStart)
	if err != nil {
		return err
	}
	if count >= f.maxDeclines {
		return ErrCardDeclinedSlidingWindowExceeded
	}
	return nil
}

func (f *fraudGuard) CheckMerchantFraudRate(ctx context.Context, merchantID uuid.UUID) error {
	profile, err := f.repo.GetMerchantRiskProfile(ctx, merchantID)
	if err != nil {
		return err
	}
	if profile.FraudRateBps > f.maxFraudBps {
		return ErrMerchantFraudRateExceeded
	}
	return nil
}
