package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/dispute/domain"
	"github.com/nexusglobal/mamr/internal/ledger/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/money"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateDispute(ctx context.Context, tx pgx.Tx, dispute *domain.PaymentDispute) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO payment_dispute (
			id, order_id, merchant_id, dispute_type, status, amount, currency,
			reason, provider_dispute_id, chargeback_fee, idempotency_key, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		dispute.ID, dispute.OrderID, dispute.MerchantID, dispute.DisputeType,
		dispute.Status, dispute.Amount, dispute.Currency, dispute.Reason,
		dispute.ProviderDisputeID, dispute.ChargebackFee, dispute.IdempotencyKey,
		dispute.CreatedAt, dispute.UpdatedAt)
	return err
}

func (r *Repository) GetDisputeByID(ctx context.Context, id uuid.UUID) (*domain.PaymentDispute, error) {
	var dispute domain.PaymentDispute
	err := r.pool.QueryRow(ctx,
		`SELECT id, order_id, merchant_id, dispute_type, status, amount, currency,
			reason, provider_dispute_id, chargeback_fee, idempotency_key, created_at, updated_at
		FROM payment_dispute WHERE id = $1`, id).Scan(
		&dispute.ID, &dispute.OrderID, &dispute.MerchantID, &dispute.DisputeType,
		&dispute.Status, &dispute.Amount, &dispute.Currency, &dispute.Reason,
		&dispute.ProviderDisputeID, &dispute.ChargebackFee, &dispute.IdempotencyKey,
		&dispute.CreatedAt, &dispute.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, domain.ErrDisputeNotFound
	}
	return &dispute, err
}

func (r *Repository) GetDisputeByIDAndIdempotencyKey(ctx context.Context, id uuid.UUID, idempotencyKey string) (*domain.PaymentDispute, error) {
	var dispute domain.PaymentDispute
	err := r.pool.QueryRow(ctx,
		`SELECT id, order_id, merchant_id, dispute_type, status, amount, currency,
			reason, provider_dispute_id, chargeback_fee, idempotency_key, created_at, updated_at
		FROM payment_dispute WHERE id = $1 AND idempotency_key = $2`, id, idempotencyKey).Scan(
		&dispute.ID, &dispute.OrderID, &dispute.MerchantID, &dispute.DisputeType,
		&dispute.Status, &dispute.Amount, &dispute.Currency, &dispute.Reason,
		&dispute.ProviderDisputeID, &dispute.ChargebackFee, &dispute.IdempotencyKey,
		&dispute.CreatedAt, &dispute.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, domain.ErrDisputeNotFound
	}
	return &dispute, err
}

func (r *Repository) UpdateDisputeStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status domain.DisputeStatus) error {
	_, err := tx.Exec(ctx,
		`UPDATE payment_dispute SET status = $2, updated_at = $3 WHERE id = $1`,
		id, status, time.Now())
	return err
}

func (r *Repository) GetOrCreateLedgerAccount(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error) {
	q := db.New(r.pool).WithTx(tx)

	account, err := q.GetLedgerAccountByMerchant(ctx, db.GetLedgerAccountByMerchantParams{
		MerchantID:  merchantID,
		AccountType: accountType,
		Currency:    currency,
	})
	if err == nil {
		return account.ID, nil
	}

	newAcc, err := q.InsertLedgerAccount(ctx, db.InsertLedgerAccountParams{
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

func (r *Repository) GetMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error) {
	var balance decimal.Decimal
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE WHEN le.direction = 'CREDIT' THEN le.amount ELSE -le.amount END), 0)
		FROM ledger_entry le
		JOIN ledger_account la ON le.account_id = la.id
		WHERE la.merchant_id = $1 AND la.currency = $2 AND la.account_type = 'SETTLED'`,
		merchantID, currency).Scan(&balance)
	return balance, err
}

func (r *Repository) GetPlatformDisputeReserveBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error) {
	var balance decimal.Decimal
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE -amount END), 0)
		FROM platform_dispute_reserve_ledger
		WHERE merchant_id = $1 AND currency = $2`,
		merchantID, currency).Scan(&balance)
	return balance, err
}

func (r *Repository) CreatePlatformDisputeReserveEntry(ctx context.Context, tx pgx.Tx, entry *domain.PlatformDisputeReserveLedger) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO platform_dispute_reserve_ledger (
			id, payment_dispute_id, merchant_id, amount, currency, direction, description, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		entry.ID, entry.PaymentDisputeID, entry.MerchantID, entry.Amount,
		entry.Currency, entry.Direction, entry.Description, entry.CreatedAt)
	return err
}

func (r *Repository) CreateMerchantNegativeBalance(ctx context.Context, tx pgx.Tx, neg *domain.MerchantNegativeBalance) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO merchant_negative_balance (
			id, merchant_id, currency, amount, dispute_id, reason, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (merchant_id, currency, dispute_id) DO NOTHING`,
		neg.ID, neg.MerchantID, neg.Currency, neg.Amount, neg.DisputeID,
		neg.Reason, neg.Status, neg.CreatedAt, neg.UpdatedAt)
	return err
}

func (r *Repository) GetMerchantNegativeBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error) {
	var balance decimal.Decimal
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0)
		FROM merchant_negative_balance
		WHERE merchant_id = $1 AND currency = $2 AND status = 'ACTIVE'`,
		merchantID, currency).Scan(&balance)
	return balance, err
}

func (r *Repository) GetDisputeByOrderIDAndType(ctx context.Context, orderID string, disputeType domain.DisputeType) (*domain.PaymentDispute, error) {
	var dispute domain.PaymentDispute
	err := r.pool.QueryRow(ctx,
		`SELECT id, order_id, merchant_id, dispute_type, status, amount, currency,
			reason, provider_dispute_id, chargeback_fee, idempotency_key, created_at, updated_at
		FROM payment_dispute WHERE order_id = $1 AND dispute_type = $2 ORDER BY created_at DESC LIMIT 1`,
		orderID, disputeType).Scan(
		&dispute.ID, &dispute.OrderID, &dispute.MerchantID, &dispute.DisputeType,
		&dispute.Status, &dispute.Amount, &dispute.Currency, &dispute.Reason,
		&dispute.ProviderDisputeID, &dispute.ChargebackFee, &dispute.IdempotencyKey,
		&dispute.CreatedAt, &dispute.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, domain.ErrDisputeNotFound
	}
	return &dispute, err
}

func decimalToNumeric(d decimal.Decimal) pgtype.Numeric {
	return money.DecimalToNumeric(d)
}
