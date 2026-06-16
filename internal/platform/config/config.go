package config

import (
	"fmt"
	"os"
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
	AcquirerMode            string        `env:"ACQUIRER_MODE" envDefault:"mock"`
	AcquirerProvider        string        `env:"ACQUIRER_PROVIDER" envDefault:"mock"`
	IssuingProvider         string        `env:"ISSUING_PROVIDER" envDefault:"mock"`
	VCCFundingProvider      string        `env:"VCC_FUNDING_PROVIDER" envDefault:"mock"`
	CheckoutOrderProvider   string        `env:"CHECKOUT_ORDER_PROVIDER" envDefault:"mock"`
	MasterKeyBase64         string        `env:"MASTER_KEY_BASE64,required"`
	JWTSecret               string        `env:"JWT_SECRET" envDefault:""`
	InternalToken           string        `env:"INTERNAL_TOKEN" envDefault:""`
	AdminSessionSecret      string        `env:"ADMIN_SESSION_SECRET" envDefault:""`
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
	DBMigrationMode         string        `env:"DB_MIGRATION_MODE" envDefault:"auto"`
	RequireVault            bool          `env:"REQUIRE_VAULT" envDefault:"true"`
	RequireWebhookSecret    bool          `env:"REQUIRE_WEBHOOK_SECRET" envDefault:"true"`
	RequireAdminAuth        bool          `env:"REQUIRE_ADMIN_AUTH" envDefault:"true"`
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
		if err := validateProductionConfig(cfg); err != nil {
			return nil, err
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
		if cfg.VaultToken == "" {
			return nil, fmt.Errorf("config: KMS_MODE=real requires VAULT_TOKEN")
		}
	}

	if cfg.AcquirerMode == "real" && cfg.KMSMode != "real" {
		return nil, fmt.Errorf("config: ACQUIRER_MODE=real requires KMS_MODE=real for credential encryption")
	}

	if cfg.CheckoutOrderProvider != "mock" && cfg.CheckoutOrderProvider != "http" && cfg.CheckoutOrderProvider != "disabled" {
		return nil, fmt.Errorf("config: CHECKOUT_ORDER_PROVIDER must be one of: mock, http, disabled")
	}

	return cfg, nil
}

func validateProductionConfig(cfg *Config) error {
	if cfg.KMSMode == "mock" {
		return fmt.Errorf("config: KMS_MODE=mock is not allowed in production")
	}

	if cfg.ExternalMode == "mock" {
		return fmt.Errorf("config: EXTERNAL_MODE=mock is not allowed in production")
	}

	if cfg.AcquirerMode == "mock" {
		return fmt.Errorf("config: ACQUIRER_MODE=mock is not allowed in production")
	}

	if cfg.AcquirerProvider == "mock" {
		return fmt.Errorf("config: ACQUIRER_PROVIDER=mock is not allowed in production")
	}

	if cfg.IssuingProvider == "mock" {
		return fmt.Errorf("config: ISSUING_PROVIDER=mock is not allowed in production")
	}

	if cfg.VCCFundingProvider == "mock" {
		return fmt.Errorf("config: VCC_FUNDING_PROVIDER=mock is not allowed in production")
	}

	if cfg.CheckoutOrderProvider == "mock" {
		return fmt.Errorf("config: CHECKOUT_ORDER_PROVIDER=mock is not allowed in production")
	}

	if cfg.MasterKeyBase64 == "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" {
		return fmt.Errorf("config: MASTER_KEY_BASE64 must not be the example key in production")
	}

	if cfg.JWTSecret == "" {
		return fmt.Errorf("config: JWT_SECRET is required in production")
	}

	if len(cfg.JWTSecret) < 32 {
		return fmt.Errorf("config: JWT_SECRET must be at least 32 bytes in production")
	}

	if cfg.InternalToken == "" {
		return fmt.Errorf("config: INTERNAL_TOKEN is required in production")
	}

	if len(cfg.InternalToken) < 32 {
		return fmt.Errorf("config: INTERNAL_TOKEN must be at least 32 bytes in production")
	}

	if cfg.AdminSessionSecret == "" {
		return fmt.Errorf("config: ADMIN_SESSION_SECRET is required in production")
	}

	if len(cfg.AdminSessionSecret) < 32 {
		return fmt.Errorf("config: ADMIN_SESSION_SECRET must be at least 32 bytes in production")
	}

	if cfg.RequireVault && cfg.VaultBaseURL == "" {
		return fmt.Errorf("config: VAULT_BASE_URL is required when REQUIRE_VAULT=true in production")
	}

	if cfg.RequireVault && cfg.VaultToken == "" {
		return fmt.Errorf("config: VAULT_TOKEN is required when REQUIRE_VAULT=true in production")
	}

	if cfg.RequireAdminAuth && cfg.AdminSessionSecret == "" {
		return fmt.Errorf("config: ADMIN_SESSION_SECRET is required when REQUIRE_ADMIN_AUTH=true in production")
	}

	if cfg.DBMigrationMode != "auto" && cfg.DBMigrationMode != "manual" {
		return fmt.Errorf("config: DB_MIGRATION_MODE must be one of: auto, manual")
	}

	return nil
}

func IsProduction() bool {
	return os.Getenv("APP_ENV") == "production"
}

func IsMockMode() bool {
	return os.Getenv("EXTERNAL_MODE") == "mock" || os.Getenv("KMS_MODE") == "mock" ||
		os.Getenv("ACQUIRER_MODE") == "mock" || os.Getenv("ACQUIRER_PROVIDER") == "mock" ||
		os.Getenv("ISSUING_PROVIDER") == "mock" || os.Getenv("VCC_FUNDING_PROVIDER") == "mock" ||
		os.Getenv("CHECKOUT_ORDER_PROVIDER") == "mock"
}
