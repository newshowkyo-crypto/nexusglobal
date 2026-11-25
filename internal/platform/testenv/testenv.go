// Package testenv — OPS-007：共享测试基础设施。
//
// 所有依赖 PostgreSQL / Redis 的集成测试统一从这里获取连接：
//
//   - 设置 TEST_DATABASE_DSN / TEST_REDIS_ADDR 时：直连外部服务
//     （CI shard 或本机常驻容器复用，按需求用唯一 schema/db 前缀隔离）。
//   - 未设置时：回退 testcontainers（本地/无外部服务环境）。
//
// 背景：当前 Windows+VirtualBox 开发环境的 Docker 守护进程跑在 VM 内，
// VBox NAT 只能逐端口转发，testcontainers 的随机端口(32768+)无法从宿主
// 访问；外部 DSN 是该环境下运行集成测试的唯一路径，同时避免了
// "每个 test 起一个容器"的资源放大。
package testenv

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

const (
	// EnvTestDatabaseDSN 外部 PostgreSQL 连接串。
	EnvTestDatabaseDSN = "TEST_DATABASE_DSN"
	// EnvTestRedisAddr 外部 Redis 地址（host:port）。
	EnvTestRedisAddr = "TEST_REDIS_ADDR"
)

// externalDSN 返回外部 PG DSN（未设置则空串）。
func externalDSN() string { return os.Getenv(EnvTestDatabaseDSN) }

// externalRedis 返回外部 Redis 地址（未设置则空串）。
func externalRedis() string { return os.Getenv(EnvTestRedisAddr) }

// ResetDatabase 清空外部库的 public schema（每个测试套件独立从空库开始）。
func ResetDatabase(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		DROP SCHEMA public CASCADE;
		CREATE SCHEMA public;
		GRANT ALL ON SCHEMA public TO public;
	`)
	require.NoError(t, err, "reset external database schema")
}

// Postgres 返回连接池与清理函数。
// 外部 DSN 优先；否则启动 testcontainers postgres:15。
func Postgres(ctx context.Context, t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()

	if dsn := externalDSN(); dsn != "" {
		pool, err := postgres.NewPool(ctx, dsn)
		require.NoError(t, err, "connect to external TEST_DATABASE_DSN")
		ResetDatabase(ctx, t, pool)
		return pool, func() { pool.Close() }
	}

	req := testcontainers.ContainerRequest{
		Image:        "postgres:15",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "test",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").
			WithStartupTimeout(2 * time.Minute),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "start postgres testcontainer")

	host, err := container.Host(ctx)
	require.NoError(t, err)
	mapped, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	dsn := fmt.Sprintf("postgres://test:test@%s:%s/test?sslmode=disable", host, mapped.Port())
	pool, err := postgres.NewPool(ctx, dsn)
	require.NoError(t, err, "connect to postgres testcontainer")

	cleanup := func() {
		pool.Close()
		_ = container.Terminate(ctx)
	}
	return pool, cleanup
}

// Redis 返回外部或 testcontainers Redis 客户端与清理函数。
func Redis(ctx context.Context, t *testing.T) (*redis.Client, func()) {
	t.Helper()

	if addr := externalRedis(); addr != "" {
		client := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 5 * time.Second})
		require.NoError(t, client.Ping(ctx).Err(), "ping external TEST_REDIS_ADDR")
		return client, func() { _ = client.Close() }
	}

	req := testcontainers.ContainerRequest{
		Image:        "redis:7",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor: wait.ForListeningPort("6379/tcp").
			WithStartupTimeout(2 * time.Minute),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "start redis testcontainer")

	host, err := container.Host(ctx)
	require.NoError(t, err)
	mapped, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%s", host, mapped.Port()),
	})
	require.NoError(t, client.Ping(ctx).Err(), "ping redis testcontainer")

	cleanup := func() {
		_ = client.Close()
		_ = container.Terminate(ctx)
	}
	return client, cleanup
}
