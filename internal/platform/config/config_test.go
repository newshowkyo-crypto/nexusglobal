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
	t.Setenv("APP_ENV", "staging")
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("OUTBOX_WORKER_CONCURRENCY", "8")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "staging", cfg.AppEnv)
	assert.Equal(t, ":9090", cfg.HTTPAddr)
	assert.Equal(t, "postgres://custom:custom@db:5432/custom?sslmode=disable", cfg.PostgresDSN)
	assert.Equal(t, "redis:6379", cfg.RedisAddr)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 8, cfg.OutboxWorkerConcurrency)
}

func TestConfig_Load_ProductionRejectsMock(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://test:test@localhost:5432/test?sslmode=disable")
	t.Setenv("MASTER_KEY_BASE64", "dGVzdGtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=")
	t.Setenv("APP_ENV", "production")

	_, err := Load()
	assert.Error(t, err, "production with mock KMS should fail")
	assert.Contains(t, err.Error(), "KMS_MODE=mock")
}

func TestConfig_Load_ProductionRejectsExampleKey(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://test:test@localhost:5432/test?sslmode=disable")
	t.Setenv("MASTER_KEY_BASE64", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("APP_ENV", "production")
	t.Setenv("KMS_MODE", "real")
	t.Setenv("EXTERNAL_MODE", "real")
	t.Setenv("JWT_SECRET", "secret")
	t.Setenv("INTERNAL_TOKEN", "token")

	_, err := Load()
	assert.Error(t, err, "production with example key should fail")
	assert.Contains(t, err.Error(), "example key")
}

func TestConfig_Load_LocalWithMockOK(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://test:test@localhost:5432/test?sslmode=disable")
	t.Setenv("MASTER_KEY_BASE64", "dGVzdGtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=")
	t.Setenv("APP_ENV", "local")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "local", cfg.AppEnv)
	assert.Equal(t, "mock", cfg.KMSMode)
}

func TestConfig_Load_RealModeRequiresURLs(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://test:test@localhost:5432/test?sslmode=disable")
	t.Setenv("MASTER_KEY_BASE64", "dGVzdGtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=")
	t.Setenv("EXTERNAL_MODE", "real")

	_, err := Load()
	assert.Error(t, err, "real mode without URLs should fail")
	assert.Contains(t, err.Error(), "LP_BASE_URL")
}

func TestConfig_Load_RealKMSRequiresVaultURL(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://test:test@localhost:5432/test?sslmode=disable")
	t.Setenv("MASTER_KEY_BASE64", "dGVzdGtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=")
	t.Setenv("KMS_MODE", "real")

	_, err := Load()
	assert.Error(t, err, "real KMS without vault URL should fail")
	assert.Contains(t, err.Error(), "VAULT_BASE_URL")
}
