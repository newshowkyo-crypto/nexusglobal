package issuing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type dbCardRepository struct {
	pool *pgxpool.Pool
}

func NewDBCardRepository(pool *pgxpool.Pool) CardRepository {
	return &dbCardRepository{pool: pool}
}

func (r *dbCardRepository) Create(ctx context.Context, card VirtualCard) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO virtual_cards (id, merchant_id, provider_id, provider_card_ref, last4, status, currency,
			spending_limit_minor, declined_count_sliding_window, declined_window_started_at, fraud_rate_bps,
			last_decline_reason, idempotency_key, payload_hash, created_by, frozen_reason, terminated_reason, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		card.ID, card.MerchantID, card.ProviderID, card.ProviderCardRef, card.Last4, card.Status, card.Currency,
		card.SpendingLimitMinor, card.DeclinedCountSlidingWindow, card.DeclinedWindowStartedAt, card.FraudRateBps,
		card.LastDeclineReason, card.IdempotencyKey, card.PayloadHash, card.CreatedBy, card.FrozenReason, card.TerminatedReason,
		card.CreatedAt, card.UpdatedAt)
	return err
}

func (r *dbCardRepository) GetByID(ctx context.Context, cardID uuid.UUID) (*VirtualCard, error) {
	var c VirtualCard
	err := r.pool.QueryRow(ctx, `
		SELECT id, merchant_id, provider_id, provider_card_ref, last4, status, currency,
		       spending_limit_minor, declined_count_sliding_window, declined_window_started_at, fraud_rate_bps,
		       last_decline_reason, idempotency_key, payload_hash, created_by, frozen_reason, terminated_reason, created_at, updated_at
		FROM virtual_cards WHERE id = $1`, cardID).Scan(
		&c.ID, &c.MerchantID, &c.ProviderID, &c.ProviderCardRef, &c.Last4, &c.Status, &c.Currency,
		&c.SpendingLimitMinor, &c.DeclinedCountSlidingWindow, &c.DeclinedWindowStartedAt, &c.FraudRateBps,
		&c.LastDeclineReason, &c.IdempotencyKey, &c.PayloadHash, &c.CreatedBy, &c.FrozenReason, &c.TerminatedReason,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCardNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *dbCardRepository) UpdateStatus(ctx context.Context, cardID uuid.UUID, status VirtualCardStatus, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE virtual_cards SET status=$2, frozen_reason=CASE WHEN $2='FROZEN' THEN $3 ELSE frozen_reason END,
		       terminated_reason=CASE WHEN $2='TERMINATED' THEN $3 ELSE terminated_reason END, updated_at=now() WHERE id=$1`,
		cardID, status, reason)
	return err
}

func (r *dbCardRepository) GetByIdempotencyKey(ctx context.Context, merchantID uuid.UUID, idempotencyKey string) (*VirtualCard, error) {
	var c VirtualCard
	err := r.pool.QueryRow(ctx, `
		SELECT id, merchant_id, provider_id, provider_card_ref, last4, status, currency,
		       spending_limit_minor, declined_count_sliding_window, declined_window_started_at, fraud_rate_bps,
		       last_decline_reason, idempotency_key, payload_hash, created_by, frozen_reason, terminated_reason, created_at, updated_at
		FROM virtual_cards WHERE merchant_id = $1 AND idempotency_key = $2`, merchantID, idempotencyKey).Scan(
		&c.ID, &c.MerchantID, &c.ProviderID, &c.ProviderCardRef, &c.Last4, &c.Status, &c.Currency,
		&c.SpendingLimitMinor, &c.DeclinedCountSlidingWindow, &c.DeclinedWindowStartedAt, &c.FraudRateBps,
		&c.LastDeclineReason, &c.IdempotencyKey, &c.PayloadHash, &c.CreatedBy, &c.FrozenReason, &c.TerminatedReason,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCardNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

type dbProviderRepository struct {
	pool *pgxpool.Pool
}

func NewDBProviderRepository(pool *pgxpool.Pool) ProviderRepository {
	return &dbProviderRepository{pool: pool}
}

func (r *dbProviderRepository) ListProviders(ctx context.Context, entityID *uuid.UUID) ([]IssuingProvider, error) {
	var rows pgx.Rows
	var err error
	if entityID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT ip.id, ip.entity_id, ce.tenant_id,
			       ip.issuing_tier, ip.funding_mode, ip.provider_code, ip.pool_code, ip.credential_vault_key,
			       ip.adapter_provider_id, ip.adapter_type, ip.status, ip.version,
			       ip.provider_config, ip.velocity_config, ip.created_at, ip.updated_at
			FROM issuing_providers ip
			LEFT JOIN corporate_entity ce ON ip.entity_id = ce.id
			WHERE ip.entity_id = $1 ORDER BY ip.created_at`, entityID)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT ip.id, ip.entity_id, ce.tenant_id,
			       ip.issuing_tier, ip.funding_mode, ip.provider_code, ip.pool_code, ip.credential_vault_key,
			       ip.adapter_provider_id, ip.adapter_type, ip.status, ip.version,
			       ip.provider_config, ip.velocity_config, ip.created_at, ip.updated_at
			FROM issuing_providers ip
			LEFT JOIN corporate_entity ce ON ip.entity_id = ce.id
			ORDER BY ip.created_at`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProviders(rows)
}

func (r *dbProviderRepository) GetProviderByID(ctx context.Context, providerID uuid.UUID) (*IssuingProvider, error) {
	var p IssuingProvider
	var providerConfigJSON, velocityConfigJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT ip.id, ip.entity_id, ce.tenant_id,
		       ip.issuing_tier, ip.funding_mode, ip.provider_code, ip.pool_code, ip.credential_vault_key,
		       ip.adapter_provider_id, ip.adapter_type, ip.status, ip.version,
		       ip.provider_config, ip.velocity_config, ip.created_at, ip.updated_at
		FROM issuing_providers ip
		LEFT JOIN corporate_entity ce ON ip.entity_id = ce.id
		WHERE ip.id = $1`, providerID).Scan(
		&p.ID, &p.EntityID, &p.TenantID, &p.IssuingTier, &p.FundingMode, &p.ProviderCode, &p.PoolCode, &p.CredentialVaultKey,
		&p.AdapterProviderID, &p.AdapterType, &p.Status, &p.Version, &providerConfigJSON, &velocityConfigJSON, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProviderNotFound
	}
	if err != nil {
		return nil, err
	}
	if providerConfigJSON != nil {
		if jsonErr := json.Unmarshal(providerConfigJSON, &p.ProviderConfig); jsonErr != nil {
			return nil, fmt.Errorf("issuing: malformed provider_config JSON for provider %s: %w", providerID, jsonErr)
		}
	}
	if velocityConfigJSON != nil {
		if jsonErr := json.Unmarshal(velocityConfigJSON, &p.VelocityConfig); jsonErr != nil {
			return nil, fmt.Errorf("issuing: malformed velocity_config JSON for provider %s: %w", providerID, jsonErr)
		}
	}
	return &p, nil
}

