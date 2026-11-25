package carding

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertRiskEvent(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID,
	deviceHash, binHash string, channelID *uuid.UUID, siteID string, at time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO risk_event
		    (tenant_id, merchant_id, app_id, decision, reason,
		     buyer_ip_hash, card_bin_hash, device_hash, channel_id, site_id,
		     window_type, threshold_hit, current_count, created_at)
		VALUES ($1, $1, 'app-detector', 'ALLOW', 'all_checks_passed',
		        'ip-hash', $2, $3, $4, $5, '60s', 0, 0, $6)
	`, merchantID, nullIfEmptyValue(binHash), nullIfEmptyValue(deviceHash), channelID, nullIfEmptyValue(siteID), at)
	require.NoError(t, err)
}

func nullIfEmptyValue(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestDetector_CrossSiteAndConcentration 验证跨站点同设备/同卡与池集中度检测。
func TestDetector_CrossSiteAndConcentration(t *testing.T) {
	pool, cleanup := setupRiskRuleDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	channelID := uuid.New()
	now := time.Now().UTC()

	// 1. 同一设备出现在两个站点
	insertRiskEvent(t, pool, merchantID, "device-abc", "", &channelID, "site-a", now.Add(-10*time.Minute))
	insertRiskEvent(t, pool, merchantID, "device-abc", "", &channelID, "site-b", now.Add(-9*time.Minute))

	// 2. 同一 BIN 出现在两个站点
	insertRiskEvent(t, pool, merchantID, "", "bin-xyz", &channelID, "site-a", now.Add(-8*time.Minute))
	insertRiskEvent(t, pool, merchantID, "", "bin-xyz", &channelID, "site-b", now.Add(-7*time.Minute))

	// 3. 池集中度：补足 50 条同通道
	for i := 0; i < 50; i++ {
		insertRiskEvent(t, pool, merchantID, "", "", &channelID, "site-a", now.Add(-time.Duration(i)*time.Second))
	}

	detector := NewDetector(pool)
	alerts, err := detector.Scan(ctx, time.Hour)
	require.NoError(t, err)

	counts := map[AlertType]int{}
	for _, a := range alerts {
		counts[a.Type]++
	}
	assert.Equal(t, 1, counts[AlertCrossSiteSameDevice])
	assert.Equal(t, 1, counts[AlertCrossSiteSameCard])
	assert.GreaterOrEqual(t, counts[AlertPoolConcentration], 1)

	for _, a := range alerts {
		if a.Type == AlertPoolConcentration {
			assert.Equal(t, channelID.String(), a.ChannelID)
			assert.GreaterOrEqual(t, a.Detail["share"].(float64), 0.8)
		}
	}
}

// TestDetector_ChannelSpike 验证单通道突增检测。
func TestDetector_ChannelSpike(t *testing.T) {
	pool, cleanup := setupRiskRuleDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	channelID := uuid.New()
	now := time.Now().UTC()

	// 基线：前 60 分钟每分钟 1 条（共 30 条样本）
	for i := 6; i < 36; i++ {
		insertRiskEvent(t, pool, merchantID, "", "", &channelID, "site-a", now.Add(-time.Duration(i)*time.Minute))
	}
	// 最近 5 分钟 20 条 → 4 条/分，显著高于基线 0.5 条/分
	for i := 0; i < 20; i++ {
		insertRiskEvent(t, pool, merchantID, "", "", &channelID, "site-a",
			now.Add(-time.Duration(i)*10*time.Second))
	}

	detector := NewDetector(pool)
	alerts, err := detector.Scan(ctx, time.Hour)
	require.NoError(t, err)

	var spike *Alert
	for i := range alerts {
		if alerts[i].Type == AlertChannelSpike {
			spike = &alerts[i]
		}
	}
	require.NotNil(t, spike, "channel spike should be detected")
	assert.Equal(t, channelID.String(), spike.ChannelID)
	assert.GreaterOrEqual(t, spike.Detail["recent_5m"].(int), 10)
}
