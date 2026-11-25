package carding

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/testenv"
	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
)

func setupRiskRuleDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping database integration test in short mode")
	}
	ctx := context.Background()
	pool, cleanup := testenv.Postgres(ctx, t)

	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	require.NoError(t, err)
	cfg := pool.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	m, err := migrate.New("file://"+filepath.ToSlash(dir), dsn)
	require.NoError(t, err)
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		require.NoError(t, err, "apply migrations")
	}
	_, _ = m.Close()
	return pool, cleanup
}

// TestRuleStore_ScopePrecedence 验证 GLOBAL → MERCHANT → SITE → CHANNEL 的覆盖顺序。
func TestRuleStore_ScopePrecedence(t *testing.T) {
	pool, cleanup := setupRiskRuleDB(t)
	defer cleanup()
	ctx := context.Background()

	store := NewRuleStore(pool)
	merchantID := uuid.New()
	channelID := uuid.New()

	// 1. 默认规则来自 migration 的 GLOBAL 种子
	got, err := store.Resolve(ctx, merchantID, "site-a", channelID)
	require.NoError(t, err)
	assert.Equal(t, 5, got.IPThreshold)
	assert.Equal(t, 3, got.BINThreshold)
	assert.Equal(t, 30.0, got.AppFailureRate)

	// 2. 商户级覆盖
	_, err = store.Upsert(ctx, domain.Rule{
		MerchantID: &merchantID, Scope: domain.ScopeMerchant, ScopeID: merchantID.String(),
		Dimension: domain.DimensionBuyerIP, Window: domain.Window60s, Threshold: 9,
	})
	require.NoError(t, err)
	got, err = store.Resolve(ctx, merchantID, "site-a", channelID)
	require.NoError(t, err)
	assert.Equal(t, 9, got.IPThreshold, "merchant scope overrides global")

	// 3. 站点级覆盖（更高优先级）
	_, err = store.Upsert(ctx, domain.Rule{
		Scope: domain.ScopeSite, ScopeID: "site-a",
		Dimension: domain.DimensionSite, Window: domain.Window5m, Threshold: 50,
	})
	require.NoError(t, err)
	got, err = store.Resolve(ctx, merchantID, "site-a", channelID)
	require.NoError(t, err)
	assert.Equal(t, 50, got.SiteThreshold)

	// 4. 通道级覆盖
	_, err = store.Upsert(ctx, domain.Rule{
		Scope: domain.ScopeChannel, ScopeID: channelID.String(),
		Dimension: domain.DimensionChannel, Window: domain.Window1h, Threshold: 100,
	})
	require.NoError(t, err)
	got, err = store.Resolve(ctx, merchantID, "site-a", channelID)
	require.NoError(t, err)
	assert.Equal(t, 100, got.ChannelThreshold)
	assert.Equal(t, domain.Window1h, got.ChannelWindow)

	// 5. 其他站点不受站点级规则影响
	other, err := store.Resolve(ctx, merchantID, "site-b", channelID)
	require.NoError(t, err)
	assert.Equal(t, 0, other.SiteThreshold)

	// 6. List 可按 scope 过滤
	rules, err := store.List(ctx, string(domain.ScopeChannel), "")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, channelID.String(), rules[0].ScopeID)
}

// stubResolver 固定返回一组有效规则。
type stubResolver struct {
	rules domain.EffectiveRules
}

func (s stubResolver) Resolve(context.Context, uuid.UUID, string, uuid.UUID) (domain.EffectiveRules, error) {
	return s.rules, nil
}

// TestGateway_ChannelAndSiteDimensions 验证通道/站点池化维度可以独立拦截。
func TestGateway_ChannelAndSiteDimensions(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()
	ctx := context.Background()

	gw := NewGateway(GatewayConfig{RDB: rdb, FailOpen: false})
	gw.SetRuleStore(stubResolver{rules: domain.EffectiveRules{
		IPThreshold:      1000,
		IPWindow:         domain.Window60s,
		ChannelThreshold: 2,
		ChannelWindow:    domain.Window60s,
		SiteThreshold:    1000,
		SiteWindow:       domain.Window60s,
		AppMinSample:     1000,
		AppWindow:        domain.Window1h,
	}})

	merchantID := uuid.New()
	channelID := uuid.New()
	req := domain.CheckRequest{
		TenantID: merchantID, MerchantID: merchantID, AppID: "app-p6",
		BuyerIP: "203.0.113.10", ChannelID: channelID, SiteID: "site-a",
	}

	for i := 0; i < 2; i++ {
		res := gw.Check(ctx, req)
		require.Equal(t, domain.DecisionAllow, res.Decision, "attempt %d should pass", i+1)
	}

	res := gw.Check(ctx, req)
	assert.Equal(t, domain.DecisionBlock, res.Decision)
	assert.Equal(t, domain.DimensionChannel, res.BlockedBy)
	assert.Equal(t, "channel_threshold_exceeded", res.Reason)

	// 换一个通道应重新计数
	otherChannel := uuid.New()
	req.ChannelID = otherChannel
	res = gw.Check(ctx, req)
	assert.Equal(t, domain.DecisionAllow, res.Decision)
}
