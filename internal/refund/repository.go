package refund

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// GetByIdempotency 按幂等键查询（不存在返回 nil）。
func (r *Repository) GetByIdempotency(ctx context.Context, merchantID uuid.UUID, key string) (*Refund, error) {
	var f Refund
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		       amount_minor, currency, status, reason, provider_refund_id, failure_code, failure_reason,
		       version, created_at
		FROM refund_request WHERE merchant_id = $1 AND idempotency_key = $2
	`, merchantID, key).Scan(&f.ID, &f.TenantID, &f.MerchantID, &f.PaymentAttemptID, &f.MerchantRefundID,
		&f.IdempotencyKey, &f.AmountMinor, &f.Currency, &f.Status, &f.Reason,
		&f.ProviderRefundID, &f.FailureCode, &f.FailureReason, &f.Version, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("refund: get by idempotency: %w", err)
	}
	return &f, nil
}

// Get 按商户与 ID 查询。
func (r *Repository) Get(ctx context.Context, merchantID, id uuid.UUID) (*Refund, error) {
	var f Refund
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		       amount_minor, currency, status, reason, provider_refund_id, failure_code, failure_reason,
		       version, created_at
		FROM refund_request WHERE merchant_id = $1 AND id = $2
	`, merchantID, id).Scan(&f.ID, &f.TenantID, &f.MerchantID, &f.PaymentAttemptID, &f.MerchantRefundID,
		&f.IdempotencyKey, &f.AmountMinor, &f.Currency, &f.Status, &f.Reason,
		&f.ProviderRefundID, &f.FailureCode, &f.FailureReason, &f.Version, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRefundNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("refund: get: %w", err)
	}
	return &f, nil
}

// GetByIdempotencyTx 事务内按幂等键查询。
func (r *Repository) GetByIdempotencyTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, key string) (*Refund, error) {
	var f Refund
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		       amount_minor, currency, status, reason, provider_refund_id, failure_code, failure_reason,
		       version, created_at
		FROM refund_request WHERE merchant_id = $1 AND idempotency_key = $2
	`, merchantID, key).Scan(&f.ID, &f.TenantID, &f.MerchantID, &f.PaymentAttemptID, &f.MerchantRefundID,
		&f.IdempotencyKey, &f.AmountMinor, &f.Currency, &f.Status, &f.Reason,
		&f.ProviderRefundID, &f.FailureCode, &f.FailureReason, &f.Version, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("refund: get by idempotency in tx: %w", err)
	}
	return &f, nil
}

// LockAttemptTx 锁定 payment_attempt 并返回退款所需字段。
func (r *Repository) LockAttemptTx(ctx context.Context, tx pgx.Tx, attemptID uuid.UUID) (*AttemptInfo, error) {
	var a AttemptInfo
	err := tx.QueryRow(ctx, `
		SELECT id, merchant_id, acquirer_channel_id, amount_minor, currency, status,
		       COALESCE(provider_ref, ''), COALESCE(provider_transaction_id, ''),
		       COALESCE(provider_idempotency_key, ''), version
		FROM payment_attempts WHERE id = $1 FOR UPDATE
	`, attemptID).Scan(&a.ID, &a.MerchantID, &a.AcquirerChannelID, &a.AmountMinor, &a.Currency,
		&a.Status, &a.ProviderRef, &a.ProviderTxID, &a.ProviderIdempotKey, &a.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttemptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("refund: lock attempt: %w", err)
	}
	return &a, nil
}

// SumActiveRefundsTx 统计已占用退款额度（含在途，防止并发超退）。
func (r *Repository) SumActiveRefundsTx(ctx context.Context, tx pgx.Tx, attemptID uuid.UUID) (int64, error) {
	var total int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_minor), 0)::bigint
		FROM refund_request
		WHERE payment_attempt_id = $1 AND status IN ('PENDING', 'PROCESSING', 'SUCCEEDED', 'UNKNOWN')
	`, attemptID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("refund: sum active refunds: %w", err)
	}
	return total, nil
}

// SumSucceededRefundsTx 统计已成功退款额（用于判定全额/部分退款状态）。
func (r *Repository) SumSucceededRefundsTx(ctx context.Context, tx pgx.Tx, attemptID uuid.UUID) (int64, error) {
	var total int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_minor), 0)::bigint
		FROM refund_request WHERE payment_attempt_id = $1 AND status = 'SUCCEEDED'
	`, attemptID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("refund: sum succeeded refunds: %w", err)
	}
	return total, nil
}

// InsertTx 创建退款单。
func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, f *Refund) error {
	err := tx.QueryRow(ctx, `
		INSERT INTO refund_request
		    (id, tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		     amount_minor, currency, status, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'PENDING', $9)
		RETURNING created_at
	`, f.ID, f.TenantID, f.MerchantID, f.PaymentAttemptID, f.MerchantRefundID, f.IdempotencyKey,
		f.AmountMinor, f.Currency, f.Reason).Scan(&f.CreatedAt)
	if err != nil {
		return fmt.Errorf("refund: insert: %w", err)
	}
	f.Status = StatusPending
	return nil
}

// ClaimTx 把 PENDING 退款置为 PROCESSING，返回是否认领成功。
// 同时返回完整退款与 attempt 信息，供事务外调用上游使用。
func (r *Repository) ClaimTx(ctx context.Context, tx pgx.Tx, refundID uuid.UUID) (*Refund, *AttemptInfo, bool, error) {
	var f Refund
	err := tx.QueryRow(ctx, `
		UPDATE refund_request
		SET status = 'PROCESSING', version = version + 1, updated_at = now()
		WHERE id = $1
		  AND (status = 'PENDING'
		       OR (status = 'PROCESSING' AND updated_at < now() - interval '2 minutes'))
		RETURNING id, tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		          amount_minor, currency, status, reason, provider_refund_id, failure_code, failure_reason,
		          version, created_at
	`, refundID).Scan(&f.ID, &f.TenantID, &f.MerchantID, &f.PaymentAttemptID, &f.MerchantRefundID,
		&f.IdempotencyKey, &f.AmountMinor, &f.Currency, &f.Status, &f.Reason,
		&f.ProviderRefundID, &f.FailureCode, &f.FailureReason, &f.Version, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("refund: claim: %w", err)
	}

	a, err := r.LockAttemptTx(ctx, tx, f.PaymentAttemptID)
	if err != nil {
		return nil, nil, false, err
	}
	return &f, a, true, nil
}

// MarkSucceededTx 标记成功。
func (r *Repository) MarkSucceededTx(ctx context.Context, tx pgx.Tx, refundID uuid.UUID, providerRefundID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE refund_request
		SET status = 'SUCCEEDED', provider_refund_id = $2, failure_code = '', failure_reason = '',
		    succeeded_at = now(), version = version + 1, updated_at = now()
		WHERE id = $1
	`, refundID, providerRefundID)
	if err != nil {
		return fmt.Errorf("refund: mark succeeded: %w", err)
	}
	return nil
}

