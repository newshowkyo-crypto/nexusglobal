package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	disputedomain "github.com/nexusglobal/mamr/internal/dispute/domain"
	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
)

type mockDisputeRepo struct {
	mock.Mock
}

func (m *mockDisputeRepo) CreateDispute(ctx context.Context, tx pgx.Tx, dispute *disputedomain.PaymentDispute) error {
	args := m.Called(ctx, tx, dispute)
	return args.Error(0)
}

func (m *mockDisputeRepo) GetDisputeByID(ctx context.Context, id uuid.UUID) (*disputedomain.PaymentDispute, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*disputedomain.PaymentDispute), args.Error(1)
}

func (m *mockDisputeRepo) GetDisputeByIDAndIdempotencyKey(ctx context.Context, id uuid.UUID, idempotencyKey string) (*disputedomain.PaymentDispute, error) {
	args := m.Called(ctx, id, idempotencyKey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*disputedomain.PaymentDispute), args.Error(1)
}

func (m *mockDisputeRepo) GetDisputeByOrderIDAndType(ctx context.Context, orderID string, disputeType disputedomain.DisputeType) (*disputedomain.PaymentDispute, error) {
	args := m.Called(ctx, orderID, disputeType)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*disputedomain.PaymentDispute), args.Error(1)
}

func (m *mockDisputeRepo) UpdateDisputeStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status disputedomain.DisputeStatus) error {
	args := m.Called(ctx, tx, id, status)
	return args.Error(0)
}

func (m *mockDisputeRepo) GetOrCreateLedgerAccount(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error) {
	args := m.Called(ctx, tx, merchantID, accountType, currency)
	return args.Get(0).(uuid.UUID), args.Error(1)
}

func (m *mockDisputeRepo) GetMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error) {
	args := m.Called(ctx, merchantID, currency)
	return args.Get(0).(decimal.Decimal), args.Error(1)
}

func (m *mockDisputeRepo) GetPlatformDisputeReserveBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error) {
	args := m.Called(ctx, merchantID, currency)
	return args.Get(0).(decimal.Decimal), args.Error(1)
}

func (m *mockDisputeRepo) CreatePlatformDisputeReserveEntry(ctx context.Context, tx pgx.Tx, entry *disputedomain.PlatformDisputeReserveLedger) error {
	args := m.Called(ctx, tx, entry)
	return args.Error(0)
}

func (m *mockDisputeRepo) CreateMerchantNegativeBalance(ctx context.Context, tx pgx.Tx, neg *disputedomain.MerchantNegativeBalance) error {
	args := m.Called(ctx, tx, neg)
	return args.Error(0)
}

func (m *mockDisputeRepo) GetMerchantNegativeBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error) {
	args := m.Called(ctx, merchantID, currency)
	return args.Get(0).(decimal.Decimal), args.Error(1)
}

type mockLedgerSvc struct {
	mock.Mock
}

func (m *mockLedgerSvc) CreateTransaction(ctx context.Context, tx pgx.Tx, txn ledgerdomain.Transaction) error {
	args := m.Called(ctx, tx, txn)
	return args.Error(0)
}

type mockTxManager struct {
	mock.Mock
}

func (m *mockTxManager) WithinTx(ctx context.Context, fn func(pgx.Tx) error) error {
	m.Called(ctx, fn)
	return fn(nil)
}

var (
	testMerchantID = uuid.New()
	testSettledID  = uuid.New()
	testReserveID  = uuid.New()
	testFeeID      = uuid.New()
	testAcquirerID = uuid.New()
	testNegID      = uuid.New()
)

func setupMocks(repo *mockDisputeRepo) {
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "SETTLED", "USD").Return(testSettledID, nil)
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "PLATFORM_DISPUTE_RESERVE", "USD").Return(testReserveID, nil)
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "FEE", "USD").Return(testFeeID, nil)
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "ACQUIRER_RECEIVABLE", "USD").Return(testAcquirerID, nil)
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "MERCHANT_NEGATIVE_BALANCE", "USD").Return(testNegID, nil)
}

