package riskprofile

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type dbRepository struct {
	pool *pgxpool.Pool
}

func NewDBRepository(pool *pgxpool.Pool) Repository {
	return &dbRepository{pool: pool}
}

func (r *dbRepository) GetByMerchantID(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error) {
	var p MerchantRiskProfile
	err := r.pool.QueryRow(ctx, `
		SELECT id, merchant_id, rolling_reserve_rate_bps, dispute_reserve_rate_bps, reserve_release_days, status, created_at, updated_at
		FROM merchant_risk_profiles WHERE merchant_id = $1`, merchantID).Scan(
		&p.ID, &p.MerchantID, &p.RollingReserveRateBps, &p.DisputeReserveRateBps, &p.ReserveReleaseDays, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRiskProfileNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *dbRepository) Create(ctx context.Context, profile *MerchantRiskProfile) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO merchant_risk_profiles (id, merchant_id, rolling_reserve_rate_bps, dispute_reserve_rate_bps, reserve_release_days, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		profile.ID, profile.MerchantID, profile.RollingReserveRateBps, profile.DisputeReserveRateBps,
		profile.ReserveReleaseDays, profile.Status, profile.CreatedAt, profile.UpdatedAt)
	return err
}

func (r *dbRepository) Update(ctx context.Context, profile *MerchantRiskProfile) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE merchant_risk_profiles SET rolling_reserve_rate_bps=$2, dispute_reserve_rate_bps=$3, reserve_release_days=$4, status=$5, updated_at=$6
		WHERE id=$1`, profile.ID, profile.RollingReserveRateBps, profile.DisputeReserveRateBps, profile.ReserveReleaseDays, profile.Status, profile.UpdatedAt)
	return err
}

func (r *dbRepository) CreateReserveEntry(ctx context.Context, entry *RollingReserveEntry) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO rolling_reserve_ledger (id, merchant_id, transaction_id, amount, reserve_type, release_date, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		entry.ID, entry.MerchantID, entry.TransactionID, decimal.NewFromInt(entry.Amount),
		entry.ReserveType, entry.ReleaseDate, entry.Status, entry.CreatedAt, entry.UpdatedAt)
	return err
}

func (r *dbRepository) GetReserveEntryByID(ctx context.Context, id uuid.UUID) (*RollingReserveEntry, error) {
	var e RollingReserveEntry
	var amount decimal.Decimal
	err := r.pool.QueryRow(ctx, `
		SELECT id, merchant_id, transaction_id, amount, reserve_type, release_date, status, created_at, updated_at
		FROM rolling_reserve_ledger WHERE id = $1`, id).Scan(
		&e.ID, &e.MerchantID, &e.TransactionID, &amount, &e.ReserveType, &e.ReleaseDate, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReserveNotFound
	}
	if err != nil {
		return nil, err
	}
	e.Amount = amount.IntPart()
	return &e, nil
}

func (r *dbRepository) GetReadyToRelease(ctx context.Context, now time.Time) ([]*RollingReserveEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, merchant_id, transaction_id, amount, reserve_type, release_date, status, created_at, updated_at
		FROM rolling_reserve_ledger WHERE status = 'HELD' AND release_date <= $1 ORDER BY release_date`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*RollingReserveEntry
	for rows.Next() {
		var e RollingReserveEntry
		var amount decimal.Decimal
		if err := rows.Scan(&e.ID, &e.MerchantID, &e.TransactionID, &amount, &e.ReserveType, &e.ReleaseDate, &e.Status, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Amount = amount.IntPart()
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

func (r *dbRepository) UpdateReserveStatus(ctx context.Context, id uuid.UUID, status ReserveStatus) error {
	_, err := r.pool.Exec(ctx, `UPDATE rolling_reserve_ledger SET status=$2, updated_at=now() WHERE id=$1`, id, status)
	return err
}

func (r *dbRepository) GetReservesByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*RollingReserveEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, merchant_id, transaction_id, amount, reserve_type, release_date, status, created_at, updated_at
		FROM rolling_reserve_ledger WHERE merchant_id = $1 ORDER BY created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*RollingReserveEntry
	for rows.Next() {
		var e RollingReserveEntry
		var amount decimal.Decimal
		if err := rows.Scan(&e.ID, &e.MerchantID, &e.TransactionID, &amount, &e.ReserveType, &e.ReleaseDate, &e.Status, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Amount = amount.IntPart()
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}
