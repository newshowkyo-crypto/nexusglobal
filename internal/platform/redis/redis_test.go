package redis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupTestRedis(t *testing.T) (*Client, func()) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}

	ctx := context.Background()
	// OPS-007: 外部共享 Redis 优先（TEST_REDIS_ADDR），testcontainers 兜底。
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		client := NewClient(addr)
		if err := client.Ping(ctx); err != nil {
			t.Fatalf("ping external TEST_REDIS_ADDR: %v", err)
		}
		return client, func() { _ = client.Close() }
	}

	req := testcontainers.ContainerRequest{
		Image:        "redis:7",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("skipping: cannot start container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get host: %v", err)
	}

	mappedPort, err := container.MappedPort(ctx, "6379")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get port: %v", err)
	}

	addr := host + ":" + mappedPort.Port()
	client := NewClient(addr)

	err = client.Ping(ctx)
	if err != nil {
		client.Close()
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot ping redis: %v", err)
	}

	cleanup := func() {
		client.Close()
		_ = container.Terminate(ctx)
	}

	return client, cleanup
}

func TestRedis_Incr(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	key := "test:incr:" + time.Now().Format(time.RFC3339Nano)

	val, err := client.IncrBy(ctx, key, 1)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), val)

	val, err = client.IncrBy(ctx, key, 5)
	assert.NoError(t, err)
	assert.Equal(t, int64(6), val)

	got, err := client.Get(ctx, key)
	assert.NoError(t, err)
	assert.Equal(t, "6", got)
}

func TestRedis_ZSet(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	key := "test:zset:" + time.Now().Format(time.RFC3339Nano)

	err := client.ZAdd(ctx, key, float64(time.Now().Add(-time.Hour).Unix()), "member1")
	assert.NoError(t, err)

	err = client.ZAdd(ctx, key, float64(time.Now().Add(time.Hour).Unix()), "member2")
	assert.NoError(t, err)

	members, err := client.ZRangeByScore(ctx, key, "-inf", "+inf")
	assert.NoError(t, err)
	assert.Len(t, members, 2)
	assert.Contains(t, members, "member1")
	assert.Contains(t, members, "member2")

	now := float64(time.Now().Unix())
	members, err = client.ZRangeByScore(ctx, key, "-inf", fmt.Sprintf("%f", now))
	assert.NoError(t, err)
	assert.Len(t, members, 1)
	assert.Equal(t, "member1", members[0])
}

func TestRedis_Lock(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	key := "test:lock:" + time.Now().Format(time.RFC3339Nano)

	ok, err := client.SetNX(ctx, key, "owner1", 10*time.Second)
	assert.NoError(t, err)
	assert.True(t, ok)

	ok, err = client.SetNX(ctx, key, "owner2", 10*time.Second)
	assert.NoError(t, err)
	assert.False(t, ok)

	err = client.Del(ctx, key)
	assert.NoError(t, err)

	ok, err = client.SetNX(ctx, key, "owner3", 10*time.Second)
	assert.NoError(t, err)
	assert.True(t, ok)
}

func TestRedis_LuaScript(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()

	script := NewLuaScript(`return redis.call('SET', KEYS[1], ARGV[1])`, client)
	err := script.Load(ctx)
	assert.NoError(t, err)

	key := "test:lua:" + time.Now().Format(time.RFC3339Nano)
	result, err := script.Run(ctx, []string{key}, "hello")
	assert.NoError(t, err)
	assert.Equal(t, "OK", result)

	got, err := client.Get(ctx, key)
	assert.NoError(t, err)
	assert.Equal(t, "hello", got)
}
