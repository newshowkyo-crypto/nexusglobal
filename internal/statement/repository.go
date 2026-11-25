package statement

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

// Repository 对账单持久化。
type Repository struct {
	pool *pgxpool.Pool
	txm  *postgres.TxManager
}

func NewRepository(pool *pgxpool.Pool, txm *postgres.TxManager) *Repository {
	return &Repository{pool: pool, txm: txm}
}

// Activity 表示某商户某币种在营业日有需要入账的数据。
type Activity struct {
	MerchantID uuid.UUID
	Currency   string
}

type paymentRow struct {
	ID              uuid.UUID
	MerchantOrderID string
	AmountMinor     int64
	OccurredAt      time.Time
}

type refundRow struct {
	ID              uuid.UUID
	MerchantOrderID string
	AmountMinor     int64
	OccurredAt      time.Time
}

type settlementRow struct {
	ID          uuid.UUID
	AmountMinor int64
	OccurredAt  time.Time
}

// ListActivity 返回当日有支付/退款/结算的 (merchant, currency) 列表。
func (r *Repository) ListActivity(ctx context.Context, day time.Time) ([]Activity, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT merchant_id, currency FROM payment_attempts
		WHERE captured_at >= $1::date AND captured_at < $1::date + interval '1 day'
		  AND status IN ('CAPTURED', 'SETTLED', 'PARTIALLY_REFUNDED', 'REFUNDED')
		UNION
		SELECT merchant_id, currency FROM refund_request
		WHERE succeeded_at >= $1::date AND succeeded_at < $1::date + interval '1 day'
		  AND status = 'SUCCEEDED'
		UNION
		SELECT merchant_id, currency FROM settlement_batch WHERE business_date = $1::date
	`, day)
	if err != nil {
		return nil, fmt.Errorf("statement: list activity: %w", err)
	}
	defer rows.Close()

	var out []Activity
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.MerchantID, &a.Currency); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Generate 为单个 (merchant, currency, day) 生成或重建对账单。
func (r *Repository) Generate(ctx context.Context, merchantID uuid.UUID, currency string, day time.Time) (*Statement, error) {
	var st *Statement
	err := r.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		payments, err := loadPayments(ctx, tx, merchantID, currency, day)
		if err != nil {
			return err
		}
		refunds, err := loadRefunds(ctx, tx, merchantID, currency, day)
		if err != nil {
			return err
		}
		settlements, err := loadSettlements(ctx, tx, merchantID, currency, day)
		if err != nil {
			return err
		}
		feeAmount, err := loadFees(ctx, tx, merchantID, currency, day)
		if err != nil {
			return err
		}

		var paymentAmount, refundAmount, settledAmount int64
		for _, p := range payments {
			paymentAmount += p.AmountMinor
		}
		for _, f := range refunds {
			refundAmount += f.AmountMinor
		}
		for _, s := range settlements {
			settledAmount += s.AmountMinor
		}

		var id uuid.UUID
		var generatedAt time.Time
		err = tx.QueryRow(ctx, `
			INSERT INTO settlement_statement
			    (tenant_id, merchant_id, currency, business_date, status,
			     payment_count, payment_amount_minor, refund_count, refund_amount_minor,
			     fee_amount_minor, net_amount_minor, settled_amount_minor, generated_at)
			VALUES ($1, $1, $2, $3::date, 'READY', $4, $5, $6, $7, $8, $9, $10, now())
			ON CONFLICT (merchant_id, currency, business_date) DO UPDATE SET
			    status = 'READY',
			    payment_count = EXCLUDED.payment_count,
			    payment_amount_minor = EXCLUDED.payment_amount_minor,
			    refund_count = EXCLUDED.refund_count,
			    refund_amount_minor = EXCLUDED.refund_amount_minor,
			    fee_amount_minor = EXCLUDED.fee_amount_minor,
			    net_amount_minor = EXCLUDED.net_amount_minor,
			    settled_amount_minor = EXCLUDED.settled_amount_minor,
			    generated_at = now()
			RETURNING id, generated_at
		`, merchantID, currency, day, len(payments), paymentAmount,
			len(refunds), refundAmount, feeAmount, paymentAmount-refundAmount, settledAmount).Scan(&id, &generatedAt)
		if err != nil {
			return fmt.Errorf("statement: upsert header: %w", err)
		}

		if _, err := tx.Exec(ctx, `DELETE FROM settlement_statement_line WHERE statement_id = $1`, id); err != nil {
			return fmt.Errorf("statement: clear lines: %w", err)
		}

		for _, p := range payments {
			if _, err := tx.Exec(ctx, `
				INSERT INTO settlement_statement_line
				    (statement_id, line_type, reference_id, merchant_order_id, amount_minor, currency, occurred_at, description)
				VALUES ($1, 'PAYMENT', $2, $3, $4, $5, $6, 'payment captured')
			`, id, p.ID, p.MerchantOrderID, p.AmountMinor, currency, p.OccurredAt); err != nil {
				return fmt.Errorf("statement: insert payment line: %w", err)
			}
		}
		for _, f := range refunds {
			if _, err := tx.Exec(ctx, `
				INSERT INTO settlement_statement_line
				    (statement_id, line_type, reference_id, merchant_order_id, amount_minor, currency, occurred_at, description)
				VALUES ($1, 'REFUND', $2, $3, $4, $5, $6, 'refund succeeded')
			`, id, f.ID, f.MerchantOrderID, f.AmountMinor, currency, f.OccurredAt); err != nil {
				return fmt.Errorf("statement: insert refund line: %w", err)
			}
		}
		for _, s := range settlements {
			if _, err := tx.Exec(ctx, `
				INSERT INTO settlement_statement_line
				    (statement_id, line_type, reference_id, merchant_order_id, amount_minor, currency, occurred_at, description)
				VALUES ($1, 'SETTLEMENT', $2, '', $3, $4, $5, 'settlement batch')
			`, id, s.ID, s.AmountMinor, currency, s.OccurredAt); err != nil {
				return fmt.Errorf("statement: insert settlement line: %w", err)
			}
		}

		st = &Statement{
			ID: id, TenantID: merchantID, MerchantID: merchantID,
			Currency: currency, BusinessDate: day.Format("2006-01-02"), Status: "READY",
			PaymentCount: len(payments), PaymentAmountMinor: paymentAmount,
			RefundCount: len(refunds), RefundAmountMinor: refundAmount,
			FeeAmountMinor: feeAmount,
			NetAmountMinor: paymentAmount - refundAmount, SettledAmountMinor: settledAmount,
			GeneratedAt: generatedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return st, nil
}

func loadPayments(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, day time.Time) ([]paymentRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, merchant_order_id, amount_minor, captured_at
		FROM payment_attempts
		WHERE merchant_id = $1 AND currency = $2
		  AND captured_at >= $3::date AND captured_at < $3::date + interval '1 day'
		  AND status IN ('CAPTURED', 'SETTLED', 'PARTIALLY_REFUNDED', 'REFUNDED')
		ORDER BY captured_at
	`, merchantID, currency, day)
	if err != nil {
		return nil, fmt.Errorf("statement: load payments: %w", err)
	}
	defer rows.Close()

	var out []paymentRow
	for rows.Next() {
		var p paymentRow
		if err := rows.Scan(&p.ID, &p.MerchantOrderID, &p.AmountMinor, &p.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func loadRefunds(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, day time.Time) ([]refundRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id, COALESCE(a.merchant_order_id, ''), r.amount_minor, r.succeeded_at
		FROM refund_request r
		JOIN payment_attempts a ON a.id = r.payment_attempt_id
		WHERE r.merchant_id = $1 AND r.currency = $2 AND r.status = 'SUCCEEDED'
		  AND r.succeeded_at >= $3::date AND r.succeeded_at < $3::date + interval '1 day'
		ORDER BY r.succeeded_at
	`, merchantID, currency, day)
	if err != nil {
		return nil, fmt.Errorf("statement: load refunds: %w", err)
	}
	defer rows.Close()

	var out []refundRow
	for rows.Next() {
		var f refundRow
		if err := rows.Scan(&f.ID, &f.MerchantOrderID, &f.AmountMinor, &f.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func loadSettlements(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, day time.Time) ([]settlementRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, total_amount_minor, COALESCE(settled_at, created_at)
		FROM settlement_batch
		WHERE merchant_id = $1 AND currency = $2 AND business_date = $3::date
		ORDER BY created_at
	`, merchantID, currency, day)
	if err != nil {
		return nil, fmt.Errorf("statement: load settlements: %w", err)
	}
	defer rows.Close()

	var out []settlementRow
	for rows.Next() {
		var s settlementRow
		if err := rows.Scan(&s.ID, &s.AmountMinor, &s.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// loadFees 汇总当日捕获订单产生的手续费。
func loadFees(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, day time.Time) (int64, error) {
	var total int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(f.fee_amount_minor), 0)::bigint
		FROM fee_record f
		JOIN payment_attempts a ON a.id = f.payment_attempt_id
		WHERE a.merchant_id = $1 AND a.currency = $2
		  AND a.captured_at >= $3::date AND a.captured_at < $3::date + interval '1 day'
	`, merchantID, currency, day).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("statement: load fees: %w", err)
	}
	return total, nil
}

// GetByID 按 ID 查询对账单（不做商户归属过滤，仅供签名下载校验后使用）。
func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*Statement, error) {
	var s Statement
	var day time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, merchant_id, currency, business_date, status,
		       payment_count, payment_amount_minor, refund_count, refund_amount_minor,
		       fee_amount_minor, net_amount_minor, settled_amount_minor, generated_at
		FROM settlement_statement WHERE id = $1
	`, id).Scan(&s.ID, &s.TenantID, &s.MerchantID, &s.Currency, &day, &s.Status,
		&s.PaymentCount, &s.PaymentAmountMinor, &s.RefundCount, &s.RefundAmountMinor,
		&s.FeeAmountMinor, &s.NetAmountMinor, &s.SettledAmountMinor, &s.GeneratedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.BusinessDate = day.Format("2006-01-02")
	return &s, nil
}

// Get 查询对账单。
func (r *Repository) Get(ctx context.Context, merchantID, id uuid.UUID) (*Statement, error) {
	var s Statement
	var day time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, merchant_id, currency, business_date, status,
		       payment_count, payment_amount_minor, refund_count, refund_amount_minor,
		       fee_amount_minor, net_amount_minor, settled_amount_minor, generated_at
		FROM settlement_statement WHERE merchant_id = $1 AND id = $2
	`, merchantID, id).Scan(&s.ID, &s.TenantID, &s.MerchantID, &s.Currency, &day, &s.Status,
		&s.PaymentCount, &s.PaymentAmountMinor, &s.RefundCount, &s.RefundAmountMinor,
		&s.FeeAmountMinor, &s.NetAmountMinor, &s.SettledAmountMinor, &s.GeneratedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.BusinessDate = day.Format("2006-01-02")
	return &s, nil
}

// List 查询商户对账单（可按日期过滤）。
func (r *Repository) List(ctx context.Context, merchantID uuid.UUID, day string, limit int) ([]Statement, error) {
	query := `
		SELECT id, tenant_id, merchant_id, currency, business_date, status,
		       payment_count, payment_amount_minor, refund_count, refund_amount_minor,
		       fee_amount_minor, net_amount_minor, settled_amount_minor, generated_at
		FROM settlement_statement WHERE merchant_id = $1`
	args := []any{merchantID}
	if day != "" {
		query += ` AND business_date = $2::date`
		args = append(args, day)
	}
	query += ` ORDER BY business_date DESC, currency LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Statement
	for rows.Next() {
		var s Statement
		var d time.Time
		if err := rows.Scan(&s.ID, &s.TenantID, &s.MerchantID, &s.Currency, &d, &s.Status,
			&s.PaymentCount, &s.PaymentAmountMinor, &s.RefundCount, &s.RefundAmountMinor,
			&s.FeeAmountMinor, &s.NetAmountMinor, &s.SettledAmountMinor, &s.GeneratedAt); err != nil {
			return nil, err
		}
		s.BusinessDate = d.Format("2006-01-02")
		out = append(out, s)
	}
	return out, rows.Err()
}

// Lines 返回对账单明细。
func (r *Repository) Lines(ctx context.Context, statementID uuid.UUID) ([]Line, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, statement_id, line_type, reference_id, merchant_order_id,
		       amount_minor, currency, occurred_at, description
		FROM settlement_statement_line WHERE statement_id = $1
		ORDER BY occurred_at, line_type
	`, statementID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Line
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.StatementID, &l.LineType, &l.ReferenceID, &l.MerchantOrderID,
			&l.AmountMinor, &l.Currency, &l.OccurredAt, &l.Description); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
