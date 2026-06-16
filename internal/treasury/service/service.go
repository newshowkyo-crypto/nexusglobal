package service

import (
	"context"
	"encoding/json"
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

// Repository 资金库仓储接口
type Repository interface {
	GetSweepRule(ctx context.Context, id uuid.UUID) (*db.SweepRule, error)
	ListSweepRules(ctx context.Context) ([]db.SweepRule, error)
	GetCalendarByName(ctx context.Context, name string) (*db.BankingCalendar, error)
	GetTransfer(ctx context.Context, id uuid.UUID) (*db.SweepTransfer, error)
	UpdateTransferStatus(ctx context.Context, id uuid.UUID, status string, version int32) error
	GetBankAccountBalance(ctx context.Context, bankAccountID uuid.UUID, currency string) (int64, error)
	CountDailySweeps(ctx context.Context, ruleID uuid.UUID, since pgtype.Timestamptz) (int64, error)
	InsertSweepApproval(ctx context.Context, params db.InsertSweepApprovalParams) (*db.SweepApproval, error)
	GetSweepApproval(ctx context.Context, id uuid.UUID) (*db.SweepApproval, error)
	ApproveSweepApproval(ctx context.Context, id uuid.UUID) error
	InsertTransfer(ctx context.Context, params db.InsertSweepTransferParams) (*db.SweepTransfer, error)
}

// Service 资金库服务
type Service struct {
	repo   Repository
	bank   bank.Client
	txm    *postgres.TxManager
	pool   *pgxpool.Pool
	delayQ *scheduler.DelayQueue
	log    *slog.Logger
}

// New 创建资金库服务实例
func New(repo Repository, bank bank.Client, txm *postgres.TxManager, pool *pgxpool.Pool, delayQ *scheduler.DelayQueue, log *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		bank:   bank,
		txm:    txm,
		pool:   pool,
		delayQ: delayQ,
		log:    log,
	}
}

// EvaluateCmd 评估命令
type EvaluateCmd struct {
	SweepRuleID uuid.UUID
	AsOf        time.Time
}

// EvaluateResult 评估结果
type EvaluateResult struct {
	Decision   domain.SweepDecision
	TransferID string
	Reason     string
}

// Evaluate 评估扫单规则
func (s *Service) Evaluate(ctx context.Context, cmd EvaluateCmd) (EvaluateResult, error) {
	rule, err := s.repo.GetSweepRule(ctx, cmd.SweepRuleID)
	if err != nil {
		return EvaluateResult{}, apperr.ErrNotFound.WithMessage("sweep rule not found")
	}
	if rule == nil {
		return EvaluateResult{}, apperr.ErrNotFound.WithMessage("sweep rule not found")
	}

	calendar, err := s.repo.GetCalendarByName(ctx, "US_FED")
	if err != nil || calendar == nil {
		return EvaluateResult{}, apperr.ErrNotFound.WithMessage("calendar not found")
	}

	balance, err := s.repo.GetBankAccountBalance(ctx, rule.SourceBankAccountID, rule.Currency)
	if err != nil {
		return EvaluateResult{}, err
	}

	dailyCount, err := s.repo.CountDailySweeps(ctx, rule.ID, pgtype.Timestamptz{Time: cmd.AsOf.Truncate(24 * time.Hour), Valid: true})
	if err != nil {
		return EvaluateResult{}, err
	}

	domainRule := &domain.SweepRule{
		ID:                     rule.ID.String(),
		SourceBankAccountID:    rule.SourceBankAccountID.String(),
		TargetBankAccountID:    rule.TargetBankAccountID.String(),
		Currency:               rule.Currency,
		Rail:                   rule.Rail,
		BeneficiaryType:        domain.BeneficiaryType(rule.BeneficiaryType),
		SameNameVerified:       rule.SameNameVerified,
		TriggerThresholdMinor:  rule.TriggerThresholdMinor,
		SweepRatioBps:          rule.SweepRatioBps,
		MaxSingleSweepMinor:    rule.MaxSingleSweepMinor,
		MinResidualMinor:       rule.MinResidualMinor,
		MaxDailySweepCount:     int(rule.MaxDailySweepCount),
		DelayQueueEnabled:      rule.DelayQueueEnabled,
		ApprovalThresholdMinor: rule.ApprovalThresholdMinor,
	}

	domainCalendar := &domain.BankingCalendar{
		Timezone:     calendar.Timezone,
		BusinessDays: parseBusinessDays(calendar.BusinessDays),
		CutOffMatrix: parseCutOffMatrix(calendar.CutOffMatrix),
	}

	withinWindow := domain.WithinBankingWindow(domainCalendar, rule.Rail, cmd.AsOf)

	netAmount := calculateNetAmount(balance, domainRule)

	beneficiaryResult := domain.EvaluateBeneficiary(domainRule, netAmount)

	triggerInput := domain.SweepTriggerInput{
		NetSweepAmount:   netAmount,
		DailySweepCount:  int(dailyCount),
		Rule:             domainRule,
		WithinWindow:     withinWindow,
		RequiresApproval: beneficiaryResult.RequiresApproval,
	}

	decision := domain.EvaluateSweepTrigger(triggerInput)

	if decision == domain.SweepApproval {
		_, err := s.repo.InsertSweepApproval(ctx, db.InsertSweepApprovalParams{
			SweepRuleID:     rule.ID,
			MerchantID:      uuid.New(),
			AmountMinor:     netAmount,
			Currency:        rule.Currency,
			Rail:            rule.Rail,
			BeneficiaryType: rule.BeneficiaryType,
			IdempotencyKey:  uuid.New().String(),
		})
		if err != nil {
			return EvaluateResult{}, err
		}
	}

	return EvaluateResult{
		Decision: decision,
		Reason:   getDecisionReason(decision, withinWindow, netAmount, domainRule),
	}, nil
}

