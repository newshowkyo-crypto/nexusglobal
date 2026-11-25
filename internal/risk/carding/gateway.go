package carding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
)

// RuleResolver 有效阈值解析器（P6）。*RuleStore 为其生产实现。
type RuleResolver interface {
	Resolve(ctx context.Context, merchantID uuid.UUID, siteID string, channelID uuid.UUID) (domain.EffectiveRules, error)
}

// Gateway Redis 滑动窗口风控网关
type Gateway struct {
	rdb         *redis.Client
	logger      *slog.Logger
	ruleConfig  domain.RuleConfig
	ruleStore   RuleResolver
	failOpen    bool          // Redis 不可用时是否 fail-open（local/mock 用）
	luaIncr     *redis.Script // Lua 脚本：原子 INCR + EXPIRE
	luaGetCount *redis.Script // Lua 脚本：获取滑动窗口计数
}

// GatewayConfig 网关配置
type GatewayConfig struct {
	RDB        *redis.Client
	Logger     *slog.Logger
	RuleConfig domain.RuleConfig
	FailOpen   bool // production 默认 false（fail-closed），local/mock 可 true
}

// NewGateway 创建风控网关
func NewGateway(cfg GatewayConfig) *Gateway {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RuleConfig.IPFailureThreshold == 0 {
		cfg.RuleConfig = domain.DefaultRuleConfig
	}

	g := &Gateway{
		rdb:        cfg.RDB,
		logger:     cfg.Logger,
		ruleConfig: cfg.RuleConfig,
		failOpen:   cfg.FailOpen,
	}

	// Lua 脚本：原子 INCR + 设置过期时间
	// 返回当前计数
	g.luaIncr = redis.NewScript(`
		local key = KEYS[1]
		local window = tonumber(ARGV[1])
		local now = tonumber(ARGV[2])
		
		-- 使用 ZSET 实现滑动窗口
		-- 移除窗口外的旧数据
		redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
		
		-- 添加当前事件
		redis.call('ZADD', key, now, now .. '-' .. math.random())
		
		-- 设置过期时间（窗口 + 1秒缓冲）
		redis.call('EXPIRE', key, window + 1)
		
		-- 返回当前计数
		return redis.call('ZCARD', key)
	`)

	// Lua 脚本：获取滑动窗口计数（不增加）
	g.luaGetCount = redis.NewScript(`
		local key = KEYS[1]
		local window = tonumber(ARGV[1])
		local now = tonumber(ARGV[2])
		
		-- 移除窗口外的旧数据
		redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
		
		-- 返回当前计数
		return redis.call('ZCARD', key)
	`)

	return g
}

// SetRuleStore 注入表化规则存储（P6）；未注入时使用默认阈值。
func (g *Gateway) SetRuleStore(store RuleResolver) { g.ruleStore = store }

