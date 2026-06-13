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

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type mockRepository struct {
	mock.Mock
}

func (m *mockRepository) PostTransaction(ctx context.Context, tx pgx.Tx, txn domain.Transaction) (*domain.Transaction, error) {
	args := m.Called(ctx, tx, txn)
	return args.Get(0).(*domain.Transaction), args.Error(1)
}

func (m *mockRepository) GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (*domain.Transaction, error) {
	args := m.Called(ctx, idempotencyKey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Transaction), args.Error(1)
}

func TestGetTransactionByIdempotencyKey_Found(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)

	svc := New(repo, nil, nil)

	txn := &domain.Transaction{
		ID: uuid.New().String(),
	}

	repo.On("GetTransactionByIdempotencyKey", ctx, "key1").Return(txn, nil)

	result, err := svc.GetTransactionByIdempotencyKey(ctx, "key1")

	require.NoError(t, err)
	assert.Equal(t, txn.ID, result.ID)
}

func TestGetTransactionByIdempotencyKey_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)

	svc := New(repo, nil, nil)

	repo.On("GetTransactionByIdempotencyKey", ctx, "key1").Return(nil, nil)

	_, err := svc.GetTransactionByIdempotencyKey(ctx, "key1")

	require.Error(t, err)
	assert.True(t, errors.As(err, &apperr.ErrNotFound))
}

func TestPost_UnbalancedTransaction(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)

	svc := New(repo, nil, nil)

	entries := []domain.Entry{
		{AccountID: "acc1", Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100)},
		{AccountID: "acc2", Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(50)},
	}

	_, err := svc.Post(ctx, PostCmd{
		IdempotencyKey:  "key1",
		TransactionType: domain.TxnTypePayoutFreeze,
		Entries:         entries,
	})

	require.Error(t, err)
}

func TestPost_IdempotentReplay(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)

	svc := New(repo, nil, nil)

	existingTxn := &domain.Transaction{
		ID: uuid.New().String(),
	}

	repo.On("GetTransactionByIdempotencyKey", ctx, "key1").Return(existingTxn, nil)

	entries := []domain.Entry{
		{AccountID: "acc1", Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100)},
		{AccountID: "acc2", Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(100)},
	}

	result, err := svc.Post(ctx, PostCmd{
		IdempotencyKey:  "key1",
		TransactionType: domain.TxnTypePayoutFreeze,
		Entries:         entries,
	})

	require.NoError(t, err)
	assert.Equal(t, existingTxn.ID, result.TransactionID)
	assert.True(t, result.Replayed)
}
