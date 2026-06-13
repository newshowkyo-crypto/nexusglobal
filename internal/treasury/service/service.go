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
	ListSweepRules(ctx context.Context) ([]db.SweepRule, error)
	InsertTransfer(ctx context.Context, params db.InsertSweepTransferParams) (*db.SweepTransfer, error)
	GetTransfer(ctx context.Context, id uuid.UUID) (*db.SweepTransfer, error)
	UpdateTransferStatus(ctx context.Context, id uuid.UUID, status string, version int32) error
	CountDailySweeps(ctx context.Context, ruleID uuid.UUID, since pgtype.Timestamptz) (int64, error)
	GetBankAccountBalance(ctx context.Context, bankAccountID uuid.UUID, currency string) (int64, error)
	DecrementBankAccountBalance(ctx context.Context, bankAccountID uuid.UUID, currency string, amount int64) error
	InsertSweepApproval(ctx context.Context, params db.InsertSweepApprovalParams) (*db.SweepApproval, error)
	GetSweepApproval(ctx context.Context, id uuid.UUID) (*db.SweepApproval, error)
	ApproveSweepApproval(ctx context.Context, id uuid.UUID) error
	ListPendingSweepApprovals(ctx context.Context, ruleID uuid.UUID) ([]db.SweepApproval, error)
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

	accumulated, err := s.repo.GetBankAccountBalance(ctx, rule.SourceBankAccountID, rule.Currency)
	if err != nil {
		return EvaluateResult{}, fmt.Errorf("get bank balance: %w", err)
	}

	var sweepAmount int64
	if accumulated > 0 && rule.SweepRatioBps > 0 {
		ratio := decimal.NewFromInt(int64(rule.SweepRatioBps)).Div(decimal.NewFromInt(10000))
		calculated := decimal.NewFromInt(accumulated).Mul(ratio).Floor()
		maxSweep := decimal.NewFromInt(rule.MaxSingleSweepMinor)
		if maxSweep.IsZero() {
			maxSweep = calculated
		}
		if calculated.GreaterThan(maxSweep) {
			calculated = maxSweep
		}
		remaining := decimal.NewFromInt(accumulated).Sub(calculated)
		minResidual := decimal.NewFromInt(rule.MinResidualMinor)
		if remaining.LessThan(minResidual) {
			calculated = decimal.NewFromInt(accumulated).Sub(minResidual)
			if calculated.LessThanOrEqual(decimal.Zero) {
				sweepAmount = 0
			} else {
				sweepAmount = calculated.IntPart()
			}
		} else {
			sweepAmount = calculated.IntPart()
		}
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
	}, sweepAmount)

	if !beneficiary.Allowed {
		return EvaluateResult{
			Decision: domain.SweepReject,
			Reason:   beneficiary.RejectReason,
		}, nil
	}

	decision := domain.EvaluateSweepTrigger(domain.SweepTriggerInput{
		NetSweepAmount:  sweepAmount, // T3-1: 使用净额
		DailySweepCount: int(dailyCount),
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
		// T3-1: 净额校验已移至 EvaluateSweepTrigger，此处只做最终确认
		if sweepAmount <= 0 {
			return EvaluateResult{
				Decision: domain.SweepAccumulate,
				Reason:   "calculated sweep amount is zero or negative",
			}, nil
		}

		transfer, err := s.repo.InsertTransfer(ctx, db.InsertSweepTransferParams{
			SweepRuleID:     cmd.SweepRuleID,
			SourceAccountID: rule.SourceBankAccountID,
			TargetAccountID: rule.TargetBankAccountID,
			AmountMinor:     sweepAmount,
			Currency:        rule.Currency,
			Rail:            rule.Rail,
			Status:          "Locked",
			IdempotencyKey:  "sweep:" + cmd.SweepRuleID.String() + ":" + cmd.AsOf.Format("20060102"),
		})
		if err != nil {
			return EvaluateResult{}, fmt.Errorf("insert transfer: %w", err)
		}

		// G10: 扣减源账户余额
		if err := s.repo.DecrementBankAccountBalance(ctx, rule.SourceBankAccountID, rule.Currency, sweepAmount); err != nil {
			return EvaluateResult{}, fmt.Errorf("decrement source balance: %w", err)
		}

		return EvaluateResult{
			Decision:   decision,
			TransferID: transfer.ID.String(),
			Reason:     fmt.Sprintf("threshold met, sweep amount=%d from accumulated=%d", sweepAmount, accumulated),
		}, nil
	}

	// T3-2: 审批工单闭环 - 持久化到 sweep_approval 表
	if decision == domain.SweepApproval {
		approvalKey := "sweep_approval:" + cmd.SweepRuleID.String() + ":" + cmd.AsOf.Format("20060102")
		approval, err := s.repo.InsertSweepApproval(ctx, db.InsertSweepApprovalParams{
			SweepRuleID:     cmd.SweepRuleID,
			MerchantID:      uuid.Nil, // 银行归集无 merchant
			AmountMinor:     sweepAmount,
			Currency:        rule.Currency,
			Rail:            rule.Rail,
			BeneficiaryType: string(domain.BeneficiaryType(rule.BeneficiaryType)),
			IdempotencyKey:  approvalKey,
		})
		if err != nil {
			return EvaluateResult{}, fmt.Errorf("insert sweep approval: %w", err)
		}
		return EvaluateResult{
			Decision: decision,
			Reason:   fmt.Sprintf("approval required, approval_id=%s amount=%d", approval.ID, sweepAmount),
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

// T3-2: 审批通过后继续执行 Sweep
func (s *Service) ApproveSweep(ctx context.Context, approvalID uuid.UUID) error {
	approval, err := s.repo.GetSweepApproval(ctx, approvalID)
	if err != nil {
		return err
	}
	if approval == nil {
		return apperr.ErrNotFound.WithMessage("sweep approval not found")
	}

	if approval.Status != "Pending" {
		return apperr.ErrIllegalTransition.WithMessage("approval not in Pending status")
	}

	// 增加审批计数
	if err := s.repo.ApproveSweepApproval(ctx, approvalID); err != nil {
		return fmt.Errorf("approve sweep: %w", err)
	}

	// 重新获取审批记录，检查是否已满
	approval, err = s.repo.GetSweepApproval(ctx, approvalID)
	if err != nil {
		return err
	}

	// 审批未满，返回
	if approval.Status != "Approved" {
		return nil
	}

	// 审批已满，继续执行 Sweep。从 rule 取真实源/目标账户
	rule, err := s.repo.GetSweepRule(ctx, approval.SweepRuleID)
	if err != nil {
		return fmt.Errorf("get sweep rule after approval: %w", err)
	}
	if rule == nil {
		return apperr.ErrNotFound.WithMessage("sweep rule not found for approval")
	}

	transfer, err := s.repo.InsertTransfer(ctx, db.InsertSweepTransferParams{
		SweepRuleID:     approval.SweepRuleID,
		SourceAccountID: rule.SourceBankAccountID,
		TargetAccountID: rule.TargetBankAccountID,
		AmountMinor:     approval.AmountMinor,
		Currency:        approval.Currency,
		Rail:            approval.Rail,
		Status:          "Locked",
		IdempotencyKey:  "sweep_approval:" + approvalID.String(),
	})
	if err != nil {
		return fmt.Errorf("insert transfer after approval: %w", err)
	}

	s.logger.Info("sweep: approved and transfer created",
		"approval_id", approvalID, "transfer_id", transfer.ID)

	return nil
}

func (s *Service) ProcessDelayed(ctx context.Context) error {
	if s.delayQ == nil {
		return nil
	}

	ruleIDs, err := s.delayQ.DequeueReady(ctx, 10)
	if err != nil {
		return fmt.Errorf("sweep: dequeue delayed: %w", err)
	}

	for _, ruleID := range ruleIDs {
		id, err := uuid.Parse(ruleID)
		if err != nil {
			s.logger.Error("sweep: invalid rule ID in delay queue", "rule_id", ruleID, "err", err)
			continue
		}

		result, err := s.Evaluate(ctx, EvaluateCmd{
			SweepRuleID: id,
			AsOf:        time.Now(),
		})
		if err != nil {
			s.logger.Error("sweep: evaluate delayed rule failed", "rule_id", ruleID, "err", err)
			continue
		}

		s.logger.Info("sweep: delayed rule evaluated",
			"rule_id", ruleID, "decision", result.Decision, "reason", result.Reason)

		if result.TransferID != "" {
			transferID, _ := uuid.Parse(result.TransferID)
			if transferID != uuid.Nil {
				if err := s.Submit(ctx, transferID); err != nil {
					s.logger.Error("sweep: submit delayed transfer failed", "transfer_id", result.TransferID, "err", err)
				}
			}
		}
	}

	return nil
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
