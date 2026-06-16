package carding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
)

// Gateway Redis 滑动窗口风控网关
type Gateway struct {
	rdb         *redis.Client
	logger      *slog.Logger
	ruleConfig  domain.RuleConfig
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

	// 根据策略模式获取调整后的阈值配置
	effectiveConfig := g.getEffectiveConfig(req.StrategyMode)

	now := time.Now().UnixMilli()

	// 1. 检查 IP 维度
	ipHash := domain.HashIP(req.BuyerIP)
	ipKey := fmt.Sprintf("carding:ip:%s", ipHash)
	ipWindowMs := domain.WindowToSeconds(effectiveConfig.IPWindow) * 1000

	ipCount, err := g.luaIncr.Run(ctx, g.rdb, []string{ipKey}, ipWindowMs, now).Int()
	if err != nil {
		g.logger.Error("lua incr failed for ip", "err", err, "ip_hash", ipHash)
		if g.failOpen {
			return domain.CheckResult{Decision: domain.DecisionAllow, Reason: "redis_error_fail_open"}
		}
		return domain.CheckResult{Decision: domain.DecisionBlock, Reason: "redis_error_fail_closed"}
	}

	if ipCount > effectiveConfig.IPFailureThreshold {
		return g.makeBlockResult(req.StrategyMode, "ip_threshold_exceeded", effectiveConfig.IPWindow,
			effectiveConfig.IPFailureThreshold, ipCount, domain.DimensionBuyerIP)
	}

	// 2. 检查 BIN 维度（如果有）
	if req.CardBIN != "" {
		binHash := domain.HashBIN(req.CardBIN)
		binKey := fmt.Sprintf("carding:bin:%s", binHash)
		binWindowMs := domain.WindowToSeconds(effectiveConfig.BINWindow) * 1000

		binCount, err := g.luaIncr.Run(ctx, g.rdb, []string{binKey}, binWindowMs, now).Int()
		if err != nil {
			g.logger.Error("lua incr failed for bin", "err", err, "bin_hash", binHash)
			if g.failOpen {
				return domain.CheckResult{Decision: domain.DecisionAllow, Reason: "redis_error_fail_open"}
			}
			return domain.CheckResult{Decision: domain.DecisionBlock, Reason: "redis_error_fail_closed"}
		}

		if binCount > effectiveConfig.BINFailureThreshold {
			return g.makeBlockResult(req.StrategyMode, "bin_threshold_exceeded", effectiveConfig.BINWindow,
				effectiveConfig.BINFailureThreshold, binCount, domain.DimensionCardBIN)
		}
	}

	// 3. 检查 App 维度（失败率）
	appKey := fmt.Sprintf("carding:app:%s:total", req.AppID)
	appFailKey := fmt.Sprintf("carding:app:%s:fail", req.AppID)
	appWindowMs := domain.WindowToSeconds(effectiveConfig.AppWindow) * 1000

	// 获取总数和失败数（不增加，只查询）
	totalCount, err := g.luaGetCount.Run(ctx, g.rdb, []string{appKey}, appWindowMs, now).Int()
	if err != nil {
		g.logger.Error("lua get count failed for app total", "err", err, "app_id", req.AppID)
	}

	failCount, err := g.luaGetCount.Run(ctx, g.rdb, []string{appFailKey}, appWindowMs, now).Int()
	if err != nil {
		g.logger.Error("lua get count failed for app fail", "err", err, "app_id", req.AppID)
	}

	// 计算失败率
	if totalCount >= effectiveConfig.AppMinSampleSize {
		failRate := float64(failCount) / float64(totalCount) * 100
		if failRate >= effectiveConfig.AppFailureRateThreshold {
			return g.makeBlockResult(req.StrategyMode, "app_failure_rate_exceeded", effectiveConfig.AppWindow,
				int(effectiveConfig.AppFailureRateThreshold), int(failRate), domain.DimensionAppID)
		}
	}

	// 4. 检查商户冷却期（如果有）
	cooldownKey := fmt.Sprintf("carding:cooldown:%s", req.MerchantID.String())
	cooldownExists, err := g.rdb.Exists(ctx, cooldownKey).Result()
	if err == nil && cooldownExists > 0 {
		result := g.makeBlockResult(req.StrategyMode, "merchant_in_cooldown", effectiveConfig.IPWindow,
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

// getEffectiveConfig 根据策略模式获取有效的规则配置
func (g *Gateway) getEffectiveConfig(strategyMode domain.ChannelStrategyMode) domain.RuleConfig {
	config := g.ruleConfig

	switch strategyMode {
	case domain.ChannelStrategyModeStrict:
		// STRICT 模式：阈值降低到 50%
		config.IPFailureThreshold = config.IPFailureThreshold * 50 / 100
		if config.IPFailureThreshold < 1 {
			config.IPFailureThreshold = 1
		}
		config.BINFailureThreshold = config.BINFailureThreshold * 50 / 100
		if config.BINFailureThreshold < 1 {
			config.BINFailureThreshold = 1
		}
		config.AppFailureRateThreshold = config.AppFailureRateThreshold * 50 / 100
	case domain.ChannelStrategyModeManualReview:
		// MANUAL_REVIEW 模式：保持默认阈值，但返回 CHALLENGE 而非 BLOCK
	case domain.ChannelStrategyModeDefault:
		// DEFAULT 模式：使用默认配置
	default:
		// 未知模式：使用默认配置
	}

	return config
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
