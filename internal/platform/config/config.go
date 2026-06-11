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
	OutboxWorkerConcurrency int           `env:"OUTBOX_WORKER_CONCURRENCY" envDefault:"4"`
	OutboxClaimBatch        int           `env:"OUTBOX_CLAIM_BATCH" envDefault:"16"`
	StreamMaxDeliveries     int           `env:"STREAM_MAX_DELIVERIES" envDefault:"5"`
	SweepTickInterval       time.Duration `env:"SWEEP_TICK_INTERVAL" envDefault:"10s"`
	LogLevel                string        `env:"LOG_LEVEL" envDefault:"info"`
}

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
