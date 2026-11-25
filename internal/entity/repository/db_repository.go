package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/entity/domain"
)

type dbRepository struct {
	pool *pgxpool.Pool
}

func NewDBRepository(pool *pgxpool.Pool) Repository {
	return &dbRepository{pool: pool}
}

func (r *dbRepository) CreateCorporateEntity(ctx context.Context, tx pgx.Tx, entity *domain.CorporateEntity) error {
	metadataJSON, _ := json.Marshal(entity.Metadata)
	_, err := tx.Exec(ctx, `
		INSERT INTO corporate_entity (id, tenant_id, name, legal_name, tax_id, country, jurisdiction, status, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		entity.ID, entity.TenantID, entity.Name, entity.LegalName, entity.TaxID,
		entity.Country, entity.Jurisdiction, entity.Status, metadataJSON, entity.CreatedAt, entity.UpdatedAt)
	return err
}

func (r *dbRepository) GetCorporateEntityByID(ctx context.Context, id uuid.UUID) (*domain.CorporateEntity, error) {
	var e domain.CorporateEntity
	var metadataJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, name, legal_name, tax_id, country, jurisdiction, status, metadata, created_at, updated_at
		FROM corporate_entity WHERE id = $1`, id).Scan(
		&e.ID, &e.TenantID, &e.Name, &e.LegalName, &e.TaxID, &e.Country, &e.Jurisdiction, &e.Status, &metadataJSON, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, err
	}
	if metadataJSON != nil {
		_ = json.Unmarshal(metadataJSON, &e.Metadata)
	}
	return &e, nil
}

func (r *dbRepository) GetCorporateEntitiesByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.CorporateEntity, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, name, legal_name, tax_id, country, jurisdiction, status, metadata, created_at, updated_at
		FROM corporate_entity WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entities []*domain.CorporateEntity
	for rows.Next() {
		var e domain.CorporateEntity
		var metadataJSON []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Name, &e.LegalName, &e.TaxID, &e.Country, &e.Jurisdiction, &e.Status, &metadataJSON, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		if metadataJSON != nil {
			_ = json.Unmarshal(metadataJSON, &e.Metadata)
		}
		entities = append(entities, &e)
	}
	return entities, rows.Err()
}

func (r *dbRepository) UpdateCorporateEntity(ctx context.Context, tx pgx.Tx, entity *domain.CorporateEntity) error {
	metadataJSON, _ := json.Marshal(entity.Metadata)
	_, err := tx.Exec(ctx, `
		UPDATE corporate_entity SET name=$2, legal_name=$3, tax_id=$4, country=$5, jurisdiction=$6, status=$7, metadata=$8, updated_at=$9
		WHERE id=$1`, entity.ID, entity.Name, entity.LegalName, entity.TaxID, entity.Country, entity.Jurisdiction, entity.Status, metadataJSON, entity.UpdatedAt)
	return err
}

func (r *dbRepository) CreateEntityRiskProfile(ctx context.Context, tx pgx.Tx, profile *domain.EntityRiskProfile) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO entity_risk_profile (id, corporate_entity_id, max_daily_transaction_amount, max_single_transaction_amount, risk_score, allowed_countries, blocked_countries, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		profile.ID, profile.CorporateEntityID, profile.MaxDailyTransactionAmount, profile.MaxSingleTransactionAmount,
		profile.RiskScore, profile.AllowedCountries, profile.BlockedCountries, profile.CreatedAt, profile.UpdatedAt)
	return err
}

