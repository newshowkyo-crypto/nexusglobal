package service

import (
	"context"
	"encoding/json"
	"math/rand"
	"time"

	"github.com/nexusglobal/mamr/internal/mae/domain"
	"github.com/nexusglobal/mamr/internal/mae/repository/db"
)

type RouteRequest struct {
	Currency     string
	TenantID     string
	Jurisdiction string
}

type RouteResult struct {
	MAE       db.MaeConfiguration
	IsTesting bool
}

type Router struct {
	repo Repository
	rng  *rand.Rand
}

func NewRouter(repo Repository) *Router {
	return &Router{
		repo: repo,
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (r *Router) Select(ctx context.Context, req RouteRequest) (*RouteResult, error) {
	configs, err := r.repo.ListConfigs(ctx)
	if err != nil {
		return nil, err
	}

	candidates := r.filterCandidates(configs, req)
	if len(candidates) == 0 {
		return nil, nil
	}

	return r.weightedSelect(candidates), nil
}

func (r *Router) filterCandidates(configs []db.MaeConfiguration, req RouteRequest) []db.MaeConfiguration {
	var candidates []db.MaeConfiguration
	for _, cfg := range configs {
		status := domain.MAEStatus(cfg.Status)
		if status != domain.StatusActive && status != domain.StatusTesting {
			continue
		}

		if !r.supportsCurrency(cfg.SupportedCurrencies, req.Currency) {
			continue
		}

		if !r.inTenantScope(cfg.TenantScope, req.TenantID) {
			continue
		}

		if cfg.Jurisdiction != "" && cfg.Jurisdiction != req.Jurisdiction {
			continue
		}

		candidates = append(candidates, cfg)
	}
	return candidates
}

func (r *Router) supportsCurrency(supportedJSON []byte, currency string) bool {
	if len(supportedJSON) == 0 {
		return true
	}
	var currencies []string
	if err := json.Unmarshal(supportedJSON, &currencies); err != nil {
		return true
	}
	if len(currencies) == 0 {
		return true
	}
	for _, c := range currencies {
		if c == currency {
			return true
		}
	}
	return false
}

func (r *Router) inTenantScope(scopeJSON []byte, tenantID string) bool {
	if len(scopeJSON) == 0 {
		return true
	}
	var tenants []string
	if err := json.Unmarshal(scopeJSON, &tenants); err != nil {
		return true
	}
	if len(tenants) == 0 {
		return true
	}
	for _, t := range tenants {
		if t == tenantID {
			return true
		}
	}
	return false
}

func (r *Router) weightedSelect(candidates []db.MaeConfiguration) *RouteResult {
	var active []db.MaeConfiguration
	var testing []db.MaeConfiguration

	for _, c := range candidates {
		if domain.MAEStatus(c.Status) == domain.StatusTesting {
			testing = append(testing, c)
		} else {
			active = append(active, c)
		}
	}

	if len(testing) > 0 {
		testingBps := testing[0].TestingTrafficBps
		if testingBps <= 0 {
			testingBps = 500
		}
		roll := r.rng.Int31n(10000)
		if roll < testingBps && len(testing) > 0 {
			selected := r.selectByWeight(testing)
			return &RouteResult{MAE: selected, IsTesting: true}
		}
	}

	if len(active) > 0 {
		selected := r.selectByWeight(active)
		return &RouteResult{MAE: selected, IsTesting: false}
	}

	if len(testing) > 0 {
		selected := r.selectByWeight(testing)
		return &RouteResult{MAE: selected, IsTesting: true}
	}

	return nil
}

func (r *Router) selectByWeight(configs []db.MaeConfiguration) db.MaeConfiguration {
	var totalWeight int32
	for _, c := range configs {
		w := c.RoutingWeight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}

	if totalWeight == 0 {
		return configs[0]
	}

	roll := r.rng.Int31n(totalWeight)
	var cumulative int32
	for _, c := range configs {
		w := c.RoutingWeight
		if w <= 0 {
			w = 1
		}
		cumulative += w
		if roll < cumulative {
			return c
		}
	}
	return configs[len(configs)-1]
}
