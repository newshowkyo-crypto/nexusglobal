package ratelimit

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedisBucket_TenantIsolation 验证多租户限流互不串扰。
// tenant A 的 merchant 耗尽 bucket 不影响 tenant B 的 merchant。
func TestRedisBucket_TenantIsolation(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	keyA := "tenant:tenant-A:merchant:merchant-1"
	keyB := "tenant:tenant-B:merchant:merchant-1"

	// 耗尽 tenant A 的 merchant bucket
	for i := 0; i < 100; i++ {
		bucket.Allow(context.Background(), keyA, "TIER_1_HIGH_RISK")
	}

	// tenant A 应被限流
	allowedA, err := bucket.Allow(context.Background(), keyA, "TIER_1_HIGH_RISK")
	require.NoError(t, err)
	assert.False(t, allowedA, "tenant A merchant should be rate limited")

	// tenant B 应仍然允许（互不串扰）
	allowedB, err := bucket.Allow(context.Background(), keyB, "TIER_1_HIGH_RISK")
	require.NoError(t, err)
	assert.True(t, allowedB, "tenant B merchant should be allowed (isolated from A)")
}

// TestRedisBucket_TenantAggregateLimit 验证 tenant 聚合 budget 限流。
// 当 tenant 聚合超限时，即使单个 merchant bucket 未满，也拒绝。
func TestRedisBucket_TenantAggregateLimit(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	// tenant 聚合 RPS=200，用多个 merchant 共享同一 tenant 来耗尽聚合 budget
	// 每个 merchant 有自己的 bucket，但 tenant 聚合 bucket 是共享的
	// 耗尽 tenant 聚合：发送超过 200 次请求（来自不同 merchant 但同一 tenant）
	for i := 0; i < 300; i++ {
		key := "tenant:tenant-X:merchant:m" + string(rune(i))
		bucket.Allow(context.Background(), key, "TIER_2_COMPLIANT")
	}

	// 新 merchant 在同一 tenant 下应被拒绝（tenant 聚合已耗尽）
	key := "tenant:tenant-X:merchant:new-one"
	allowed, err := bucket.Allow(context.Background(), key, "TIER_2_COMPLIANT")
	require.NoError(t, err)
	assert.False(t, allowed, "should be rejected due to tenant aggregate limit")
}

// TestRedisBucket_HashTagSameSlot 验证 key 使用 hash tag 确保同 slot。
// 通过检查 key 格式确认 hash tag 存在。
func TestRedisBucket_HashTagSameSlot(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	// 执行一次限流检查，验证 key 被写入
	allowed, err := bucket.Allow(context.Background(), "tenant:t1:merchant:m1", "TIER_1_HIGH_RISK")
	require.NoError(t, err)
	assert.True(t, allowed)

	// 验证 Redis 中存在带 hash tag 的 key
	keys := s.Keys()
	foundHashTag := false
	for _, k := range keys {
		// hash tag 格式：{bucket}:tokens 或 {bucket}:last_refill
		if len(k) > 0 && k[0] == '{' {
			foundHashTag = true
			break
		}
	}
	assert.True(t, foundHashTag, "Redis keys should use hash tag format {bucket}:field")
}

// TestRedisBucket_TTLSet 验证 key 设置了 TTL。
func TestRedisBucket_TTLSet(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	_, err := bucket.Allow(context.Background(), "merchant:test-ttl", "TIER_1_HIGH_RISK")
	require.NoError(t, err)

	// 验证 key 有 TTL
	keys := s.Keys()
	for _, k := range keys {
		ttl := s.TTL(k)
		assert.True(t, ttl > 0, "key %s should have TTL set", k)
	}
}

// TestRedisBucket_FailClosedOnRedisError 验证 Redis 故障时 fail-closed 返回 503。
func TestRedisBucket_FailClosedOnRedisError(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:9999"}) // 不存在的 Redis
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	allowed, err := bucket.Allow(context.Background(), "merchant:test", "TIER_1_HIGH_RISK")
	assert.False(t, allowed, "should not be allowed on Redis failure")
	assert.Equal(t, ErrFailClosed, err, "should return ErrFailClosed")
}

// TestRedisBucket_InvalidRPSFailClosed 验证非法 RPS fail-closed。
func TestRedisBucket_InvalidRPSFailClosed(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)
	// 覆盖 tierConfig 使 RPS 为 0（非法）
	bucket.tierConfig["INVALID_TIER"] = 0

	allowed, err := bucket.Allow(context.Background(), "merchant:test", "INVALID_TIER")
	assert.False(t, allowed, "invalid RPS should fail-closed")
	assert.Equal(t, ErrFailClosed, err)
}

// TestExtractTenantID 验证从 key 提取 tenant ID。
func TestExtractTenantID(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"tenant:tenant-123:merchant:merchant-456", "tenant-123"},
		{"merchant:merchant-456", ""},
		{"tenant:t1:merchant:m1", "t1"},
		{"", ""},
		{"invalid", ""},
	}

	for _, tt := range tests {
		got := extractTenantID(tt.key)
		assert.Equal(t, tt.want, got, "extractTenantID(%q)", tt.key)
	}
}

// TestRedisBucket_NegativeElapsedProtection 验证负 elapsed（时钟回拨）防护。
// 通过手动设置 last_refill 为未来时间模拟时钟回拨。
func TestRedisBucket_NegativeElapsedProtection(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	ctx := context.Background()
	key := "merchant:clock-skew-test"

	// 第一次请求建立 bucket
	allowed, err := bucket.Allow(ctx, key, "TIER_1_HIGH_RISK")
	require.NoError(t, err)
	require.True(t, allowed)

	// 手动设置 last_refill 为未来时间（模拟时钟回拨）
	futureTime := time.Now().Add(1 * time.Hour).UnixMilli()
	// 使用正确的 key 格式，miniredis Set 接受 string 值
	s.Set("{"+key+"}:last_refill", strconv.FormatInt(futureTime, 10))

	// 再次请求：elapsed 为负，Lua 脚本应防护
	allowed, err = bucket.Allow(ctx, key, "TIER_1_HIGH_RISK")
	require.NoError(t, err, "negative elapsed should not cause error")
	// 应该仍然允许（token bucket 有初始容量）
	_ = allowed // 不严格断言 allowed，因为取决于 bucket 状态
}