// MarkFailed 标记失败（明确拒绝）。
func (r *Repository) MarkFailed(ctx context.Context, refundID uuid.UUID, code, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE refund_request
		SET status = 'FAILED', failure_code = $2, failure_reason = $3, version = version + 1, updated_at = now()
		WHERE id = $1
	`, refundID, code, reason)
	return err
}

// ListUnknownForReconcile 列出对账窗口内的 UNKNOWN 退款单。
// 只取创建时间在 maxAge 之内（渠道幂等键保留期内，重放安全）、
// 且已静默 2 分钟以上的（给首次处理与上一轮对账留退避时间）。
func (r *Repository) ListUnknownForReconcile(ctx context.Context, maxAge time.Duration, limit int) ([]uuid.UUID, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM refund_request
		WHERE status = 'UNKNOWN'
		  AND created_at > $1
		  AND updated_at < now() - interval '2 minutes'
		ORDER BY created_at
		LIMIT $2
	`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("refund: list unknown for reconcile: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ClaimUnknownTx 把 UNKNOWN 退款置回 PROCESSING 以便对账重放（并发安全，2 分钟静默保护）。
// 返回完整退款与 attempt 信息，供事务外重放上游使用。
func (r *Repository) ClaimUnknownTx(ctx context.Context, tx pgx.Tx, refundID uuid.UUID) (*Refund, *AttemptInfo, bool, error) {
	var f Refund
	err := tx.QueryRow(ctx, `
		UPDATE refund_request
		SET status = 'PROCESSING', version = version + 1, updated_at = now()
		WHERE id = $1
		  AND status = 'UNKNOWN'
		  AND updated_at < now() - interval '2 minutes'
		RETURNING id, tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		          amount_minor, currency, status, reason, provider_refund_id, failure_code, failure_reason,
		          version, created_at
	`, refundID).Scan(&f.ID, &f.TenantID, &f.MerchantID, &f.PaymentAttemptID, &f.MerchantRefundID,
		&f.IdempotencyKey, &f.AmountMinor, &f.Currency, &f.Status, &f.Reason,
		&f.ProviderRefundID, &f.FailureCode, &f.FailureReason, &f.Version, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("refund: claim unknown: %w", err)
	}

	a, err := r.LockAttemptTx(ctx, tx, f.PaymentAttemptID)
	if err != nil {
		return nil, nil, false, err
	}
	return &f, a, true, nil
}

// MarkUnknown 标记未知（超时/网络），等待人工对账。
func (r *Repository) MarkUnknown(ctx context.Context, refundID uuid.UUID, code, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE refund_request
		SET status = 'UNKNOWN', failure_code = $2, failure_reason = $3, version = version + 1, updated_at = now()
		WHERE id = $1
	`, refundID, code, reason)
	return err
}

// AdvanceAttemptStatusTx 根据累计成功退款额推进 payment_attempt 状态。
func (r *Repository) AdvanceAttemptStatusTx(ctx context.Context, tx pgx.Tx, attemptID uuid.UUID,
	attemptAmount, succeededTotal int64) error {
	next := "PARTIALLY_REFUNDED"
	if succeededTotal >= attemptAmount {
		next = "REFUNDED"
	}
	_, err := tx.Exec(ctx, `
		UPDATE payment_attempts
		SET status = $2, version = version + 1, updated_at = now()
		WHERE id = $1 AND status IN ('CAPTURED', 'SETTLED', 'PARTIALLY_CAPTURED', 'PARTIALLY_REFUNDED')
	`, attemptID, next)
	if err != nil {
		return fmt.Errorf("refund: advance attempt status: %w", err)
	}
	return nil
}

// ListByMerchant 列出商户退款单。
func (r *Repository) ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit int) ([]Refund, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		       amount_minor, currency, status, reason, provider_refund_id, failure_code, failure_reason,
		       version, created_at
		FROM refund_request WHERE merchant_id = $1
		ORDER BY created_at DESC LIMIT $2
	`, merchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Refund
	for rows.Next() {
		var f Refund
		if err := rows.Scan(&f.ID, &f.TenantID, &f.MerchantID, &f.PaymentAttemptID, &f.MerchantRefundID,
			&f.IdempotencyKey, &f.AmountMinor, &f.Currency, &f.Status, &f.Reason,
			&f.ProviderRefundID, &f.FailureCode, &f.FailureReason, &f.Version, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