func (r *dbRepository) GetEntityRiskProfile(ctx context.Context, corporateEntityID uuid.UUID) (*domain.EntityRiskProfile, error) {
	var p domain.EntityRiskProfile
	err := r.pool.QueryRow(ctx, `
		SELECT id, corporate_entity_id, max_daily_transaction_amount, max_single_transaction_amount, risk_score, allowed_countries, blocked_countries, created_at, updated_at
		FROM entity_risk_profile WHERE corporate_entity_id = $1`, corporateEntityID).Scan(
		&p.ID, &p.CorporateEntityID, &p.MaxDailyTransactionAmount, &p.MaxSingleTransactionAmount,
		&p.RiskScore, &p.AllowedCountries, &p.BlockedCountries, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *dbRepository) UpdateEntityRiskProfile(ctx context.Context, tx pgx.Tx, profile *domain.EntityRiskProfile) error {
	_, err := tx.Exec(ctx, `
		UPDATE entity_risk_profile SET max_daily_transaction_amount=$2, max_single_transaction_amount=$3, risk_score=$4, allowed_countries=$5, blocked_countries=$6, updated_at=$7
		WHERE id=$1`, profile.ID, profile.MaxDailyTransactionAmount, profile.MaxSingleTransactionAmount, profile.RiskScore, profile.AllowedCountries, profile.BlockedCountries, profile.UpdatedAt)
	return err
}

func (r *dbRepository) CreateEntityAssetLimit(ctx context.Context, tx pgx.Tx, limit *domain.EntityAssetLimit) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO entity_asset_limit (id, corporate_entity_id, asset_code, max_balance, max_daily_outflow, max_daily_inflow, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		limit.ID, limit.CorporateEntityID, limit.AssetCode, limit.MaxBalance, limit.MaxDailyOutflow, limit.MaxDailyInflow, limit.CreatedAt, limit.UpdatedAt)
	return err
}

func (r *dbRepository) GetEntityAssetLimit(ctx context.Context, corporateEntityID uuid.UUID, assetCode string) (*domain.EntityAssetLimit, error) {
	var l domain.EntityAssetLimit
	err := r.pool.QueryRow(ctx, `
		SELECT id, corporate_entity_id, asset_code, max_balance, max_daily_outflow, max_daily_inflow, created_at, updated_at
		FROM entity_asset_limit WHERE corporate_entity_id = $1 AND asset_code = $2`, corporateEntityID, assetCode).Scan(
		&l.ID, &l.CorporateEntityID, &l.AssetCode, &l.MaxBalance, &l.MaxDailyOutflow, &l.MaxDailyInflow, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *dbRepository) UpdateEntityAssetLimit(ctx context.Context, tx pgx.Tx, limit *domain.EntityAssetLimit) error {
	_, err := tx.Exec(ctx, `
		UPDATE entity_asset_limit SET max_balance=$2, max_daily_outflow=$3, max_daily_inflow=$4, updated_at=$5
		WHERE id=$1`, limit.ID, limit.MaxBalance, limit.MaxDailyOutflow, limit.MaxDailyInflow, limit.UpdatedAt)
	return err
}

func (r *dbRepository) UpdateDailyFlow(ctx context.Context, tx pgx.Tx, corporateEntityID uuid.UUID, assetCode string, inflow, outflow decimal.Decimal) error {
	tag, err := tx.Exec(ctx, `
		UPDATE entity_daily_flow SET total_inflow = total_inflow + $3, total_outflow = total_outflow + $4, updated_at = now()
		WHERE corporate_entity_id = $1 AND asset_code = $2 AND date = CURRENT_DATE`,
		corporateEntityID, assetCode, inflow, outflow)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO entity_daily_flow (id, corporate_entity_id, asset_code, date, total_inflow, total_outflow, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, $2, CURRENT_DATE, $3, $4, now(), now())`,
			corporateEntityID, assetCode, inflow, outflow)
	}
	return err
}

func (r *dbRepository) GetDailyFlow(ctx context.Context, corporateEntityID uuid.UUID, assetCode string) (*DailyFlow, error) {
	var flow DailyFlow
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(total_inflow, 0), COALESCE(total_outflow, 0)
		FROM entity_daily_flow WHERE corporate_entity_id = $1 AND asset_code = $2 AND date = CURRENT_DATE`,
		corporateEntityID, assetCode).Scan(&flow.TotalInflow, &flow.TotalOutflow)
	if errors.Is(err, pgx.ErrNoRows) {
		return &DailyFlow{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &flow, nil
}
