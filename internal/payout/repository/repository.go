package repository

import (
	"context"
	"errors"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerdb "github.com/nexusglobal/mamr/internal/ledger/repository/db"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type Balance struct {
	ID                     uuid.UUID
	MerchantID             uuid.UUID
	Currency               string
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
		SettledBalance:         numericToDecimal(row.SettledBalance),
		FrozenPayoutBalance:    numericToDecimal(row.FrozenPayoutBalance),
		ReserveBalance:         numericToDecimal(row.ReserveBalance),
		DisputeHoldBalance:     numericToDecimal(row.DisputeHoldBalance),
		AvailablePayoutBalance: numericToDecimal(row.AvailablePayoutBalance),
		Version:                row.Version,
	}, nil
}

func (r *Repository) UpdateBalance(ctx context.Context, tx pgx.Tx, b *Balance) error {
	q := db.New(r.pool).WithTx(tx)
	return q.UpdateBalance(ctx, db.UpdateBalanceParams{
		ID:                     b.ID,
		PendingBalance:         pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true},
		SettledBalance:         decimalToNumeric(b.SettledBalance),
		ReserveBalance:         decimalToNumeric(b.ReserveBalance),
		FrozenPayoutBalance:    decimalToNumeric(b.FrozenPayoutBalance),
		DisputeHoldBalance:     decimalToNumeric(b.DisputeHoldBalance),
		AvailablePayoutBalance: decimalToNumeric(b.AvailablePayoutBalance),
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

func numericToDecimal(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid {
		return decimal.Zero
	}
	d, _ := decimal.NewFromString(n.Int.String())
	if n.Exp < 0 {
		divisor := decimal.NewFromInt(1)
		for i := int32(0); i < -n.Exp; i++ {
			divisor = divisor.Mul(decimal.NewFromInt(10))
		}
		return d.Div(divisor)
	}
	multiplier := decimal.NewFromInt(1)
	for i := int32(0); i < n.Exp; i++ {
		multiplier = multiplier.Mul(decimal.NewFromInt(10))
	}
	return d.Mul(multiplier)
}

func decimalToNumeric(d decimal.Decimal) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(d.StringFixed(4))
	return n
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

	aggUUID, _ := uuid.Parse(txn.AggregateID)
	insertTxn, err := q.InsertLedgerTransaction(ctx, ledgerdb.InsertLedgerTransactionParams{
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
		num := decimalToNumeric(entry.Amount)
		_, err := q.InsertLedgerEntry(ctx, ledgerdb.InsertLedgerEntryParams{
			TransactionID: insertTxn.ID,
			AccountID:     accUUID,
			Direction:     string(entry.Direction),
			Amount:        num,
		})
		if err != nil {
			return nil, err
		}
	}

	return &ledgerdomain.Transaction{
		ID:              insertTxn.ID.String(),
		IdempotencyKey:  insertTxn.IdempotencyKey,
		TransactionType: ledgerdomain.TransactionType(insertTxn.TransactionType),
		AggregateType:   insertTxn.AggregateType,
		AggregateID:     txn.AggregateID,
		Description:     insertTxn.Description,
		Entries:         txn.Entries,
	}, nil
}
