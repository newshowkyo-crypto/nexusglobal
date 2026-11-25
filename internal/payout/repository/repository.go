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

	"github.com/nexusglobal/mamr/internal/ledger/balance"
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

// UpdateBalance 直接写 merchant_treasury_ledger 全量余额。
//
// Deprecated: P0 4.5 之后 merchant_treasury_ledger 是账本投影，
// 业务代码必须通过 internal/settlement/projection 的增量方法写入，
// 本方法仅保留给历史兼容与测试，禁止新增调用。
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

// ErrVersionConflict 乐观锁冲突：CAS 更新影响 0 行（SEC-016）。
var ErrVersionConflict = errors.New("payout repository: version conflict (concurrent update)")

// ErrStaleOutboxOwner Outbox fencing 冲突：当前 owner 不再持有该事件
// （已超时被其他 worker 重新认领，SEC-011）。调用方应放弃 ACK/Fail。
var ErrStaleOutboxOwner = errors.New("payout repository: stale outbox owner (event re-claimed)")

func (r *Repository) UpdatePayoutRequestStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, version int32) error {
	tag, err := tx.Exec(ctx,
		`UPDATE payout_request
		 SET status = $2, version = version + 1, updated_at = now()
		 WHERE id = $1 AND version = $3`,
		id, status, version)
	if err != nil {
		return err
	}
	// SEC-016: CAS 0 行 = 并发版本冲突，必须显式报错而不是假成功。
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

// UpdatePayoutRequestLockID 更新 lock_id。SEC-015: 必须在传入事务内执行，
// 与同事务内其他状态变更原子提交，禁止绕过 tx 直接使用 pool。
func (r *Repository) UpdatePayoutRequestLockID(ctx context.Context, tx pgx.Tx, id uuid.UUID, lockID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE payout_request SET lock_id = $1, updated_at = now() WHERE id = $2`,
		lockID, id)
	return err
}

// UpdatePayoutRequestProviderRef 更新 provider_ref。SEC-015: 同上，事务内执行。
func (r *Repository) UpdatePayoutRequestProviderRef(ctx context.Context, tx pgx.Tx, id uuid.UUID, providerRef string) error {
	_, err := tx.Exec(ctx,
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

// ClaimOutboxBatch 认领一批待处理 Outbox 事件（SEC-011）。
//
// 认领范围：
//  1. Pending/Failed 且到达重试时间的事件（原有语义）。
//  2. 卡在 Processing 且 lease（locked_at）早于 leaseSeconds 秒前的事件 ——
//     崩溃/网络分区的 worker 遗留事件可被安全重新认领。
//
// FOR UPDATE SKIP LOCKED 保证多 worker 并发认领互不阻塞、不重复。
// locked_by 记录认领者，配合 Ack/Fail/DeadLetter 的 owner 校验实现 fencing。
func (r *Repository) ClaimOutboxBatch(ctx context.Context, lockedBy string, limit int32, leaseSeconds int32) ([]db.TransactionalOutbox, error) {
	if leaseSeconds <= 0 {
		leaseSeconds = 300 // 默认 5 分钟 lease
	}
	rows, err := r.pool.Query(ctx, `
UPDATE transactional_outbox
SET status = 'Processing', locked_by = $1, locked_at = now(), updated_at = now()
WHERE id IN (
    SELECT id FROM transactional_outbox
    WHERE (
            (status IN ('Pending', 'Failed') AND next_retry_at <= now())
         OR (status = 'Processing' AND locked_at < now() - make_interval(secs => $3::int))
          )
      AND event_type NOT IN ('VCC_CRYPTO_BRIDGE_REQUESTED', 'VCC_FUNDING_REQUESTED', 'VCC_ISSUING_FUNDING_REQUESTED')
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
RETURNING id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status, retry_count, next_retry_at, locked_by, locked_at, created_at, updated_at
`, lockedBy, limit, leaseSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []db.TransactionalOutbox
	for rows.Next() {
		var e db.TransactionalOutbox
		if err := rows.Scan(&e.ID, &e.AggregateType, &e.AggregateID, &e.EventType, &e.Payload,
			&e.IdempotencyKey, &e.Status, &e.RetryCount, &e.NextRetryAt, &e.LockedBy, &e.LockedAt,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// AckOutboxEvent 将事件标记为 Completed。
// fencing（SEC-011）：只有当前 owner 且事件仍处于 Processing 才允许 ACK；
// owner 过期（事件已被重新认领）返回 ErrStaleOutboxOwner，旧 owner 不得覆盖新 owner 的进度。
func (r *Repository) AckOutboxEvent(ctx context.Context, id uuid.UUID, owner string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE transactional_outbox
		 SET status = 'Completed', updated_at = now()
		 WHERE id = $1 AND locked_by = $2 AND status = 'Processing'`,
		id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStaleOutboxOwner
	}
	return nil
}

// FailOutboxEvent 将事件标记为 Failed 并按指数退避安排重试。
// fencing（SEC-011）：同 AckOutboxEvent，仅当前 owner 可标记失败。
func (r *Repository) FailOutboxEvent(ctx context.Context, id uuid.UUID, owner string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE transactional_outbox
		 SET status = 'Failed', retry_count = retry_count + 1,
		     next_retry_at = now() + (interval '1 minute' * (1 << LEAST(retry_count, 6))),
		     updated_at = now()
		 WHERE id = $1 AND locked_by = $2 AND status = 'Processing'`,
		id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStaleOutboxOwner
	}
	return nil
}

// UpdateOutboxStatus 状态转移（当前仅用于 DeadLetter）。
// fencing（SEC-011）：仅当前 owner 可将 Processing 事件转入终态。
func (r *Repository) UpdateOutboxStatus(ctx context.Context, id uuid.UUID, status, owner string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE transactional_outbox
		 SET status = $2, updated_at = now()
		 WHERE id = $1 AND locked_by = $3 AND status = 'Processing'`,
		id, status, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStaleOutboxOwner
	}
	return nil
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
	// SEC-005: 与严格账本服务统一不变量——交易必须携带非空且借贷平衡的分录。
	// 禁止再写入无分录交易（空 entries 会导致资金动作不可追溯，违反 RED-13）。
	if err := ledgerdomain.ValidateBalanced(txn.Entries); err != nil {
		return nil, fmt.Errorf("post transaction %s: %w", txn.IdempotencyKey, err)
	}

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
		if txn.UpdateBalances {
			if err := balance.Apply(ctx, tx, entry.AccountID, string(entry.Direction),
				entry.Amount, entry.AssetCode, txn.TenantID); err != nil {
				return nil, fmt.Errorf("apply balance for entry: %w", err)
			}
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