func TestRefundConfirmed_Success(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)

	txm.On("WithinTx", mock.Anything, mock.Anything).Return(nil)

	svc := New(repo, ledger, txm)

	merchantID := uuid.New()
	settledID := uuid.New()
	acquirerID := uuid.New()

	repo.On("GetDisputeByOrderIDAndType", mock.Anything, "order-1", disputedomain.DisputeTypeRefund).
		Return(nil, disputedomain.ErrDisputeNotFound)
	repo.On("CreateDispute", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "SETTLED", "USD").Return(settledID, nil)
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "ACQUIRER_RECEIVABLE", "USD").Return(acquirerID, nil)
	ledger.On("CreateTransaction", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := svc.RefundConfirmed(context.Background(), disputedomain.CreateDisputeCmd{
		OrderID:    "order-1",
		MerchantID: merchantID,
		Amount:     decimal.NewFromInt(100),
		Currency:   "USD",
	})

	require.NoError(t, err)
	repo.AssertExpectations(t)
	ledger.AssertExpectations(t)

	txn := ledger.Calls[0].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, ledgerdomain.TxnTypeOrderRefund, txn.TransactionType)
	assert.Len(t, txn.Entries, 2)
	assert.Equal(t, ledgerdomain.DirectionDebit, txn.Entries[0].Direction)
	assert.Equal(t, settledID, txn.Entries[0].AccountID)
	assert.Equal(t, ledgerdomain.DirectionCredit, txn.Entries[1].Direction)
	assert.Equal(t, acquirerID, txn.Entries[1].AccountID)
}

func TestRefundConfirmed_IdempotentReplay(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)
	svc := New(repo, ledger, txm)

	existing := &disputedomain.PaymentDispute{
		ID:             uuid.New(),
		IdempotencyKey: "refund-order-1",
	}
	repo.On("GetDisputeByOrderIDAndType", mock.Anything, "order-1", disputedomain.DisputeTypeRefund).
		Return(existing, nil)

	err := svc.RefundConfirmed(context.Background(), disputedomain.CreateDisputeCmd{
		OrderID:        "order-1",
		MerchantID:     uuid.New(),
		Amount:         decimal.NewFromInt(100),
		Currency:       "USD",
		IdempotencyKey: "refund-order-1",
	})

	require.NoError(t, err)
	repo.AssertNotCalled(t, "CreateDispute", mock.Anything, mock.Anything, mock.Anything)
}

func TestChargebackReceived_SufficientBalance(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)

	txm.On("WithinTx", mock.Anything, mock.Anything).Return(nil)

	svc := New(repo, ledger, txm)

	merchantID := uuid.New()
	setupMocks(repo)

	repo.On("GetDisputeByOrderIDAndType", mock.Anything, "order-2", disputedomain.DisputeTypeChargeback).
		Return(nil, disputedomain.ErrDisputeNotFound)
	repo.On("CreateDispute", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	repo.On("GetMerchantSettledBalance", mock.Anything, merchantID, "USD").
		Return(decimal.NewFromInt(500), nil) // 充足余额
	repo.On("CreatePlatformDisputeReserveEntry", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	ledger.On("CreateTransaction", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := svc.ChargebackReceived(context.Background(), disputedomain.CreateDisputeCmd{
		OrderID:       "order-2",
		MerchantID:    merchantID,
		Amount:        decimal.NewFromInt(200),
		Currency:      "USD",
		ChargebackFee: decimal.NewFromInt(15),
	})

	require.NoError(t, err)

	// 验证创建了两个 transaction: CHARGEBACK_RECEIVED 和 CHARGEBACK_FEE
	require.Len(t, ledger.Calls, 2)

	// 第一个 transaction: CHARGEBACK_RECEIVED (本金)
	// settled=500 >= dispute=200，settled debit=200, negative_balance debit=0, reserve credit=200
	txn1 := ledger.Calls[0].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, ledgerdomain.TxnTypeChargebackReceived, txn1.TransactionType)
	assert.Len(t, txn1.Entries, 2) // 充足余额只需要 2 条
	assert.Equal(t, ledgerdomain.DirectionDebit, txn1.Entries[0].Direction)
	assert.Equal(t, testSettledID, txn1.Entries[0].AccountID)
	assert.Equal(t, decimal.NewFromInt(200), txn1.Entries[0].Amount)
	assert.Equal(t, ledgerdomain.DirectionCredit, txn1.Entries[1].Direction)
	assert.Equal(t, testReserveID, txn1.Entries[1].AccountID)
	assert.Equal(t, decimal.NewFromInt(200), txn1.Entries[1].Amount)

	// 验证借贷平衡
	assert.True(t, ledgerdomain.ValidateBalanced(txn1.Entries) == nil)

	// 第二个 transaction: CHARGEBACK_FEE (费用)
	// settled 剩余 300 >= fee 15，settled debit=15, negative_balance debit=0, fee credit=15
	txn2 := ledger.Calls[1].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, ledgerdomain.TxnTypeChargebackFee, txn2.TransactionType)
	assert.Len(t, txn2.Entries, 2) // 充足余额只需要 2 条
	assert.Equal(t, ledgerdomain.DirectionDebit, txn2.Entries[0].Direction)
	assert.Equal(t, testSettledID, txn2.Entries[0].AccountID)
	assert.Equal(t, decimal.NewFromInt(15), txn2.Entries[0].Amount)
	assert.Equal(t, ledgerdomain.DirectionCredit, txn2.Entries[1].Direction)
	assert.Equal(t, testFeeID, txn2.Entries[1].AccountID)
	assert.Equal(t, decimal.NewFromInt(15), txn2.Entries[1].Amount)

	// 验证借贷平衡
	assert.True(t, ledgerdomain.ValidateBalanced(txn2.Entries) == nil)
}

