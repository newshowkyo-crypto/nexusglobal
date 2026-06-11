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
	q := db.New(r.pool)
	row, err := q.InsertSweepTransfer(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
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

func (r *Repository) UpdateTransferStatus(ctx context.Context, id uuid.UUID, status string, version int32) error {
	q := db.New(r.pool)
	return q.UpdateSweepTransferStatus(ctx, db.UpdateSweepTransferStatusParams{
		ID:      id,
		Status:  status,
		Version: version,
	})
}

func (r *Repository) CountDailySweeps(ctx context.Context, ruleID uuid.UUID, since pgtype.Timestamptz) (int64, error) {
	q := db.New(r.pool)
	return q.CountDailySweeps(ctx, db.CountDailySweepsParams{
		SweepRuleID: ruleID,
		CreatedAt:   since,
	})
}
