package pool

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/pool/domain"
)

// Store 是分配器依赖的持久化接口，便于单元测试替换。
type Store interface {
	GetPolicy(ctx context.Context, merchantID uuid.UUID, siteID, businessLine string) (*domain.Policy, error)
	ListChannels(ctx context.Context, ids []uuid.UUID) ([]domain.Channel, error)
	GetChannel(ctx context.Context, id uuid.UUID) (*domain.Channel, error)
	MonthUsage(ctx context.Context, channelID uuid.UUID, day time.Time) (int64, error)
	ReserveWatermark(ctx context.Context, channelID uuid.UUID, day time.Time, amountMinor int64, limitMinor int64) (bool, error)
	ReserveMonth(ctx context.Context, channelID uuid.UUID, day time.Time, amountMinor int64, limitMinor int64) (bool, error)
	ConfirmWatermark(ctx context.Context, channelID uuid.UUID, day time.Time, amountMinor int64) error
	ReleaseWatermark(ctx context.Context, channelID uuid.UUID, day time.Time, amountMinor int64) error
	InsertLeases(ctx context.Context, leases []domain.Lease) error
	SetLeaseState(ctx context.Context, attemptID, channelID uuid.UUID, state string) error
	NextStandby(ctx context.Context, attemptID uuid.UUID) (*domain.Lease, error)
}

// Repository 是 Store 的 PostgreSQL 实现。
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// GetPolicy 按 (site, business_line) → (site, "") → ("", "") 逐级回退。
func (r *Repository) GetPolicy(ctx context.Context, merchantID uuid.UUID, siteID, businessLine string) (*domain.Policy, error) {
	candidates := [][2]string{
		{siteID, businessLine},
		{siteID, ""},
		{"", businessLine},
		{"", ""},
	}
	for _, c := range candidates {
		p, err := r.getPolicyExact(ctx, merchantID, c[0], c[1])
		if err != nil {
			return nil, err
		}
		if p != nil {
			return p, nil
		}
	}
	return nil, nil
}

func (r *Repository) getPolicyExact(ctx context.Context, merchantID uuid.UUID, siteID, businessLine string) (*domain.Policy, error) {
	var p domain.Policy
	var weights map[string]float64
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, merchant_id, site_id, business_line,
		       allowed_channel_ids, strategy, weights, status, version
		FROM pool_policy
		WHERE merchant_id = $1 AND site_id = $2 AND business_line = $3 AND status = 'ACTIVE'
	`, merchantID, siteID, businessLine).Scan(
		&p.ID, &p.TenantID, &p.MerchantID, &p.SiteID, &p.BusinessLine,
		&p.AllowedChannelIDs, &p.Strategy, &weights, &p.Status, &p.Version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("pool: load policy: %w", err)
	}
	p.Weights = weights
	return &p, nil
}

func (r *Repository) ListChannels(ctx context.Context, ids []uuid.UUID) ([]domain.Channel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.tenant_id, c.merchant_id, c.acquirer_channel_id, c.entity_id,
		       c.psp_code, c.external_mid, c.mcc, c.business_lines, c.currencies, c.card_brands,
		       c.min_amount_minor, c.max_amount_minor, c.daily_limit_minor, c.monthly_limit_minor,
		       c.cost_bps, c.settlement_days, c.weight, c.health_score, c.status,
		       COALESCE(w.used_amount_minor, 0), COALESCE(w.reserved_amount_minor, 0), COALESCE(w.used_count, 0)
		FROM pool_channel c
		LEFT JOIN pool_watermark w ON w.channel_id = c.id AND w.biz_date = CURRENT_DATE
		WHERE c.id = ANY($1)
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("pool: list channels: %w", err)
	}
	defer rows.Close()

	var out []domain.Channel
	for rows.Next() {
		var c domain.Channel
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.MerchantID, &c.AcquirerChannelID, &c.EntityID,
			&c.PSPCode, &c.ExternalMID, &c.MCC, &c.BusinessLines, &c.Currencies, &c.CardBrands,
			&c.MinAmountMinor, &c.MaxAmountMinor, &c.DailyLimitMinor, &c.MonthlyLimitMinor,
			&c.CostBps, &c.SettlementDays, &c.Weight, &c.HealthScore, &c.Status,
			&c.Watermark.UsedAmountMinor, &c.Watermark.ReservedAmountMinor, &c.Watermark.UsedCount,
		); err != nil {
			return nil, fmt.Errorf("pool: scan channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) GetChannel(ctx context.Context, id uuid.UUID) (*domain.Channel, error) {
	channels, err := r.ListChannels(ctx, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		return nil, nil
	}
	return &channels[0], nil
}

func (r *Repository) MonthUsage(ctx context.Context, channelID uuid.UUID, day time.Time) (int64, error) {
	var used int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(used_amount_minor + reserved_amount_minor, 0)::bigint
		FROM pool_month_watermark
		WHERE channel_id = $1 AND month_start = date_trunc('month', $2::date)::date
	`, channelID, day).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("pool: month usage: %w", err)
	}
	return used, nil
}