// Check 检查请求是否应该被拦截
func (g *Gateway) Check(ctx context.Context, req domain.CheckRequest) domain.CheckResult {
	// Redis 不可用时的处理
	if g.rdb == nil {
		if g.failOpen {
			g.logger.Warn("redis unavailable, fail-open allowing request")
			return domain.CheckResult{
				Decision: domain.DecisionAllow,
				Reason:   "redis_unavailable_fail_open",
			}
		}
		g.logger.Warn("redis unavailable, fail-closed blocking request")
		return domain.CheckResult{
			Decision: domain.DecisionBlock,
			Reason:   "redis_unavailable_fail_closed",
		}
	}

	effective := g.effectiveRules(ctx, req)
	applyStrategyMode(&effective, req.StrategyMode)

	now := time.Now().UnixMilli()

	// 1. IP 维度
	if effective.IPThreshold > 0 {
		ipHash := domain.HashIP(req.BuyerIP)
		key := fmt.Sprintf("carding:ip:%s", ipHash)
		count, err := g.bump(ctx, key, effective.IPWindow, now)
		if err != nil {
			return g.redisErrResult(err)
		}
		if count > effective.IPThreshold {
			return g.makeBlockResult(req.StrategyMode, "ip_threshold_exceeded", effective.IPWindow,
				effective.IPThreshold, count, domain.DimensionBuyerIP)
		}
	}

	// 2. BIN 维度
	if req.CardBIN != "" && effective.BINThreshold > 0 {
		binHash := domain.HashBIN(req.CardBIN)
		key := fmt.Sprintf("carding:bin:%s", binHash)
		count, err := g.bump(ctx, key, effective.BINWindow, now)
		if err != nil {
			return g.redisErrResult(err)
		}
		if count > effective.BINThreshold {
			return g.makeBlockResult(req.StrategyMode, "bin_threshold_exceeded", effective.BINWindow,
				effective.BINThreshold, count, domain.DimensionCardBIN)
		}
	}

	// 3. 设备维度
	if req.DeviceID != "" && effective.DeviceThreshold > 0 {
		key := fmt.Sprintf("carding:device:%s", domain.HashDevice(req.DeviceID))
		count, err := g.bump(ctx, key, effective.DeviceWindow, now)
		if err != nil {
			return g.redisErrResult(err)
		}
		if count > effective.DeviceThreshold {
			return g.makeBlockResult(req.StrategyMode, "device_threshold_exceeded", effective.DeviceWindow,
				effective.DeviceThreshold, count, domain.DimensionDevice)
		}
	}

	// 4. 通道维度（池化）
	if req.ChannelID != uuid.Nil && effective.ChannelThreshold > 0 {
		key := fmt.Sprintf("carding:channel:%s", req.ChannelID.String())
		count, err := g.bump(ctx, key, effective.ChannelWindow, now)
		if err != nil {
			return g.redisErrResult(err)
		}
		if count > effective.ChannelThreshold {
			return g.makeBlockResult(req.StrategyMode, "channel_threshold_exceeded", effective.ChannelWindow,
				effective.ChannelThreshold, count, domain.DimensionChannel)
		}
	}

	// 5. 站点维度（池化）
	if req.SiteID != "" && effective.SiteThreshold > 0 {
		key := fmt.Sprintf("carding:site:%s", req.SiteID)
		count, err := g.bump(ctx, key, effective.SiteWindow, now)
		if err != nil {
			return g.redisErrResult(err)
		}
		if count > effective.SiteThreshold {
			return g.makeBlockResult(req.StrategyMode, "site_threshold_exceeded", effective.SiteWindow,
				effective.SiteThreshold, count, domain.DimensionSite)
		}
	}

	// 6. App 维度（失败率）
	appKey := fmt.Sprintf("carding:app:%s:total", req.AppID)
	appFailKey := fmt.Sprintf("carding:app:%s:fail", req.AppID)
	appWindowMs := domain.WindowToSeconds(effective.AppWindow) * 1000

	totalCount, err := g.luaGetCount.Run(ctx, g.rdb, []string{appKey}, appWindowMs, now).Int()
	if err != nil {
		g.logger.Error("lua get count failed for app total", "err", err, "app_id", req.AppID)
	}
	failCount, err := g.luaGetCount.Run(ctx, g.rdb, []string{appFailKey}, appWindowMs, now).Int()
	if err != nil {
		g.logger.Error("lua get count failed for app fail", "err", err, "app_id", req.AppID)
	}

	if effective.AppMinSample > 0 && totalCount >= effective.AppMinSample {
		failRate := float64(failCount) / float64(totalCount) * 100
		if failRate >= effective.AppFailureRate {
			return g.makeBlockResult(req.StrategyMode, "app_failure_rate_exceeded", effective.AppWindow,
				int(effective.AppFailureRate), int(failRate), domain.DimensionAppID)
		}
	}

	// 7. 商户冷却期
	cooldownKey := fmt.Sprintf("carding:cooldown:%s", req.MerchantID.String())
	cooldownExists, err := g.rdb.Exists(ctx, cooldownKey).Result()
	if err == nil && cooldownExists > 0 {
		result := g.makeBlockResult(req.StrategyMode, "merchant_in_cooldown", effective.IPWindow,
			0, 0, domain.DimensionMerchantID)
		result.Decision = domain.DecisionCooldown
		return result
	}

	// 所有检查通过，允许
	return domain.CheckResult{
		Decision: domain.DecisionAllow,
		Reason:   "all_checks_passed",
	}
}

// effectiveRules 解析有效阈值：优先表化规则，失败时回退默认值。
func (g *Gateway) effectiveRules(ctx context.Context, req domain.CheckRequest) domain.EffectiveRules {
	if g.ruleStore == nil {
		return effectiveFromConfig(g.ruleConfig)
	}
	rules, err := g.ruleStore.Resolve(ctx, req.MerchantID, req.SiteID, req.ChannelID)
	if err != nil {
		g.logger.Warn("risk rule resolve failed, fallback to defaults", "err", err)
		return effectiveFromConfig(g.ruleConfig)
	}
	return rules
}

// bump 在滑动窗口内计数 +1。
func (g *Gateway) bump(ctx context.Context, key string, window domain.WindowType, now int64) (int, error) {
	windowMs := domain.WindowToSeconds(window) * 1000
	return g.luaIncr.Run(ctx, g.rdb, []string{key}, windowMs, now).Int()
}

func (g *Gateway) redisErrResult(err error) domain.CheckResult {
	g.logger.Error("risk gateway redis operation failed", "err", err)
	if g.failOpen {
		return domain.CheckResult{Decision: domain.DecisionAllow, Reason: "redis_error_fail_open"}
	}
	return domain.CheckResult{Decision: domain.DecisionBlock, Reason: "redis_error_fail_closed"}
}

