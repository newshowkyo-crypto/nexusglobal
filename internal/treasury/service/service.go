package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/treasury/domain"
	"github.com/nexusglobal/mamr/internal/treasury/repository/db"
	"github.com/nexusglobal/mamr/internal/treasury/scheduler"
	"github.com/nexusglobal/mamr/pkg/external/bank"
)

type Repository interface {
	GetCalendarByName(ctx context.Context, name string) (*db.BankingCalendar, error)
	GetSweepRule(ctx context.Context, id uuid.UUID) (*db.SweepRule, error)
	InsertTransfer(ctx context.Context, params db.InsertSweepTransferParams) (*db.SweepTransfer, error)
	GetTransfer(ctx context.Context, id uuid.UUID) (*db.SweepTransfer, error)
	UpdateTransferStatus(ctx context.Context, id uuid.UUID, status string, version int32) error
	CountDailySweeps(ctx context.Context, ruleID uuid.UUID, since pgtype.Timestamptz) (int64, error)
}

type Service struct {
	repo    Repository
	bankCli bank.Client
	txm     *postgres.TxManager
	pool    *pgxpool.Pool
	delayQ  *scheduler.DelayQueue
	logger  *slog.Logger
}

func New(repo Repository, bankCli bank.Client, txm *postgres.TxManager, pool *pgxpool.Pool, delayQ *scheduler.DelayQueue, logger *slog.Logger) *Service {
	return &Service{
		repo:    repo,
		bankCli: bankCli,
		txm:     txm,
		pool:    pool,
		delayQ:  delayQ,
		logger:  logger,
	}
}

type EvaluateCmd struct {
	SweepRuleID uuid.UUID
	AsOf        time.Time
}

type EvaluateResult struct {
	Decision   domain.SweepDecision
	TransferID string
	Reason     string
}

func (s *Service) Evaluate(ctx context.Context, cmd EvaluateCmd) (EvaluateResult, error) {
	rule, err := s.repo.GetSweepRule(ctx, cmd.SweepRuleID)
	if err != nil {
		return EvaluateResult{}, err
	}
	if rule == nil {
		return EvaluateResult{}, apperr.ErrNotFound.WithMessage("sweep rule not found")
	}

	calendar, err := s.repo.GetCalendarByName(ctx, "US_FED")
	if err != nil || calendar == nil {
		return EvaluateResult{}, apperr.ErrNotFound.WithMessage("banking calendar not found")
	}

	withinWindow := domain.WithinBankingWindow(&domain.BankingCalendar{
		Timezone:     calendar.Timezone,
		BusinessDays: parseBusinessDays(calendar.BusinessDays),
		Holidays:     parseHolidays(calendar.Holidays),
		CutOffMatrix: parseCutOffMatrix(calendar.CutOffMatrix),
	}, rule.Rail, cmd.AsOf)

	dailyCount, _ := s.repo.CountDailySweeps(ctx, cmd.SweepRuleID, pgtype.Timestamptz{
		Time:  time.Date(cmd.AsOf.Year(), cmd.AsOf.Month(), cmd.AsOf.Day(), 0, 0, 0, 0, cmd.AsOf.Location()),
		Valid: true,
	})

	beneficiary := domain.EvaluateBeneficiary(&domain.SweepRule{
		BeneficiaryType:        domain.BeneficiaryType(rule.BeneficiaryType),
		SameNameVerified:       rule.SameNameVerified,
		ApprovalThresholdMinor: rule.ApprovalThresholdMinor,
	}, 0)

	if !beneficiary.Allowed {
		return EvaluateResult{
			Decision: domain.SweepReject,
			Reason:   beneficiary.RejectReason,
		}, nil
	}

	decision := domain.EvaluateSweepTrigger(domain.SweepTriggerInput{
		AccumulatedMinor: 0,
		DailySweepCount:  int(dailyCount),
		Rule: &domain.SweepRule{
			TriggerThresholdMinor: rule.TriggerThresholdMinor,
			MaxDailySweepCount:    int(rule.MaxDailySweepCount),
			DelayQueueEnabled:     rule.DelayQueueEnabled,
		},
		WithinWindow:     withinWindow,
		RequiresApproval: beneficiary.RequiresApproval,
	})

	if decision == domain.SweepDelayed {
		s.delayQ.Enqueue(ctx, cmd.SweepRuleID.String(), cmd.AsOf.Add(10*time.Minute))
		return EvaluateResult{
			Decision: decision,
			Reason:   "outside banking window, queued for later",
		}, nil
	}

	if decision == domain.SweepSubmit {
		transfer, err := s.repo.InsertTransfer(ctx, db.InsertSweepTransferParams{
			SweepRuleID:     cmd.SweepRuleID,
			SourceAccountID: rule.SourceBankAccountID,
			TargetAccountID: rule.TargetBankAccountID,
			AmountMinor:     rule.TriggerThresholdMinor,
			Currency:        rule.Currency,
			Rail:            rule.Rail,
			Status:          "Locked",
			IdempotencyKey:  "sweep:" + cmd.SweepRuleID.String() + ":" + cmd.AsOf.Format("20060102"),
		})
		if err != nil {
			return EvaluateResult{}, fmt.Errorf("insert transfer: %w", err)
		}
		return EvaluateResult{
			Decision:   decision,
			TransferID: transfer.ID.String(),
			Reason:     "threshold met, within window",
		}, nil
	}

	return EvaluateResult{
		Decision: decision,
		Reason:   string(decision),
	}, nil
}

