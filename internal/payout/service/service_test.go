package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type mockPayoutRepo struct {
	mock.Mock
}

func (m *mockPayoutRepo) LockBalanceForUpdate(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string) (*repository.Balance, error) {
	args := m.Called(ctx, tx, merchantID, currency)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*repository.Balance), args.Error(1)
}

func (m *mockPayoutRepo) UpdateBalance(ctx context.Context, tx pgx.Tx, b *repository.Balance) error {
	args := m.Called(ctx, tx, b)
	return args.Error(0)
}

func (m *mockPayoutRepo) InsertPayoutRequest(ctx context.Context, tx pgx.Tx, params db.InsertPayoutRequestParams) (*db.PayoutRequest, error) {
	args := m.Called(ctx, tx, params)
	return args.Get(0).(*db.PayoutRequest), args.Error(1)
}

func (m *mockPayoutRepo) GetPayoutRequest(ctx context.Context, id uuid.UUID) (*db.PayoutRequest, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.PayoutRequest), args.Error(1)
}

func (m *mockPayoutRepo) GetPayoutRequestByIdempotencyKey(ctx context.Context, key string) (*db.PayoutRequest, error) {
	args := m.Called(ctx, key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.PayoutRequest), args.Error(1)
}

func (m *mockPayoutRepo) GetPayoutAddress(ctx context.Context, id uuid.UUID) (*db.MerchantPayoutAddress, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.MerchantPayoutAddress), args.Error(1)
}

func (m *mockPayoutRepo) InsertOutboxEvent(ctx context.Context, tx pgx.Tx, params db.InsertOutboxEventParams) (*db.TransactionalOutbox, error) {
	args := m.Called(ctx, tx, params)
	return args.Get(0).(*db.TransactionalOutbox), args.Error(1)
}

func (m *mockPayoutRepo) InsertPayoutAddress(ctx context.Context, params db.InsertPayoutAddressParams) (*db.MerchantPayoutAddress, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*db.MerchantPayoutAddress), args.Error(1)
}

func (m *mockPayoutRepo) InsertReserveBatch(ctx context.Context, params db.InsertReserveBatchParams) (*db.ReserveBatch, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*db.ReserveBatch), args.Error(1)
}

func (m *mockPayoutRepo) ListDueReserveBatches(ctx context.Context, limit int32) ([]db.ReserveBatch, error) {
	args := m.Called(ctx, limit)
	return args.Get(0).([]db.ReserveBatch), args.Error(1)
}

func (m *mockPayoutRepo) UpdateReserveBatchStatus(ctx context.Context, id uuid.UUID, status string) error {
	args := m.Called(ctx, id, status)
	return args.Error(0)
}

type mockLedgerRepo struct {
	mock.Mock
}

func (m *mockLedgerRepo) PostTransaction(ctx context.Context, tx pgx.Tx, txn ledgerdomain.Transaction) (*ledgerdomain.Transaction, error) {
	args := m.Called(ctx, tx, txn)
	return args.Get(0).(*ledgerdomain.Transaction), args.Error(1)
}

func (m *mockLedgerRepo) GetOrCreateAccount(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error) {
	args := m.Called(ctx, tx, merchantID, accountType, currency)
	return args.Get(0).(uuid.UUID), args.Error(1)
}

func (m *mockLedgerRepo) GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (*ledgerdomain.Transaction, error) {
	args := m.Called(ctx, idempotencyKey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ledgerdomain.Transaction), args.Error(1)
}

func TestFreeze_IdempotentReplay(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	ledgerRepo := new(mockLedgerRepo)

	svc := New(payoutRepo, ledgerRepo, nil, nil, nil)

	existingPayout := &db.PayoutRequest{
		ID:     uuid.New(),
		Status: "BalanceFrozen",
	}

	payoutRepo.On("GetPayoutRequestByIdempotencyKey", ctx, "key1").Return(existingPayout, nil)

	result, err := svc.Freeze(ctx, FreezeCmd{
		MerchantID:     uuid.New(),
		Amount:         decimal.NewFromInt(3000),
		FiatCurrency:   "USD",
		IdempotencyKey: "key1",
	})

	require.NoError(t, err)
	assert.Equal(t, existingPayout.ID.String(), result.PayoutRequestID)
	assert.Equal(t, "BalanceFrozen", result.Status)
	assert.True(t, result.Replayed)
}