// ReserveMonth 原子预占当月额度；limit<=0 表示不限。
func (r *Repository) ReserveMonth(ctx context.Context, channelID uuid.UUID, day time.Time, amountMinor, limitMinor int64) (bool, error) {
	if limitMinor <= 0 {
		return true, nil
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO pool_month_watermark (channel_id, month_start, reserved_amount_minor)
		VALUES ($1, date_trunc('month', $2::date)::date, $3)
		ON CONFLICT (channel_id, month_start) DO UPDATE
		SET reserved_amount_minor = pool_month_watermark.reserved_amount_minor + EXCLUDED.reserved_amount_minor,
		    updated_at = now()
		WHERE pool_month_watermark.used_amount_minor + pool_month_watermark.reserved_amount_minor + $3 <= $4
	`, channelID, day, amountMinor, limitMinor)
	if err != nil {
		return false, fmt.Errorf("pool: reserve month: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ConfirmWatermark 把在途额度转为已用（日 + 月）。
func (r *Repository) ConfirmWatermark(ctx context.Context, channelID uuid.UUID, day time.Time, amountMinor int64) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE pool_watermark
		SET reserved_amount_minor = GREATEST(reserved_amount_minor - $3, 0),
		    used_amount_minor = used_amount_minor + $3,
		    used_count = used_count + 1,
		    updated_at = now()
		WHERE channel_id = $1 AND biz_date = $2::date
	`, channelID, day, amountMinor); err != nil {
		return fmt.Errorf("pool: confirm daily watermark: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE pool_month_watermark
		SET reserved_amount_minor = GREATEST(reserved_amount_minor - $3, 0),
		    used_amount_minor = used_amount_minor + $3,
		    used_count = used_count + 1,
		    updated_at = now()
		WHERE channel_id = $1 AND month_start = date_trunc('month', $2::date)::date
	`, channelID, day, amountMinor); err != nil {
		return fmt.Errorf("pool: confirm monthly watermark: %w", err)
	}
	return nil
}

// ReleaseWatermark 释放在途额度（日 + 月）。
func (r *Repository) ReleaseWatermark(ctx context.Context, channelID uuid.UUID, day time.Time, amountMinor int64) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE pool_watermark
		SET reserved_amount_minor = GREATEST(reserved_amount_minor - $3, 0),
		    updated_at = now()
		WHERE channel_id = $1 AND biz_date = $2::date
	`, channelID, day, amountMinor); err != nil {
		return fmt.Errorf("pool: release daily watermark: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE pool_month_watermark
		SET reserved_amount_minor = GREATEST(reserved_amount_minor - $3, 0),
		    updated_at = now()
		WHERE channel_id = $1 AND month_start = date_trunc('month', $2::date)::date
	`, channelID, day, amountMinor); err != nil {
		return fmt.Errorf("pool: release monthly watermark: %w", err)
	}
	return nil
}

// ReserveWatermark 原子预占当日额度；limit<=0 表示不限。
func (r *Repository) ReserveWatermark(ctx context.Context, channelID uuid.UUID, day time.Time, amountMinor, limitMinor int64) (bool, error) {
	if limitMinor <= 0 {
		_, err := r.pool.Exec(ctx, `
			INSERT INTO pool_watermark (channel_id, biz_date, reserved_amount_minor)
			VALUES ($1, $2, $3)
			ON CONFLICT (channel_id, biz_date) DO UPDATE
			SET reserved_amount_minor = pool_watermark.reserved_amount_minor + EXCLUDED.reserved_amount_minor,
			    updated_at = now()
		`, channelID, day, amountMinor)
		if err != nil {
			return false, fmt.Errorf("pool: reserve watermark: %w", err)
		}
		return true, nil
	}

	tag, err := r.pool.Exec(ctx, `
		INSERT INTO pool_watermark (channel_id, biz_date, reserved_amount_minor)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, biz_date) DO UPDATE
		SET reserved_amount_minor = pool_watermark.reserved_amount_minor + EXCLUDED.reserved_amount_minor,
		    updated_at = now()
		WHERE pool_watermark.used_amount_minor + pool_watermark.reserved_amount_minor + $3 <= $4
	`, channelID, day, amountMinor, limitMinor)
	if err != nil {
		return false, fmt.Errorf("pool: reserve watermark: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) InsertLeases(ctx context.Context, leases []domain.Lease) error {
	for _, l := range leases {
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO pool_lease (id, channel_id, attempt_id, amount_minor, rank, state, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (attempt_id, channel_id) DO UPDATE
			SET rank = EXCLUDED.rank, state = EXCLUDED.state, expires_at = EXCLUDED.expires_at
		`, l.ID, l.ChannelID, l.AttemptID, l.AmountMinor, l.Rank, l.State, l.ExpiresAt); err != nil {
			return fmt.Errorf("pool: insert lease: %w", err)
		}
	}
	return nil
}

