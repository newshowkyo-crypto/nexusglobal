package carding

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ============================================================================
// P6: 池化风控告警检测
//
// 五类检测：
//   1. 跨站点同设备（CROSS_SITE_SAME_DEVICE）
//   2. 跨站点同卡 BIN（CROSS_SITE_SAME_CARD）
//   3. 单通道突增（CHANNEL_SPIKE）
//   4. 池集中度（POOL_CONCENTRATION）
//   5. 拒付率传导（CHARGEBACK_RATE）
//
// 检测只读数据和产出告警，不改变交易决策；处置仍走人工/规则。
// ============================================================================

// AlertType 告警类型。
type AlertType string

const (
	AlertCrossSiteSameDevice AlertType = "CROSS_SITE_SAME_DEVICE"
	AlertCrossSiteSameCard   AlertType = "CROSS_SITE_SAME_CARD"
	AlertChannelSpike        AlertType = "CHANNEL_SPIKE"
	AlertPoolConcentration   AlertType = "POOL_CONCENTRATION"
	AlertChargebackRate      AlertType = "CHARGEBACK_RATE"
)

// Alert 一条风控告警。
type Alert struct {
	Type       AlertType      `json:"type"`
	Severity   string         `json:"severity"`
	MerchantID uuid.UUID      `json:"merchant_id"`
	SiteID     string         `json:"site_id,omitempty"`
	ChannelID  string         `json:"channel_id,omitempty"`
	Detail     map[string]any `json:"detail"`
	ObservedAt time.Time      `json:"observed_at"`
}

// Detector 池化风险检测器。
type Detector struct {
	pool *pgxpool.Pool
}

func NewDetector(pool *pgxpool.Pool) *Detector {
	return &Detector{pool: pool}
}

// Scan 执行全部检测；window 为观察窗口（默认 1 小时）。
func (d *Detector) Scan(ctx context.Context, window time.Duration) ([]Alert, error) {
	if window <= 0 {
		window = time.Hour
	}
	if window > 24*time.Hour {
		window = 24 * time.Hour
	}

	var alerts []Alert
	detectors := []func(context.Context, time.Duration) ([]Alert, error){
		d.detectCrossSiteDevice,
		d.detectCrossSiteCard,
		d.detectChannelSpike,
		d.detectPoolConcentration,
		d.detectChargebackRate,
	}
	for _, fn := range detectors {
		found, err := fn(ctx, window)
		if err != nil {
			return alerts, err
		}
		alerts = append(alerts, found...)
	}
	return alerts, nil
}

func (d *Detector) detectCrossSiteDevice(ctx context.Context, window time.Duration) ([]Alert, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT merchant_id, device_hash, COUNT(DISTINCT site_id)::int, array_agg(DISTINCT site_id)
		FROM risk_event
		WHERE created_at >= now() - $1::interval
		  AND device_hash IS NOT NULL AND device_hash <> ''
		  AND site_id IS NOT NULL AND site_id <> ''
		GROUP BY merchant_id, device_hash
		HAVING COUNT(DISTINCT site_id) >= 2
		ORDER BY COUNT(DISTINCT site_id) DESC
		LIMIT 100
	`, fmt.Sprintf("%d minutes", int(window.Minutes())))
	if err != nil {
		return nil, fmt.Errorf("risk detector: cross-site device: %w", err)
	}
	defer rows.Close()

	var out []Alert
	for rows.Next() {
		var merchantID uuid.UUID
		var deviceHash string
		var sites int
		var siteList []string
		if err := rows.Scan(&merchantID, &deviceHash, &sites, &siteList); err != nil {
			return nil, err
		}
		out = append(out, Alert{
			Type:       AlertCrossSiteSameDevice,
			Severity:   "HIGH",
			MerchantID: merchantID,
			Detail:     map[string]any{"device_hash": deviceHash, "site_count": sites, "sites": siteList},
			ObservedAt: time.Now().UTC(),
		})
	}
	return out, rows.Err()
}

func (d *Detector) detectCrossSiteCard(ctx context.Context, window time.Duration) ([]Alert, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT merchant_id, card_bin_hash, COUNT(DISTINCT site_id)::int, array_agg(DISTINCT site_id)
		FROM risk_event
		WHERE created_at >= now() - $1::interval
		  AND card_bin_hash IS NOT NULL AND card_bin_hash <> ''
		  AND site_id IS NOT NULL AND site_id <> ''
		GROUP BY merchant_id, card_bin_hash
		HAVING COUNT(DISTINCT site_id) >= 2
		ORDER BY COUNT(DISTINCT site_id) DESC
		LIMIT 100
	`, fmt.Sprintf("%d minutes", int(window.Minutes())))
	if err != nil {
		return nil, fmt.Errorf("risk detector: cross-site card: %w", err)
	}
	defer rows.Close()

	var out []Alert
	for rows.Next() {
		var merchantID uuid.UUID
		var binHash string
		var sites int
		var siteList []string
		if err := rows.Scan(&merchantID, &binHash, &sites, &siteList); err != nil {
			return nil, err
		}
		out = append(out, Alert{
			Type:       AlertCrossSiteSameCard,
			Severity:   "HIGH",
			MerchantID: merchantID,
			Detail:     map[string]any{"card_bin_hash": binHash, "site_count": sites, "sites": siteList},
			ObservedAt: time.Now().UTC(),
		})
	}
	return out, rows.Err()
}