func TestChargebackReceived_InsufficientBalance(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)

	txm.On("WithinTx", mock.Anything, mock.Anything).Return(nil)

	svc := New(repo, ledger, txm)

	merchantID := uuid.New()
	settledID := uuid.New()
	reserveID := uuid.New()
	negID := uuid.New()
	feeID := uuid.New()

	// 使用正确的参数数量（5个参数 + 1个返回值）
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, merchantID, "SETTLED", "USD").Return(settledID, nil).Once()
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, merchantID, "PLATFORM_DISPUTE_RESERVE", "USD").Return(reserveID, nil).Once()
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, merchantID, "MERCHANT_NEGATIVE_BALANCE", "USD").Return(negID, nil).Once()
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, merchantID, "FEE", "USD").Return(feeID, nil).Once()

	repo.On("GetDisputeByOrderIDAndType", mock.Anything, "order-3", disputedomain.DisputeTypeChargeback).
		Return(nil, disputedomain.ErrDisputeNotFound)
	repo.On("CreateDispute", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	repo.On("GetMerchantSettledBalance", mock.Anything, merchantID, "USD").
		Return(decimal.NewFromInt(50), nil) // 不足余额
	repo.On("CreatePlatformDisputeReserveEntry", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	repo.On("CreateMerchantNegativeBalance", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	ledger.On("CreateTransaction", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := svc.ChargebackReceived(context.Background(), disputedomain.CreateDisputeCmd{
		OrderID:       "order-3",
		MerchantID:    merchantID,
		Amount:        decimal.NewFromInt(200),
		Currency:      "USD",
		ChargebackFee: decimal.NewFromInt(15),
	})

	require.NoError(t, err)

	// 验证创建了两个 transaction: CHARGEBACK_RECEIVED 和 CHARGEBACK_FEE
	require.Len(t, ledger.Calls, 2)

	// 第一个 transaction: CHARGEBACK_RECEIVED (本金)
	// settled=50 < dispute=200，settled debit=50, reserve credit=200, negative_balance debit=150
	txn1 := ledger.Calls[0].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, ledgerdomain.TxnTypeChargebackReceived, txn1.TransactionType)
	assert.Len(t, txn1.Entries, 3) // 不足余额需要 3 条：settled + reserve + negative_balance
	assert.Equal(t, ledgerdomain.DirectionDebit, txn1.Entries[0].Direction)
	assert.Equal(t, settledID, txn1.Entries[0].AccountID)
	assert.Equal(t, decimal.NewFromInt(50), txn1.Entries[0].Amount) // 只能扣 50
	assert.Equal(t, ledgerdomain.DirectionCredit, txn1.Entries[1].Direction)
	assert.Equal(t, reserveID, txn1.Entries[1].AccountID)
	assert.Equal(t, decimal.NewFromInt(200), txn1.Entries[1].Amount) // reserve 入账 200
	assert.Equal(t, ledgerdomain.DirectionDebit, txn1.Entries[2].Direction)
	assert.Equal(t, negID, txn1.Entries[2].AccountID)
	assert.Equal(t, decimal.NewFromInt(150), txn1.Entries[2].Amount) // 本金缺口 150

	// 验证借贷平衡: 50 + 150 = 200
	assert.True(t, ledgerdomain.ValidateBalanced(txn1.Entries) == nil)

	// 第二个 transaction: CHARGEBACK_FEE (费用)
	// settled 剩余 0 < fee 15，无 settled 可扣，全部进入 negative_balance
	// entries: negative_balance debit 15, fee credit 15 (共 2 条，settled debit 0 被跳过)
	txn2 := ledger.Calls[1].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, ledgerdomain.TxnTypeChargebackFee, txn2.TransactionType)
	assert.Len(t, txn2.Entries, 2) // settled debit 为 0 被跳过，只有 negative_balance + fee
	assert.Equal(t, ledgerdomain.DirectionDebit, txn2.Entries[0].Direction)
	assert.Equal(t, negID, txn2.Entries[0].AccountID)
	assert.True(t, txn2.Entries[0].Amount.Equal(decimal.NewFromInt(15)), "fee shortfall should be 15, got %s", txn2.Entries[0].Amount.String())
	assert.Equal(t, ledgerdomain.DirectionCredit, txn2.Entries[1].Direction)
	assert.Equal(t, feeID, txn2.Entries[1].AccountID)
	assert.True(t, txn2.Entries[1].Amount.Equal(decimal.NewFromInt(15)), "fee credit should be 15, got %s", txn2.Entries[1].Amount.String())

	// 验证借贷平衡: 15 = 15
	assert.True(t, ledgerdomain.ValidateBalanced(txn2.Entries) == nil)

	// 验证创建了 MerchantNegativeBalance
	// totalShortfall = 150 (本金) + 15 (fee) = 165
	repo.AssertCalled(t, "CreateMerchantNegativeBalance", mock.Anything, mock.Anything, mock.MatchedBy(func(neg *disputedomain.MerchantNegativeBalance) bool {
		return neg.Amount.Equal(decimal.NewFromInt(165))
	}))
}

func TestChargebackReceived_ZeroSettledBalance(t *testing.T) {
	// 测试 settled=0, dispute=200, fee=15 场景
	// principal transaction: settled debit=0（不生成）, reserve credit=200, negative_balance debit=200
	// fee transaction: settled debit=0（不生成）, negative_balance debit=15, fee credit=15

	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)

	txm.On("WithinTx", mock.Anything, mock.Anything).Return(nil)

	svc := New(repo, ledger, txm)

	merchantID := uuid.New()
	settledID := uuid.New()
	reserveID := uuid.New()
	negID := uuid.New()
	feeID := uuid.New()

	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, merchantID, "SETTLED", "USD").Return(settledID, nil).Once()
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, merchantID, "PLATFORM_DISPUTE_RESERVE", "USD").Return(reserveID, nil).Once()
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, merchantID, "MERCHANT_NEGATIVE_BALANCE", "USD").Return(negID, nil).Once()
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, merchantID, "FEE", "USD").Return(feeID, nil).Once()

	repo.On("GetDisputeByOrderIDAndType", mock.Anything, "order-zero", disputedomain.DisputeTypeChargeback).
		Return(nil, disputedomain.ErrDisputeNotFound)
	repo.On("CreateDispute", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	repo.On("GetMerchantSettledBalance", mock.Anything, merchantID, "USD").
		Return(decimal.Zero, nil) // 零余额
	repo.On("CreatePlatformDisputeReserveEntry", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	repo.On("CreateMerchantNegativeBalance", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	ledger.On("CreateTransaction", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := svc.ChargebackReceived(context.Background(), disputedomain.CreateDisputeCmd{
		OrderID:       "order-zero",
		MerchantID:    merchantID,
		Amount:        decimal.NewFromInt(200),
		Currency:      "USD",
		ChargebackFee: decimal.NewFromInt(15),
	})

	require.NoError(t, err)

	// 验证创建了两个 transaction: CHARGEBACK_RECEIVED 和 CHARGEBACK_FEE
	require.Len(t, ledger.Calls, 2)

	// 第一个 transaction: CHARGEBACK_RECEIVED (本金)
	// settled=0, dispute=200 -> settled debit=0（不生成）, reserve credit=200, negative_balance debit=200
	txn1 := ledger.Calls[0].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, ledgerdomain.TxnTypeChargebackReceived, txn1.TransactionType)
	assert.Len(t, txn1.Entries, 2) // 零余额只需要 2 条：无 settled debit，只有 reserve + negative_balance
	assert.Equal(t, ledgerdomain.DirectionCredit, txn1.Entries[0].Direction)
	assert.Equal(t, reserveID, txn1.Entries[0].AccountID)
	assert.Equal(t, decimal.NewFromInt(200), txn1.Entries[0].Amount)
	assert.Equal(t, ledgerdomain.DirectionDebit, txn1.Entries[1].Direction)
	assert.Equal(t, negID, txn1.Entries[1].AccountID)
	assert.Equal(t, decimal.NewFromInt(200), txn1.Entries[1].Amount)

	// 验证借贷平衡
	assert.True(t, ledgerdomain.ValidateBalanced(txn1.Entries) == nil)

	// 第二个 transaction: CHARGEBACK_FEE (费用)
	// settled=0, fee=15 -> settled debit=0（不生成）, negative_balance debit=15, fee credit=15
	txn2 := ledger.Calls[1].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, ledgerdomain.TxnTypeChargebackFee, txn2.TransactionType)
	assert.Len(t, txn2.Entries, 2) // 零余额只需要 2 条：无 settled debit，只有 negative_balance + fee
	assert.Equal(t, ledgerdomain.DirectionDebit, txn2.Entries[0].Direction)
	assert.Equal(t, negID, txn2.Entries[0].AccountID)
	assert.Equal(t, decimal.NewFromInt(15), txn2.Entries[0].Amount)
	assert.Equal(t, ledgerdomain.DirectionCredit, txn2.Entries[1].Direction)
	assert.Equal(t, feeID, txn2.Entries[1].AccountID)
	assert.Equal(t, decimal.NewFromInt(15), txn2.Entries[1].Amount)

	// 验证借贷平衡
	assert.True(t, ledgerdomain.ValidateBalanced(txn2.Entries) == nil)
}

func TestChargebackReceived_IdempotentReplay(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)
	svc := New(repo, ledger, txm)

	existing := &disputedomain.PaymentDispute{
		ID:             uuid.New(),
		IdempotencyKey: "chargeback-order-4",
	}
	repo.On("GetDisputeByOrderIDAndType", mock.Anything, "order-4", disputedomain.DisputeTypeChargeback).
		Return(existing, nil)

	err := svc.ChargebackReceived(context.Background(), disputedomain.CreateDisputeCmd{
		OrderID:        "order-4",
		MerchantID:     uuid.New(),
		Amount:         decimal.NewFromInt(100),
		Currency:       "USD",
		IdempotencyKey: "chargeback-order-4",
	})

	require.NoError(t, err)
	repo.AssertNotCalled(t, "CreateDispute", mock.Anything, mock.Anything, mock.Anything)
}

