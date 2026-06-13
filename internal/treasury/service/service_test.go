package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/treasury/domain"
	"github.com/nexusglobal/mamr/internal/treasury/repository/db"
	"github.com/nexusglobal/mamr/pkg/external/bank"
)

type mockRepository struct {
	mock.Mock
}

func (m *mockRepository) GetCalendarByName(ctx context.Context, name string) (*db.BankingCalendar, error) {
	args := m.Called(ctx, name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.BankingCalendar), args.Error(1)
}

func (m *mockRepository) GetSweepRule(ctx context.Context, id uuid.UUID) (*db.SweepRule, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.SweepRule), args.Error(1)
}

func (m *mockRepository) ListSweepRules(ctx context.Context) ([]db.SweepRule, error) {
	args := m.Called(ctx)
	return args.Get(0).([]db.SweepRule), args.Error(1)
}

func (m *mockRepository) InsertTransfer(ctx context.Context, params db.InsertSweepTransferParams) (*db.SweepTransfer, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*db.SweepTransfer), args.Error(1)
}

func (m *mockRepository) GetTransfer(ctx context.Context, id uuid.UUID) (*db.SweepTransfer, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.SweepTransfer), args.Error(1)
}

func (m *mockRepository) UpdateTransferStatus(ctx context.Context, id uuid.UUID, status string, version int32) error {
	args := m.Called(ctx, id, status, version)
	return args.Error(0)
}

func (m *mockRepository) CountDailySweeps(ctx context.Context, ruleID uuid.UUID, since pgtype.Timestamptz) (int64, error) {
	args := m.Called(ctx, ruleID, since)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockRepository) GetBankAccountBalance(ctx context.Context, bankAccountID uuid.UUID, currency string) (int64, error) {
	args := m.Called(ctx, bankAccountID, currency)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockRepository) DecrementBankAccountBalance(ctx context.Context, bankAccountID uuid.UUID, currency string, amount int64) error {
	args := m.Called(ctx, bankAccountID, currency, amount)
	return args.Error(0)
}

func (m *mockRepository) InsertSweepApproval(ctx context.Context, params db.InsertSweepApprovalParams) (*db.SweepApproval, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*db.SweepApproval), args.Error(1)
}

func (m *mockRepository) GetSweepApproval(ctx context.Context, id uuid.UUID) (*db.SweepApproval, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.SweepApproval), args.Error(1)
}

func (m *mockRepository) ApproveSweepApproval(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *mockRepository) ListPendingSweepApprovals(ctx context.Context, ruleID uuid.UUID) ([]db.SweepApproval, error) {
	args := m.Called(ctx, ruleID)
	return args.Get(0).([]db.SweepApproval), args.Error(1)
}

type mockBankClient struct {
	mock.Mock
}

func (m *mockBankClient) SubmitTransfer(ctx context.Context, req bank.TransferRequest) (bank.TransferResult, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(bank.TransferResult), args.Error(1)
}

func (m *mockBankClient) QueryTransfer(ctx context.Context, bankRef string) (bank.TransferStatus, error) {
	args := m.Called(ctx, bankRef)
	return args.Get(0).(bank.TransferStatus), args.Error(1)
}

func TestEvaluate_RuleNotFound(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}

	svc := New(repo, bankCli, txm, nil, nil, nil)

	ruleID := uuid.New()
	repo.On("GetSweepRule", ctx, ruleID).Return(nil, nil)

	_, err := svc.Evaluate(ctx, EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        time.Now(),
	})

	require.Error(t, err)
	assert.True(t, errors.As(err, &apperr.ErrNotFound))
}

func TestEvaluate_CalendarNotFound(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}

	svc := New(repo, bankCli, txm, nil, nil, nil)

	ruleID := uuid.New()
	repo.On("GetSweepRule", ctx, ruleID).Return(&db.SweepRule{
		ID: ruleID,
	}, nil)
	repo.On("GetCalendarByName", ctx, "US_FED").Return(nil, nil)

	_, err := svc.Evaluate(ctx, EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        time.Now(),
	})

	require.Error(t, err)
	assert.True(t, errors.As(err, &apperr.ErrNotFound))
}

func TestSubmit_Success(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}
	logger := slog.Default()

	svc := New(repo, bankCli, txm, nil, nil, logger)

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&db.SweepTransfer{
		ID:      transferID,
		Status:  "Locked",
		Version: 1,
	}, nil)
	bankCli.On("SubmitTransfer", mock.Anything, mock.Anything).Return(bank.TransferResult{
		BankRef: "bank-ref-123",
	}, nil)
	repo.On("UpdateTransferStatus", ctx, transferID, "Submitted", int32(1)).Return(nil)

	err := svc.Submit(ctx, transferID)

	require.NoError(t, err)
}

