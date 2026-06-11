package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/admin"
	"github.com/nexusglobal/mamr/internal/ledger/handler"
	"github.com/nexusglobal/mamr/internal/ledger/repository/db"
	maehandler "github.com/nexusglobal/mamr/internal/mae/handler"
	maerepo "github.com/nexusglobal/mamr/internal/mae/repository"
	maeservice "github.com/nexusglobal/mamr/internal/mae/service"
	payouthandler "github.com/nexusglobal/mamr/internal/payout/handler"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	payoutservice "github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/stream"
	treasuryhandler "github.com/nexusglobal/mamr/internal/treasury/handler"
	treasuryrepo "github.com/nexusglobal/mamr/internal/treasury/repository"
	"github.com/nexusglobal/mamr/internal/treasury/scheduler"
	treasuryservice "github.com/nexusglobal/mamr/internal/treasury/service"
	webhookhandler "github.com/nexusglobal/mamr/internal/webhook/handler"
	webhookrepo "github.com/nexusglobal/mamr/internal/webhook/repository"
	"github.com/nexusglobal/mamr/pkg/external/bank"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel)
	ctx := context.Background()

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

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(httpx.RequestID)
	r.Use(httpx.RequestLogger(log))
	r.Use(httpx.Recover(log))

	kms, _ := crypto.NewMockKMS(cfg.MasterKeyBase64)
	var enc *crypto.Encryptor
	if kms != nil {
		enc = crypto.NewEncryptor(kms)
	}

	queries := db.New(pool)
	ledgerH := handler.New(queries)

	payoutRepo := payoutrepo.New(pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(pool)
	txm := postgres.NewTxManager(pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, txm, pool)
	payoutH := payouthandler.New(payoutSvc)

	maeRepo := maerepo.New(pool)
	maeSvc := maeservice.New(maeRepo, enc, pool)
	maeH := maehandler.New(maeSvc)

	var webhookH *webhookhandler.Handler
	var producer *stream.Producer
	if rdb != nil {
		producer = stream.NewProducer(rdb)
		webhookRepoInst := webhookrepo.New(pool)
		webhookH = webhookhandler.New(webhookRepoInst, producer, enc)
	} else {
		webhookH = webhookhandler.New(nil, nil, enc)
	}

	treasuryRepoInst := treasuryrepo.New(pool)
	var delayQ *scheduler.DelayQueue
	if rdb != nil {
		delayQ = scheduler.NewDelayQueue(rdb, "SWEEP_DELAY")
	}
	treasurySvc := treasuryservice.New(treasuryRepoInst, bank.NewMockBank(), txm, pool, delayQ, log)
	treasuryH := treasuryhandler.New(treasurySvc)

	outboxH := payouthandler.NewOutboxHandler(pool)

	dbPing := func() error { return pool.Ping(context.Background()) }
	redisPing := func() error {
		if rdb == nil {
			return http.ErrServerClosed
		}
		return rdb.Ping(context.Background()).Err()
	}

	r.Get("/healthz", httpx.HealthCheck(dbPing, redisPing))
	r.Get("/readyz", httpx.ReadinessCheck(dbPing, redisPing))
	r.Get("/admin/dashboard", httpx.AdminDashboardHTML(pool, rdb, cfg.AppEnv))
	r.Get("/admin/dashboard/data", httpx.DashboardHandler(pool, rdb, cfg.AppEnv))

	adminH := admin.NewHandler(pool)

	r.Route("/admin", func(r chi.Router) {
		r.Use(httpx.AdminAuth)

		// Stats
		r.Get("/stats", adminH.GetStats)

		// MAE management
		r.Get("/mae-list", adminH.ListMAEs)
		r.Get("/mae-detail/{mae_id}", adminH.GetMAE)

		// Payout management
		r.Get("/payouts", adminH.ListPayouts)
		r.Get("/payout/{id}", adminH.GetPayout)

		// Outbox
		r.Get("/outbox", adminH.ListOutbox)

		// Treasury
		r.Get("/calendars", adminH.ListCalendars)
		r.Get("/sweep-rules", adminH.ListSweepRules)
		r.Get("/sweep-transfers", adminH.ListSweepTransfers)

		// Webhook
		r.Get("/webhook-events", adminH.ListWebhookEvents)
		r.Get("/webhook-event/{id}", adminH.GetWebhookEvent)

		// Ledger
		r.Get("/ledger/transactions", adminH.ListLedgerTransactions)
		r.Get("/ledger/entries", adminH.ListLedgerEntries)

		// Merchants
		r.Get("/merchants", adminH.ListMerchants)

		r.Route("/merchant/{id}", func(r chi.Router) {
			r.Get("/balance", ledgerH.GetBalance)
		})
		r.Route("/mae", func(r chi.Router) {
			r.Post("/", maeH.Create)
			r.Patch("/{mae_id}", maeH.Update)
			r.Post("/{mae_id}/rotate-secret", maeH.RotateSecret)
			r.Post("/{mae_id}/status", maeH.ChangeStatus)
			r.Get("/{mae_id}/metrics", maeH.GetMetrics)
		})
		r.Route("/banking-calendar", func(r chi.Router) {
			r.Post("/", treasuryH.CreateCalendar)
		})
		r.Route("/sweep-rule", func(r chi.Router) {
			r.Post("/", treasuryH.CreateSweepRule)
			r.Patch("/{id}", treasuryH.UpdateSweepRule)
		})
		r.Get("/webhook/events", webhookH.ListEvents)
		r.Post("/webhook/events/{id}/replay", webhookH.Replay)
		r.Get("/outbox", outboxH.List)
		r.Post("/outbox/{id}/replay", outboxH.Replay)
	})

	r.Route("/merchant", func(r chi.Router) {
		r.Use(httpx.MerchantAuth)
		r.Post("/payout-address", payoutH.CreateAddress)
		r.Post("/payout-request", payoutH.CreatePayoutRequest)
	})

	r.Route("/internal", func(r chi.Router) {
		r.Use(httpx.InternalAuth)
		r.Post("/payout/{id}/freeze", payoutH.Freeze)
		r.Post("/payout/{id}/fx-lock", payoutH.FxLock)
		r.Post("/payout/{id}/submit", payoutH.Submit)
		r.Post("/payout/{id}/reconcile", payoutH.Reconcile)
		r.Post("/sweep/evaluate", treasuryH.Evaluate)
		r.Post("/sweep/{id}/submit", treasuryH.Submit)
		r.Post("/sweep/{id}/reconcile", treasuryH.Reconcile)
		r.Post("/mae/{mae_id}/circuit-break", maeH.CircuitBreak)
		r.Post("/mae/{mae_id}/testing-evaluate", maeH.TestingEvaluate)
	})

	r.Route("/webhook", func(r chi.Router) {
		r.Post("/mae/{provider_id}", webhookH.HandleMAE)
		r.Post("/bank/{provider_id}", webhookH.HandleBank)
		r.Post("/lp/{provider_id}", webhookH.HandleLP)
	})

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: r,
	}

	go func() {
		log.Info("API server starting", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	log.Info("server stopped")
}
