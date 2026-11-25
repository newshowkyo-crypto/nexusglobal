package carding

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
)

// setupTestRedis 启动 testcontainers Redis 并返回客户端
func setupTestRedis(t *testing.T) (*redis.Client, func()) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}

	ctx := context.Background()
	// OPS-007: 外部共享 Redis 优先（TEST_REDIS_ADDR），testcontainers 兜底。
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		require.NoError(t, rdb.Ping(ctx).Err(), "ping external TEST_REDIS_ADDR")
		return rdb, func() { _ = rdb.Close() }
	}

	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("skipping: cannot start testcontainers redis: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get container host: %v", err)
	}

	mappedPort, err := container.MappedPort(ctx, "6379")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get mapped port: %v", err)
	}

	addr := host + ":" + mappedPort.Port()
	client := redis.NewClient(&redis.Options{Addr: addr})

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot ping redis: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		_ = container.Terminate(ctx)
	}

	return client, cleanup
}

func TestHashFunctions(t *testing.T) {
	t.Run("HashIP", func(t *testing.T) {
		ip := "192.168.1.1"
		hash := domain.HashIP(ip)
		assert.NotEmpty(t, hash)
		assert.Len(t, hash, 64) // SHA-256 hex = 64 chars

		// 相同 IP 应产生相同 hash
		hash2 := domain.HashIP(ip)
		assert.Equal(t, hash, hash2)

		// 不同 IP 应产生不同 hash
		hash3 := domain.HashIP("10.0.0.1")
		assert.NotEqual(t, hash, hash3)

		// 空 IP 应返回空
		assert.Empty(t, domain.HashIP(""))
	})

	t.Run("HashBIN", func(t *testing.T) {
		bin := "411111"
		hash := domain.HashBIN(bin)
		assert.NotEmpty(t, hash)
		assert.Len(t, hash, 64)

		// 相同 BIN 应产生相同 hash
		hash2 := domain.HashBIN(bin)
		assert.Equal(t, hash, hash2)

		// 不同 BIN 应产生不同 hash
		hash3 := domain.HashBIN("424242")
		assert.NotEqual(t, hash, hash3)

		// 超过 8 位应截断
		longBin := "4111111234567890"
		hashLong := domain.HashBIN(longBin)
		hash8 := domain.HashBIN("41111112")
		assert.Equal(t, hashLong, hash8)

		// 空 BIN 应返回空
		assert.Empty(t, domain.HashBIN(""))
	})

	t.Run("HashDevice", func(t *testing.T) {
		device := "device-fingerprint-123"
		hash := domain.HashDevice(device)
		assert.NotEmpty(t, hash)
		assert.Len(t, hash, 64)

		// 相同设备应产生相同 hash
		hash2 := domain.HashDevice(device)
		assert.Equal(t, hash, hash2)

		// 空 device 应返回空
		assert.Empty(t, domain.HashDevice(""))
	})
}

func TestWindowConversion(t *testing.T) {
	t.Run("WindowToDuration", func(t *testing.T) {
		assert.Equal(t, 60*1000*1000*1000, int(domain.WindowToDuration(domain.Window60s)))
		assert.Equal(t, 5*60*1000*1000*1000, int(domain.WindowToDuration(domain.Window5m)))
		assert.Equal(t, 60*60*1000*1000*1000, int(domain.WindowToDuration(domain.Window1h)))
	})

	t.Run("WindowToSeconds", func(t *testing.T) {
		assert.Equal(t, int64(60), domain.WindowToSeconds(domain.Window60s))
		assert.Equal(t, int64(300), domain.WindowToSeconds(domain.Window5m))
		assert.Equal(t, int64(3600), domain.WindowToSeconds(domain.Window1h))
	})
}

func TestGateway_FailOpen(t *testing.T) {
	// 无 Redis 时，fail-open 应允许请求
	gw := NewGateway(GatewayConfig{
		RDB:      nil, // 无 Redis
		FailOpen: true,
	})

	req := domain.CheckRequest{
		TenantID:   uuid.New(),
		MerchantID: uuid.New(),
		AppID:      "test-app",
		BuyerIP:    "192.168.1.1",
	}

	result := gw.Check(context.Background(), req)
	assert.Equal(t, domain.DecisionAllow, result.Decision)
	assert.Equal(t, "redis_unavailable_fail_open", result.Reason)
}

func TestGateway_FailClosed(t *testing.T) {
	// 无 Redis 时，fail-closed 应拦截请求
	gw := NewGateway(GatewayConfig{
		RDB:      nil,   // 无 Redis
		FailOpen: false, // production 默认
	})

	req := domain.CheckRequest{
		TenantID:   uuid.New(),
		MerchantID: uuid.New(),
		AppID:      "test-app",
		BuyerIP:    "192.168.1.1",
	}

	result := gw.Check(context.Background(), req)
	assert.Equal(t, domain.DecisionBlock, result.Decision)
	assert.Equal(t, "redis_unavailable_fail_closed", result.Reason)
}

func TestGateway_WithRedis(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	// 使用真实 Redis 客户端测试基本连接
	ctx := context.Background()
	err := rdb.Ping(ctx).Err()
	assert.NoError(t, err)
	t.Log("Redis integration test with testcontainers: PASS")
}

func TestDefaultRuleConfig(t *testing.T) {
	cfg := domain.DefaultRuleConfig
	assert.Equal(t, 5, cfg.IPFailureThreshold)
	assert.Equal(t, domain.Window60s, cfg.IPWindow)
	assert.Equal(t, 3, cfg.BINFailureThreshold)
	assert.Equal(t, domain.Window5m, cfg.BINWindow)
	assert.Equal(t, 30.0, cfg.AppFailureRateThreshold)
	assert.Equal(t, 10, cfg.AppMinSampleSize)
	assert.Equal(t, domain.Window1h, cfg.AppWindow)
}