func TestSubmit_TransferNotFound(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}

	svc := New(repo, bankCli, txm, nil, nil, nil)

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(nil, nil)

	err := svc.Submit(ctx, transferID)

	require.Error(t, err)
	assert.True(t, errors.As(err, &apperr.ErrNotFound))
}

func TestSubmit_InvalidStatus(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}

	svc := New(repo, bankCli, txm, nil, nil, nil)

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&db.SweepTransfer{
		ID:      transferID,
		Status:  "Submitted",
		Version: 1,
	}, nil)

	err := svc.Submit(ctx, transferID)

	require.Error(t, err)
	assert.True(t, errors.As(err, &apperr.ErrIllegalTransition))
}

func TestReconcile_Success(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}

	svc := New(repo, bankCli, txm, nil, nil, nil)

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&db.SweepTransfer{
		ID:      transferID,
		Status:  "Submitted",
		Version: 1,
	}, nil)
	repo.On("UpdateTransferStatus", ctx, transferID, "Confirmed", int32(1)).Return(nil)

	err := svc.Reconcile(ctx, transferID, "bank-ref-123")

	require.NoError(t, err)
}

func TestReconcile_AlreadyConfirmed(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}

	svc := New(repo, bankCli, txm, nil, nil, nil)

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&db.SweepTransfer{
		ID:      transferID,
		Status:  "Confirmed",
		Version: 1,
	}, nil)

	err := svc.Reconcile(ctx, transferID, "bank-ref-123")

	require.NoError(t, err)
}

func TestReconcile_InvalidStatus(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}

	svc := New(repo, bankCli, txm, nil, nil, nil)

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&db.SweepTransfer{
		ID:      transferID,
		Status:  "Locked",
		Version: 1,
	}, nil)

	err := svc.Reconcile(ctx, transferID, "bank-ref-123")

	require.Error(t, err)
	assert.True(t, errors.As(err, &apperr.ErrIllegalTransition))
}

// T3-3: 测试净额为0时不SUBMIT
func TestEvaluate_NetAmountZero_NoSubmit(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}
	logger := slog.Default()

	svc := New(repo, bankCli, txm, nil, nil, logger)

	ruleID := uuid.New()
	repo.On("GetSweepRule", ctx, ruleID).Return(&db.SweepRule{
		ID:                    ruleID,
		SourceBankAccountID:   uuid.New(),
		TargetBankAccountID:   uuid.New(),
		Currency:              "USD",
		Rail:                  "ACH",
		BeneficiaryType:       "SAME_NAME",
		SameNameVerified:      true,
		TriggerThresholdMinor: 1000,
		SweepRatioBps:         5000, // 50%
		MaxSingleSweepMinor:   10000,
		MinResidualMinor:      900, // 最小留存 900
		DelayQueueEnabled:     false,
	}, nil)
	repo.On("GetCalendarByName", ctx, "US_FED").Return(&db.BankingCalendar{
		Timezone:     "America/New_York",
		BusinessDays: []byte(`["Monday","Tuesday","Wednesday","Thursday","Friday"]`),
		Holidays:     []byte(`[]`),
		CutOffMatrix: []byte(`{"ACH":"17:00"}`),
	}, nil)
	// 毛额 1000，ratio=50% → 净额 500，但 minResidual=900 → 净额 = 1000-900 = 100
	// 100 < triggerThreshold 1000 → ACCUMULATE
	repo.On("GetBankAccountBalance", ctx, mock.Anything, "USD").Return(int64(1000), nil)
	repo.On("CountDailySweeps", ctx, ruleID, mock.Anything).Return(int64(0), nil)

	result, err := svc.Evaluate(ctx, EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC),
	})

	require.NoError(t, err)
	assert.Equal(t, domain.SweepAccumulate, result.Decision, "net amount below threshold should ACCUMULATE")
}

