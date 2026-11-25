package token

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupTestRedis(t *testing.T) (*redis.Client, func()) {
	t.Helper()
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
		Image:        "redis:7",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "6379")

	rdb := redis.NewClient(&redis.Options{
		Addr: host + ":" + port.Port(),
	})

	for i := 0; i < 30; i++ {
		if err := rdb.Ping(ctx).Err(); err == nil {
			break
		}
		time.Sleep(1 * time.Second)
		if i == 29 {
			require.NoError(t, rdb.Ping(ctx).Err(), "failed to connect to redis after 30 seconds")
		}
	}

	cleanup := func() {
		rdb.Close()
		_ = container.Terminate(ctx)
	}

	return rdb, cleanup
}

func TestRedisStore_Create(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRedisStore(rdb)
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID:       "order-123",
		MerchantID:    "merchant-1",
		AllowedOrigin: "https://checkout.example.com",
		MaxUses:       1,
		TTL:           15 * time.Minute,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, token.ID)
	assert.NotEmpty(t, token.Value)
	assert.Equal(t, "order-123", token.OrderID)
	assert.Equal(t, 1, token.MaxUses)
	assert.Equal(t, 0, token.UsedCount)
	assert.True(t, token.ExpiresAt.After(time.Now()))

	stored, err := store.Get(ctx, token.Value)
	require.NoError(t, err)
	assert.Equal(t, token.ID, stored.ID)
	assert.Equal(t, token.OrderID, stored.OrderID)
}

func TestRedisStore_Redeem_SingleUse(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRedisStore(rdb)
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID:    "order-123",
		MerchantID: "merchant-1",
		MaxUses:    1,
		TTL:        15 * time.Minute,
	})
	require.NoError(t, err)

	redeemed, err := svc.Redeem(ctx, RedeemTokenCmd{
		Value:   token.Value,
		Origin:  "https://checkout.example.com",
		OrderID: "order-123",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, redeemed.UsedCount)

	_, err = svc.Redeem(ctx, RedeemTokenCmd{
		Value:   token.Value,
		Origin:  "https://checkout.example.com",
		OrderID: "order-123",
	})
	assert.ErrorIs(t, err, ErrTokenUsed)
}

func TestRedisStore_Redeem_Expired(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRedisStore(rdb)
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID: "order-123",
		MaxUses: 1,
		TTL:     1 * time.Second,
	})
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	_, err = svc.Redeem(ctx, RedeemTokenCmd{Value: token.Value})
	assert.ErrorIs(t, err, ErrTokenNotFound)
}

func TestRedisStore_Redeem_NotFound(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRedisStore(rdb)
	svc := NewService(store)
	ctx := context.Background()

	_, err := svc.Redeem(ctx, RedeemTokenCmd{Value: "nonexistent-token-value"})
	assert.ErrorIs(t, err, ErrTokenNotFound)
}
