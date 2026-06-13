package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	AppEnv                  string        `env:"APP_ENV" envDefault:"local"`
	HTTPAddr                string        `env:"HTTP_ADDR" envDefault:":8080"`
	PostgresDSN             string        `env:"POSTGRES_DSN,required"`
	RedisAddr               string        `env:"REDIS_ADDR" envDefault:"localhost:6379"`
	ExternalMode            string        `env:"EXTERNAL_MODE" envDefault:"mock"`
	KMSMode                 string        `env:"KMS_MODE" envDefault:"mock"`
	MasterKeyBase64         string        `env:"MASTER_KEY_BASE64,required"`
	JWTSecret               string        `env:"JWT_SECRET" envDefault:""`
	InternalToken           string        `env:"INTERNAL_TOKEN" envDefault:""`
	LPBaseURL               string        `env:"LP_BASE_URL" envDefault:""`
	LPAPIKey                string        `env:"LP_API_KEY" envDefault:""`
	BankBaseURL             string        `env:"BANK_BASE_URL" envDefault:""`
	BankAPIKey              string        `env:"BANK_API_KEY" envDefault:""`
	MAEBaseURL              string        `env:"MAE_BASE_URL" envDefault:""`
	MAEAPIKey               string        `env:"MAE_API_KEY" envDefault:""`
	VaultBaseURL            string        `env:"VAULT_BASE_URL" envDefault:""`
	VaultToken              string        `env:"VAULT_TOKEN" envDefault:""`
	VaultMountPath          string        `env:"VAULT_MOUNT_PATH" envDefault:"transit"`
	VaultKeyName            string        `env:"VAULT_KEY_NAME" envDefault:"mamr-master"`
	OutboxWorkerConcurrency int           `env:"OUTBOX_WORKER_CONCURRENCY" envDefault:"4"`
	OutboxClaimBatch        int           `env:"OUTBOX_CLAIM_BATCH" envDefault:"16"`
	StreamMaxDeliveries     int           `env:"STREAM_MAX_DELIVERIES" envDefault:"5"`
	SweepTickInterval       time.Duration `env:"SWEEP_TICK_INTERVAL" envDefault:"10s"`
	LogLevel                string        `env:"LOG_LEVEL" envDefault:"info"`
	OTelCollectorURL        string        `env:"OTEL_COLLECTOR_URL" envDefault:""`
	OTelSampleRatio         float64       `env:"OTEL_SAMPLE_RATIO" envDefault:"1.0"`
	TokenStore              string        `env:"TOKEN_STORE" envDefault:"memory"`
}

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if cfg.AppEnv == "production" {
		if cfg.KMSMode == "mock" {
			return nil, fmt.Errorf("config: KMS_MODE=mock is not allowed in production")
		}
		if cfg.ExternalMode == "mock" {
			return nil, fmt.Errorf("config: EXTERNAL_MODE=mock is not allowed in production")
		}
		if cfg.MasterKeyBase64 == "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" {
			return nil, fmt.Errorf("config: MASTER_KEY_BASE64 must not be the example key in production")
		}
		if cfg.JWTSecret == "" {
			return nil, fmt.Errorf("config: JWT_SECRET is required in production")
		}
		if len(cfg.JWTSecret) < 32 {
			return nil, fmt.Errorf("config: JWT_SECRET must be at least 32 bytes in production")
		}
		if cfg.InternalToken == "" {
			return nil, fmt.Errorf("config: INTERNAL_TOKEN is required in production")
		}
		if len(cfg.InternalToken) < 32 {
			return nil, fmt.Errorf("config: INTERNAL_TOKEN must be at least 32 bytes in production")
		}
	}
	if cfg.ExternalMode == "real" {
		if cfg.LPBaseURL == "" {
			return nil, fmt.Errorf("config: EXTERNAL_MODE=real requires LP_BASE_URL")
		}
		if cfg.BankBaseURL == "" {
			return nil, fmt.Errorf("config: EXTERNAL_MODE=real requires BANK_BASE_URL")
		}
		if cfg.MAEBaseURL == "" {
			return nil, fmt.Errorf("config: EXTERNAL_MODE=real requires MAE_BASE_URL")
		}
	}
	if cfg.KMSMode == "real" {
		if cfg.VaultBaseURL == "" {
			return nil, fmt.Errorf("config: KMS_MODE=real requires VAULT_BASE_URL")
		}
	}
	return cfg, nil
}
