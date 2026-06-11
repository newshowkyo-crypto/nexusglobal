package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Load_Defaults(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://test:test@localhost:5432/test?sslmode=disable")
	t.Setenv("MASTER_KEY_BASE64", "dGVzdGtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "local", cfg.AppEnv)
	assert.Equal(t, ":8080", cfg.HTTPAddr)
	assert.Equal(t, "localhost:6379", cfg.RedisAddr)
	assert.Equal(t, "mock", cfg.ExternalMode)
	assert.Equal(t, "mock", cfg.KMSMode)
	assert.Equal(t, 4, cfg.OutboxWorkerConcurrency)
	assert.Equal(t, 16, cfg.OutboxClaimBatch)
	assert.Equal(t, 5, cfg.StreamMaxDeliveries)
	assert.Equal(t, 10*time.Second, cfg.SweepTickInterval)
	assert.Equal(t, "info", cfg.LogLevel)
}

func TestConfig_Load_MissingRequired(t *testing.T) {
	// Clear required vars
	os.Unsetenv("POSTGRES_DSN")
	os.Unsetenv("MASTER_KEY_BASE64")

	_, err := Load()
	assert.Error(t, err)
}

func TestConfig_Load_Override(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://custom:custom@db:5432/custom?sslmode=disable")
	t.Setenv("MASTER_KEY_BASE64", "Y3VzdG9ta2V5MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMg==")
	t.Setenv("APP_ENV", "production")
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("OUTBOX_WORKER_CONCURRENCY", "8")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "production", cfg.AppEnv)
	assert.Equal(t, ":9090", cfg.HTTPAddr)
	assert.Equal(t, "postgres://custom:custom@db:5432/custom?sslmode=disable", cfg.PostgresDSN)
	assert.Equal(t, "redis:6379", cfg.RedisAddr)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 8, cfg.OutboxWorkerConcurrency)
}
