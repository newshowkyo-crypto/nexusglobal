package main

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/nexusglobal/mamr/internal/acquirer"
	acquireradapter "github.com/nexusglobal/mamr/internal/acquirer/adapter"
	acquirerdomain "github.com/nexusglobal/mamr/internal/acquirer/domain"
	acquirerhandler "github.com/nexusglobal/mamr/internal/acquirer/handler"
	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/acquirer/repository"
	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/pipeline"
	pipelinerepo "github.com/nexusglobal/mamr/internal/pipeline/repository"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/factories"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	"github.com/nexusglobal/mamr/internal/platform/otel"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/router"
	riskcarding "github.com/nexusglobal/mamr/internal/risk/carding"
	"github.com/nexusglobal/mamr/internal/token"
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

	deps, err := factories.NewExternalDeps(cfg)
	if err != nil {
		log.Error("init external deps", "err", err)
		os.Exit(1)
	}

	var enc *crypto.Encryptor
	if deps.KMS != nil {
		enc = crypto.NewEncryptor(deps.KMS)
	}

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
		riskSvc.SetRuleStore(riskcarding.NewRuleStore(pool))
		riskH = riskcarding.NewHandler(riskSvc)
	}

	// 初始化 Acquirer Registry（所有模式都需要）
	checkoutTmpl, err := template.ParseFiles("web/checkout/checkout.html")
	if err != nil {
		log.Warn("checkout template not found, checkout disabled", "err", err)
	}

	// PAY-003: 状态页模板（/checkout/v3/{id} 与 /pay/{token} 的浏览器视图）
	statusTmpl, err := template.ParseFiles("web/checkout/status.html")
	if err != nil {
		log.Warn("status template not found, resolver returns JSON only", "err", err)
		statusTmpl = nil
	}

	var paymentGW checkout.PaymentGateway
	var acqH *acquirerhandler.Handler
	var acqRepo *repository.Repository
	var acqReg *registry.ChannelRegistry

	if checkoutTmpl != nil {
		// 始终创建 acquirerRepo 和 acqReg（无论 mock 或 real 模式）
		acqRepo = repository.New(pool)

		// 判断是否是 real 模式
		isRealMode := cfg.AcquirerMode == "real" || cfg.AppEnv == "production"

		// P0-3: 创建 DefaultAdapterFactory 并注册 provider 构造器
		factory := registry.NewDefaultAdapterFactory(log)
		if !isRealMode {
			factory.Register("mock", func(credentials map[acquirerdomain.CredentialType]string, config map[string]any) (acquirerdomain.AcquirerAdapter, error) {
				webhookSecret := credentials[acquirerdomain.CredentialTypeWebhookSecret]
				mockAcquirer := acquireradapter.NewMockAcquirer(webhookSecret)
				if config != nil {
					if scenarios, ok := config["scenarios"].(map[string]interface{}); ok {
						for orderID, scenarioStr := range scenarios {
							switch fmt.Sprintf("%v", scenarioStr) {
							case "success":
								mockAcquirer.SetScenario(orderID, acquireradapter.ScenarioSuccess)
							case "failure":
								mockAcquirer.SetScenario(orderID, acquireradapter.ScenarioFailure)
							case "timeout":
								mockAcquirer.SetScenario(orderID, acquireradapter.ScenarioTimeout)
							case "partial":
								mockAcquirer.SetScenario(orderID, acquireradapter.ScenarioPartial)
							}
						}
					}
					if timeoutMs, ok := config["timeout_ms"].(float64); ok && timeoutMs > 0 {
						mockAcquirer.SetDefaultDelay(time.Duration(timeoutMs) * time.Millisecond)
					} else {
						mockAcquirer.SetDefaultDelay(100 * time.Millisecond)
					}
				}
				return mockAcquirer, nil
			})
		}
		factory.Register("stripe", func(credentials map[acquirerdomain.CredentialType]string, config map[string]any) (acquirerdomain.AcquirerAdapter, error) {
			secretKey := credentials[acquirerdomain.CredentialTypeSecret]
			webhookSecret := credentials[acquirerdomain.CredentialTypeWebhookSecret]
			if secretKey == "" {
				return nil, acquirerdomain.ErrCredentialVaultKeyEmpty
			}
			timeout := 30 * time.Second
			if cfgTimeout, ok := config["timeout_ms"].(float64); ok && cfgTimeout > 0 {
				timeout = time.Duration(cfgTimeout) * time.Millisecond
			}
			stripeClient, err := acquireradapter.NewStripeClient(acquireradapter.StripeConfig{
				SecretKey:     secretKey,
				WebhookSecret: webhookSecret,
				Timeout:       timeout,
			})
			if err != nil {
				return nil, err
			}
			return acquireradapter.NewStripeAcquirer(stripeClient), nil
		})
		factory.Register("nxtapay", func(credentials map[acquirerdomain.CredentialType]string, config map[string]any) (acquirerdomain.AcquirerAdapter, error) {
			apiKey := credentials[acquirerdomain.CredentialTypeAPIKey]
			apiSecret := credentials[acquirerdomain.CredentialTypeSecret]
			webhookSecret := credentials[acquirerdomain.CredentialTypeWebhookSecret]
			if apiKey == "" {
				return nil, acquirerdomain.ErrCredentialVaultKeyEmpty
			}
			if apiSecret == "" {
				return nil, acquirerdomain.ErrCredentialVaultKeyEmpty
			}
			baseURL := "https://sandbox.nxtapay.com/api/v1"
			if cfgBaseURL, ok := config["base_url"].(string); ok && cfgBaseURL != "" {
				baseURL = cfgBaseURL
			}
			timeout := 30 * time.Second
			if cfgTimeout, ok := config["timeout_ms"].(float64); ok && cfgTimeout > 0 {
				timeout = time.Duration(cfgTimeout) * time.Millisecond
			}
			merchantID := ""
			if cfgMerchantID, ok := config["merchant_id"].(string); ok {
				merchantID = cfgMerchantID
			}
			nxtapayClient, err := acquireradapter.NewNxtapayClient(acquireradapter.NxtapayConfig{
				APIKey:        apiKey,
				APISecret:     apiSecret,
				MerchantID:    merchantID,
				WebhookSecret: webhookSecret,
				BaseURL:       baseURL,
				Timeout:       timeout,
			})
			if err != nil {
				return nil, err
			}
			return acquireradapter.NewNxtapayAcquirer(nxtapayClient), nil
		})

		acqReg = registry.NewChannelRegistry(registry.RegistryConfig{
			Repo:       acqRepo,
			Encryptor:  enc,
			RDB:        rdb,
			Logger:     log,
			FailMode:   registry.FailModeClosed, // production 默认 fail-closed
			IsRealMode: isRealMode,
			Factory:    factory, // P0-3: 注入 Factory
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
		acqSvc := acquirer.NewService(acqRepo, acqReg, enc, log)
		acqH = acquirerhandler.New(acqSvc, enc)

		log.Info("acquirer gateway initialized", "is_real", isRealMode)
	}

	// 设置 checkout handler（如果模板可用）
	if checkoutTmpl != nil && acqReg != nil {
		var orderProvider checkout.OrderProvider
		switch cfg.CheckoutOrderProvider {
		case "mock":
			orderProvider = checkout.NewMockOrderProvider()
		case "disabled":
			log.Warn("checkout order provider disabled, checkout routes not registered")
		case "http":
			if cfg.CheckoutOrderAPIURL == "" {
				log.Error("checkout HTTP order provider requires CHECKOUT_ORDER_API_URL config")
				os.Exit(1)
			}
			orderProvider = checkout.NewHTTPOrderProvider(cfg.CheckoutOrderAPIURL)
			log.Info("checkout HTTP order provider initialized", "api_url", cfg.CheckoutOrderAPIURL)
		default:
			log.Error("unknown checkout order provider", "provider", cfg.CheckoutOrderProvider)
			os.Exit(1)
		}

		if orderProvider != nil {
			// P0-3: 在组合根中先构造 PipelineService，再注入 AcquirerGateway。
			// production/real 模式下 PipelineService 缺失必须启动失败。
			// 不允许回退到 NoopRepository（G05 fail-closed）。
			isRealMode := cfg.AcquirerMode == "real" || cfg.AppEnv == "production"
			var pipelineSvc *pipeline.Service
			if pool != nil {
				pipelineRepo := pipelinerepo.NewDBRepository(pool)
				pipelineSvc = pipeline.NewService(pipelineRepo, time.Now)
			} else if isRealMode {
				log.Error("pipeline service required in real/production mode but pool is nil, fail-closed")
				os.Exit(1)
			}
			paymentGW = checkout.NewAcquirerGateway(acqReg, pipelineSvc, isRealMode)
		}
	}

	// R3-2: 按配置选择 token store（仅用于日志提示，实际构造在 BuildRouter 内）
	if rdb != nil && cfg.TokenStore == "redis" {
		log.Info("using redis token store")
	} else {
		log.Info("using memory token store")
	}
	_ = token.NewService(token.NewMemoryStore()) // 触发 token 包初始化

	// G07: 构造持久化 Kill Switch Service 并从 DB 加载状态到进程内缓存。
	// DB 是真相（RED-14），启动时必须加载以支持重启恢复与多节点同步。
	failClosedSvc := failclosed.NewService(pool, rdb, log)
	if rec, err := failClosedSvc.LoadFromDB(ctx); err != nil {
		log.Error("failclosed: load from DB failed, fail-closed", "err", err)
		// 启动时无法加载控制状态：fail closed，拒绝启动
		os.Exit(1)
	} else if rec.IsLocked() {
		log.Warn("failclosed: system loaded in LOCKED state from DB; new financial flows will be blocked")
	}

	// G07: 启动多节点同步订阅器（Redis pub/sub）
	var failClosedSub *failclosed.Subscriber
	if rdb != nil {
		failClosedSub = failclosed.NewSubscriber(failClosedSvc, rdb, log)
		if err := failClosedSub.Start(ctx); err != nil {
			log.Warn("failclosed: subscriber start failed, continuing without pub/sub sync", "err", err)
		}
		defer failClosedSub.Stop()
	}

	// 构造 Deps 并委托给 BuildRouter 装配路由树。
	// 这保证 main() 与测试走同一装配路径 (G03 前置要求)。
	routerDeps := router.Deps{
		Cfg:               cfg,
		Log:               log,
		Pool:              pool,
		Rdb:               rdb,
		Enc:               enc,
		CheckoutTmpl:      checkoutTmpl,
		StatusTmpl:        statusTmpl,
		ExternalDeps:      deps,
		RiskGateway:       riskGateway,
		RiskService:       riskSvc,
		RiskHandler:       riskH,
		AcquirerGateway:   paymentGW,
		AcquirerHandler:   acqH,
		AcquirerRepo:      acqRepo,
		AcquirerReg:       acqReg,
		FailClosedService: failClosedSvc,
	}
	r := router.BuildRouter(routerDeps)

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: otelhttp.NewHandler(r, "nexusglobal-api"),
		// OPS: 生产必须设置超时——防慢连接耗尽 fd 与 Slowloris
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
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
