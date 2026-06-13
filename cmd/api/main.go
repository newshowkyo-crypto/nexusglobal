package main

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/nexusglobal/mamr/internal/admin"
	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/ledger/handler"
	"github.com/nexusglobal/mamr/internal/ledger/repository/db"
	maehandler "github.com/nexusglobal/mamr/internal/mae/handler"
	maemetric "github.com/nexusglobal/mamr/internal/mae/metric"
	maerepo "github.com/nexusglobal/mamr/internal/mae/repository"
	maeservice "github.com/nexusglobal/mamr/internal/mae/service"
	payouthandler "github.com/nexusglobal/mamr/internal/payout/handler"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	payoutservice "github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/factories"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	"github.com/nexusglobal/mamr/internal/platform/otel"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/stream"
	"github.com/nexusglobal/mamr/internal/token"
	treasuryhandler "github.com/nexusglobal/mamr/internal/treasury/handler"
	treasuryrepo "github.com/nexusglobal/mamr/internal/treasury/repository"
	"github.com/nexusglobal/mamr/internal/treasury/scheduler"
	treasuryservice "github.com/nexusglobal/mamr/internal/treasury/service"
	webhookhandler "github.com/nexusglobal/mamr/internal/webhook/handler"
	webhookrepo "github.com/nexusglobal/mamr/internal/webhook/repository"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel)
	ctx := context.Background()

	shutdown, err := otel.InitTracer(ctx, otel.Config{
		ServiceName:  "nexusglobal-api",
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
		log.Warn("redis unavailable", "err", err)
		rdb = nil
	}

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(httpx.RequestID)
	r.Use(httpx.RequestLogger(log))
	r.Use(httpx.Recover(log))

	deps, err := factories.NewExternalDeps(cfg)
	if err != nil {
		log.Error("init external deps", "err", err)
		os.Exit(1)
	}

	var enc *crypto.Encryptor
	if deps.KMS != nil {
		enc = crypto.NewEncryptor(deps.KMS)
	}

	queries := db.New(pool)
	ledgerH := handler.New(queries)

	payoutRepo := payoutrepo.New(pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(pool)
	txm := postgres.NewTxManager(pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, enc, txm, pool)
	payoutH := payouthandler.New(payoutSvc)

	maeRepo := maerepo.New(pool)
	maeSvc := maeservice.New(maeRepo, enc, pool)
	var maeMetrics *maemetric.Aggregator
	if rdb != nil {
		maeMetrics = maemetric.NewAggregator(rdb)
	}
	maeH := maehandler.New(maeSvc, maeMetrics)

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
	treasurySvc := treasuryservice.New(treasuryRepoInst, deps.Bank, txm, pool, delayQ, log)
	treasuryH := treasuryhandler.New(treasurySvc)

	dbPing := func() error { return pool.Ping(context.Background()) }
	redisPing := func() error {
		if rdb == nil {
			return http.ErrServerClosed
		}
		return rdb.Ping(context.Background()).Err()
	}

	r.Get("/healthz", httpx.HealthCheck(dbPing, redisPing))
	r.Get("/readyz", httpx.ReadinessCheck(dbPing, redisPing))

	adminH := admin.NewHandler(pool, enc)

	r.Route("/admin", func(r chi.Router) {
		r.Use(httpx.AdminAuth(cfg.JWTSecret))

		r.Get("/dashboard", httpx.AdminDashboardHTML(pool, rdb, cfg.AppEnv))
		r.Get("/dashboard/data", httpx.DashboardHandler(pool, rdb, cfg.AppEnv))

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

		// R3-3: 数据录入端点
		r.Post("/bank-account", adminH.CreateBankAccount) // 银行账户（表 bank_account_balance）
		r.Post("/bank-calendar", adminH.CreateBankCalendar)
		r.Post("/mae-config", adminH.CreateMAEConfig)
		r.Post("/sweep-rule-create", adminH.CreateSweepRule)
		r.Post("/webhook-proxy", adminH.CreateWebhookProxy)
		r.Post("/merchant-create", adminH.CreateMerchant)
		r.Post("/payout-address", adminH.CreatePayoutAddress)

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
	})

	r.Route("/merchant", func(r chi.Router) {
		r.Use(httpx.MerchantAuth(cfg.JWTSecret))
		r.Post("/payout-address", payoutH.CreateAddress)
		r.Post("/payout-request", payoutH.CreatePayoutRequest)
	})

	r.Route("/internal", func(r chi.Router) {
		r.Use(httpx.InternalAuth(cfg.InternalToken))
		r.Get("/metrics", httpx.MetricsHandler().ServeHTTP) // G9: 移入鉴权保护
		r.Post("/payout/{id}/freeze", payoutH.Freeze)
		r.Post("/payout/{id}/fx-lock", payoutH.FxLock)
		r.Post("/payout/{id}/submit", payoutH.Submit)
		r.Post("/payout/{id}/reconcile", payoutH.Reconcile)
		r.Post("/sweep/evaluate", treasuryH.Evaluate)
		r.Post("/sweep/{id}/submit", treasuryH.Submit)
		r.Post("/sweep/{id}/reconcile", treasuryH.Reconcile)
		r.Post("/sweep/approval/{id}/approve", treasuryH.ApproveSweep)
		r.Post("/mae/{mae_id}/circuit-break", maeH.CircuitBreak)
		// R2-2: TestingEvaluate 已下线（worker tick 覆盖真实评估）
	})

	r.Route("/webhook", func(r chi.Router) {
		r.Post("/mae/{provider_id}", webhookH.HandleMAE)
		r.Post("/bank/{provider_id}", webhookH.HandleBank)
		r.Post("/lp/{provider_id}", webhookH.HandleLP)
	})

	// Checkout routes
	// R3-2: 按配置选择 token store
	var tokenStore token.Store
	if rdb != nil && cfg.TokenStore == "redis" {
		tokenStore = token.NewRedisStore(rdb)
		log.Info("using redis token store")
	} else {
		tokenStore = token.NewMemoryStore()
		log.Info("using memory token store")
	}
	tokenSvc := token.NewService(tokenStore)
	checkoutTmpl, err := template.ParseFiles("web/checkout/checkout.html")
	if err != nil {
		log.Warn("checkout template not found, checkout disabled", "err", err)
	} else {
		// R3-1: 创建支付网关（mock/real 按配置切换）
		paymentGW := checkout.NewMockPaymentGateway()
		checkoutH := checkout.NewHandler(tokenSvc, nil, paymentGW, checkoutTmpl)
		r.Get("/checkout", checkoutH.ServeCheckoutPage)
		r.Route("/api/checkout", func(r chi.Router) {
			r.Get("/validate", checkoutH.HandleValidateToken)
			r.Post("/charge", checkoutH.HandleCharge)
		})
	}

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: otelhttp.NewHandler(r, "nexusglobal-api"),
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
