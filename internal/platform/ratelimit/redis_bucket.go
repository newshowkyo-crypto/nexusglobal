// Package ratelimit 提供 Redis token bucket 限流器（G14 修复产物）。
//
// 修复内容：
//  1. 使用同一 hash tag 的 key（{bucket}:tokens, {bucket}:last_refill）确保
//     Redis Cluster 同 slot，原子性保证。
//  2. 设置 TTL，避免 key 永驻。
//  3. Lua 脚本拒绝负 elapsed（时钟回拨防护）和非法 RPS。
//  4. 分别执行 merchant budget 和 tenant aggregate budget，任何一层不足都限流。
//  5. 金融入口生产默认 failClosed=true；Redis 故障返回 503 RATE_LIMITER_FAIL_CLOSED。
package ratelimit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter 限流器接口。
type RateLimiter interface {
	Allow(ctx context.Context, key string, merchantTier string) (bool, error)
}

// RedisBucket 基于 Redis token bucket 的限流器。
//
// key 约定：中间件传入 "tenant:{tenantID}:merchant:{merchantID}" 或 "merchant:{merchantID}"。
// 当 key 包含 tenant 前缀时，本实现额外对 tenant 做聚合限流（tenant 级别使用更宽松的 budget）。
type RedisBucket struct {
	rdb        *redis.Client
	failClosed bool
	tierConfig map[string]int // merchant 级 RPS
	tenantRPS  int            // tenant 聚合级 RPS
	defaultRPS int
	ttlSeconds int // key TTL，避免永驻
}

// NewRedisBucket 创建限流器。failClosed=true 时 Redis 故障拒绝所有限流请求。
func NewRedisBucket(rdb *redis.Client, failClosed bool) *RedisBucket {
	return &RedisBucket{
		rdb:        rdb,
		failClosed: failClosed,
		tierConfig: map[string]int{
			"TIER_1_HIGH_RISK": 10,
			"TIER_2_COMPLIANT": 50,
		},
		tenantRPS:  200, // tenant 聚合级别：200 RPS（比单个 merchant 宽松）
		defaultRPS: 50,
		ttlSeconds: 3600, // 1 小时 TTL
	}
}

func (b *RedisBucket) getRPS(merchantTier string) int {
	if rps, ok := b.tierConfig[merchantTier]; ok {
		return rps
	}
	return b.defaultRPS
}

// tokenBucketLua 单 key token bucket Lua 脚本。
//
// 使用 hash tag 确保多 key 同 slot：KEYS[1]={bucket}:tokens, KEYS[2]={bucket}:last_refill。
// 设置 TTL 避免永驻。拒绝负 elapsed（时钟回拨防护）。
//
// 返回 1=允许，0=拒绝。
const tokenBucketLua = `
local key_tokens = KEYS[1]
local key_last_refill = KEYS[2]
local capacity = tonumber(ARGV[1])
local rps = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

-- 拒绝非法 RPS
if rps == nil or rps <= 0 then
    return 0
end
if capacity == nil or capacity <= 0 then
    return 0
end

local tokens = tonumber(redis.call('GET', key_tokens))
local last_refill = tonumber(redis.call('GET', key_last_refill))

if not tokens then
    tokens = capacity * 1000
end
if not last_refill then
    last_refill = now
end

local elapsed_ms = now - last_refill
-- 拒绝负 elapsed（时钟回拨防护）：重置 last_refill 但不补充 token
if elapsed_ms < 0 then
    redis.call('SET', key_last_refill, now, 'EX', ttl)
    if tokens >= 1000 then
        tokens = tokens - 1000
        redis.call('SET', key_tokens, tokens, 'EX', ttl)
        return 1
    end
    return 0
end

local refill_amount = rps * elapsed_ms
tokens = math.min(tokens + refill_amount, capacity * 1000)

if tokens >= 1000 then
    tokens = tokens - 1000
    redis.call('SET', key_tokens, tokens, 'EX', ttl)
    redis.call('SET', key_last_refill, now, 'EX', ttl)
    return 1
else
    -- 即使拒绝也更新 last_refill，避免下次 elapsed 过大一次性补充过多
    redis.call('SET', key_last_refill, now, 'EX', ttl)
    return 0
end
`

