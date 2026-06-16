package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/mae/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) InsertConfig(ctx context.Context, params db.InsertMAEConfigParams) (*db.MaeConfiguration, error) {
	q := db.New(r.pool)
	row, err := q.InsertMAEConfig(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetConfig(ctx context.Context, id uuid.UUID) (*db.MaeConfiguration, error) {
	q := db.New(r.pool)
	row, err := q.GetMAEConfig(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetConfigByMAEID(ctx context.Context, maeID string) (*db.MaeConfiguration, error) {
	q := db.New(r.pool)
	row, err := q.GetMAEConfigByMAEID(ctx, maeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ListConfigs(ctx context.Context) ([]db.MaeConfiguration, error) {
	q := db.New(r.pool)
	return q.ListMAEConfigs(ctx)
}

func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, status, reason string, version int32, lockUntil *time.Time) error {
	q := db.New(r.pool)
	var lockDate pgtype.Date
	if lockUntil != nil {
		lockDate = pgtype.Date{Time: *lockUntil, Valid: true}
	}
	return q.UpdateMAEStatus(ctx, db.UpdateMAEStatusParams{
		ID:                   id,
		Status:               status,
		StatusReason:         reason,
		BusinessDayLockUntil: lockDate,
		Version:              version,
	})
}

func (r *Repository) RotateSecret(ctx context.Context, id uuid.UUID, apiKeyCiphertext, webhookSecretCiphertext []byte, version int32) error {
	tag, err := r.pool.Exec(ctx, `UPDATE mae_configuration
SET api_key_ciphertext = $2, webhook_secret_ciphertext = $3,
    version = version + 1, updated_at = now()
WHERE id = $1 AND version = $4`,
		id, apiKeyCiphertext, webhookSecretCiphertext, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrVersionConflict.WithMessage("version conflict during secret rotation")
	}
	return nil
}

func (r *Repository) UpdateConfig(ctx context.Context, params db.UpdateMAEConfigParams) error {
	tag, err := r.pool.Exec(ctx, `UPDATE mae_configuration
SET display_name = $2, mae_type = $3, tenant_scope = $4, jurisdiction = $5,
    supported_currencies = $6, api_base_url = $7, webhook_proxy_url = $8,
    risk_label = $9, daily_vmax_minor = $10, monthly_vmax_minor = $11,
    cmax_bps = $12, card_testing_limit = $13, routing_weight = $14,
    cooldown_minutes = $15, testing_traffic_bps = $16,
    testing_min_txn_count = $17, testing_min_success_rate_bps = $18,
    testing_max_p95_latency_ms = $19, version = version + 1, updated_at = now()
WHERE id = $1 AND version = $20`,
		params.ID,
		params.DisplayName,
		params.MaeType,
		params.TenantScope,
		params.Jurisdiction,
		params.SupportedCurrencies,
		params.ApiBaseUrl,
		params.WebhookProxyUrl,
		params.RiskLabel,
		params.DailyVmaxMinor,
		params.MonthlyVmaxMinor,
		params.CmaxBps,
		params.CardTestingLimit,
		params.RoutingWeight,
		params.CooldownMinutes,
		params.TestingTrafficBps,
		params.TestingMinTxnCount,
		params.TestingMinSuccessRateBps,
		params.TestingMaxP95LatencyMs,
		params.Version,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrVersionConflict.WithMessage("version conflict during config update")
	}
	return nil
}