func (r *dbProviderRepository) ListProvidersByTierAndFundingMode(ctx context.Context, entityID *uuid.UUID, tier IssuingTier, fundingMode IssuingFundingMode) ([]IssuingProvider, error) {
	var rows pgx.Rows
	var err error
	if entityID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT ip.id, ip.entity_id, ce.tenant_id,
			       ip.issuing_tier, ip.funding_mode, ip.provider_code, ip.pool_code, ip.credential_vault_key,
			       ip.adapter_provider_id, ip.adapter_type, ip.status, ip.version,
			       ip.provider_config, ip.velocity_config, ip.created_at, ip.updated_at
			FROM issuing_providers ip
			LEFT JOIN corporate_entity ce ON ip.entity_id = ce.id
			WHERE ip.entity_id = $1 AND ip.issuing_tier = $2 AND ip.funding_mode = $3 AND ip.status = 'ACTIVE'`,
			entityID, tier, fundingMode)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT ip.id, ip.entity_id, ce.tenant_id,
			       ip.issuing_tier, ip.funding_mode, ip.provider_code, ip.pool_code, ip.credential_vault_key,
			       ip.adapter_provider_id, ip.adapter_type, ip.status, ip.version,
			       ip.provider_config, ip.velocity_config, ip.created_at, ip.updated_at
			FROM issuing_providers ip
			LEFT JOIN corporate_entity ce ON ip.entity_id = ce.id
			WHERE ip.issuing_tier = $1 AND ip.funding_mode = $2 AND ip.status = 'ACTIVE'`,
			tier, fundingMode)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProviders(rows)
}

