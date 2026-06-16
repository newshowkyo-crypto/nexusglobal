package treasury

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type SweepService interface {
	ExecuteSweep(ctx context.Context, sweepID uuid.UUID) error
	ScheduleSweep(ctx context.Context, merchantID uuid.UUID, ruleID string) (*SweepScheduleResult, error)
	GetSweepStatus(ctx context.Context, sweepID uuid.UUID) (*SweepStatus, error)
}

type SweepScheduleResult struct {
	SweepID         uuid.UUID
	ScheduledAt     time.Time
	EstimatedAmount int64
	Status          string
}

type SweepStatus struct {
	SweepID       uuid.UUID
	Status        string
	AmountMinor   int64
	SourceTier    ProviderTier
	FundingMode   FundingMode
	BridgeBatchID string
	LedgerTxID    string
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type sweepService struct {
	router        VCCFundingRouter
	repo          SweepRepository
	fundingRouter VCCFundingRouter
}

type SweepRepository interface {
	CreateSweep(ctx context.Context, sweep Sweep) error
	UpdateSweepStatus(ctx context.Context, sweepID uuid.UUID, status string, ledgerTxID string, errorMsg string) error
	GetSweep(ctx context.Context, sweepID uuid.UUID) (*Sweep, error)
	ListPendingSweeps(ctx context.Context) ([]Sweep, error)
}

type Sweep struct {
	ID               uuid.UUID
	MerchantID       uuid.UUID
	SourceChannelID  uuid.UUID
	SourceTier       ProviderTier
	TargetProviderID uuid.UUID
	FundingMode      FundingMode
	AmountMinor      int64
	Currency         string
	Status           string
	BridgeBatchID    string
	LedgerTxID       string
	Error            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func NewSweepService(router VCCFundingRouter, repo SweepRepository) SweepService {
	return &sweepService{
		router:        router,
		repo:          repo,
		fundingRouter: router,
	}
}

func (s *sweepService) ExecuteSweep(ctx context.Context, sweepID uuid.UUID) error {
	sweep, err := s.repo.GetSweep(ctx, sweepID)
	if err != nil {
		return err
	}

	if sweep.Status != "PENDING" {
		return errors.New("sweep is not in pending state")
	}

	req := FundingRouteRequest{
		MerchantID:        sweep.MerchantID,
		SourceChannelID:   sweep.SourceChannelID,
		SourceTier:        sweep.SourceTier,
		TargetProviderID:  sweep.TargetProviderID,
		TargetFundingMode: sweep.FundingMode,
		AmountMinor:       sweep.AmountMinor,
		Currency:          sweep.Currency,
		SourceLedgerTxID:  sweep.LedgerTxID,
	}

	result, err := s.fundingRouter.RouteFunding(ctx, req)
	if err != nil {
		return s.repo.UpdateSweepStatus(ctx, sweepID, "FAILED", "", err.Error())
	}

	if !result.Approved {
		return s.repo.UpdateSweepStatus(ctx, sweepID, "REJECTED", "", result.RejectReason)
	}

	sweep.BridgeBatchID = result.BridgeBatchID
	sweep.Status = "COMPLETED"

	if err := s.repo.UpdateSweepStatus(ctx, sweepID, "COMPLETED", result.Provenance.SourceLedgerTxID, ""); err != nil {
		return err
	}

	return nil
}

func (s *sweepService) ScheduleSweep(ctx context.Context, merchantID uuid.UUID, ruleID string) (*SweepScheduleResult, error) {
	sweep := Sweep{
		ID:         uuid.New(),
		MerchantID: merchantID,
		Status:     "PENDING",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := s.repo.CreateSweep(ctx, sweep); err != nil {
		return nil, err
	}

	return &SweepScheduleResult{
		SweepID:     sweep.ID,
		ScheduledAt: sweep.CreatedAt,
		Status:      sweep.Status,
	}, nil
}

func (s *sweepService) GetSweepStatus(ctx context.Context, sweepID uuid.UUID) (*SweepStatus, error) {
	sweep, err := s.repo.GetSweep(ctx, sweepID)
	if err != nil {
		return nil, err
	}

	return &SweepStatus{
		SweepID:       sweep.ID,
		Status:        sweep.Status,
		AmountMinor:   sweep.AmountMinor,
		SourceTier:    sweep.SourceTier,
		FundingMode:   sweep.FundingMode,
		BridgeBatchID: sweep.BridgeBatchID,
		LedgerTxID:    sweep.LedgerTxID,
		Error:         sweep.Error,
		CreatedAt:     sweep.CreatedAt,
		UpdatedAt:     sweep.UpdatedAt,
	}, nil
}