func TestChargebackReversed_Success(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)

	txm.On("WithinTx", mock.Anything, mock.Anything).Return(nil)

	svc := New(repo, ledger, txm)

	disputeID := uuid.New()
	merchantID := uuid.New()
	setupMocks(repo)

	dispute := &disputedomain.PaymentDispute{
		ID:         disputeID,
		OrderID:    "order-5",
		MerchantID: merchantID,
		Status:     disputedomain.DisputeStatusOpen,
		Amount:     decimal.NewFromInt(200),
		Currency:   "USD",
	}
	repo.On("GetDisputeByID", mock.Anything, disputeID).Return(dispute, nil)
	repo.On("GetPlatformDisputeReserveBalance", mock.Anything, merchantID, "USD").
		Return(decimal.NewFromInt(300), nil)
	repo.On("UpdateDisputeStatus", mock.Anything, mock.Anything, disputeID, disputedomain.DisputeStatusReversed).Return(nil)
	repo.On("CreatePlatformDisputeReserveEntry", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	ledger.On("CreateTransaction", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := svc.ChargebackReversed(context.Background(), disputedomain.ReverseDisputeCmd{
		DisputeID:      disputeID,
		IdempotencyKey: "reverse-5",
	})

	require.NoError(t, err)

	txn := ledger.Calls[0].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, ledgerdomain.TxnTypeChargebackReversed, txn.TransactionType)
	assert.Len(t, txn.Entries, 2)

	assert.Equal(t, testReserveID, txn.Entries[0].AccountID)
	assert.Equal(t, ledgerdomain.DirectionDebit, txn.Entries[0].Direction)
	assert.Equal(t, decimal.NewFromInt(200), txn.Entries[0].Amount)

	assert.Equal(t, testSettledID, txn.Entries[1].AccountID)
	assert.Equal(t, ledgerdomain.DirectionCredit, txn.Entries[1].Direction)
	assert.Equal(t, decimal.NewFromInt(200), txn.Entries[1].Amount)

	repo.AssertCalled(t, "UpdateDisputeStatus", mock.Anything, mock.Anything, disputeID, disputedomain.DisputeStatusReversed)
}

