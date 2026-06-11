package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/webhook/consumer"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, pgErr := postgres.NewPool(ctx, cfg.PostgresDSN)
	if pgErr != nil {
		log.Error("postgres unavailable", "err", pgErr)
		os.Exit(1)
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn("redis unavailable", "err", err)
		rdb = nil
	}

	log.Info("worker starting", "concurrency", cfg.OutboxWorkerConcurrency)

	if rdb != nil {
		lpConsumer := consumer.New(rdb, nil, log)
		go func() {
			if err := lpConsumer.Start(ctx); err != nil {
				log.Error("lp consumer error", "err", err)
			}
		}()
		log.Info("lp webhook consumer started")
	}

	log.Info("outbox worker would start here (requires service wiring)")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down worker...")
	cancel()
	if pool != nil {
		pool.Close()
	}
	if rdb != nil {
		rdb.Close()
	}
	log.Info("worker stopped")
}
