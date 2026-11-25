package carding

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
)

// ruleCacheTTL 规则缓存有效期：规则变更最多 30s 生效。
const ruleCacheTTL = 30 * time.Second

type cachedRules struct {
	rules     domain.EffectiveRules
	expiresAt time.Time
}

// RuleStore 表化风控规则存储（P6）。
//
// 解析顺序（后者覆盖前者）：GLOBAL → MERCHANT → SITE → CHANNEL。
type RuleStore struct {
	pool *pgxpool.Pool

	mu    sync.RWMutex
	cache map[string]cachedRules
}

func NewRuleStore(pool *pgxpool.Pool) *RuleStore {
	return &RuleStore{pool: pool, cache: make(map[string]cachedRules)}
}

// Resolve 返回某商户在指定站点/通道下的有效阈值。
func (s *RuleStore) Resolve(ctx context.Context, merchantID uuid.UUID, siteID string, channelID uuid.UUID) (domain.EffectiveRules, error) {
	key := merchantID.String() + "|" + siteID + "|" + channelID.String()

	s.mu.RLock()
	hit, ok := s.cache[key]
	s.mu.RUnlock()
	if ok && time.Now().Before(hit.expiresAt) {
		return hit.rules, nil
	}

	rules, err := s.load(ctx, merchantID, siteID, channelID)
	if err != nil {
		return domain.DefaultEffectiveRules(), err
	}

	s.mu.Lock()
	s.cache[key] = cachedRules{rules: rules, expiresAt: time.Now().Add(ruleCacheTTL)}
	s.mu.Unlock()
	return rules, nil
}

func (s *RuleStore) load(ctx context.Context, merchantID uuid.UUID, siteID string, channelID uuid.UUID) (domain.EffectiveRules, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT scope, dimension, window_type, threshold, failure_rate::float8, min_sample, priority
		FROM risk_rule
		WHERE status = 'ACTIVE'
		  AND (
		    scope = 'GLOBAL'
		    OR (scope = 'MERCHANT' AND scope_id = $1)
		    OR (scope = 'SITE' AND scope_id = $2)
		    OR (scope = 'CHANNEL' AND scope_id = $3)
		  )
		ORDER BY CASE scope
		           WHEN 'GLOBAL' THEN 1
		           WHEN 'MERCHANT' THEN 2
		           WHEN 'SITE' THEN 3
		           WHEN 'CHANNEL' THEN 4
		           ELSE 5
		         END ASC, priority ASC
	`, merchantID.String(), siteID, channelID.String())
	if err != nil {
		return domain.DefaultEffectiveRules(), fmt.Errorf("risk rule: load: %w", err)
	}
	defer rows.Close()

	effective := domain.DefaultEffectiveRules()
	for rows.Next() {
		var (
			scope     string
			dimension string
			window    string
			threshold int
			rate      float64
			minSample int
			priority  int
		)
		if err := rows.Scan(&scope, &dimension, &window, &threshold, &rate, &minSample, &priority); err != nil {
			return domain.DefaultEffectiveRules(), fmt.Errorf("risk rule: scan: %w", err)
		}
		w := domain.WindowType(window)
		switch domain.DimensionType(dimension) {
		case domain.DimensionBuyerIP:
			effective.IPThreshold = threshold
			effective.IPWindow = w
		case domain.DimensionCardBIN:
			effective.BINThreshold = threshold
			effective.BINWindow = w
		case domain.DimensionDevice:
			effective.DeviceThreshold = threshold
			effective.DeviceWindow = w
		case domain.DimensionChannel:
			effective.ChannelThreshold = threshold
			effective.ChannelWindow = w
		case domain.DimensionSite:
			effective.SiteThreshold = threshold
			effective.SiteWindow = w
		case domain.DimensionAppID:
			effective.AppFailureRate = rate
			effective.AppMinSample = minSample
			effective.AppWindow = w
		}
	}
	if err := rows.Err(); err != nil {
		return domain.DefaultEffectiveRules(), err
	}
	return effective, nil
}

// Invalidate 清空缓存（规则写入后调用）。
func (s *RuleStore) Invalidate() {
	s.mu.Lock()
	s.cache = make(map[string]cachedRules)
	s.mu.Unlock()
}

// List 列出规则（scope/scopeID 为空时返回全部）。
func (s *RuleStore) List(ctx context.Context, scope, scopeID string) ([]domain.Rule, error) {
	query := `
		SELECT id, tenant_id, merchant_id, scope, scope_id, dimension, window_type,
		       threshold, failure_rate::float8, min_sample, priority, status, version
		FROM risk_rule WHERE 1 = 1`
	args := []any{}
	if scope != "" {
		args = append(args, scope)
		query += fmt.Sprintf(" AND scope = $%d", len(args))
	}
	if scopeID != "" {
		args = append(args, scopeID)
		query += fmt.Sprintf(" AND scope_id = $%d", len(args))
	}
	query += " ORDER BY scope, scope_id, dimension"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Rule
	for rows.Next() {
		var (
			r          domain.Rule
			tenantID   *uuid.UUID
			merchantID *uuid.UUID
		)
		if err := rows.Scan(&r.ID, &tenantID, &merchantID, &r.Scope, &r.ScopeID, &r.Dimension,
			&r.Window, &r.Threshold, &r.FailureRate, &r.MinSample, &r.Priority,
			&r.Status, &r.Version); err != nil {
			return nil, err
		}
		r.TenantID = tenantID
		r.MerchantID = merchantID
		out = append(out, r)
	}
	return out, rows.Err()
}

// Upsert 写入规则（按 scope+scope_id+dimension+window 唯一）。
func (s *RuleStore) Upsert(ctx context.Context, r domain.Rule) (uuid.UUID, error) {
	if r.Scope == "" {
		r.Scope = domain.ScopeGlobal
	}
	if r.Window == "" {
		r.Window = domain.Window60s
	}
	if r.Status == "" {
		r.Status = "ACTIVE"
	}
	if r.Priority == 0 {
		r.Priority = 100
	}

	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO risk_rule
		    (tenant_id, merchant_id, scope, scope_id, dimension, window_type,
		     threshold, failure_rate, min_sample, priority, status, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 1)
		ON CONFLICT (scope, scope_id, dimension, window_type) DO UPDATE SET
		    tenant_id = EXCLUDED.tenant_id,
		    merchant_id = EXCLUDED.merchant_id,
		    threshold = EXCLUDED.threshold,
		    failure_rate = EXCLUDED.failure_rate,
		    min_sample = EXCLUDED.min_sample,
		    priority = EXCLUDED.priority,
		    status = EXCLUDED.status,
		    version = risk_rule.version + 1,
		    updated_at = now()
		RETURNING id
	`, r.TenantID, r.MerchantID, r.Scope, r.ScopeID, r.Dimension, r.Window,
		r.Threshold, r.FailureRate, r.MinSample, r.Priority, r.Status).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("risk rule: upsert: %w", err)
	}
	s.Invalidate()
	return id, nil
}
