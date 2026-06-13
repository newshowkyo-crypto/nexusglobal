package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/ledger/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/money"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) PostTransaction(ctx context.Context, tx pgx.Tx, txn domain.Transaction) (*domain.Transaction, error) {
	q := db.New(r.pool).WithTx(tx)

	aggUUID, _ := uuid.Parse(txn.AggregateID)
	insertTxn, err := q.InsertLedgerTransaction(ctx, db.InsertLedgerTransactionParams{
		IdempotencyKey:  txn.IdempotencyKey,
		TransactionType: string(txn.TransactionType),
		AggregateType:   txn.AggregateType,
		AggregateID:     pgtype.UUID{Bytes: aggUUID, Valid: true},
		Description:     txn.Description,
	})
	if err != nil {
		return nil, err
	}

	for _, entry := range txn.Entries {
		accUUID, _ := uuid.Parse(entry.AccountID)
		num, err := decimalToNumeric(entry.Amount)
		if err != nil {
			return nil, err
		}
		_, err = q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{
			TransactionID: insertTxn.ID,
			AccountID:     accUUID,
			Direction:     string(entry.Direction),
			Amount:        num,
		})
		if err != nil {
			return nil, err
		}
	}

	return &domain.Transaction{
		ID:              insertTxn.ID.String(),
		IdempotencyKey:  insertTxn.IdempotencyKey,
		TransactionType: domain.TransactionType(insertTxn.TransactionType),
		AggregateType:   insertTxn.AggregateType,
		AggregateID:     txn.AggregateID,
		Description:     insertTxn.Description,
		Entries:         txn.Entries,
	}, nil
}

func (r *Repository) GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (*domain.Transaction, error) {
	q := db.New(r.pool)
	t, err := q.GetLedgerTransactionByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &domain.Transaction{
		ID:              t.ID.String(),
		IdempotencyKey:  t.IdempotencyKey,
		TransactionType: domain.TransactionType(t.TransactionType),
		AggregateType:   t.AggregateType,
	}, nil
}

// decimalToNumeric converts a decimal to pgtype.Numeric for DB writes.
// Delegates to money.DecimalToNumeric (StringFixed(4)); a previous hand-rolled
// version negated the exponent and corrupted every fractional amount.
func decimalToNumeric(d decimal.Decimal) (pgtype.Numeric, error) {
	return money.DecimalToNumeric(d), nil
}