func TestChargebackReversed_InvalidStatus(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)
	svc := New(repo, ledger, txm)

	dispute := &disputedomain.PaymentDispute{
		ID:     uuid.New(),
		Status: disputedomain.DisputeStatusWon,
	}
	repo.On("GetDisputeByID", mock.Anything, dispute.ID).Return(dispute, nil)

	err := svc.ChargebackReversed(context.Background(), disputedomain.ReverseDisputeCmd{
		DisputeID: dispute.ID,
	})

	require.Error(t, err)
	assert.Equal(t, disputedomain.ErrInvalidDisputeStatus, err)
}

func TestChargebackReversed_InsufficientReserve(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)
	svc := New(repo, ledger, txm)

	merchantID := uuid.New()
	dispute := &disputedomain.PaymentDispute{
		ID:         uuid.New(),
		MerchantID: merchantID,
		Status:     disputedomain.DisputeStatusOpen,
		Amount:     decimal.NewFromInt(200),
		Currency:   "USD",
	}
	repo.On("GetDisputeByID", mock.Anything, dispute.ID).Return(dispute, nil)
	repo.On("GetPlatformDisputeReserveBalance", mock.Anything, merchantID, "USD").
		Return(decimal.NewFromInt(50), nil)

	err := svc.ChargebackReversed(context.Background(), disputedomain.ReverseDisputeCmd{
		DisputeID: dispute.ID,
	})

	require.Error(t, err)
	assert.Equal(t, disputedomain.ErrInsufficientSettled, err)
}