// detectChannelSpike 最近 5 分钟速率 >= 基线（前 60 分钟）3 倍，且样本 >= 10。
func (d *Detector) detectChannelSpike(ctx context.Context, _ time.Duration) ([]Alert, error) {
	rows, err := d.pool.Query(ctx, `
		WITH recent AS (
			SELECT merchant_id, channel_id, COUNT(*)::numeric AS c
			FROM risk_event
			WHERE channel_id IS NOT NULL AND created_at >= now() - interval '5 minutes'
			GROUP BY merchant_id, channel_id
		), baseline AS (
			SELECT merchant_id, channel_id, COUNT(*)::numeric / 60.0 AS per_min
			FROM risk_event
			WHERE channel_id IS NOT NULL
			  AND created_at >= now() - interval '65 minutes'
			  AND created_at < now() - interval '5 minutes'
			GROUP BY merchant_id, channel_id
		)
		SELECT r.merchant_id, r.channel_id, r.c::int, COALESCE(b.per_min, 0)::float8
		FROM recent r
		LEFT JOIN baseline b USING (merchant_id, channel_id)
		WHERE r.c >= 10
		  AND (b.per_min IS NULL OR r.c / 5.0 >= b.per_min * 3)
		ORDER BY r.c DESC
		LIMIT 100
	`)
	if err != nil {
		return nil, fmt.Errorf("risk detector: channel spike: %w", err)
	}
	defer rows.Close()

	var out []Alert
	for rows.Next() {
		var merchantID, channelID uuid.UUID
		var recent int
		var baseline float64
		if err := rows.Scan(&merchantID, &channelID, &recent, &baseline); err != nil {
			return nil, err
		}
		out = append(out, Alert{
			Type:       AlertChannelSpike,
			Severity:   "MEDIUM",
			MerchantID: merchantID,
			ChannelID:  channelID.String(),
			Detail: map[string]any{
				"recent_5m": recent, "baseline_per_min": baseline,
				"ratio": safeRatio(float64(recent)/5.0, baseline),
			},
			ObservedAt: time.Now().UTC(),
		})
	}
	return out, rows.Err()
}

// detectPoolConcentration 单通道占比 >= 80% 且样本 >= 50。
func (d *Detector) detectPoolConcentration(ctx context.Context, window time.Duration) ([]Alert, error) {
	rows, err := d.pool.Query(ctx, `
		WITH per_channel AS (
			SELECT merchant_id, channel_id, COUNT(*)::numeric AS c,
			       SUM(COUNT(*)) OVER (PARTITION BY merchant_id)::numeric AS total
			FROM risk_event
			WHERE channel_id IS NOT NULL AND created_at >= now() - $1::interval
			GROUP BY merchant_id, channel_id
		)
		SELECT merchant_id, channel_id, c::int, total::int, (c / total)::float8
		FROM per_channel
		WHERE total >= 50 AND c / total >= 0.8
		ORDER BY total DESC
		LIMIT 100
	`, fmt.Sprintf("%d minutes", int(window.Minutes())))
	if err != nil {
		return nil, fmt.Errorf("risk detector: pool concentration: %w", err)
	}
	defer rows.Close()

	var out []Alert
	for rows.Next() {
		var merchantID, channelID uuid.UUID
		var count, total int
		var share float64
		if err := rows.Scan(&merchantID, &channelID, &count, &total, &share); err != nil {
			return nil, err
		}
		out = append(out, Alert{
			Type:       AlertPoolConcentration,
			Severity:   "MEDIUM",
			MerchantID: merchantID,
			ChannelID:  channelID.String(),
			Detail:     map[string]any{"channel_count": count, "total": total, "share": share},
			ObservedAt: time.Now().UTC(),
		})
	}
	return out, rows.Err()
}

// detectChargebackRate 近 7 天按通道拒付率 >= 1%，且样本 >= 50。
func (d *Detector) detectChargebackRate(ctx context.Context, _ time.Duration) ([]Alert, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT a.merchant_id, a.acquirer_channel_id,
		       COUNT(DISTINCT d.id)::int AS disputes,
		       COUNT(*)::int AS attempts,
		       (COUNT(DISTINCT d.id)::numeric / COUNT(*))::float8 AS rate
		FROM payment_attempts a
		LEFT JOIN payment_dispute d
		  ON d.order_id = a.order_id::text AND d.dispute_type = 'CHARGEBACK'
		WHERE a.created_at >= now() - interval '7 days'
		  AND a.acquirer_channel_id IS NOT NULL
		GROUP BY a.merchant_id, a.acquirer_channel_id
		HAVING COUNT(*) >= 50
		   AND COUNT(DISTINCT d.id)::numeric / COUNT(*) >= 0.01
		ORDER BY rate DESC
		LIMIT 100
	`)
	if err != nil {
		return nil, fmt.Errorf("risk detector: chargeback rate: %w", err)
	}
	defer rows.Close()

	var out []Alert
	for rows.Next() {
		var merchantID, channelID uuid.UUID
		var disputes, attempts int
		var rate float64
		if err := rows.Scan(&merchantID, &channelID, &disputes, &attempts, &rate); err != nil {
			return nil, err
		}
		out = append(out, Alert{
			Type:       AlertChargebackRate,
			Severity:   "HIGH",
			MerchantID: merchantID,
			ChannelID:  channelID.String(),
			Detail:     map[string]any{"disputes": disputes, "attempts": attempts, "rate": rate},
			ObservedAt: time.Now().UTC(),
		})
	}
	return out, rows.Err()
}

func safeRatio(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	return numerator / denominator
}