func scanProviders(rows pgx.Rows) ([]IssuingProvider, error) {
	var providers []IssuingProvider
	for rows.Next() {
		var p IssuingProvider
		var providerConfigJSON, velocityConfigJSON []byte
		if err := rows.Scan(
			&p.ID, &p.EntityID, &p.TenantID, &p.IssuingTier, &p.FundingMode, &p.ProviderCode, &p.PoolCode, &p.CredentialVaultKey,
			&p.AdapterProviderID, &p.AdapterType, &p.Status, &p.Version, &providerConfigJSON, &velocityConfigJSON, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if providerConfigJSON != nil {
			if jsonErr := json.Unmarshal(providerConfigJSON, &p.ProviderConfig); jsonErr != nil {
				return nil, fmt.Errorf("issuing: malformed provider_config JSON: %w", jsonErr)
			}
		}
		if velocityConfigJSON != nil {
			if jsonErr := json.Unmarshal(velocityConfigJSON, &p.VelocityConfig); jsonErr != nil {
				return nil, fmt.Errorf("issuing: malformed velocity_config JSON: %w", jsonErr)
			}
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

type dbFraudGuardRepository struct {
	pool *pgxpool.Pool
}

func NewDBFraudGuardRepository(pool *pgxpool.Pool) FraudGuardRepository {
	return &dbFraudGuardRepository{pool: pool}
}

func (r *dbFraudGuardRepository) GetMerchantRiskProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error) {
	var profile MerchantRiskProfile
	var status string
	err := r.pool.QueryRow(ctx, `
		SELECT merchant_id, status, updated_at FROM merchant_risk_profiles WHERE merchant_id = $1`, merchantID).Scan(
		&profile.MerchantID, &status, &profile.LastUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return &MerchantRiskProfile{MerchantID: merchantID, IsIssuingAllowed: true}, nil
	}
	if err != nil {
		return nil, err
	}
	profile.IsIssuingAllowed = status == "ACTIVE"
	return &profile, nil
}

func (r *dbFraudGuardRepository) GetCardDeclineStats(ctx context.Context, cardID uuid.UUID, windowStart time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT declined_count_sliding_window FROM virtual_cards
		WHERE id = $1 AND (declined_window_started_at IS NULL OR declined_window_started_at >= $2)`,
		cardID, windowStart).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *dbFraudGuardRepository) UpdateCardDeclineStats(ctx context.Context, cardID uuid.UUID, event CardAuthEvent) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE virtual_cards SET
			declined_count_sliding_window = declined_count_sliding_window + CASE WHEN $2 THEN 1 ELSE 0 END,
			last_decline_reason = CASE WHEN $2 THEN $3 ELSE last_decline_reason END,
			declined_window_started_at = CASE WHEN declined_window_started_at IS NULL THEN $4 ELSE declined_window_started_at END,
			updated_at = now()
		WHERE id = $1`, cardID, event.IsDeclined, event.DeclineReason, event.Timestamp)
	return err
}

func (r *dbFraudGuardRepository) UpdateMerchantFraudRate(ctx context.Context, merchantID uuid.UUID, event CardAuthEvent) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE merchant_risk_profiles SET updated_at = now() WHERE merchant_id = $1`, merchantID)
	return err
}