func (s *Service) Submit(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := s.repo.GetTransfer(ctx, transferID)
	if err != nil {
		return err
	}
	if transfer == nil {
		return apperr.ErrNotFound.WithMessage("transfer not found")
	}

	if transfer.Status != "Locked" {
		return apperr.ErrIllegalTransition.WithMessage("transfer not in Locked status")
	}

	bankCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := s.bankCli.SubmitTransfer(bankCtx, bank.TransferRequest{
		SweepTransferID: transferID,
		SourceAccountID: transfer.SourceAccountID,
		TargetAccountID: transfer.TargetAccountID,
		Amount:          decimal.NewFromInt(transfer.AmountMinor),
		Currency:        transfer.Currency,
		Rail:            transfer.Rail,
	})

	if err != nil {
		if errors.Is(err, bank.ErrTimeout) {
			s.logger.Warn("sweep: bank timeout, setting UnknownExternalState",
				"transfer_id", transferID)
			return s.repo.UpdateTransferStatus(ctx, transferID, "UnknownExternalState", transfer.Version)
		}
		if errors.Is(err, bank.ErrExplicitFailure) {
			s.logger.Warn("sweep: bank failure",
				"transfer_id", transferID)
			return s.repo.UpdateTransferStatus(ctx, transferID, "Failed", transfer.Version)
		}
		return fmt.Errorf("sweep: bank.SubmitTransfer: %w", err)
	}

	s.logger.Info("sweep: submitted",
		"transfer_id", transferID, "bank_ref", result.BankRef)

	return s.repo.UpdateTransferStatus(ctx, transferID, "Submitted", transfer.Version)
}

func (s *Service) Reconcile(ctx context.Context, transferID uuid.UUID, bankRef string) error {
	transfer, err := s.repo.GetTransfer(ctx, transferID)
	if err != nil {
		return err
	}
	if transfer == nil {
		return apperr.ErrNotFound.WithMessage("transfer not found")
	}

	if transfer.Status == "Confirmed" || transfer.Status == "Reconciled" {
		return nil
	}

	if transfer.Status != "Submitted" && transfer.Status != "UnknownExternalState" {
		return apperr.ErrIllegalTransition.WithMessage("invalid status for reconcile")
	}

	return s.repo.UpdateTransferStatus(ctx, transferID, "Confirmed", transfer.Version)
}

func parseBusinessDays(b []byte) []string {
	var days []string
	json.Unmarshal(b, &days)
	return days
}

func parseHolidays(b []byte) []time.Time {
	var holidays []time.Time
	json.Unmarshal(b, &holidays)
	return holidays
}

func parseCutOffMatrix(b []byte) map[string]string {
	var m map[string]string
	json.Unmarshal(b, &m)
	return m
}
