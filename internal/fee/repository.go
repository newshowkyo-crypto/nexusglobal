// Package fee 提供商户费率解析与手续费计算（P5）。
package fee

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pricing 费率配置。
type Pricing struct {
	ID            uuid.UUID `json:"id"`
	TenantID      uuid.UUID `json:"tenant_id"`
	MerchantID    uuid.UUID `json:"merchant_id"`
	Currency      string    `json:"currency"`
	CardBrand     string    `json:"card_brand"`
	ChannelID     uuid.UUID `json:"channel_id"`
	RateBps       int       `json:"rate_bps"`
	FixedFeeMinor int64     `json:"fixed_fee_minor"`
	MinFeeMinor   int64     `json:"min_fee_minor"`
	MaxFeeMinor   int64     `json:"max_fee_minor"`
	Status        string    `json:"status"`
}

var ErrNoPricing = errors.New("fee: no active pricing")

// Repository 费率与手续费记录。
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Resolve 按"渠道 > 币种 > 卡组织"的特异性选择最优费率；无匹配返回 nil。
func (r *Repository) Resolve(ctx context.Context, merchantID uuid.UUID, currency, cardBrand string, channelID uuid.UUID) (*Pricing, error) {
	const wildcard = "00000000-0000-0000-0000-000000000000"
	var p Pricing
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, merchant_id, currency, card_brand, channel_id,
		       rate_bps, fixed_fee_minor, min_fee_minor, max_fee_minor, status
		FROM merchant_pricing
		WHERE merchant_id = $1 AND status = 'ACTIVE'
		  AND (currency = '' OR currency = $2)
		  AND (card_brand = '' OR card_brand = $3)
		  AND (channel_id = $5::uuid OR channel_id = $4)
		ORDER BY (
		    CASE WHEN channel_id = $4 AND $4 <> $5::uuid THEN 8 ELSE 0 END +
		    CASE WHEN currency = $2 THEN 4 ELSE 0 END +
		    CASE WHEN card_brand = $3 THEN 2 ELSE 0 END
		) DESC, created_at DESC
		LIMIT 1
	`, merchantID, currency, cardBrand, channelID, wildcard).Scan(&p.ID, &p.TenantID, &p.MerchantID,
		&p.Currency, &p.CardBrand, &p.ChannelID, &p.RateBps, &p.FixedFeeMinor,
		&p.MinFeeMinor, &p.MaxFeeMinor, &p.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fee: resolve pricing: %w", err)
	}
	return &p, nil
}

// ChargeTx 计算手续费并落 fee_record（幂等）。
// 返回该 attempt 已记录的手续费（重复调用返回首次结果）。
func (r *Repository) ChargeTx(ctx context.Context, tx pgx.Tx, attemptID, merchantID uuid.UUID,
	currency string, channelID uuid.UUID, baseAmount int64) (int64, error) {
	pricing, err := r.Resolve(ctx, merchantID, currency, "", channelID)
	if err != nil {
		return 0, err
	}

	var feeMinor int64
	var pricingID *uuid.UUID
	var rateBps int
	var fixed int64
	if pricing != nil {
		feeMinor = Compute(baseAmount, pricing.RateBps, pricing.FixedFeeMinor, pricing.MinFeeMinor, pricing.MaxFeeMinor)
		pricingID = &pricing.ID
		rateBps = pricing.RateBps
		fixed = pricing.FixedFeeMinor
	}

	var inserted bool
	err = tx.QueryRow(ctx, `
		INSERT INTO fee_record
		    (tenant_id, merchant_id, payment_attempt_id, currency, base_amount_minor,
		     rate_bps, fixed_fee_minor, fee_amount_minor, pricing_id, source)
		VALUES ($1, $1, $2, $3, $4, $5, $6, $7, $8, 'CAPTURE')
		ON CONFLICT (payment_attempt_id, source) DO NOTHING
		RETURNING true
	`, merchantID, attemptID, currency, baseAmount, rateBps, fixed, feeMinor, pricingID).Scan(&inserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("fee: insert fee record: %w", err)
	}
	if inserted {
		return feeMinor, nil
	}

	// 已存在：返回首次记录
	var existing int64
	if err := tx.QueryRow(ctx, `
		SELECT fee_amount_minor FROM fee_record
		WHERE payment_attempt_id = $1 AND source = 'CAPTURE'
	`, attemptID).Scan(&existing); err != nil {
		return 0, fmt.Errorf("fee: read existing fee: %w", err)
	}
	return existing, nil
}

// Compute 计算手续费：rate(bps) 四舍五入 + 固定费，再按 min/max 夹取，且不超过本金。
func Compute(base int64, rateBps int, fixedMinor, minMinor, maxMinor int64) int64 {
	if base <= 0 {
		return 0
	}
	fee := (base*int64(rateBps) + 5000) / 10000
	fee += fixedMinor
	if minMinor > 0 && fee < minMinor {
		fee = minMinor
	}
	if maxMinor > 0 && fee > maxMinor {
		fee = maxMinor
	}
	if fee > base {
		fee = base
	}
	if fee < 0 {
		fee = 0
	}
	return fee
}

// ListPricing 列出商户费率。
func (r *Repository) ListPricing(ctx context.Context, merchantID uuid.UUID) ([]Pricing, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, merchant_id, currency, card_brand, channel_id,
		       rate_bps, fixed_fee_minor, min_fee_minor, max_fee_minor, status
		FROM merchant_pricing WHERE merchant_id = $1 ORDER BY created_at DESC
	`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pricing
	for rows.Next() {
		var p Pricing
		if err := rows.Scan(&p.ID, &p.TenantID, &p.MerchantID, &p.Currency, &p.CardBrand,
			&p.ChannelID, &p.RateBps, &p.FixedFeeMinor, &p.MinFeeMinor, &p.MaxFeeMinor, &p.Status); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertPricing 创建或更新费率（按 merchant+currency+brand+channel 唯一）。
func (r *Repository) UpsertPricing(ctx context.Context, p Pricing) (uuid.UUID, error) {
	if p.Status == "" {
		p.Status = "ACTIVE"
	}
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `
		INSERT INTO merchant_pricing
		    (id, tenant_id, merchant_id, currency, card_brand, channel_id,
		     rate_bps, fixed_fee_minor, min_fee_minor, max_fee_minor, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (merchant_id, currency, card_brand, channel_id) DO UPDATE SET
		    rate_bps = EXCLUDED.rate_bps,
		    fixed_fee_minor = EXCLUDED.fixed_fee_minor,
		    min_fee_minor = EXCLUDED.min_fee_minor,
		    max_fee_minor = EXCLUDED.max_fee_minor,
		    status = EXCLUDED.status,
		    updated_at = now()
		RETURNING id
	`, uuid.New(), p.TenantID, p.MerchantID, p.Currency, p.CardBrand, p.ChannelID,
		p.RateBps, p.FixedFeeMinor, p.MinFeeMinor, p.MaxFeeMinor, p.Status).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fee: upsert pricing: %w", err)
	}
	return id, nil
}