func TestCreateAddress_InvalidAddress(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	ledgerRepo := new(mockLedgerRepo)

	svc := New(payoutRepo, ledgerRepo, nil, nil, nil)

	_, err := svc.CreateAddress(ctx, CreateAddressCmd{
		MerchantID:    uuid.New(),
		TargetAsset:   "USDT",
		TargetNetwork: "TRON_TRC20",
		TargetAddress: "",
	})

	require.Error(t, err)
	assert.True(t, errors.As(err, &apperr.ErrInvalidArgument))
}

func TestCreatePayoutRequest_Idempotent(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	ledgerRepo := new(mockLedgerRepo)

	svc := New(payoutRepo, ledgerRepo, nil, nil, nil)

	existingReq := &db.PayoutRequest{
		ID: uuid.New(),
	}

	payoutRepo.On("GetPayoutRequestByIdempotencyKey", ctx, "key1").Return(existingReq, nil)

	result, err := svc.CreatePayoutRequest(ctx, CreateRequestCmd{
		IdempotencyKey: "key1",
	})

	require.NoError(t, err)
	assert.Equal(t, existingReq.ID, result)
}

// G8/R2-1: 同 Idempotency-Key + 不同 payload → 409 冲突。
func TestCreatePayoutRequest_PayloadConflict(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	ledgerRepo := new(mockLedgerRepo)

	svc := New(payoutRepo, ledgerRepo, nil, nil, nil)

	existingReq := &db.PayoutRequest{ID: uuid.New(), PayloadHash: "hash-of-original"}
	payoutRepo.On("GetPayoutRequestByIdempotencyKey", ctx, "key1").Return(existingReq, nil)

	// 同 key，但 payload hash 不同 → 必须 409，且不得返回 existing.ID
	result, err := svc.CreatePayoutRequest(ctx, CreateRequestCmd{
		IdempotencyKey: "key1",
		PayloadHash:    "hash-of-DIFFERENT-body",
	})

	require.Error(t, err)
	assert.Equal(t, uuid.Nil, result)
	appErr, ok := apperr.AsAPIError(err)
	require.True(t, ok)
	assert.Equal(t, apperr.ErrIdempotencyConflict.Code, appErr.Code)
	assert.Equal(t, apperr.ErrIdempotencyConflict.HTTP, appErr.HTTP)

	// 同 key + 同 payload → 幂等返回原 ID，不报错
	result2, err2 := svc.CreatePayoutRequest(ctx, CreateRequestCmd{
		IdempotencyKey: "key1",
		PayloadHash:    "hash-of-original",
	})
	require.NoError(t, err2)
	assert.Equal(t, existingReq.ID, result2)
}

func TestReserveHold_Idempotent(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	ledgerRepo := new(mockLedgerRepo)

	svc := New(payoutRepo, ledgerRepo, nil, nil, nil)

	ledgerRepo.On("GetTransactionByIdempotencyKey", ctx, "hold-key").Return(&ledgerdomain.Transaction{}, nil)

	result, err := svc.ReserveHold(ctx, ReserveHoldCmd{
		IdempotencyKey: "hold-key",
	})

	require.NoError(t, err)
	assert.True(t, result.Replayed)
}

func TestReserveRelease_Idempotent(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	ledgerRepo := new(mockLedgerRepo)

	svc := New(payoutRepo, ledgerRepo, nil, nil, nil)

	ledgerRepo.On("GetTransactionByIdempotencyKey", ctx, "release-key").Return(&ledgerdomain.Transaction{}, nil)

	result, err := svc.ReserveRelease(ctx, ReserveReleaseCmd{
		IdempotencyKey: "release-key",
	})

	require.NoError(t, err)
	assert.True(t, result.Replayed)
}
