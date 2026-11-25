package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/treasury/repository/db"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) InsertCalendar(ctx context.Context, params db.InsertBankingCalendarParams) (*db.BankingCalendar, error) {
	q := db.New(r.pool)
	row, err := q.InsertBankingCalendar(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetCalendarByName(ctx context.Context, name string) (*db.BankingCalendar, error) {
	q := db.New(r.pool)
	row, err := q.GetBankingCalendarByName(ctx, name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) InsertSweepRule(ctx context.Context, params db.InsertSweepRuleParams) (*db.SweepRule, error) {
	q := db.New(r.pool)
	row, err := q.InsertSweepRule(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetSweepRule(ctx context.Context, id uuid.UUID) (*db.SweepRule, error) {
	q := db.New(r.pool)
	row, err := q.GetSweepRule(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) InsertTransfer(ctx context.Context, params db.InsertSweepTransferParams) (*db.SweepTransfer, error) {
	var transfer db.SweepTransfer
	err := r.pool.QueryRow(ctx,
		`INSERT INTO sweep_transfer (sweep_rule_id, source_account_id, target_account_id, amount_minor, currency, rail, status, idempotency_key)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (idempotency_key) DO NOTHING
		 RETURNING id, sweep_rule_id, source_account_id, target_account_id, amount_minor, currency, rail, status, idempotency_key, bank_ref, version, created_at, updated_at`,
		params.SweepRuleID, params.SourceAccountID, params.TargetAccountID, params.AmountMinor,
		params.Currency, params.Rail, params.Status, params.IdempotencyKey,
	).Scan(
		&transfer.ID, &transfer.SweepRuleID, &transfer.SourceAccountID, &transfer.TargetAccountID,
		&transfer.AmountMinor, &transfer.Currency, &transfer.Rail, &transfer.Status,
		&transfer.IdempotencyKey, &transfer.BankRef, &transfer.Version, &transfer.CreatedAt, &transfer.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			q := db.New(r.pool)
			row, err := q.GetSweepTransferByKey(ctx, params.IdempotencyKey)
			if err != nil {
				return nil, err
			}
			return &row, nil
		}
		return nil, err
	}
	return &transfer, nil
}

func (r *Repository) GetTransfer(ctx context.Context, id uuid.UUID) (*db.SweepTransfer, error) {
	q := db.New(r.pool)
	row, err := q.GetSweepTransfer(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// ErrVersionConflict 乐观锁冲突：CAS 更新影响 0 行（SEC-016）。
var ErrVersionConflict = errors.New("treasury repository: version conflict (concurrent update)")

// UpdateSweepTransferBankRef 持久化银行返回的 provider 参考号。
// 在 bank.SubmitTransfer 成功返回后、事务外单独执行（单语句幂等写）。
func (r *Repository) UpdateSweepTransferBankRef(ctx context.Context, id uuid.UUID, bankRef string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE sweep_transfer SET bank_ref = $2, updated_at = now() WHERE id = $1`,
		id, bankRef)
	return err
}

// InsertOutboxEvent 写入 transactional_outbox（Pending），由 Outbox Worker 异步消费。
// SEC-013：对账等资金终态动作必须走持久命令，不得同步伪造。
func (r *Repository) InsertOutboxEvent(ctx context.Context, aggregateType string, aggregateID uuid.UUID, eventType, payload, idempotencyKey string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `
INSERT INTO transactional_outbox
    (aggregate_type, aggregate_id, event_type, payload, idempotency_key, status, retry_count, next_retry_at)
VALUES ($1, $2, $3, $4, $5, 'Pending', 0, now())
ON CONFLICT (idempotency_key) DO UPDATE SET updated_at = now()
RETURNING id`,
		aggregateType, aggregateID, eventType, []byte(payload), idempotencyKey).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// UpdateTransferStatus CAS 状态更新（SEC-016：0 行必须显式冲突，不得假成功）。
func (r *Repository) UpdateTransferStatus(ctx context.Context, id uuid.UUID, status string, version int32) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE sweep_transfer
		 SET status = $2, version = version + 1, updated_at = now()
		 WHERE id = $1 AND version = $3`,
		id, status, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

func (r *Repository) CountDailySweeps(ctx context.Context, ruleID uuid.UUID, since pgtype.Timestamptz) (int64, error) {
	q := db.New(r.pool)
	return q.CountDailySweeps(ctx, db.CountDailySweepsParams{
		SweepRuleID: ruleID,
		CreatedAt:   since,
	})
}

func (r *Repository) GetBankAccountBalance(ctx context.Context, bankAccountID uuid.UUID, currency string) (int64, error) {
	q := db.New(r.pool)
	row, err := q.GetBankAccountBalance(ctx, db.GetBankAccountBalanceParams{
		BankAccountID: bankAccountID,
		Currency:      currency,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return row.AccumulatedMinor, nil
}

func (r *Repository) DecrementBankAccountBalance(ctx context.Context, bankAccountID uuid.UUID, currency string, amount int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE bank_account_balance SET accumulated_minor = accumulated_minor - $1, updated_at = now()
		 WHERE bank_account_id = $2 AND currency = $3 AND accumulated_minor >= $1`,
		amount, bankAccountID, currency)
	return err
}

func (r *Repository) ListSweepRules(ctx context.Context) ([]db.SweepRule, error) {
	q := db.New(r.pool)
	return q.ListSweepRules(ctx)
}

func (r *Repository) InsertSweepApproval(ctx context.Context, params db.InsertSweepApprovalParams) (*db.SweepApproval, error) {
	var approval db.SweepApproval
	err := r.pool.QueryRow(ctx,
		`INSERT INTO sweep_approval (sweep_rule_id, merchant_id, amount_minor, currency, rail, beneficiary_type, idempotency_key)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (idempotency_key) DO NOTHING
		 RETURNING id, sweep_rule_id, merchant_id, amount_minor, currency, rail, beneficiary_type, status, required_approvals, current_approvals, idempotency_key, created_at, updated_at`,
		params.SweepRuleID, params.MerchantID, params.AmountMinor,
		params.Currency, params.Rail, params.BeneficiaryType, params.IdempotencyKey,
	).Scan(
		&approval.ID, &approval.SweepRuleID, &approval.MerchantID, &approval.AmountMinor,
		&approval.Currency, &approval.Rail, &approval.BeneficiaryType, &approval.Status,
		&approval.RequiredApprovals, &approval.CurrentApprovals, &approval.IdempotencyKey,
		&approval.CreatedAt, &approval.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			q := db.New(r.pool)
			row, err := q.GetSweepApprovalByKey(ctx, params.IdempotencyKey)
			if err != nil {
				return nil, err
			}
			return &row, nil
		}
		return nil, err
	}
	return &approval, nil
}

func (r *Repository) GetSweepApproval(ctx context.Context, id uuid.UUID) (*db.SweepApproval, error) {
	q := db.New(r.pool)
	row, err := q.GetSweepApproval(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ApproveSweepApproval(ctx context.Context, id uuid.UUID) error {
	q := db.New(r.pool)
	return q.ApproveSweepApproval(ctx, id)
}

func (r *Repository) ListPendingSweepApprovals(ctx context.Context, ruleID uuid.UUID) ([]db.SweepApproval, error) {
	q := db.New(r.pool)
	return q.ListPendingSweepApprovals(ctx, ruleID)
}