func (r *Repository) SetLeaseState(ctx context.Context, attemptID, channelID uuid.UUID, state string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE pool_lease SET state = $3 WHERE attempt_id = $1 AND channel_id = $2
	`, attemptID, channelID, state)
	if err != nil {
		return fmt.Errorf("pool: set lease state: %w", err)
	}
	return nil
}

func (r *Repository) NextStandby(ctx context.Context, attemptID uuid.UUID) (*domain.Lease, error) {
	var l domain.Lease
	err := r.pool.QueryRow(ctx, `
		SELECT id, channel_id, attempt_id, amount_minor, rank, state, expires_at
		FROM pool_lease
		WHERE attempt_id = $1 AND state = 'STANDBY' AND expires_at > now()
		ORDER BY rank
		LIMIT 1
	`, attemptID).Scan(&l.ID, &l.ChannelID, &l.AttemptID, &l.AmountMinor, &l.Rank, &l.State, &l.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrLeaseNotFound
		}
		return nil, fmt.Errorf("pool: next standby: %w", err)
	}
	return &l, nil
}

// ── 管理端 CRUD ──

// ListAllChannels 返回某商户的全部池成员。
func (r *Repository) ListAllChannels(ctx context.Context, merchantID uuid.UUID) ([]domain.Channel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM pool_channel WHERE merchant_id = $1 ORDER BY created_at
	`, merchantID)
	if err != nil {
		return nil, fmt.Errorf("pool: list channel ids: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return r.ListChannels(ctx, ids)
}

// UpsertChannel 创建或更新池成员。
func (r *Repository) UpsertChannel(ctx context.Context, c domain.Channel) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `
		INSERT INTO pool_channel (
		    id, tenant_id, merchant_id, acquirer_channel_id, entity_id, psp_code, external_mid, mcc,
		    business_lines, currencies, card_brands, min_amount_minor, max_amount_minor,
		    daily_limit_minor, monthly_limit_minor, cost_bps, settlement_days, weight, health_score, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT (acquirer_channel_id) DO UPDATE SET
		    entity_id = EXCLUDED.entity_id,
		    psp_code = EXCLUDED.psp_code,
		    external_mid = EXCLUDED.external_mid,
		    mcc = EXCLUDED.mcc,
		    business_lines = EXCLUDED.business_lines,
		    currencies = EXCLUDED.currencies,
		    card_brands = EXCLUDED.card_brands,
		    min_amount_minor = EXCLUDED.min_amount_minor,
		    max_amount_minor = EXCLUDED.max_amount_minor,
		    daily_limit_minor = EXCLUDED.daily_limit_minor,
		    monthly_limit_minor = EXCLUDED.monthly_limit_minor,
		    cost_bps = EXCLUDED.cost_bps,
		    settlement_days = EXCLUDED.settlement_days,
		    weight = EXCLUDED.weight,
		    health_score = EXCLUDED.health_score,
		    status = EXCLUDED.status,
		    updated_at = now()
		RETURNING id
	`, uuid.New(), c.TenantID, c.MerchantID, c.AcquirerChannelID, c.EntityID, c.PSPCode, c.ExternalMID, c.MCC,
		c.BusinessLines, c.Currencies, c.CardBrands, c.MinAmountMinor, c.MaxAmountMinor,
		c.DailyLimitMinor, c.MonthlyLimitMinor, c.CostBps, c.SettlementDays, c.Weight, c.HealthScore, c.Status).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("pool: upsert channel: %w", err)
	}
	return id, nil
}

