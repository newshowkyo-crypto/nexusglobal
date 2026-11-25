// Package query 提供商户自助查询：订单、支付尝试、退款汇总。
package query

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AttemptView 支付尝试视图。
type AttemptView struct {
	AttemptID         uuid.UUID  `json:"attempt_id"`
	Status            string     `json:"status"`
	EntryChannel      string     `json:"entry_channel"`
	AmountMinor       int64      `json:"amount_minor"`
	Currency          string     `json:"currency"`
	ProviderRef       string     `json:"provider_ref"`
	FailureCode       string     `json:"failure_code"`
	CreatedAt         time.Time  `json:"created_at"`
	CapturedAt        *time.Time `json:"captured_at"`
	RefundedAmountMin int64      `json:"refunded_amount_minor"`
}

// OrderView 订单视图。
type OrderView struct {
	OrderID         uuid.UUID     `json:"order_id"`
	MerchantOrderID string        `json:"merchant_order_id"`
	AmountMinor     int64         `json:"amount_minor"`
	Currency        string        `json:"currency"`
	Status          string        `json:"status"`
	SiteID          string        `json:"site_id"`
	CreatedAt       time.Time     `json:"created_at"`
	Attempts        []AttemptView `json:"attempts"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

var ErrNotFound = errors.New("query: not found")

// OrderByMerchantOrderID 按商户订单号查询订单及其实例。
func (r *Repository) OrderByMerchantOrderID(ctx context.Context, merchantID uuid.UUID, merchantOrderID string) (*OrderView, error) {
	var o OrderView
	err := r.pool.QueryRow(ctx, `
		SELECT id, merchant_order_id, amount_minor, currency, status, COALESCE(site_id, ''), created_at
		FROM checkout_orders WHERE merchant_id = $1 AND merchant_order_id = $2
	`, merchantID, merchantOrderID).Scan(&o.OrderID, &o.MerchantOrderID, &o.AmountMinor,
		&o.Currency, &o.Status, &o.SiteID, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query: order: %w", err)
	}

	attempts, err := r.attemptsByOrder(ctx, merchantID, o.OrderID)
	if err != nil {
		return nil, err
	}
	o.Attempts = attempts
	return &o, nil
}

func (r *Repository) attemptsByOrder(ctx context.Context, merchantID, orderID uuid.UUID) ([]AttemptView, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.status, a.entry_channel, a.amount_minor, a.currency,
		       COALESCE(a.provider_ref, ''), COALESCE(a.failure_code, ''), a.created_at, a.captured_at
		FROM payment_attempts a
		WHERE a.merchant_id = $1 AND a.order_id = $2
		ORDER BY a.created_at
	`, merchantID, orderID)
	if err != nil {
		return nil, fmt.Errorf("query: attempts by order: %w", err)
	}
	defer rows.Close()

	var out []AttemptView
	for rows.Next() {
		var a AttemptView
		if err := rows.Scan(&a.AttemptID, &a.Status, &a.EntryChannel, &a.AmountMinor, &a.Currency,
			&a.ProviderRef, &a.FailureCode, &a.CreatedAt, &a.CapturedAt); err != nil {
			return nil, err
		}
		refunded, err := r.refundedTotal(ctx, a.AttemptID)
		if err != nil {
			return nil, err
		}
		a.RefundedAmountMin = refunded
		out = append(out, a)
	}
	return out, rows.Err()
}

// Attempt 按 ID 查询支付尝试。
func (r *Repository) Attempt(ctx context.Context, merchantID, attemptID uuid.UUID) (*AttemptView, error) {
	var a AttemptView
	err := r.pool.QueryRow(ctx, `
		SELECT id, status, entry_channel, amount_minor, currency,
		       COALESCE(provider_ref, ''), COALESCE(failure_code, ''), created_at, captured_at
		FROM payment_attempts WHERE merchant_id = $1 AND id = $2
	`, merchantID, attemptID).Scan(&a.AttemptID, &a.Status, &a.EntryChannel, &a.AmountMinor,
		&a.Currency, &a.ProviderRef, &a.FailureCode, &a.CreatedAt, &a.CapturedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query: attempt: %w", err)
	}
	refunded, err := r.refundedTotal(ctx, a.AttemptID)
	if err != nil {
		return nil, err
	}
	a.RefundedAmountMin = refunded
	return &a, nil
}

// ListAttempts 列出商户交易（可按状态过滤）。
func (r *Repository) ListAttempts(ctx context.Context, merchantID uuid.UUID, status string, limit, offset int) ([]AttemptView, error) {
	query := `
		SELECT id, status, entry_channel, amount_minor, currency,
		       COALESCE(provider_ref, ''), COALESCE(failure_code, ''), created_at, captured_at
		FROM payment_attempts WHERE merchant_id = $1`
	args := []any{merchantID}
	if status != "" {
		query += ` AND status = $2`
		args = append(args, status)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: list attempts: %w", err)
	}
	defer rows.Close()

	var out []AttemptView
	for rows.Next() {
		var a AttemptView
		if err := rows.Scan(&a.AttemptID, &a.Status, &a.EntryChannel, &a.AmountMinor, &a.Currency,
			&a.ProviderRef, &a.FailureCode, &a.CreatedAt, &a.CapturedAt); err != nil {
			return nil, err
		}
		refunded, err := r.refundedTotal(ctx, a.AttemptID)
		if err != nil {
			return nil, err
		}
		a.RefundedAmountMin = refunded
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) refundedTotal(ctx context.Context, attemptID uuid.UUID) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_minor), 0)::bigint
		FROM refund_request WHERE payment_attempt_id = $1 AND status = 'SUCCEEDED'
	`, attemptID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("query: refunded total: %w", err)
	}
	return total, nil
}
