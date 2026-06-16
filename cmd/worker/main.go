package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	ledgerrepo "github.com/nexusglobal/mamr/internal/ledger/repository"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	maedomain "github.com/nexusglobal/mamr/internal/mae/domain"
	maemetric "github.com/nexusglobal/mamr/internal/mae/metric"
	maerepo "github.com/nexusglobal/mamr/internal/mae/repository"
	maeservice "github.com/nexusglobal/mamr/internal/mae/service"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	payoutservice "github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/factories"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	"github.com/nexusglobal/mamr/internal/platform/otel"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	treasuryrepo "github.com/nexusglobal/mamr/internal/treasury/repository"
	"github.com/nexusglobal/mamr/internal/treasury/scheduler"
	treasuryservice "github.com/nexusglobal/mamr/internal/treasury/service"
	"github.com/nexusglobal/mamr/internal/webhook/consumer"
	webhookrepo "github.com/nexusglobal/mamr/internal/webhook/repository"
	"github.com/nexusglobal/mamr/internal/worker/fxlock"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
	payoutworker "github.com/nexusglobal/mamr/internal/worker/payout"
	"github.com/nexusglobal/mamr/internal/worker/reconcile"
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

	shutdown, err := otel.InitTracer(ctx, otel.Config{
		ServiceName:  "nexusglobal-worker",
		CollectorURL: cfg.OTelCollectorURL,
		SampleRatio:  cfg.OTelSampleRatio,
		Environment:  cfg.AppEnv,
	})
	if err != nil {
		log.Error("otel init failed", "err", err)
		os.Exit(1)
	}
	defer shutdown(ctx)

	pool, pgErr := postgres.NewPool(ctx, cfg.PostgresDSN)
	if pgErr != nil {
		log.Error("postgres unavailable", "err", pgErr)
		os.Exit(1)
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		// reconcile worker 依赖 Redis 分布式锁保护资金对账，缺 Redis 不允许启动。
		log.Error("redis unavailable, worker requires redis for reconcile distributed lock", "err", err)
		os.Exit(1)
	}

	log.Info("worker starting", "concurrency", cfg.OutboxWorkerConcurrency)

	deps, err := factories.NewExternalDeps(cfg)
	if err != nil {
		log.Error("init external deps", "err", err)
		os.Exit(1)
	}

	txm := postgres.NewTxManager(pool)
	repo := payoutrepo.New(pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(pool)

	// 初始化费用摊销服务
	feeRepo := ledgerrepo.NewFeeRepo(pool)
	feeService := ledgerservice.NewFeeService(feeRepo)

	fxlockWorker := fxlock.New(repo, deps.LP, feeService, txm, pool, log)
	payoutWorker := payoutworker.New(repo, ledgerRepo, deps.LP, feeService, txm, pool, log)

	// rdb 已在启动时保证非 nil（reconcile 分布式锁强依赖 Redis）
	locker := reconcile.NewRedisLockProvider(rdb)
	reconcileWorker := reconcile.New(repo, payoutWorker, deps.LP, locker, log)

	maeRepoInst := maerepo.New(pool)
	maeSvc := maeservice.New(maeRepoInst, nil, pool)
	// rdb 在此保证非 nil（启动时已 fail-fast）。
	maeMetrics := maemetric.NewAggregator(rdb)
	// T1-1: 注入 Testing 连续灰度失败计数器（Redis），使探测退出判定读到真实失败次数。
	maeSvc = maeSvc.WithFailCounter(maeMetrics)

	treasuryRepoInst := treasuryrepo.New(pool)
	var delayQ *scheduler.DelayQueue
	if rdb != nil {
		delayQ = scheduler.NewDelayQueue(rdb, "SWEEP_DELAY")
	}
	treasurySvc := treasuryservice.New(treasuryRepoInst, deps.Bank, txm, pool, delayQ, log)
	payoutSvc := payoutservice.New(repo, ledgerRepo, nil, txm, pool)

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
		webhookRepo := webhookrepo.New(pool)

		// G5: 启动 LP、MAE、Bank 三个消费者
		lpConsumer := consumer.NewLPConsumer(rdb, webhookRepo, repo, ledgerRepo, nil, payoutWorker, log)
		go func() {
			if err := lpConsumer.Start(ctx); err != nil {
				log.Error("lp consumer error", "err", err)
			}
		}()
		log.Info("lp webhook consumer started")

		maeConsumer := consumer.NewMAEConsumer(rdb, webhookRepo, repo, ledgerRepo, nil, log)
		go func() {
			if err := maeConsumer.Start(ctx); err != nil {
				log.Error("mae consumer error", "err", err)
			}
		}()
		log.Info("mae webhook consumer started")

		bankConsumer := consumer.NewBankConsumer(rdb, webhookRepo, repo, ledgerRepo, nil, log)
		go func() {
			if err := bankConsumer.Start(ctx); err != nil {
				log.Error("bank consumer error", "err", err)
			}
		}()
		log.Info("bank webhook consumer started")

		// T2-4: 定期回收超时 pending 消息
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for _, c := range []*consumer.Consumer{lpConsumer, maeConsumer, bankConsumer} {
						reclaimed, err := c.ReclaimPending(ctx)
						if err != nil {
							log.Error("reclaim pending error", "err", err)
						} else if reclaimed > 0 {
							log.Info("reclaimed pending messages", "count", reclaimed)
						}
					}
				}
			}
		}()
	}

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := reconcileWorker.ReconcileAll(ctx); err != nil {
					log.Error("reconcile tick error", "err", err)
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				configs, err := maeSvc.ListConfigs(ctx)
				if err != nil {
					log.Error("circuit breaker tick: list configs", "err", err)
					continue
				}
				businessDate := maemetric.FormatBusinessDate(time.Now())
				for _, cfg := range configs {
					if maeMetrics == nil {
						continue
					}
					exposure, _ := maeMetrics.GetExposure(ctx, cfg.MaeID, businessDate)
					if err := maeSvc.EvaluateCircuitBreaker(ctx, maeMetrics, maeservice.EvaluateCircuitBreakerCmd{
						MAEID:        cfg.MaeID,
						BusinessDate: businessDate,
					}); err != nil {
						log.Error("circuit breaker tick failed", "mae_id", cfg.MaeID, "err", err)
					} else if exposure > 0 {
						log.Info("circuit breaker tick", "mae_id", cfg.MaeID, "exposure", exposure)
					}
				}
			}
		}
	}()

	// T1-1: Testing 探测窗口超时巡检，超 cooldown_minutes 未晋级则回 Cooldown。
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				configs, err := maeSvc.ListConfigs(ctx)
				if err != nil {
					log.Error("testing timeout tick: list configs", "err", err)
					continue
				}
				now := time.Now()
				for _, cfg := range configs {
					if maedomain.MAEStatus(cfg.Status) != maedomain.StatusTesting {
						continue
					}
					timedOut, err := maeSvc.EvaluateTestingTimeout(ctx, cfg.MaeID, now)
					if err != nil {
						log.Error("testing timeout tick failed", "mae_id", cfg.MaeID, "err", err)
					} else if timedOut {
						log.Info("testing window timeout, reverted to cooldown", "mae_id", cfg.MaeID)
					}
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rules, err := treasuryRepoInst.ListSweepRules(ctx)
				if err != nil {
					log.Error("sweep tick: list rules", "err", err)
					continue
				}
				for _, rule := range rules {
					result, err := treasurySvc.Evaluate(ctx, treasuryservice.EvaluateCmd{
						SweepRuleID: rule.ID,
						AsOf:        time.Now(),
					})
					if err != nil {
						log.Error("sweep tick: evaluate", "rule_id", rule.ID, "err", err)
						continue
					}
					if result.TransferID != "" {
						log.Info("sweep tick: transfer created", "rule_id", rule.ID, "transfer_id", result.TransferID, "decision", result.Decision)
					}
				}
				if err := treasurySvc.ProcessDelayed(ctx); err != nil {
					log.Error("sweep tick: process delayed", "err", err)
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				processed, err := payoutSvc.ProcessDueReserveBatches(ctx)
				if err != nil {
					log.Error("reserve release tick error", "err", err)
				} else if processed > 0 {
					log.Info("reserve release tick", "released", processed)
				}
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