// T3-3: 测试非同名大额进审批表
func TestEvaluate_ThirdPartyLargeAmount_ApprovalRequired(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}
	logger := slog.Default()

	svc := New(repo, bankCli, txm, nil, nil, logger)

	ruleID := uuid.New()
	repo.On("GetSweepRule", ctx, ruleID).Return(&db.SweepRule{
		ID:                     ruleID,
		SourceBankAccountID:    uuid.New(),
		TargetBankAccountID:    uuid.New(),
		Currency:               "USD",
		Rail:                   "ACH",
		BeneficiaryType:        "THIRD_PARTY",
		SameNameVerified:       false,
		TriggerThresholdMinor:  1000,
		SweepRatioBps:          10000, // 100%
		MaxSingleSweepMinor:    100000,
		MinResidualMinor:       0,
		ApprovalThresholdMinor: 5000,
		MaxDailySweepCount:     10,
		DelayQueueEnabled:      false,
	}, nil)
	repo.On("GetCalendarByName", ctx, "US_FED").Return(&db.BankingCalendar{
		Timezone:     "America/New_York",
		BusinessDays: []byte(`["Monday","Tuesday","Wednesday","Thursday","Friday"]`),
		Holidays:     []byte(`[]`),
		CutOffMatrix: []byte(`{"ACH":"17:00"}`),
	}, nil)
	repo.On("GetBankAccountBalance", ctx, mock.Anything, "USD").Return(int64(10000), nil)
	repo.On("CountDailySweeps", ctx, ruleID, mock.Anything).Return(int64(0), nil)
	repo.On("InsertSweepApproval", ctx, mock.Anything).Return(&db.SweepApproval{
		ID: uuid.New(),
	}, nil)

	result, err := svc.Evaluate(ctx, EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), // 周五
	})

	require.NoError(t, err)
	t.Logf("Decision: %s, Reason: %s", result.Decision, result.Reason)
	assert.Equal(t, domain.SweepApproval, result.Decision, "third party large amount should require approval")
}

// T3-3: 测试审批通过后续做
func TestApproveSweep_Success(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}
	logger := slog.Default()

	svc := New(repo, bankCli, txm, nil, nil, logger)

	approvalID := uuid.New()
	ruleID := uuid.New()
	srcAcct := uuid.New()
	tgtAcct := uuid.New()
	repo.On("GetSweepApproval", ctx, approvalID).Return(&db.SweepApproval{
		ID:                approvalID,
		SweepRuleID:       ruleID,
		AmountMinor:       5000,
		Currency:          "USD",
		Rail:              "ACH",
		BeneficiaryType:   "THIRD_PARTY",
		Status:            "Pending",
		RequiredApprovals: 2,
		CurrentApprovals:  1,
	}, nil).Once()
	repo.On("ApproveSweepApproval", ctx, approvalID).Return(nil)
	repo.On("GetSweepApproval", ctx, approvalID).Return(&db.SweepApproval{
		ID:                approvalID,
		SweepRuleID:       ruleID,
		AmountMinor:       5000,
		Currency:          "USD",
		Rail:              "ACH",
		BeneficiaryType:   "THIRD_PARTY",
		Status:            "Approved",
		RequiredApprovals: 2,
		CurrentApprovals:  2,
	}, nil).Once()
	repo.On("GetSweepRule", ctx, ruleID).Return(&db.SweepRule{
		ID:                  ruleID,
		SourceBankAccountID: srcAcct,
		TargetBankAccountID: tgtAcct,
		Currency:            "USD",
		Rail:                "ACH",
	}, nil)
	// 断言：审批放行建的 transfer 必须带真实源/目标账户，不能是 uuid.Nil
	repo.On("InsertTransfer", ctx, mock.MatchedBy(func(p db.InsertSweepTransferParams) bool {
		return p.SourceAccountID == srcAcct &&
			p.TargetAccountID == tgtAcct &&
			p.SourceAccountID != uuid.Nil &&
			p.TargetAccountID != uuid.Nil &&
			p.AmountMinor == 5000
	})).Return(&db.SweepTransfer{
		ID: uuid.New(),
	}, nil)

	err := svc.ApproveSweep(ctx, approvalID)

	require.NoError(t, err)
	repo.AssertExpectations(t)
}

// T3-3: 测试审批未满时不创建 transfer
func TestApproveSweep_NotEnoughApprovals(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	bankCli := new(mockBankClient)
	txm := &postgres.TxManager{}
	logger := slog.Default()

	svc := New(repo, bankCli, txm, nil, nil, logger)

	approvalID := uuid.New()
	repo.On("GetSweepApproval", ctx, approvalID).Return(&db.SweepApproval{
		ID:                approvalID,
		SweepRuleID:       uuid.New(),
		AmountMinor:       5000,
		Currency:          "USD",
		Rail:              "ACH",
		BeneficiaryType:   "THIRD_PARTY",
		Status:            "Pending",
		RequiredApprovals: 2,
		CurrentApprovals:  0,
	}, nil).Once()
	repo.On("ApproveSweepApproval", ctx, approvalID).Return(nil)
	repo.On("GetSweepApproval", ctx, approvalID).Return(&db.SweepApproval{
		ID:                approvalID,
		SweepRuleID:       uuid.New(),
		AmountMinor:       5000,
		Currency:          "USD",
		Rail:              "ACH",
		BeneficiaryType:   "THIRD_PARTY",
		Status:            "Pending",
		RequiredApprovals: 2,
		CurrentApprovals:  1,
	}, nil).Once()

	err := svc.ApproveSweep(ctx, approvalID)

	require.NoError(t, err)
	repo.AssertNotCalled(t, "InsertTransfer")
}
