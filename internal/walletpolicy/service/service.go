package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/walletpolicy/domain"
	"github.com/nexusglobal/mamr/internal/walletpolicy/repository"
)

type Service struct {
	repo repository.Repository
}

func New(repo repository.Repository) *Service {
	return &Service{repo: repo}
}

type WithdrawalValidationCmd struct {
	MerchantID    uuid.UUID
	Network       domain.Network
	AssetCode     string
	Address       string
	AmountMinor   int64
	TxHash        string
	Confirmations int
}

func (s *Service) ValidateWithdrawal(ctx context.Context, cmd WithdrawalValidationCmd) error {
	policy, err := s.repo.GetPolicy(ctx, cmd.MerchantID, cmd.Network, cmd.AssetCode)
	if err != nil {
		return err
	}

	if err := policy.ValidateNetwork(cmd.Network); err != nil {
		return err
	}

	if policy.AllowListEnabled {
		allowlisted, err := s.repo.CheckAllowlisted(ctx, cmd.MerchantID, cmd.Network, cmd.Address)
		if err != nil {
			return err
		}
		if !allowlisted {
			return domain.ErrAddressNotAllowlisted
		}
	}

	if err := policy.ValidateSingleLimit(cmd.AmountMinor); err != nil {
		return err
	}

	dailyTotal, err := s.repo.GetDailyWithdrawalTotal(ctx, cmd.MerchantID, cmd.Network, cmd.AssetCode)
	if err != nil {
		return err
	}

	if err := policy.ValidateDailyLimit(dailyTotal, cmd.AmountMinor); err != nil {
		return err
	}

	if cmd.TxHash != "" {
		exists, err := s.repo.CheckTxHashExists(ctx, cmd.TxHash)
		if err != nil {
			return err
		}
		if exists {
			return domain.ErrDuplicateTxHash
		}
	}

	return nil
}

func (s *Service) FinalizeWithdrawal(ctx context.Context, cmd WithdrawalValidationCmd) error {
	policy, err := s.repo.GetPolicy(ctx, cmd.MerchantID, cmd.Network, cmd.AssetCode)
	if err != nil {
		return err
	}

	if err := policy.ValidateConfirmations(cmd.Confirmations); err != nil {
		return err
	}

	exists, err := s.repo.CheckTxHashExists(ctx, cmd.TxHash)
	if err != nil {
		return err
	}
	if exists {
		return domain.ErrDuplicateTxHash
	}

	req := &domain.WithdrawalRequest{
		ID:            uuid.New(),
		MerchantID:    cmd.MerchantID,
		Network:       cmd.Network,
		AssetCode:     cmd.AssetCode,
		Address:       cmd.Address,
		AmountMinor:   cmd.AmountMinor,
		TxHash:        cmd.TxHash,
		Confirmations: cmd.Confirmations,
		Status:        "COMPLETED",
		SubmittedAt:   time.Now(),
	}

	return s.repo.RecordWithdrawal(ctx, req)
}
