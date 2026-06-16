package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerdb "github.com/nexusglobal/mamr/internal/ledger/repository/db"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/money"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Pool() *pgxpool.Pool {
	return r.pool
}

type Balance struct {
	ID                     uuid.UUID
	MerchantID             uuid.UUID
	Currency               string
	PendingBalance         decimal.Decimal
	SettledBalance         decimal.Decimal
	FrozenPayoutBalance    decimal.Decimal
	ReserveBalance         decimal.Decimal
	DisputeHoldBalance     decimal.Decimal
	AvailablePayoutBalance decimal.Decimal
	Version                int32
}

func (r *Repository) LockBalanceForUpdate(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string) (*Balance, error) {
	q := db.New(r.pool).WithTx(tx)
	row, err := q.LockBalanceForUpdate(ctx, db.LockBalanceForUpdateParams{
		MerchantID: merchantID,
		Currency:   currency,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &Balance{
		ID:                     row.ID,
		MerchantID:             row.MerchantID,
		Currency:               row.Currency,
		PendingBalance:         money.NumericToDecimal(row.PendingBalance),
		SettledBalance:         money.NumericToDecimal(row.SettledBalance),
		FrozenPayoutBalance:    money.NumericToDecimal(row.FrozenPayoutBalance),
		ReserveBalance:         money.NumericToDecimal(row.ReserveBalance),
		DisputeHoldBalance:     money.NumericToDecimal(row.DisputeHoldBalance),
		AvailablePayoutBalance: money.NumericToDecimal(row.AvailablePayoutBalance),
		Version:                row.Version,
	}, nil
}

func (r *Repository) UpdateBalance(ctx context.Context, tx pgx.Tx, b *Balance) error {
	q := db.New(r.pool).WithTx(tx)
	return q.UpdateBalance(ctx, db.UpdateBalanceParams{
		ID:                     b.ID,
		PendingBalance:         money.DecimalToNumeric(b.PendingBalance),
		SettledBalance:         money.DecimalToNumeric(b.SettledBalance),
		ReserveBalance:         money.DecimalToNumeric(b.ReserveBalance),
		FrozenPayoutBalance:    money.DecimalToNumeric(b.FrozenPayoutBalance),
		DisputeHoldBalance:     money.DecimalToNumeric(b.DisputeHoldBalance),
		AvailablePayoutBalance: money.DecimalToNumeric(b.AvailablePayoutBalance),
		Version:                b.Version,
	})
}

func (r *Repository) InsertPayoutRequest(ctx context.Context, tx pgx.Tx, params db.InsertPayoutRequestParams) (*db.PayoutRequest, error) {
	q := db.New(r.pool).WithTx(tx)
	row, err := q.InsertPayoutRequest(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) InsertPayoutAddress(ctx context.Context, params db.InsertPayoutAddressParams) (*db.MerchantPayoutAddress, error) {
	q := db.New(r.pool)
	row, err := q.InsertPayoutAddress(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetPayoutAddress(ctx context.Context, id uuid.UUID) (*db.MerchantPayoutAddress, error) {
	q := db.New(r.pool)
	row, err := q.GetPayoutAddress(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetPayoutRequest(ctx context.Context, id uuid.UUID) (*db.PayoutRequest, error) {
	q := db.New(r.pool)
	row, err := q.GetPayoutRequest(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) LockPayoutRequestForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*db.PayoutRequest, error) {
	q := db.New(r.pool).WithTx(tx)
	row, err := q.LockPayoutRequestForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ListPayoutRequestsByStatus(ctx context.Context, status string, limit int32) ([]db.PayoutRequest, error) {
	q := db.New(r.pool)
	rows, err := q.ListPayoutRequestsByStatus(ctx, db.ListPayoutRequestsByStatusParams{
		Status: status,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *Repository) GetPayoutRequestByIdempotencyKey(ctx context.Context, key string) (*db.PayoutRequest, error) {
	q := db.New(r.pool)
	row, err := q.GetPayoutRequestByIdempotencyKey(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) UpdatePayoutRequestStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, version int32) error {
	q := db.New(r.pool).WithTx(tx)
	return q.UpdatePayoutRequestStatus(ctx, db.UpdatePayoutRequestStatusParams{
		ID:      id,
		Status:  status,
		Version: version,
	})
}

func (r *Repository) UpdatePayoutRequestLockID(ctx context.Context, tx pgx.Tx, id uuid.UUID, lockID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE payout_request SET lock_id = $1, updated_at = now() WHERE id = $2`,
		lockID, id)
	return err
}

func (r *Repository) UpdatePayoutRequestProviderRef(ctx context.Context, tx pgx.Tx, id uuid.UUID, providerRef string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE payout_request SET provider_ref = $1, updated_at = now() WHERE id = $2`,
		providerRef, id)
	return err
}

func (r *Repository) InsertOutboxEvent(ctx context.Context, tx pgx.Tx, params db.InsertOutboxEventParams) (*db.TransactionalOutbox, error) {
	q := db.New(r.pool).WithTx(tx)
	row, err := q.InsertOutboxEvent(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ClaimOutboxBatch(ctx context.Context, lockedBy string, limit int32) ([]db.TransactionalOutbox, error) {
	q := db.New(r.pool)
	rows, err := q.ClaimOutboxBatch(ctx, db.ClaimOutboxBatchParams{
		LockedBy: &lockedBy,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *Repository) AckOutboxEvent(ctx context.Context, id uuid.UUID) error {
	q := db.New(r.pool)
	return q.AckOutboxEvent(ctx, id)
}

func (r *Repository) FailOutboxEvent(ctx context.Context, id uuid.UUID) error {
	q := db.New(r.pool)
	return q.FailOutboxEvent(ctx, id)
}

func (r *Repository) UpdateOutboxStatus(ctx context.Context, id uuid.UUID, status string) error {
	q := db.New(r.pool)
	return q.UpdateOutboxStatus(ctx, db.UpdateOutboxStatusParams{
		ID:     id,
		Status: status,
	})
}

func (r *Repository) InsertReserveBatch(ctx context.Context, params db.InsertReserveBatchParams) (*db.ReserveBatch, error) {
	q := db.New(r.pool)
	row, err := q.InsertReserveBatch(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ListDueReserveBatches(ctx context.Context, limit int32) ([]db.ReserveBatch, error) {
	q := db.New(r.pool)
	return q.ListDueReserveBatches(ctx, limit)
}

func (r *Repository) UpdateReserveBatchStatus(ctx context.Context, id uuid.UUID, status string) error {
	q := db.New(r.pool)
	return q.UpdateReserveBatchStatus(ctx, db.UpdateReserveBatchStatusParams{
		ID:     id,
		Status: status,
	})
}

type LedgerRepo struct {
	pool *pgxpool.Pool
}

func NewLedgerRepo(pool *pgxpool.Pool) *LedgerRepo {
	return &LedgerRepo{pool: pool}
}

func (r *LedgerRepo) GetOrCreateAccount(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error) {
	q := ledgerdb.New(r.pool).WithTx(tx)

	account, err := q.GetLedgerAccountByMerchant(ctx, ledgerdb.GetLedgerAccountByMerchantParams{
		MerchantID:  merchantID,
		AccountType: accountType,
		Currency:    currency,
	})
	if err == nil {
		return account.ID, nil
	}

	newAcc, err := q.InsertLedgerAccount(ctx, ledgerdb.InsertLedgerAccountParams{
		MerchantID:  merchantID,
		AccountType: accountType,
		Currency:    currency,
		Description: accountType + " account for " + merchantID.String(),
	})
	if err != nil {
		return uuid.Nil, err
	}
	return newAcc.ID, nil
}

func (r *LedgerRepo) PostTransaction(ctx context.Context, tx pgx.Tx, txn ledgerdomain.Transaction) (*ledgerdomain.Transaction, error) {
	q := ledgerdb.New(r.pool).WithTx(tx)

	aggID, _ := uuid.Parse(txn.AggregateID)
	insertTxn, err := q.InsertLedgerTransaction(ctx, ledgerdb.InsertLedgerTransactionParams{
		IdempotencyKey:  txn.IdempotencyKey,
		TransactionType: string(txn.TransactionType),
		AggregateType:   txn.AggregateType,
		AggregateID:     pgtype.UUID{Bytes: aggID, Valid: aggID != uuid.Nil},
		Description:     txn.Description,
	})
	if err != nil {
		return nil, err
	}

	// 插入 ledger entries
	for _, entry := range txn.Entries {
		amountNumeric := money.DecimalToNumeric(entry.Amount)
		_, err := q.InsertLedgerEntry(ctx, ledgerdb.InsertLedgerEntryParams{
			TransactionID: insertTxn.ID,
			AccountID:     entry.AccountID,
			Direction:     string(entry.Direction),
			Amount:        amountNumeric,
		})
		if err != nil {
			return nil, fmt.Errorf("insert ledger entry failed: %w", err)
		}
	}

	return &ledgerdomain.Transaction{
		ID:              insertTxn.ID,
		IdempotencyKey:  insertTxn.IdempotencyKey,
		TransactionType: ledgerdomain.TransactionType(insertTxn.TransactionType),
		AggregateType:   insertTxn.AggregateType,
		AggregateID:     insertTxn.AggregateID.String(),
		Description:     insertTxn.Description,
	}, nil
}

func (r *LedgerRepo) GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (*ledgerdomain.Transaction, error) {
	q := ledgerdb.New(r.pool)
	row, err := q.GetLedgerTransactionByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &ledgerdomain.Transaction{
		ID:              row.ID,
		IdempotencyKey:  row.IdempotencyKey,
		TransactionType: ledgerdomain.TransactionType(row.TransactionType),
		AggregateType:   row.AggregateType,
		AggregateID:     row.AggregateID.String(),
		Description:     row.Description,
	}, nil
}

func (r *Repository) UpdateReconcileAttempt(ctx context.Context, id uuid.UUID, attempts int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE payout_request
		 SET reconcile_attempts = $1, last_reconcile_at = now(), updated_at = now()
		 WHERE id = $2`,
		attempts, id)
	return err
}

func (r *Repository) MarkManualReview(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE payout_request
		 SET manual_review = true, manual_review_reason = $1, updated_at = now()
		 WHERE id = $2`,
		reason, id)
	return err
}