// ListPolicies 返回某商户的全部策略。
func (r *Repository) ListPolicies(ctx context.Context, merchantID uuid.UUID) ([]domain.Policy, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, merchant_id, site_id, business_line, allowed_channel_ids,
		       strategy, weights, status, version
		FROM pool_policy WHERE merchant_id = $1 ORDER BY site_id, business_line
	`, merchantID)
	if err != nil {
		return nil, fmt.Errorf("pool: list policies: %w", err)
	}
	defer rows.Close()

	var out []domain.Policy
	for rows.Next() {
		var p domain.Policy
		if err := rows.Scan(&p.ID, &p.TenantID, &p.MerchantID, &p.SiteID, &p.BusinessLine,
			&p.AllowedChannelIDs, &p.Strategy, &p.Weights, &p.Status, &p.Version); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertPolicy 创建或更新策略。
func (r *Repository) UpsertPolicy(ctx context.Context, p domain.Policy) (uuid.UUID, error) {
	if p.Strategy == "" {
		p.Strategy = "WEIGHTED"
	}
	if p.Status == "" {
		p.Status = "ACTIVE"
	}
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `
		INSERT INTO pool_policy (
		    id, tenant_id, merchant_id, site_id, business_line, allowed_channel_ids,
		    strategy, weights, status, version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1)
		ON CONFLICT (merchant_id, site_id, business_line) DO UPDATE SET
		    allowed_channel_ids = EXCLUDED.allowed_channel_ids,
		    strategy = EXCLUDED.strategy,
		    weights = EXCLUDED.weights,
		    status = EXCLUDED.status,
		    version = pool_policy.version + 1,
		    updated_at = now()
		RETURNING id
	`, uuid.New(), p.TenantID, p.MerchantID, p.SiteID, p.BusinessLine, p.AllowedChannelIDs,
		p.Strategy, p.Weights, p.Status).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("pool: upsert policy: %w", err)
	}
	return id, nil
}

// WatermarkRow 管理端展示用的水位行。
type WatermarkRow struct {
	ChannelID           uuid.UUID `json:"channel_id"`
	BizDate             string    `json:"biz_date"`
	UsedAmountMinor     int64     `json:"used_amount_minor"`
	ReservedAmountMinor int64     `json:"reserved_amount_minor"`
	UsedCount           int64     `json:"used_count"`
}

// ListWatermarks 返回指定日期的水位。
func (r *Repository) ListWatermarks(ctx context.Context, merchantID uuid.UUID, day string) ([]WatermarkRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT w.channel_id, w.biz_date::text, w.used_amount_minor, w.reserved_amount_minor, w.used_count
		FROM pool_watermark w
		JOIN pool_channel c ON c.id = w.channel_id
		WHERE c.merchant_id = $1 AND w.biz_date = $2::date
		ORDER BY w.channel_id
	`, merchantID, day)
	if err != nil {
		return nil, fmt.Errorf("pool: list watermarks: %w", err)
	}
	defer rows.Close()

	var out []WatermarkRow
	for rows.Next() {
		var w WatermarkRow
		if err := rows.Scan(&w.ChannelID, &w.BizDate, &w.UsedAmountMinor, &w.ReservedAmountMinor, &w.UsedCount); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