func calculateNetAmount(balance int64, rule *domain.SweepRule) int64 {
	if balance <= rule.MinResidualMinor {
		return 0
	}
	available := balance - rule.MinResidualMinor
	amount := (available * int64(rule.SweepRatioBps)) / 10000
	if amount > rule.MaxSingleSweepMinor {
		amount = rule.MaxSingleSweepMinor
	}
	return amount
}

func getDecisionReason(decision domain.SweepDecision, withinWindow bool, netAmount int64, rule *domain.SweepRule) string {
	switch decision {
	case domain.SweepAccumulate:
		if !withinWindow {
			return "outside banking window"
		}
		if netAmount <= 0 {
			return "net amount zero or negative"
		}
		if netAmount < rule.TriggerThresholdMinor {
			return "net amount below threshold"
		}
		return "daily sweep count reached"
	case domain.SweepApproval:
		return "requires approval"
	case domain.SweepSubmit:
		return "ready for submit"
	default:
		return "default evaluation"
	}
}

func parseBusinessDays(data []byte) []string {
	if len(data) == 0 {
		return []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday"}
	}
	var days []string
	_ = json.Unmarshal(data, &days)
	return days
}

func parseCutOffMatrix(data []byte) map[string]string {
	if len(data) == 0 {
		return map[string]string{}
	}
	var matrix map[string]string
	_ = json.Unmarshal(data, &matrix)
	return matrix
}

// Submit 提交转账
func (s *Service) Submit(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := s.repo.GetTransfer(ctx, transferID)
	if err != nil {
		return apperr.ErrNotFound.WithMessage("transfer not found")
	}
	if transfer == nil {
		return apperr.ErrNotFound.WithMessage("transfer not found")
	}

	if transfer.Status != "Locked" {
		return apperr.ErrIllegalTransition.WithMessage("invalid transfer status")
	}

	_, err = s.bank.SubmitTransfer(ctx, bank.TransferRequest{
		SweepTransferID: transferID,
		SourceAccountID: transfer.SourceAccountID,
		TargetAccountID: transfer.TargetAccountID,
		Amount:          decimal.NewFromInt(transfer.AmountMinor),
		Currency:        transfer.Currency,
		Rail:            transfer.Rail,
	})
	if err != nil {
		return err
	}

	return s.repo.UpdateTransferStatus(ctx, transferID, "Submitted", transfer.Version)
}

// Reconcile 对账
func (s *Service) Reconcile(ctx context.Context, transferID uuid.UUID, bankRef string) error {
	transfer, err := s.repo.GetTransfer(ctx, transferID)
	if err != nil {
		return apperr.ErrNotFound.WithMessage("transfer not found")
	}
	if transfer == nil {
		return apperr.ErrNotFound.WithMessage("transfer not found")
	}

	if transfer.Status == "Confirmed" {
		return nil
	}

	if transfer.Status != "Submitted" {
		return apperr.ErrIllegalTransition.WithMessage("invalid transfer status for reconciliation")
	}

	return s.repo.UpdateTransferStatus(ctx, transferID, "Confirmed", transfer.Version)
}

// ApproveSweep 批准扫单
func (s *Service) ApproveSweep(ctx context.Context, approvalID uuid.UUID) error {
	approval, err := s.repo.GetSweepApproval(ctx, approvalID)
	if err != nil {
		return apperr.ErrNotFound.WithMessage("sweep approval not found")
	}
	if approval == nil {
		return apperr.ErrNotFound.WithMessage("sweep approval not found")
	}

	if err := s.repo.ApproveSweepApproval(ctx, approvalID); err != nil {
		return err
	}

	approval, err = s.repo.GetSweepApproval(ctx, approvalID)
	if err != nil {
		return err
	}

	if approval.Status != "Approved" {
		return nil
	}

	rule, err := s.repo.GetSweepRule(ctx, approval.SweepRuleID)
	if err != nil {
		return err
	}

	_, err = s.repo.InsertTransfer(ctx, db.InsertSweepTransferParams{
		SweepRuleID:     rule.ID,
		SourceAccountID: rule.SourceBankAccountID,
		TargetAccountID: rule.TargetBankAccountID,
		AmountMinor:     approval.AmountMinor,
		Currency:        approval.Currency,
		Rail:            approval.Rail,
		Status:          "Locked",
		IdempotencyKey:  uuid.New().String(),
	})

	return err
}

// ProcessDelayed 处理延迟队列中的转账
func (s *Service) ProcessDelayed(ctx context.Context) error {
	return nil
}
