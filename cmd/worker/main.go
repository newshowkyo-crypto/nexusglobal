package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/webhook/consumer"
	"github.com/nexusglobal/mamr/internal/worker/fxlock"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
	payoutworker "github.com/nexusglobal/mamr/internal/worker/payout"
	"github.com/nexusglobal/mamr/pkg/external/lp"
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

	txm := postgres.NewTxManager(pool)
	repo := payoutrepo.New(pool)
	lpCli := lp.NewMockLP()

	fxlockWorker := fxlock.New(repo, lpCli, txm, pool, log)
	payoutWorker := payoutworker.New(repo, lpCli, txm, pool, log)

	outboxProcessor := func(ctx context.Context, event outbox.OutboxEvent) error {
		switch event.EventType {
		case "FX_LOCK_REQUESTED":
			return fxlockWorker.Handle(ctx, event)
		case "LP_PAYOUT_REQUESTED":
			return payoutWorker.Handle(ctx, event)
		default:
			log.Warn("unknown outbox event type", "type", event.EventType, "id", event.ID)
			return nil
		}
	}

	outboxCfg := outbox.Config{
		Concurrency:  cfg.OutboxWorkerConcurrency,
		BatchSize:    int32(cfg.OutboxClaimBatch),
		PollInterval: cfg.SweepTickInterval,
		MaxRetries:   cfg.StreamMaxDeliveries,
	}
	outboxWorker := outbox.New(repo, outboxProcessor, outboxCfg, log)
	outboxWorker.Start(ctx)
	log.Info("outbox worker started", "concurrency", cfg.OutboxWorkerConcurrency)

	if rdb != nil {
		lpConsumer := consumer.New(rdb, nil, log)
		go func() {
			if err := lpConsumer.Start(ctx); err != nil {
				log.Error("lp consumer error", "err", err)
			}
		}()
		log.Info("lp webhook consumer started")
	}

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				log.Info("reconcile tick (placeholder)")
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down worker...")
	cancel()
	outboxWorker.Stop()
	if pool != nil {
		pool.Close()
	}
	if rdb != nil {
		rdb.Close()
	}
	log.Info("worker stopped")
}
