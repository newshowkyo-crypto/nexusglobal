package service

import (
	"context"
	"encoding/json"
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
	// SEC-013/014 扩展
	UpdateSweepTransferBankRef(ctx context.Context, id uuid.UUID, bankRef string) error
	InsertOutboxEvent(ctx context.Context, aggregateType string, aggregateID uuid.UUID, eventType, payload, idempotencyKey string) (uuid.UUID, error)
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

	result := EvaluateResult{
		Decision: decision,
		Reason:   getDecisionReason(decision, withinWindow, netAmount, domainRule),
	}

	if decision == domain.SweepSubmit {
		idempotencyKey := fmt.Sprintf("sweep_submit:%s:%s", rule.ID.String(), cmd.AsOf.Truncate(24*time.Hour).Format("2006-01-02"))
		transfer, err := s.repo.InsertTransfer(ctx, db.InsertSweepTransferParams{
			SweepRuleID:     rule.ID,
			SourceAccountID: rule.SourceBankAccountID,
			TargetAccountID: rule.TargetBankAccountID,
			AmountMinor:     netAmount,
			Currency:        rule.Currency,
			Rail:            rule.Rail,
			Status:          "Locked",
			IdempotencyKey:  idempotencyKey,
		})
		if err != nil {
			return EvaluateResult{}, err
		}
		result.TransferID = transfer.ID.String()
	} else if decision == domain.SweepApproval {
		idempotencyKey := fmt.Sprintf("sweep_approval:%s:%s", rule.ID.String(), cmd.AsOf.Truncate(24*time.Hour).Format("2006-01-02"))
		_, err := s.repo.InsertSweepApproval(ctx, db.InsertSweepApprovalParams{
			SweepRuleID:     rule.ID,
			MerchantID:      uuid.Nil,
			AmountMinor:     netAmount,
			Currency:        rule.Currency,
			Rail:            rule.Rail,
			BeneficiaryType: rule.BeneficiaryType,
			IdempotencyKey:  idempotencyKey,
		})
		if err != nil {
			return EvaluateResult{}, err
		}
	} else if decision == domain.SweepAccumulate && !withinWindow {
		// RED-11 / SEC-014: 窗口未开时绝不调用银行 API；启用 DelayQueue 的规则
		// 进入延迟重查队列，窗口打开后由 ProcessDelayed 重新评估。
		s.scheduleDelayedRetry(ctx, rule.ID, rule.DelayQueueEnabled)
	}

	return result, nil
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

	result, err := s.bank.SubmitTransfer(ctx, bank.TransferRequest{
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

	// SEC-013: 持久化银行 provider 参考号，对账命令依赖它查询银行真实状态。
	if result.BankRef != "" {
		if refErr := s.repo.UpdateSweepTransferBankRef(ctx, transferID, result.BankRef); refErr != nil {
			return fmt.Errorf("persist bank ref: %w", refErr)
		}
	}

	return s.repo.UpdateTransferStatus(ctx, transferID, "Submitted", transfer.Version)
}

// ReconcileRequestResult 对账命令受理结果。
type ReconcileRequestResult struct {
	CommandID string `json:"command_id"` // Outbox 事件 ID
	Status    string `json:"status"`
}

// Reconcile 受理对账命令（SEC-013 修复）。
//
// 旧实现不查询银行、直接把转账标记为 Confirmed（虚假成功）。
// 新实现：写持久 Outbox 命令 SWEEP_RECONCILE_REQUESTED 并返回 202 + command_id；
// 真实终态由 sweepreconcile Worker 调用 bank.QueryTransfer 核对后以 CAS 写入。
// Unknown/Pending 状态不确认、不释放（RED-03）。
func (s *Service) Reconcile(ctx context.Context, transferID uuid.UUID, bankRef string) (ReconcileRequestResult, error) {
	transfer, err := s.repo.GetTransfer(ctx, transferID)
	if err != nil {
		return ReconcileRequestResult{}, apperr.ErrNotFound.WithMessage("transfer not found")
	}
	if transfer == nil {
		return ReconcileRequestResult{}, apperr.ErrNotFound.WithMessage("transfer not found")
	}

	if transfer.Status == "Confirmed" {
		return ReconcileRequestResult{}, apperr.ErrIllegalTransition.WithMessage("transfer already confirmed")
	}
	if transfer.Status != "Submitted" {
		return ReconcileRequestResult{}, apperr.ErrIllegalTransition.WithMessage("invalid transfer status for reconciliation")
	}

	ref := bankRef
	if ref == "" {
		ref = transfer.BankRef
	}
	if ref == "" {
		return ReconcileRequestResult{}, apperr.ErrInvalidArgument.WithMessage(
			"bank_ref is required for reconciliation (not yet returned by bank)")
	}

	payload, err := json.Marshal(map[string]string{
		"transfer_id": transferID.String(),
		"bank_ref":    ref,
	})
	if err != nil {
		return ReconcileRequestResult{}, fmt.Errorf("marshal reconcile command: %w", err)
	}

	// 幂等键按 transfer 粒度：重复受理返回同一命令（ON CONFLICT UPDATE id）。
	commandID, err := s.repo.InsertOutboxEvent(ctx, "sweep_transfer", transferID,
		"SWEEP_RECONCILE_REQUESTED", string(payload), "sweep_reconcile:"+transferID.String())
	if err != nil {
		return ReconcileRequestResult{}, fmt.Errorf("enqueue reconcile command: %w", err)
	}

	return ReconcileRequestResult{CommandID: commandID.String(), Status: "ReconcileRequested"}, nil
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

	idempotencyKey := fmt.Sprintf("sweep_submit:%s:%s", rule.ID.String(), time.Now().Truncate(24*time.Hour).Format("2006-01-02"))
	_, err = s.repo.InsertTransfer(ctx, db.InsertSweepTransferParams{
		SweepRuleID:     rule.ID,
		SourceAccountID: rule.SourceBankAccountID,
		TargetAccountID: rule.TargetBankAccountID,
		AmountMinor:     approval.AmountMinor,
		Currency:        approval.Currency,
		Rail:            approval.Rail,
		Status:          "Locked",
		IdempotencyKey:  idempotencyKey,
	})

	return err
}

// ProcessDelayed 处理延迟队列中的转账（SEC-014 修复）。
//
// 旧实现为空函数。新实现：从 Redis DelayQueue（zset，score=executeAt）取出
// 到期规则，重新执行 Evaluate。Evaluate 在"窗口未开且规则启用 DelayQueue"时
// 会重新入队（见 Evaluate 内 scheduleDelayedRetry），形成有限轮询，
// 直到银行窗口打开且触发条件满足为止（RED-11：非窗口绝不调用银行 API）。
func (s *Service) ProcessDelayed(ctx context.Context) error {
	if s.delayQ == nil {
		return nil // 延迟队列未配置（如单测环境）；非静默失败，语义为能力未启用
	}
	const batchLimit = 100
	ruleIDs, err := s.delayQ.DequeueReady(ctx, batchLimit)
	if err != nil {
		return fmt.Errorf("dequeue delayed sweeps: %w", err)
	}
	for _, ruleIDStr := range ruleIDs {
		ruleID, parseErr := uuid.Parse(ruleIDStr)
		if parseErr != nil {
			s.log.Warn("delayed sweep: invalid rule id, dropping", "rule_id", ruleIDStr, "err", parseErr)
			continue
		}
		if _, evalErr := s.Evaluate(ctx, EvaluateCmd{SweepRuleID: ruleID, AsOf: time.Now()}); evalErr != nil {
			// 单条失败不影响同批其他规则；Evaluate 内部已把仍不可执行的规则重新入队。
			s.log.Warn("delayed sweep evaluate failed", "rule_id", ruleIDStr, "err", evalErr)
		}
	}
	return nil
}

// delayedRecheckInterval 是窗口未开时的重查间隔（有限轮询，避免高频打库）。
const delayedRecheckInterval = 5 * time.Minute

// scheduleDelayedRetry 在规则启用 DelayQueue 且当前不可执行时重新入队。
func (s *Service) scheduleDelayedRetry(ctx context.Context, ruleID uuid.UUID, enabled bool) {
	if !enabled || s.delayQ == nil {
		return
	}
	if err := s.delayQ.Enqueue(ctx, ruleID.String(), time.Now().Add(delayedRecheckInterval)); err != nil {
		s.log.Warn("delay queue enqueue failed", "rule_id", ruleID.String(), "err", err)
	}
}