// effectiveFromConfig 由硬编码默认配置生成有效阈值。
func effectiveFromConfig(cfg domain.RuleConfig) domain.EffectiveRules {
	return domain.EffectiveRules{
		IPThreshold:    cfg.IPFailureThreshold,
		IPWindow:       cfg.IPWindow,
		BINThreshold:   cfg.BINFailureThreshold,
		BINWindow:      cfg.BINWindow,
		AppFailureRate: cfg.AppFailureRateThreshold,
		AppMinSample:   cfg.AppMinSampleSize,
		AppWindow:      cfg.AppWindow,
	}
}

// applyStrategyMode STRICT 模式收紧阈值（50%，下限 1）。
func applyStrategyMode(rules *domain.EffectiveRules, mode domain.ChannelStrategyMode) {
	if mode != domain.ChannelStrategyModeStrict {
		return
	}
	half := func(v int) int {
		if v <= 0 {
			return v
		}
		out := v * 50 / 100
		if out < 1 {
			out = 1
		}
		return out
	}
	rules.IPThreshold = half(rules.IPThreshold)
	rules.BINThreshold = half(rules.BINThreshold)
	rules.DeviceThreshold = half(rules.DeviceThreshold)
	rules.ChannelThreshold = half(rules.ChannelThreshold)
	rules.SiteThreshold = half(rules.SiteThreshold)
	rules.AppFailureRate = rules.AppFailureRate * 50 / 100
}

// makeBlockResult 根据策略模式生成拦截结果
func (g *Gateway) makeBlockResult(strategyMode domain.ChannelStrategyMode, reason string,
	windowType domain.WindowType, threshold, currentCount int, blockedBy domain.DimensionType) domain.CheckResult {

	result := domain.CheckResult{
		Reason:               reason,
		WindowType:           windowType,
		Threshold:            threshold,
		CurrentCount:         currentCount,
		BlockedBy:            blockedBy,
		RequiresManualReview: strategyMode == domain.ChannelStrategyModeManualReview,
	}

	if strategyMode == domain.ChannelStrategyModeManualReview {
		result.Decision = domain.DecisionChallenge
	} else {
		result.Decision = domain.DecisionBlock
	}

	return result
}

// RecordFailure 记录失败事件（用于 App 维度的失败率计算）
func (g *Gateway) RecordFailure(ctx context.Context, req domain.CheckRequest) {
	if g.rdb == nil {
		return
	}

	now := time.Now().UnixMilli()
	appKey := fmt.Sprintf("carding:app:%s:total", req.AppID)
	appFailKey := fmt.Sprintf("carding:app:%s:fail", req.AppID)
	appWindowMs := domain.WindowToSeconds(g.ruleConfig.AppWindow) * 1000

	// 增加总数
	g.luaIncr.Run(ctx, g.rdb, []string{appKey}, appWindowMs, now)

	// 增加失败数
	g.luaIncr.Run(ctx, g.rdb, []string{appFailKey}, appWindowMs, now)
}

// RecordSuccess 记录成功事件（用于 App 维度的失败率计算）
func (g *Gateway) RecordSuccess(ctx context.Context, req domain.CheckRequest) {
	if g.rdb == nil {
		return
	}

	now := time.Now().UnixMilli()
	appKey := fmt.Sprintf("carding:app:%s:total", req.AppID)
	appWindowMs := domain.WindowToSeconds(g.ruleConfig.AppWindow) * 1000

	// 只增加总数，不增加失败数
	g.luaIncr.Run(ctx, g.rdb, []string{appKey}, appWindowMs, now)
}

// SetCooldown 设置商户冷却期
func (g *Gateway) SetCooldown(ctx context.Context, merchantID string) {
	if g.rdb == nil {
		return
	}

	cooldownKey := fmt.Sprintf("carding:cooldown:%s", merchantID)
	g.rdb.Set(ctx, cooldownKey, "1", g.ruleConfig.CooldownDuration)
}

// ClearCooldown 清除商户冷却期
func (g *Gateway) ClearCooldown(ctx context.Context, merchantID string) {
	if g.rdb == nil {
		return
	}

	cooldownKey := fmt.Sprintf("carding:cooldown:%s", merchantID)
	g.rdb.Del(ctx, cooldownKey)
}

// GetCount 获取指定维度的当前计数（用于调试/监控）
func (g *Gateway) GetCount(ctx context.Context, dimension domain.DimensionType, hash string, window domain.WindowType) (int, error) {
	if g.rdb == nil {
		return 0, domain.ErrRedisUnavailable
	}

	key := fmt.Sprintf("carding:%s:%s", dimension, hash)
	windowMs := domain.WindowToSeconds(window) * 1000
	now := time.Now().UnixMilli()

	return g.luaGetCount.Run(ctx, g.rdb, []string{key}, windowMs, now).Int()
}