func TestGateway_IPThreshold(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	// 清理测试数据
	_ = rdb.FlushAll(ctx)

	gw := NewGateway(GatewayConfig{
		RDB:        rdb,
		FailOpen:   false,
		RuleConfig: domain.DefaultRuleConfig,
	})

	merchantID := uuid.New()
	req := domain.CheckRequest{
		TenantID:   uuid.New(),
		MerchantID: merchantID,
		AppID:      "test-app",
		BuyerIP:    "192.168.1.100",
	}

	// 前 5 次应允许
	for i := 0; i < 5; i++ {
		result := gw.Check(ctx, req)
		assert.Equal(t, domain.DecisionAllow, result.Decision, "iteration %d", i)
	}

	// 第 6 次应被拦截（阈值 5）
	result := gw.Check(ctx, req)
	assert.Equal(t, domain.DecisionBlock, result.Decision)
	assert.Equal(t, "ip_threshold_exceeded", result.Reason)
	assert.Equal(t, domain.DimensionBuyerIP, result.BlockedBy)
}

func TestGateway_BINThreshold(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	_ = rdb.FlushAll(ctx)

	gw := NewGateway(GatewayConfig{
		RDB:        rdb,
		FailOpen:   false,
		RuleConfig: domain.DefaultRuleConfig,
	})

	merchantID := uuid.New()
	req := domain.CheckRequest{
		TenantID:   uuid.New(),
		MerchantID: merchantID,
		AppID:      "test-app",
		BuyerIP:    "10.0.0.1", // 不同 IP，避免 IP 阈值
		CardBIN:    "411111",   // 测试 BIN
	}

	// 前 3 次应允许
	for i := 0; i < 3; i++ {
		result := gw.Check(ctx, req)
		assert.Equal(t, domain.DecisionAllow, result.Decision, "iteration %d", i)
	}

	// 第 4 次应被拦截（阈值 3）
	result := gw.Check(ctx, req)
	assert.Equal(t, domain.DecisionBlock, result.Decision)
	assert.Equal(t, "bin_threshold_exceeded", result.Reason)
	assert.Equal(t, domain.DimensionCardBIN, result.BlockedBy)
}

func TestGateway_Cooldown(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	_ = rdb.FlushAll(ctx)

	gw := NewGateway(GatewayConfig{
		RDB:        rdb,
		FailOpen:   false,
		RuleConfig: domain.DefaultRuleConfig,
	})

	merchantID := uuid.New()

	// 设置冷却期
	gw.SetCooldown(ctx, merchantID.String())

	req := domain.CheckRequest{
		TenantID:   uuid.New(),
		MerchantID: merchantID,
		AppID:      "test-app",
		BuyerIP:    "10.0.0.1",
	}

	result := gw.Check(ctx, req)
	assert.Equal(t, domain.DecisionCooldown, result.Decision)
	assert.Equal(t, "merchant_in_cooldown", result.Reason)

	// 清除冷却期
	gw.ClearCooldown(ctx, merchantID.String())

	result = gw.Check(ctx, req)
	assert.Equal(t, domain.DecisionAllow, result.Decision)
}

func TestGateway_LuaScriptConcurrency(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	_ = rdb.FlushAll(ctx)

	gw := NewGateway(GatewayConfig{
		RDB:        rdb,
		FailOpen:   false,
		RuleConfig: domain.DefaultRuleConfig,
	})

	merchantID := uuid.New()
	req := domain.CheckRequest{
		TenantID:   uuid.New(),
		MerchantID: merchantID,
		AppID:      "test-app",
		BuyerIP:    "192.168.1.200",
	}

	// 并发 100 goroutine 同时 Check
	done := make(chan domain.CheckResult, 100)
	for i := 0; i < 100; i++ {
		go func() {
			result := gw.Check(ctx, req)
			done <- result
		}()
	}

	// 收集结果
	allowCount := 0
	blockCount := 0
	for i := 0; i < 100; i++ {
		result := <-done
		if result.Decision == domain.DecisionAllow {
			allowCount++
		} else if result.Decision == domain.DecisionBlock {
			blockCount++
		}
	}

	// 应有部分被允许，部分被拦截（阈值 5）
	// Lua 脚本保证计数正确，不会超卖
	t.Logf("allowCount=%d, blockCount=%d", allowCount, blockCount)
	assert.LessOrEqual(t, allowCount, 5, "at most 5 should be allowed")
	assert.GreaterOrEqual(t, blockCount, 95, "at least 95 should be blocked")
}

func TestService_IsBlocked(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)

	assert.True(t, svc.IsBlocked(domain.CheckResult{Decision: domain.DecisionBlock}))
	assert.True(t, svc.IsBlocked(domain.CheckResult{Decision: domain.DecisionCooldown}))
	assert.False(t, svc.IsBlocked(domain.CheckResult{Decision: domain.DecisionAllow}))
	assert.False(t, svc.IsBlocked(domain.CheckResult{Decision: domain.DecisionChallenge}))
}

func TestService_ShouldBlockAcquirer(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)

	// 命中 Block 时应阻止调用 acquirer
	assert.True(t, svc.ShouldBlockAcquirer(domain.CheckResult{Decision: domain.DecisionBlock}))
	assert.True(t, svc.ShouldBlockAcquirer(domain.CheckResult{Decision: domain.DecisionCooldown}))

	// 正常低频请求不应阻止
	assert.False(t, svc.ShouldBlockAcquirer(domain.CheckResult{Decision: domain.DecisionAllow}))
}