func TestRefundConfirmed_DefaultIdempotencyKey(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)

	txm.On("WithinTx", mock.Anything, mock.Anything).Return(nil)

	svc := New(repo, ledger, txm)

	merchantID := uuid.New()
	settledID := uuid.New()
	acquirerID := uuid.New()

	repo.On("GetDisputeByOrderIDAndType", mock.Anything, "order-6", disputedomain.DisputeTypeRefund).
		Return(nil, disputedomain.ErrDisputeNotFound)
	repo.On("CreateDispute", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "SETTLED", "USD").Return(settledID, nil)
	repo.On("GetOrCreateLedgerAccount", mock.Anything, mock.Anything, mock.Anything, "ACQUIRER_RECEIVABLE", "USD").Return(acquirerID, nil)
	ledger.On("CreateTransaction", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := svc.RefundConfirmed(context.Background(), disputedomain.CreateDisputeCmd{
		OrderID:    "order-6",
		MerchantID: merchantID,
		Amount:     decimal.NewFromInt(100),
		Currency:   "USD",
	})

	require.NoError(t, err)

	txn := ledger.Calls[0].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Equal(t, "refund-order-6", txn.IdempotencyKey)
}

func TestChargebackReceived_ZeroFee(t *testing.T) {
	repo := new(mockDisputeRepo)
	ledger := new(mockLedgerSvc)
	txm := new(mockTxManager)

	txm.On("WithinTx", mock.Anything, mock.Anything).Return(nil)

	svc := New(repo, ledger, txm)

	merchantID := uuid.New()
	setupMocks(repo)

	repo.On("GetDisputeByOrderIDAndType", mock.Anything, "order-7", disputedomain.DisputeTypeChargeback).
		Return(nil, disputedomain.ErrDisputeNotFound)
	repo.On("CreateDispute", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	repo.On("GetMerchantSettledBalance", mock.Anything, merchantID, "USD").
		Return(decimal.NewFromInt(500), nil)
	repo.On("CreatePlatformDisputeReserveEntry", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	ledger.On("CreateTransaction", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := svc.ChargebackReceived(context.Background(), disputedomain.CreateDisputeCmd{
		OrderID:    "order-7",
		MerchantID: merchantID,
		Amount:     decimal.NewFromInt(200),
		Currency:   "USD",
	})

	require.NoError(t, err)

	txn := ledger.Calls[0].Arguments.Get(2).(ledgerdomain.Transaction)
	assert.Len(t, txn.Entries, 2, "zero fee should not add fee entry")

	debitTotal := decimal.Zero
	creditTotal := decimal.Zero
	for _, e := range txn.Entries {
		switch e.Direction {
		case ledgerdomain.DirectionDebit:
			debitTotal = debitTotal.Add(e.Amount)
		case ledgerdomain.DirectionCredit:
			creditTotal = creditTotal.Add(e.Amount)
		}
	}
	assert.True(t, debitTotal.Equal(creditTotal))
}
