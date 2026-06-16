package carding

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
)

// Repository 风控审计日志存储
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository 创建仓库
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// InsertRiskEvent 写入风控审计日志
func (r *Repository) InsertRiskEvent(ctx context.Context, event *domain.RiskEvent) error {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}

	_, err := r.pool.Exec(ctx,
		`INSERT INTO risk_event (
			id, tenant_id, merchant_id, app_id, decision, reason,
			buyer_ip_hash, card_bin_hash, device_hash, window_type,
			threshold_hit, current_count, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		event.ID, event.TenantID, event.MerchantID, event.AppID,
		event.Decision, event.Reason,
		event.BuyerIPHash, event.CardBINHash, event.DeviceHash,
		event.WindowType, event.ThresholdHit, event.CurrentCount,
		event.CreatedAt)
	return err
}

// ListRiskEvents 查询风控审计日志
func (r *Repository) ListRiskEvents(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.RiskEvent, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, merchant_id, app_id, decision, reason,
			buyer_ip_hash, card_bin_hash, device_hash, window_type,
			threshold_hit, current_count, created_at
		FROM risk_event
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2`,
		tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.RiskEvent
	for rows.Next() {
		var e domain.RiskEvent
		err := rows.Scan(
			&e.ID, &e.TenantID, &e.MerchantID, &e.AppID,
			&e.Decision, &e.Reason,
			&e.BuyerIPHash, &e.CardBINHash, &e.DeviceHash,
			&e.WindowType, &e.ThresholdHit, &e.CurrentCount,
			&e.CreatedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

// GetRiskEventByID 根据 ID 查询
func (r *Repository) GetRiskEventByID(ctx context.Context, id uuid.UUID) (*domain.RiskEvent, error) {
	var e domain.RiskEvent
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, merchant_id, app_id, decision, reason,
			buyer_ip_hash, card_bin_hash, device_hash, window_type,
			threshold_hit, current_count, created_at
		FROM risk_event WHERE id = $1`, id).Scan(
		&e.ID, &e.TenantID, &e.MerchantID, &e.AppID,
		&e.Decision, &e.Reason,
		&e.BuyerIPHash, &e.CardBINHash, &e.DeviceHash,
		&e.WindowType, &e.ThresholdHit, &e.CurrentCount,
		&e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// CountBlockedEvents 统计被拦截的事件数量
func (r *Repository) CountBlockedEvents(ctx context.Context, merchantID uuid.UUID, since time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM risk_event
		WHERE merchant_id = $1 AND decision = 'BLOCK' AND created_at >= $2`,
		merchantID, since).Scan(&count)
	return count, err
}