// Allow 检查请求是否允许通过。
//
// key 格式：
//   - "tenant:{tenantID}:merchant:{merchantID}"：同时检查 merchant budget 和 tenant aggregate budget
//   - "merchant:{merchantID}"：只检查 merchant budget
//
// 任何一层不足都返回 false。Redis 故障根据 failClosed 决定拒绝或放行。
func (b *RedisBucket) Allow(ctx context.Context, key string, merchantTier string) (bool, error) {
	rps := b.getRPS(merchantTier)
	if rps <= 0 {
		// 非法 RPS：fail-closed 拒绝
		if b.failClosed {
			return false, ErrFailClosed
		}
		return false, nil
	}

	capacity := rps
	now := time.Now().UnixMilli()
	script := redis.NewScript(tokenBucketLua)

	// 1. Merchant budget 检查
	// 使用 hash tag {key} 确保多 key 同 slot
	merchantAllowed, err := b.runBucket(ctx, script, key, capacity, rps, now)
	if err != nil {
		return false, err
	}
	if !merchantAllowed {
		return false, nil
	}

	// 2. Tenant aggregate budget 检查（如果 key 包含 tenant 前缀）
	tenantID := extractTenantID(key)
	if tenantID != "" {
		tenantKey := "tenant:" + tenantID
		tenantCapacity := b.tenantRPS
		tenantAllowed, err := b.runBucket(ctx, script, tenantKey, tenantCapacity, b.tenantRPS, now)
		if err != nil {
			return false, err
		}
		if !tenantAllowed {
			return false, nil
		}
	}

	return true, nil
}

// runBucket 执行单个 bucket 的 token 检查。
// 使用 hash tag {bucket} 确保两个 key 在 Redis Cluster 同 slot。
func (b *RedisBucket) runBucket(ctx context.Context, script *redis.Script, bucket string, capacity, rps int, now int64) (bool, error) {
	// hash tag：{bucket}:tokens 和 {bucket}:last_refill 共享同一 hash slot
	keyTokens := fmt.Sprintf("{%s}:tokens", bucket)
	keyLastRefill := fmt.Sprintf("{%s}:last_refill", bucket)

	result, err := script.Run(ctx, b.rdb, []string{keyTokens, keyLastRefill},
		capacity, rps, now, b.ttlSeconds).Result()
	if err != nil {
		if b.failClosed {
			return false, ErrFailClosed
		}
		// 非 fail-closed 模式：Redis 故障时放行（仅限非生产）
		return true, nil
	}

	allowed, ok := result.(int64)
	return ok && allowed == 1, nil
}

// extractTenantID 从 key 中提取 tenant ID。
// key 格式 "tenant:{tenantID}:merchant:{merchantID}" → 返回 tenantID
// key 格式 "merchant:{merchantID}" → 返回 ""
func extractTenantID(key string) string {
	if !strings.HasPrefix(key, "tenant:") {
		return ""
	}
	rest := key[len("tenant:"):]
	idx := strings.Index(rest, ":")
	if idx <= 0 {
		return ""
	}
	return rest[:idx]
}

// ErrFailClosed Redis 故障时 fail-closed 错误。
// 中间件将其映射为 HTTP 503 RATE_LIMITER_FAIL_CLOSED。
var ErrFailClosed = &RateLimitError{Code: "RATE_LIMITER_FAIL_CLOSED", Message: "rate limiter fail closed"}

// RateLimitError 限流错误。
type RateLimitError struct {
	Code    string
	Message string
}

func (e *RateLimitError) Error() string {
	return e.Code + ": " + e.Message
}
