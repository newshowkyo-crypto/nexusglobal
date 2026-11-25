package config

import (
	"fmt"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	AppEnv                string `env:"APP_ENV" envDefault:"local"`
	HTTPAddr              string `env:"HTTP_ADDR" envDefault:":8080"`
	PostgresDSN           string `env:"POSTGRES_DSN,required"`
	RedisAddr             string `env:"REDIS_ADDR" envDefault:"localhost:6379"`
	ExternalMode          string `env:"EXTERNAL_MODE" envDefault:"mock"`
	KMSMode               string `env:"KMS_MODE" envDefault:"mock"`
	AcquirerMode          string `env:"ACQUIRER_MODE" envDefault:"mock"`
	AcquirerProvider      string `env:"ACQUIRER_PROVIDER" envDefault:"mock"`
	IssuingProvider       string `env:"ISSUING_PROVIDER" envDefault:"mock"`
	VCCFundingProvider    string `env:"VCC_FUNDING_PROVIDER" envDefault:"mock"`
	CheckoutOrderProvider string `env:"CHECKOUT_ORDER_PROVIDER" envDefault:"mock"`
	CheckoutOrderAPIURL   string `env:"CHECKOUT_ORDER_API_URL" envDefault:""`
	// PoolMode 支付池模式：off / shadow / enforce。默认 shadow（只记录决策不切流量）。
	PoolMode                string        `env:"POOL_MODE" envDefault:"shadow"`
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
	AdminSessionTTL         time.Duration `env:"ADMIN_SESSION_TTL" envDefault:"15m"` // 管理会话 Cookie 时长：默认 15m，上限 12h；生产保持默认
	LogLevel                string        `env:"LOG_LEVEL" envDefault:"info"`
	OTelCollectorURL        string        `env:"OTEL_COLLECTOR_URL" envDefault:""`
	OTelSampleRatio         float64       `env:"OTEL_SAMPLE_RATIO" envDefault:"1.0"`
	TokenStore              string        `env:"TOKEN_STORE" envDefault:"memory"`
	CheckoutPublicBaseURL   string        `env:"CHECKOUT_PUBLIC_BASE_URL" envDefault:"http://localhost:8080"`
	// RateLimitFailClosed 控制限流器 Redis 故障行为。
	// true（生产默认）：Redis 故障时拒绝所有限流请求（503 RATE_LIMITER_FAIL_CLOSED）。
	// false（local 默认）：Redis 故障时放行。
	RateLimitFailClosed bool `env:"RATE_LIMIT_FAIL_CLOSED" envDefault:"true"`
	// WebhookSigningKey Unified Webhook Receiver 的 HMAC-SHA256 签名密钥（P0-5）。
	// B7: 只用于 HMAC 验签，不用于加密。
	// 生产环境必须配置（>=32 字节）。为空时 webhook receiver fail closed。
	WebhookSigningKey string `env:"WEBHOOK_SIGNING_KEY" envDefault:""`
	// StatementSigningKey 对账单签名下载密钥（HMAC-SHA256）。
	// 为空时回退 WEBHOOK_SIGNING_KEY；两者都为空则不开放签名下载。
	StatementSigningKey string `env:"STATEMENT_SIGNING_KEY" envDefault:""`
	// WebhookPayloadKeyBase64 Webhook payload 加密密钥（B7）。
	// 独立于 WEBHOOK_SIGNING_KEY，只用于 AES-256-GCM payload 加密/解密。
	// local/mock 模式使用此 key 或 MASTER_KEY_BASE64 派生 AES key。
	// production 模式必须通过 KMS/Envelope Encryptor，此 key 仅用于 local/mock。
	WebhookPayloadKeyBase64 string `env:"WEBHOOK_PAYLOAD_KEY_BASE64" envDefault:""`
}

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if cfg.AdminSessionTTL <= 0 || cfg.AdminSessionTTL > 12*time.Hour {
		return nil, fmt.Errorf("config: ADMIN_SESSION_TTL must be greater than 0 and at most 12h")
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

	// G15: VCC_FUNDING_PROVIDER 白名单校验。
	// 允许的值：disabled（不启动 worker）、mock（本地闭环测试，真实账本）、local（开发环境）。
	// 任何其他值在启动时由 VCCAdapterFactory 返回 ErrUnsupportedCapability fail-closed。
	// 此处仅做语法校验，capability 边界由 factory 在 worker 启动时强制。
	if cfg.VCCFundingProvider != "disabled" && cfg.VCCFundingProvider != "mock" && cfg.VCCFundingProvider != "local" {
		// 允许其他值通过配置校验，但会在 worker 启动时被 VCCAdapterFactory 拒绝。
		// 这样可以在日志中清晰看到 UNSUPPORTED_CAPABILITY 错误，而不是配置解析错误。
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

	if cfg.VCCFundingProvider == "mock" || cfg.VCCFundingProvider == "local" {
		return fmt.Errorf("config: VCC_FUNDING_PROVIDER=%s is not allowed in production", cfg.VCCFundingProvider)
	}

	// P0-B1: production 只允许 disabled 或已注册的真实 Provider。
	// mock/local 只能在 local/mock 环境运行。
	// 未注册 Provider 在启动时由 VCCAdapterFactory 返回 ErrUnsupportedCapability fail-closed。
	if cfg.VCCFundingProvider != "disabled" {
		// 非 disabled 必须通过 VCCAdapterFactory 注册；
		// 此处仅做语法校验，capability 边界由 factory 在 worker 启动时强制。
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

	// R25 A1: WEBHOOK_SIGNING_KEY production 校验
	// 不允许自动生成默认密钥，不允许回退到 MASTER_KEY_BASE64 充当签名密钥
	if cfg.WebhookSigningKey == "" {
		return fmt.Errorf("config: WEBHOOK_SIGNING_KEY is required in production")
	}
	if len(cfg.WebhookSigningKey) < 32 {
		return fmt.Errorf("config: WEBHOOK_SIGNING_KEY must be at least 32 bytes in production (got %d)", len(cfg.WebhookSigningKey))
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
		os.Getenv("VCC_FUNDING_PROVIDER") == "local" || os.Getenv("CHECKOUT_ORDER_PROVIDER") == "mock"
}
