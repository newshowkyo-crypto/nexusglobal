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

	"github.com/nexusglobal/mamr/internal/acquirer"
	acquirerhandler "github.com/nexusglobal/mamr/internal/acquirer/handler"
	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/acquirer/repository"
	"github.com/nexusglobal/mamr/internal/admin"
	"github.com/nexusglobal/mamr/internal/checkout"
	disputehandler "github.com/nexusglobal/mamr/internal/dispute/handler"
	disputerepo "github.com/nexusglobal/mamr/internal/dispute/repository"
	disputeservice "github.com/nexusglobal/mamr/internal/dispute/service"
	ledgerhandler "github.com/nexusglobal/mamr/internal/ledger/handler"
	"github.com/nexusglobal/mamr/internal/ledger/repository/db"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	maehandler "github.com/nexusglobal/mamr/internal/mae/handler"
	maemetric "github.com/nexusglobal/mamr/internal/mae/metric"
	maerepo "github.com/nexusglobal/mamr/internal/mae/repository"
	maeservice "github.com/nexusglobal/mamr/internal/mae/service"
	payouthandler "github.com/nexusglobal/mamr/internal/payout/handler"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	payoutservice "github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/pipeline"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/factories"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	"github.com/nexusglobal/mamr/internal/platform/otel"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/stream"
	riskcarding "github.com/nexusglobal/mamr/internal/risk/carding"
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
	ledgerH := ledgerhandler.New(queries)

	payoutRepo := payoutrepo.New(pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(pool)
	txm := postgres.NewTxManager(pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, enc, txm, pool)
	payoutH := payouthandler.New(payoutSvc)

	ledgerSvc := ledgerservice.New(ledgerRepo, txm, pool)
	disputeRepo := disputerepo.New(pool)
	disputeSvc := disputeservice.New(disputeRepo, ledgerSvc, txm)
	disputeH := disputehandler.New(disputeSvc)

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

	// 任务包 B: Anti-Carding Redis Sliding Window Gateway
	var riskGateway *riskcarding.Gateway
	var riskSvc *riskcarding.Service
	var riskH *riskcarding.Handler
	if rdb != nil {
		riskGateway = riskcarding.NewGateway(riskcarding.GatewayConfig{
			RDB:      rdb,
			Logger:   log,
			FailOpen: cfg.AppEnv == "local" || cfg.AppEnv == "mock", // production 默认 fail-closed
		})
		riskRepo := riskcarding.NewRepository(pool)
		riskSvc = riskcarding.NewService(riskGateway, riskRepo, pool, log)
		riskH = riskcarding.NewHandler(riskSvc)
	}

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
	v3ChannelsH := admin.NewV3ChannelsHandler(pool, rdb)
	v3IssuingPoolsH := admin.NewV3IssuingPoolsHandler(pool, rdb)
	killSwitchH := admin.NewKillSwitchHandler(pool, rdb)

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
		r.Get("/outbox/{id}", adminH.GetOutbox)

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

		// Universal Adapter Registry API
		r.Route("/adapter", func(r chi.Router) {
			r.Get("/providers", adminH.ListAdapterProviders)
			r.Post("/providers", adminH.CreateAdapterProvider)
			r.Patch("/providers/{id}", adminH.UpdateAdapterProvider)
			r.Delete("/providers/{id}", adminH.DeleteAdapterProvider)
			r.Get("/channels", adminH.ListAdapterChannels)
			r.Post("/channels", adminH.CreateAdapterChannel)
			r.Patch("/channels/{id}", adminH.UpdateAdapterChannel)
			r.Post("/channels/{id}/enable", adminH.EnableAdapterChannel)
			r.Post("/channels/{id}/disable", adminH.DisableAdapterChannel)
			r.Post("/channels/{id}/reload", adminH.ReloadAdapterChannel)
		})

		// Corporate Entity API
		r.Route("/entities", func(r chi.Router) {
			r.Get("/", adminH.ListEntities)
			r.Post("/", adminH.CreateEntity)
			r.Patch("/{id}", adminH.UpdateEntity)
			r.Post("/{id}/risk-hold", adminH.SetEntityRiskHold)
			r.Post("/{id}/enable", adminH.EnableEntity)
		})

		// Asset Bridge API
		r.Route("/asset-bridge/profiles", func(r chi.Router) {
			r.Get("/", adminH.ListBridgeProfiles)
			r.Post("/", adminH.CreateBridgeProfile)
			r.Patch("/{id}", adminH.UpdateBridgeProfile)
			r.Post("/{id}/enable", adminH.EnableBridgeProfile)
			r.Post("/{id}/disable", adminH.DisableBridgeProfile)
		})

		// Wallet Policy API
		r.Route("/wallet-policies", func(r chi.Router) {
			r.Get("/", adminH.ListWalletPolicies)
			r.Post("/", adminH.CreateWalletPolicy)
			r.Patch("/{id}", adminH.UpdateWalletPolicy)
			r.Post("/{id}/enable", adminH.EnableWalletPolicy)
			r.Post("/{id}/disable", adminH.DisableWalletPolicy)
		})

		// V3 合规升级 API
		r.Route("/v3", func(r chi.Router) {
			// 通道风控配置
			r.Get("/channel-risk", adminH.ListChannelRiskConfigs)
			r.Patch("/channel-risk/{id}", adminH.UpdateChannelRiskConfig)

			// 商户风险配置
			r.Get("/merchant-risk-profiles", adminH.ListMerchantRiskProfiles)
			r.Post("/merchant-risk-profiles", adminH.CreateMerchantRiskProfile)

			// 滚动准备金记录
			r.Get("/rolling-reserves", adminH.ListRollingReserveEntries)

			// 费用摊销记录
			r.Get("/fee-allocations", adminH.ListFeeAllocations)

			// 管道隔离组
			r.Get("/isolation-groups", adminH.ListIsolationGroups)
			r.Post("/isolation-groups", adminH.CreateIsolationGroup)
			r.Post("/isolation-groups/{id}/freeze", adminH.FreezeIsolationGroup)
			r.Post("/isolation-groups/{id}/unfreeze", adminH.UnfreezeIsolationGroup)

			// RFI 合规资料包
			r.Get("/rfi-documents", adminH.ListRFIDocuments)
			r.Post("/rfi-documents/generate", adminH.GenerateRFIDocument)
			r.Get("/rfi-documents/{id}", adminH.GetRFIDocument)
			r.Post("/rfi-documents/{id}/approve", adminH.ApproveRFIDocument)
			r.Post("/rfi-documents/{id}/reject", adminH.RejectRFIDocument)

			// Override 审计日志
			r.Get("/override-audit-logs", adminH.ListOverrideAuditLogs)

			// Tier-Based Acquiring Channels API
			r.Route("/api/v3/channels", func(r chi.Router) {
				r.Get("/", v3ChannelsH.ListChannels)
				r.Post("/", v3ChannelsH.CreateChannel)
				r.Patch("/{id}", v3ChannelsH.UpdateChannel)
				r.Post("/{id}/enable", v3ChannelsH.EnableChannel)
				r.Post("/{id}/disable", v3ChannelsH.DisableChannel)
				r.Post("/{id}/reload", v3ChannelsH.ReloadChannel)
			})

			// Tier-Based Issuing Pools API
			r.Route("/api/v3/issuing/pools", func(r chi.Router) {
				r.Get("/", v3IssuingPoolsH.ListPools)
				r.Post("/", v3IssuingPoolsH.CreatePool)
				r.Patch("/{id}", v3IssuingPoolsH.UpdatePool)
				r.Post("/{id}/enable", v3IssuingPoolsH.EnablePool)
				r.Post("/{id}/disable", v3IssuingPoolsH.DisablePool)
				r.Post("/{id}/reload", v3IssuingPoolsH.ReloadPool)
			})
		})

		// R3-3: 数据录入端点
		r.Post("/bank-account", adminH.CreateBankAccount) // 银行账户（表 bank_account_balance）
		r.Post("/bank-calendar", adminH.CreateBankCalendar)
		r.Post("/mae-config", adminH.CreateMAEConfig)
		r.Post("/sweep-rule-create", adminH.CreateSweepRule)
		r.Post("/webhook-proxy", adminH.CreateWebhookProxy)
		r.Post("/merchant-create", adminH.CreateMerchant)
		r.Post("/payout-address", adminH.CreatePayoutAddress)

		// Tier-Based Admin API (Dashboard calls /admin/api/v3/*)
		r.Route("/api/v3", func(r chi.Router) {
			r.Route("/channels", func(r chi.Router) {
				r.Get("/", v3ChannelsH.ListChannels)
				r.Post("/", v3ChannelsH.CreateChannel)
				r.Patch("/{id}", v3ChannelsH.UpdateChannel)
				r.Post("/{id}/enable", v3ChannelsH.EnableChannel)
				r.Post("/{id}/disable", v3ChannelsH.DisableChannel)
				r.Post("/{id}/reload", v3ChannelsH.ReloadChannel)
			})
			r.Route("/issuing/pools", func(r chi.Router) {
				r.Get("/", v3IssuingPoolsH.ListPools)
				r.Post("/", v3IssuingPoolsH.CreatePool)
				r.Patch("/{id}", v3IssuingPoolsH.UpdatePool)
				r.Post("/{id}/enable", v3IssuingPoolsH.EnablePool)
				r.Post("/{id}/disable", v3IssuingPoolsH.DisablePool)
				r.Post("/{id}/reload", v3IssuingPoolsH.ReloadPool)
			})
			r.Route("/system", func(r chi.Router) {
				r.Post("/lockdown", killSwitchH.Lockdown)
			})
			r.Get("/events/stream", killSwitchH.EventsStream)
		})

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

		// 任务包 B: Risk events
		if riskH != nil {
			r.Get("/risk/events", riskH.ListEvents)
			r.Get("/risk/events/{id}", riskH.GetEvent)
		}
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
		// Dispute endpoints
		r.Post("/dispute/refund", disputeH.RefundConfirmed)
		r.Post("/dispute/chargeback", disputeH.ChargebackReceived)
		r.Post("/dispute/{dispute_id}/reverse", disputeH.ChargebackReversed)
		// 任务包 B: Anti-Carding endpoints
		if riskH != nil {
			r.Post("/risk/evaluate", riskH.Evaluate)
			r.Post("/risk/failure", riskH.RecordFailure)
			r.Post("/risk/success", riskH.RecordSuccess)
			r.Post("/risk/cooldown/{merchant_id}", riskH.SetCooldown)
			r.Delete("/risk/cooldown/{merchant_id}", riskH.ClearCooldown)
		}
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
	}

	// 初始化 Acquirer Registry（所有模式都需要）
	var paymentGW checkout.PaymentGateway
	var acqH *acquirerhandler.Handler

	if checkoutTmpl != nil {
		// 始终创建 acquirerRepo 和 acqReg（无论 mock 或 real 模式）
		acquirerRepo := repository.New(pool)

		// 判断是否是 real 模式
		isRealMode := cfg.AcquirerMode == "real" || cfg.AppEnv == "production"

		acqReg := registry.NewChannelRegistry(registry.RegistryConfig{
			Repo:       acquirerRepo,
			Encryptor:  enc,
			RDB:        rdb,
			Logger:     log,
			FailMode:   registry.FailModeClosed, // production 默认 fail-closed
			IsRealMode: isRealMode,
		})

		// 启动时预热：加载所有 active channel（无论 mock 或 real 模式）
		if err := acqReg.LoadAll(ctx); err != nil {
			log.Error("acquirer registry load all failed", "err", err)
			if isRealMode {
				log.Error("acquirer registry initialization failed in real/production mode, fail-closed")
				os.Exit(1)
			}
			// mock/local 模式允许继续（会使用 fallback mock）
			log.Warn("continuing in mock/local mode without preloaded channels")
		} else {
			channelCount := len(acqReg.ListAll())
			log.Info("acquirer registry loaded", "channels", channelCount)
		}

		// Redis 可用时订阅配置变更（无论 mock 或 real 模式）
		if rdb != nil {
			if err := acqReg.SubscribeConfigUpdates(ctx); err != nil {
				log.Warn("acquirer registry subscribe failed", "err", err)
			} else {
				log.Info("acquirer registry subscribed to config updates")
			}
		}

		// 创建 AcquirerGateway（使用 ChannelRegistry）
		// isReal=true 时会拒绝 raw card，要求 payment_method_token
		// 初始化管道隔离服务（暂时使用 nil repository，后续需要实现）
		pipelineSvc := pipeline.NewService(nil, time.Now)
		paymentGW = checkout.NewAcquirerGateway(acqReg, pipelineSvc, isRealMode)

		// 创建 acquirer service 和 handler（无论 mock 或 real 模式）
		acqSvc := acquirer.NewService(acquirerRepo, acqReg, enc, log)
		acqH = acquirerhandler.New(acqSvc, enc)

		// 注册 Acquirer 管理 API（无论 mock 或 real 模式）
		r.Route("/admin/acquirer", func(r chi.Router) {
			r.Use(httpx.AdminAuth(cfg.JWTSecret))
			r.Get("/providers", acqH.ListProviders)
			r.Post("/providers", acqH.CreateProvider)
			r.Get("/channels", acqH.ListChannels)
			r.Post("/channels", acqH.CreateChannel)
			r.Patch("/channels/{id}", acqH.UpdateChannel)
			r.Post("/channels/{id}/enable", acqH.EnableChannel)
			r.Post("/channels/{id}/disable", acqH.DisableChannel)
			r.Post("/channels/{id}/rotate-secret", acqH.RotateSecret)
			r.Post("/channels/{id}/reload", acqH.ReloadChannel)
		})

		log.Info("acquirer gateway initialized", "is_real", isRealMode)
	}

	// 设置 checkout handler（如果模板可用）
	if checkoutTmpl != nil && paymentGW != nil {
		var orderProvider checkout.OrderProvider
		switch cfg.CheckoutOrderProvider {
		case "mock":
			orderProvider = checkout.NewMockOrderProvider()
		case "disabled":
			log.Warn("checkout order provider disabled, checkout routes not registered")
		case "http":
			log.Error("checkout HTTP order provider is not implemented; refusing to start with mock fallback")
			os.Exit(1)
		default:
			log.Error("unknown checkout order provider", "provider", cfg.CheckoutOrderProvider)
			os.Exit(1)
		}

		if orderProvider != nil {
			checkoutH := checkout.NewHandler(tokenSvc, orderProvider, paymentGW, riskGateway, checkoutTmpl)
			r.Get("/checkout", checkoutH.ServeCheckoutPage)
			r.Route("/api/checkout", func(r chi.Router) {
				r.Post("/token", checkoutH.HandleCreateToken)
				r.Get("/validate", checkoutH.HandleValidateToken)
				r.Post("/charge", checkoutH.HandleCharge)
			})
		}
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
