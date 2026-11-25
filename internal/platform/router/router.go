// Package router 提供共享�?BuildRouter 函数，供 cmd/api/main 和测试使用�?
//
// 提取动机 (G03 前置)：原�?cmd/api/main.go 内联�?600+ 行路由装配，
// 测试无法复用同一装配路径，导致鉴�?边界测试只能 mock 单个中间件，
// 无法覆盖"真实路由�?+ 真实中间件链"的端到端语义�?
//
// BuildRouter 接受依赖注入参数 (Deps)，返�?*chi.Mux�?
// main() 与测试构造不同的 Deps 实例，但走同一装配函数�?
package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chi_middleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/acquirer"
	acquirerhandler "github.com/nexusglobal/mamr/internal/acquirer/handler"
	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/acquirer/repository"
	"github.com/nexusglobal/mamr/internal/admin"
	"github.com/nexusglobal/mamr/internal/checkout"
	disputehandler "github.com/nexusglobal/mamr/internal/dispute/handler"
	disputerepo "github.com/nexusglobal/mamr/internal/dispute/repository"
	disputeservice "github.com/nexusglobal/mamr/internal/dispute/service"
	"github.com/nexusglobal/mamr/internal/fee"
	"github.com/nexusglobal/mamr/internal/issuing"
	ledgerhandler "github.com/nexusglobal/mamr/internal/ledger/handler"
	"github.com/nexusglobal/mamr/internal/ledger/repository/db"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	maehandler "github.com/nexusglobal/mamr/internal/mae/handler"
	maemetric "github.com/nexusglobal/mamr/internal/mae/metric"
	maerepo "github.com/nexusglobal/mamr/internal/mae/repository"
	maeservice "github.com/nexusglobal/mamr/internal/mae/service"
	merchantquery "github.com/nexusglobal/mamr/internal/merchantportal/query"
	merchantwebhook "github.com/nexusglobal/mamr/internal/merchantportal/webhook"
	payouthandler "github.com/nexusglobal/mamr/internal/payout/handler"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	payoutservice "github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/pipeline"
	pipelinerepo "github.com/nexusglobal/mamr/internal/pipeline/repository"
	"github.com/nexusglobal/mamr/internal/platform/config"
	poolpkg "github.com/nexusglobal/mamr/internal/pool"
	pooldomain "github.com/nexusglobal/mamr/internal/pool/domain"
	"github.com/nexusglobal/mamr/internal/refund"
	settlementservice "github.com/nexusglobal/mamr/internal/settlement"
	"github.com/nexusglobal/mamr/internal/split"
	"github.com/nexusglobal/mamr/internal/statement"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/factories"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	middleware "github.com/nexusglobal/mamr/internal/platform/httpx/middleware"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/ratelimit"
	"github.com/nexusglobal/mamr/internal/platform/stream"
	riskcarding "github.com/nexusglobal/mamr/internal/risk/carding"
	"github.com/nexusglobal/mamr/internal/token"
	treasuryhandler "github.com/nexusglobal/mamr/internal/treasury/handler"
	treasuryrepo "github.com/nexusglobal/mamr/internal/treasury/repository"
	"github.com/nexusglobal/mamr/internal/treasury/scheduler"
	treasuryservice "github.com/nexusglobal/mamr/internal/treasury/service"
	webhookhandler "github.com/nexusglobal/mamr/internal/webhook/handler"
	webhookrepo "github.com/nexusglobal/mamr/internal/webhook/repository"

	bankPkg "github.com/nexusglobal/mamr/pkg/external/bank"
)

// Deps 封装 BuildRouter 所需的全部依赖�?
//
// 字段含义�?
//   - Cfg: 应用配置（含 JWTSecret、InternalToken、AppEnv 等）
//   - Log: 结构化日�?
//   - Pool: pgx 连接池（可为 nil，测试场景）
//   - Rdb:  Redis 客户端（可为 nil�?
//   - Enc:  凭证加密器（可为 nil�?
//   - CheckoutTmpl: checkout.html 模板；nil 表示禁用 checkout 路由
//   - ExternalDeps: 外部依赖工厂结果（KMS/Bank/MAE/LP�?
//
// 测试场景下，调用方可以只�?Cfg + Log，其余留空，BuildRouter 会装配出
// 一�?最小可路由"的路由树，鉴权中间件照常生效，handler 内部�?pool=nil
// 会返�?503/500，但鉴权�?(401/403) 仍可被测试覆盖�?
type Deps struct {
	Cfg          *config.Config
	Log          *slog.Logger
	Pool         *pgxpool.Pool
	Rdb          *redis.Client
	Enc          *crypto.Encryptor
	CheckoutTmpl *template.Template
	// StatusTmpl: PAY-003 状态页模板（/checkout/v3/{id}、/pay/{token} 浏览器视图）；nil = JSON only
	StatusTmpl   *template.Template
	ExternalDeps *factories.ExternalDeps

	// MerchantVerifier 注入点；若为 nil �?Pool != nil，则自动构�?DBMerchantVerifier�?
	// 测试可注�?mock verifier�?
	MerchantVerifier middleware.MerchantVerifier

	// RateLimiter 注入点；若为 nil �?Rdb != nil，则自动构�?RedisBucket�?
	RateLimiter ratelimit.RateLimiter

	// AcquirerGateway 注入点；测试可注�?mock�?
	AcquirerGateway checkout.PaymentGateway
	AcquirerHandler *acquirerhandler.Handler
	AcquirerRepo    *repository.Repository
	AcquirerReg     *registry.ChannelRegistry

	// RiskGateway 注入�?
	RiskGateway *riskcarding.Gateway
	RiskService *riskcarding.Service
	RiskHandler *riskcarding.Handler

	// PipelineService 注入点；若为 nil �?Pool != nil，则自动构�?
	// 基于 DBRepository 的真�?pipeline.Service（G05：禁�?Noop 代替内部仓储）�?
	// 测试可注�?mock service；Pool == nil 时留空�?
	PipelineService *pipeline.Service

	// FailClosedService 注入点（G07）：持久�?Kill Switch 服务�?
	// 若为 nil �?Pool != nil，则自动构造基�?pool 的真�?Service�?
	// 启动时由 main.go 调用 LoadFromDB 加载状态到进程内缓存�?
	FailClosedService *failclosed.Service
}

// BuildRouter 装配完整�?chi.Mux 路由树�?
//
// 装配顺序�?cmd/api/main.go 原顺序一致：
//  1. 全局中间�?(RealIP, RequestID, RequestLogger, Recover)
//  2. /healthz, /readyz
//  3. /admin/* (AdminAuth 守卫)
//  4. /merchant/* (MerchantAuth JWT 守卫)
//  5. /internal/* (InternalAuth 守卫)
//  6. /webhook/*
//  7. /checkout, /api/checkout/*
//  8. /api/v3/checkout/*, /api/v3/* (MerchantAuth + RateLimit)
//  9. /admin/api/v3/* (AdminAuth 守卫)
//
// 返回�?*chi.Mux 可直接传�?httptest.NewServer 进行端到端测试�?
func BuildRouter(deps Deps) *chi.Mux {
	cfg := deps.Cfg
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(chi_middleware.RealIP)
	r.Use(httpx.RequestID)
	r.Use(httpx.RequestLogger(log))
	r.Use(httpx.Recover(log))

	// 健康检�?
	dbPing := func() error {
		if deps.Pool == nil {
			return http.ErrServerClosed
		}
		return deps.Pool.Ping(context.Background())
	}
	redisPing := func() error {
		if deps.Rdb == nil {
			return http.ErrServerClosed
		}
		return deps.Rdb.Ping(context.Background()).Err()
	}
	r.Get("/healthz", httpx.HealthCheck(dbPing, redisPing))
	r.Get("/readyz", httpx.ReadinessCheck(dbPing, redisPing))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
	})
	// 构造各 handler。pool �?nil �?handler 内部访问会返回错误，但鉴权层仍生效�?
	queries := db.New(deps.Pool)
	ledgerH := ledgerhandler.New(queries)

	payoutRepo := payoutrepo.New(deps.Pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(deps.Pool)
	txm := postgres.NewTxManager(deps.Pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, deps.Enc, txm, deps.Pool)
	payoutH := payouthandler.New(payoutSvc)

	ledgerSvc := ledgerservice.New(ledgerRepo, txm, deps.Pool)
	settlementSvc := settlementservice.New(deps.Pool, txm, ledgerSvc)
	feeRepo := fee.NewRepository(deps.Pool)
	settlementSvc.SetFeeEngine(feeRepo)
	feeH := fee.NewHandler(feeRepo, log)
	splitRepo := split.NewRepository(deps.Pool)
	settlementSvc.SetSplitEngine(splitRepo)
	splitH := split.NewHandler(splitRepo, log)
	settlementH := settlementservice.NewHandler(settlementSvc, log)
	// P1: 支付池分配器（off / shadow / enforce）
	poolRepo := poolpkg.NewRepository(deps.Pool)
	poolSvc := poolpkg.New(poolRepo, pooldomain.ParseMode(cfg.PoolMode), log)
	if deps.Rdb != nil {
		poolSvc.SetRedis(deps.Rdb)
	}
	poolH := poolpkg.NewHandler(poolRepo, log)
	// A2: 商户出站回调
	merchantWebhookRepo := merchantwebhook.NewRepository(deps.Pool, deps.Enc)
	merchantWebhookH := merchantwebhook.NewHandler(merchantWebhookRepo, log)
	// A2: 商户退款
	refundRepo := refund.NewRepository(deps.Pool)
	refundSvc := refund.NewService(deps.Pool, txm, refundRepo)
	refundH := refund.NewHandler(refundSvc, log)
	// A2: 日终对账单
	statementRepo := statement.NewRepository(deps.Pool, txm)
	statementSvc := statement.NewService(statementRepo)
	// P2: 对账单签名下载（缺省回退 webhook 签名密钥；都为空则不开放）
	statementSigningKey := cfg.StatementSigningKey
	if statementSigningKey == "" {
		statementSigningKey = cfg.WebhookSigningKey
	}
	if statementSigningKey != "" {
		statementSvc.SetSigningKey([]byte(statementSigningKey))
	} else {
		log.Warn("statement: signing key not configured, signed download disabled")
	}
	statementH := statement.NewHandler(statementSvc, log)
	// P2: 对账单签名下载入口（公开路由，仅接受有效签名 + 未过期）
	r.Get("/public/statements/{id}/download", statementH.PublicDownload)
	// A2: 商户查询
	queryRepo := merchantquery.NewRepository(deps.Pool)
	queryH := merchantquery.NewHandler(queryRepo, log)
	merchantKeyH := admin.NewMerchantKeyHandler(deps.Pool, log)
	disputeRepo := disputerepo.New(deps.Pool)
	disputeSvc := disputeservice.New(disputeRepo, ledgerSvc, txm)
	disputeH := disputehandler.New(disputeSvc)

	maeRepo := maerepo.New(deps.Pool)
	maeSvc := maeservice.New(maeRepo, deps.Enc, deps.Pool)
	var maeMetrics *maemetric.Aggregator
	if deps.Rdb != nil {
		maeMetrics = maemetric.NewAggregator(deps.Rdb)
	}
	maeH := maehandler.New(maeSvc, maeMetrics)

	var webhookH *webhookhandler.Handler
	var producer *stream.Producer
	if deps.Rdb != nil {
		producer = stream.NewProducer(deps.Rdb)
		webhookRepoInst := webhookrepo.New(deps.Pool)
		webhookH = webhookhandler.New(webhookRepoInst, producer, deps.Enc)
	} else {
		webhookH = webhookhandler.New(nil, nil, deps.Enc)
	}

	treasuryRepoInst := treasuryrepo.New(deps.Pool)
	var delayQ *scheduler.DelayQueue
	if deps.Rdb != nil {
		delayQ = scheduler.NewDelayQueue(deps.Rdb, "SWEEP_DELAY")
	}
	var bankAdapter bankPkg.Client
	if deps.ExternalDeps != nil {
		bankAdapter = deps.ExternalDeps.Bank
	}
	treasurySvc := treasuryservice.New(treasuryRepoInst, bankAdapter, txm, deps.Pool, delayQ, log)
	treasuryH := treasuryhandler.New(treasurySvc, deps.Pool)

	riskH := deps.RiskHandler
	if riskH == nil && deps.Rdb != nil {
		riskGateway := riskcarding.NewGateway(riskcarding.GatewayConfig{
			RDB:      deps.Rdb,
			Logger:   log,
			FailOpen: cfg.AppEnv == "local" || cfg.AppEnv == "mock",
		})
		riskRepo := riskcarding.NewRepository(deps.Pool)
		riskSvc := riskcarding.NewService(riskGateway, riskRepo, deps.Pool, log)
		riskSvc.SetRuleStore(riskcarding.NewRuleStore(deps.Pool))
		riskH = riskcarding.NewHandler(riskSvc)
	}
	if riskH != nil {
		// P6: 表化规则（channel/site 维度）
		riskH.SetRuleStore(riskcarding.NewRuleStore(deps.Pool))
	}

	adminH := admin.NewHandler(deps.Pool, deps.Enc)
	v3ChannelsH := admin.NewV3ChannelsHandler(deps.Pool, deps.Rdb)
	v3IssuingPoolsH := admin.NewV3IssuingPoolsHandler(deps.Pool, deps.Rdb)
	// R20 P0-2: 使用 ResolveRuntimeMode pure resolver，禁止硬编码 production=true。
	issuingRuntimeMode, modeErr := issuing.ResolveRuntimeMode(cfg.AppEnv, cfg.ExternalMode)
	if modeErr != nil {
		log.Error("issuing: invalid runtime mode configuration, fail-closed", "err", modeErr)
		panic("issuing: invalid runtime mode configuration: " + modeErr.Error())
	}
	v3IssuingPoolsH.SetRuntimeMode(issuingRuntimeMode)
	v3BankH := admin.NewV3BankHandler(deps.Pool, deps.Rdb)
	v3MerchantH := admin.NewV3MerchantHandler(deps.Pool)
	sitePolicyH := admin.NewSitePolicyHandler(deps.Pool)
	v3MerchantPolicyH := admin.NewV3MerchantPolicyHandler(deps.Pool, deps.Rdb)

	// G07: 构造持久化 Kill Switch Service（若未注入）
	failClosedSvc := deps.FailClosedService
	if failClosedSvc == nil && deps.Pool != nil {
		failClosedSvc = failclosed.NewService(deps.Pool, deps.Rdb, log)
	}
	killSwitchH := admin.NewKillSwitchHandlerWithService(deps.Pool, deps.Rdb, failClosedSvc)

	// P0-3: 构�?TokenEncryptor 并注入到 CheckoutService �?PaymentLinkService�?
	// Production 使用 KMS，local/mock 使用 MASTER_KEY_BASE64�?
	// 密钥缺失或非法时 fail-closed（panic）�?
	var tokenEncryptor checkout.TokenEncryptor
	if cfg.AppEnv == "production" {
		if deps.ExternalDeps == nil || deps.ExternalDeps.KMS == nil {
			log.Error("token encryptor: production mode requires KMS, fail-closed")
			panic("token encryptor: production mode requires KMS (P0-3)")
		}
		te, teErr := checkout.NewTokenEncryptorFromKMS(context.Background(), deps.ExternalDeps.KMS)
		if teErr != nil {
			log.Error("token encryptor: KMS DEK derivation failed, fail-closed", "err", teErr)
			panic("token encryptor: KMS DEK derivation failed (P0-3)")
		}
		tokenEncryptor = te
	} else {
		masterKey := cfg.MasterKeyBase64
		if masterKey == "" {
			log.Error("token encryptor: MASTER_KEY_BASE64 is empty, fail-closed")
			panic("token encryptor: MASTER_KEY_BASE64 is empty (P0-3)")
		}
		te, teErr := checkout.NewTokenEncryptorFromMasterKey(masterKey)
		if teErr != nil {
			log.Error("token encryptor: master key invalid, fail-closed", "err", teErr)
			panic("token encryptor: master key invalid (P0-3)")
		}
		tokenEncryptor = te
	}

	// P0-C2: 统一构造带 Encryptor �?PaymentLinkService，Admin 和公开 Handler 共享同一实例�?
	paymentLinkSvc := checkout.NewPaymentLinkServiceWithEncryptor(deps.Pool, tokenEncryptor)

	// P0-1: 注入 ChannelRegistry �?PaymentLinkService，确�?CreatePaymentLink 能调�?SelectChannel�?
	paymentLinkSvc.SetChannelRegistry(deps.AcquirerReg)
	if cfg.PoolMode != "off" {
		paymentLinkSvc.SetPoolSelector(poolSvc)
	}

	// P0-8: 提前构�?paymentLinksHandler，以便在 /admin 路由块内注册路由�?
	paymentLinksHandler := admin.NewPaymentLinksHandlerWithService(deps.Pool, paymentLinkSvc)

	// P0-2: �?AcquirerGateway �?*checkout.AcquirerGateway，注�?failClosedSvc�?
	// 这确�?Provider dispatch (Charge) 前会调用 ExecutionCheck�?
	if failClosedSvc != nil {
		if acqGW, ok := deps.AcquirerGateway.(*checkout.AcquirerGateway); ok {
			acqGW.SetFailClosedService(failClosedSvc)
		}
	}

	// /admin/* �?Admin JWT 守卫 + RBAC 权限矩阵（P0-8�?
	// 每条 Admin 路由必须同时�?AdminAuth �?RequirePermission�?
	// 最低权限矩阵：
	//   - GET：admin:read
	//   - 创建 Payment Link：payment_link:create
	//   - 创建审批：admin:request_change
	//   - 批准/拒绝：admin:approve
	//   - Kill Switch：admin:kill_switch
	//   - Recovery：admin:recovery
	//   - 凭证覆盖/rotate-secret：credential:write
	//   - risk-hold/freeze：admin:risk:hold
	// viewer 不得调用任何写接口；operator 不得批准自己的申请�?
	r.Route("/admin", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
		})
		// P0-1: 登录页和会话交换路由不需�?AdminAuth 守卫�?
		// 使用嵌套 Route 分离公开路由和受保护路由�?
		r.Group(func(r chi.Router) {
			// ADM-020: 登录交换是未鉴权入口，用固定窗口对 IP 简单限流防爆破
			r.Use(ipLoginRateLimit())
			r.Get("/login", httpx.AdminLoginPageHandler(cfg.AppEnv))
			r.Post("/session/exchange", httpx.AdminSessionExchangeHandler(cfg.JWTSecret, cfg.AdminSessionSecret, cfg.AppEnv))
			r.Post("/session/logout", httpx.AdminSessionLogoutHandler(cfg.AppEnv))
		})

		// P0-1: AdminAuthStrict 现在同时支持 Bearer JWT 和已签名会话 Cookie�?
		// sessionSecret 为空时只允许 Bearer JWT（向后兼容）�?
		r.Group(func(r chi.Router) {
			r.Use(httpx.AdminAuthStrict(cfg.JWTSecret, httpx.AdminIssuerExpected, httpx.AdminAudienceExpected, cfg.AdminSessionSecret))

			// ── 权限中间�?──
			readMW := httpx.RequirePermission(httpx.PermAdminRead)
			writeMW := httpx.RequirePermission(httpx.PermAdminWriteConfig)
			riskHoldMW := httpx.RequirePermission(httpx.PermAdminRiskHold)
			approveMW := httpx.RequirePermission(httpx.PermAdminApprove)
			requestChangeMW := httpx.RequirePermission(httpx.PermAdminRequestChange)
			killSwitchMW := httpx.RequirePermission(httpx.PermAdminKillSwitch)
			recoveryMW := httpx.RequirePermission(httpx.PermAdminRecovery)
			credentialWriteMW := httpx.RequirePermission(httpx.PermAdminCredentialWrite)
			r.With(readMW).Get("/operations/readiness", operationsReadiness(cfg))
			r.With(readMW).Get("/operations/summary", adminH.WorkbenchSummary)
			r.With(readMW).Get("/operations/funds.csv", adminH.WorkbenchExport)
			r.With(readMW).Get("/operations/records/{resource}", adminH.WorkbenchRecords)

			// ── 只读路由（admin:read）──
			r.With(readMW).Get("/dashboard", httpx.AdminDashboardHTML(deps.Pool, deps.Rdb, cfg.AppEnv, !issuingRuntimeMode.IsProduction()))
			r.With(readMW).Get("/dashboard/data", httpx.DashboardHandler(deps.Pool, deps.Rdb, cfg.AppEnv, !issuingRuntimeMode.IsProduction()))

			r.With(readMW).Get("/stats", adminH.GetStats)
			r.With(readMW).Get("/mae-list", adminH.ListMAEs)
			r.With(readMW).Get("/mae-detail/{mae_id}", adminH.GetMAE)
			r.With(readMW).Get("/payouts", adminH.ListPayouts)
			r.With(readMW).Get("/payout/{id}", adminH.GetPayout)
			r.With(readMW).Get("/outbox", adminH.ListOutbox)
			r.With(readMW).Get("/outbox/{id}", adminH.GetOutbox)
			r.With(readMW).Get("/calendars", adminH.ListCalendars)
			r.With(readMW).Get("/sweep-rules", adminH.ListSweepRules)
			r.With(readMW).Get("/sweep-transfers", adminH.ListSweepTransfers)
			r.With(readMW).Get("/webhook-events", adminH.ListWebhookEvents)
			r.With(readMW).Get("/webhook-event/{id}", adminH.GetWebhookEvent)
			r.With(readMW).Get("/ledger/transactions", adminH.ListLedgerTransactions)
			r.With(readMW).Get("/ledger/entries", adminH.ListLedgerEntries)
			r.With(readMW).Get("/merchants", adminH.ListMerchants)

			// ── 写配置路由（admin:write:config）──
			r.Route("/adapter", func(r chi.Router) {
				r.With(readMW).Get("/providers", adminH.ListAdapterProviders)
				r.With(writeMW).Post("/providers", adminH.CreateAdapterProvider)
				r.With(writeMW).Patch("/providers/{id}", adminH.UpdateAdapterProvider)
				r.With(writeMW).Delete("/providers/{id}", adminH.DeleteAdapterProvider)
				r.With(readMW).Get("/channels", adminH.ListAdapterChannels)
				r.With(writeMW).Post("/channels", adminH.CreateAdapterChannel)
				r.With(writeMW).Patch("/channels/{id}", adminH.UpdateAdapterChannel)
				r.With(writeMW).Post("/channels/{id}/enable", adminH.EnableAdapterChannel)
				r.With(writeMW).Post("/channels/{id}/disable", adminH.DisableAdapterChannel)
				r.With(writeMW).Post("/channels/{id}/reload", adminH.ReloadAdapterChannel)
			})

			r.Route("/entities", func(r chi.Router) {
				r.With(readMW).Get("/", adminH.ListEntities)
				r.With(writeMW).Post("/", adminH.CreateEntity)
				r.With(writeMW).Patch("/{id}", adminH.UpdateEntity)
				r.With(riskHoldMW).Post("/{id}/risk-hold", adminH.SetEntityRiskHold)
				r.With(writeMW).Post("/{id}/enable", adminH.EnableEntity)
			})

			r.Route("/asset-bridge/profiles", func(r chi.Router) {
				r.With(readMW).Get("/", adminH.ListBridgeProfiles)
				r.With(writeMW).Post("/", adminH.CreateBridgeProfile)
				r.With(writeMW).Patch("/{id}", adminH.UpdateBridgeProfile)
				r.With(writeMW).Post("/{id}/enable", adminH.EnableBridgeProfile)
				r.With(writeMW).Post("/{id}/disable", adminH.DisableBridgeProfile)
			})

			r.Route("/wallet-policies", func(r chi.Router) {
				r.With(readMW).Get("/", adminH.ListWalletPolicies)
				r.With(writeMW).Post("/", adminH.CreateWalletPolicy)
				r.With(writeMW).Patch("/{id}", adminH.UpdateWalletPolicy)
				r.With(writeMW).Post("/{id}/enable", adminH.EnableWalletPolicy)
				r.With(writeMW).Post("/{id}/disable", adminH.DisableWalletPolicy)
			})

			r.Route("/v3", func(r chi.Router) {
				r.With(readMW).Get("/channel-risk", adminH.ListChannelRiskConfigs)
				r.With(writeMW).Patch("/channel-risk/{id}", adminH.UpdateChannelRiskConfig)
				r.With(readMW).Get("/merchant-risk-profiles", adminH.ListMerchantRiskProfiles)
				r.With(writeMW).Post("/merchant-risk-profiles", adminH.CreateMerchantRiskProfile)
				r.With(readMW).Get("/rolling-reserves", adminH.ListRollingReserveEntries)
				r.With(readMW).Get("/fee-allocations", adminH.ListFeeAllocations)
				r.With(readMW).Get("/fee-summary", adminH.FeeSummary)
				r.With(readMW).Get("/isolation-groups", adminH.ListIsolationGroups)
				r.With(writeMW).Post("/isolation-groups", adminH.CreateIsolationGroup)
				r.With(riskHoldMW).Post("/isolation-groups/{id}/freeze", adminH.FreezeIsolationGroup)
				r.With(writeMW).Post("/isolation-groups/{id}/unfreeze", adminH.UnfreezeIsolationGroup)
				r.With(readMW).Get("/rfi-documents", adminH.ListRFIDocuments)
				r.With(writeMW).Post("/rfi-documents/generate", adminH.GenerateRFIDocument)
				r.With(readMW).Get("/rfi-documents/{id}", adminH.GetRFIDocument)
				r.With(approveMW).Post("/rfi-documents/{id}/approve", adminH.ApproveRFIDocument)
				r.With(approveMW).Post("/rfi-documents/{id}/reject", adminH.RejectRFIDocument)
				r.With(readMW).Get("/override-audit-logs", adminH.ListOverrideAuditLogs)

			})

			r.With(writeMW).Post("/bank-account", adminH.CreateBankAccount)
			r.With(writeMW).Post("/bank-calendar", adminH.CreateBankCalendar)
			r.With(writeMW).Post("/mae-config", adminH.CreateMAEConfig)
			r.With(writeMW).Post("/sweep-rule-create", adminH.CreateSweepRule)
			r.With(writeMW).Post("/webhook-proxy", adminH.CreateWebhookProxy)
			r.With(writeMW).Post("/merchant-create", adminH.CreateMerchant)
			r.With(writeMW).Post("/payout-address", adminH.CreatePayoutAddress)

			approvalsH := admin.NewApprovalHandler(deps.Pool, deps.Rdb)
			r.Route("/api/v3", func(r chi.Router) {
				r.Route("/channels", func(r chi.Router) {
					r.With(readMW).Get("/", v3ChannelsH.ListChannels)
					r.With(writeMW).Post("/", v3ChannelsH.CreateChannel)
					r.With(writeMW).Patch("/{id}", v3ChannelsH.UpdateChannel)
					r.With(writeMW).Post("/{id}/enable", v3ChannelsH.EnableChannel)
					r.With(writeMW).Post("/{id}/disable", v3ChannelsH.DisableChannel)
					r.With(writeMW).Post("/{id}/reload", v3ChannelsH.ReloadChannel)
				})
				r.Route("/sites", func(r chi.Router) {
					r.With(readMW).Get("/", sitePolicyH.ListSites)
					r.With(writeMW).Post("/", sitePolicyH.CreateSite)
					r.With(writeMW).Patch("/{id}/status", sitePolicyH.SetSiteStatus)
				})
				r.Route("/site-policies", func(r chi.Router) {
					r.With(readMW).Get("/", sitePolicyH.ListPolicies)
					r.With(writeMW).Post("/", sitePolicyH.CreatePolicy)
					r.With(writeMW).Patch("/{id}/status", sitePolicyH.SetPolicyStatus)
				})
				r.Route("/merchant-policies", func(r chi.Router) {
					r.With(readMW).Get("/", v3MerchantPolicyH.ListPolicies)
					r.With(writeMW).Post("/", v3MerchantPolicyH.CreatePolicy)
					r.With(writeMW).Patch("/{id}", v3MerchantPolicyH.UpdatePolicy)
				})
				r.Route("/issuing/pools", func(r chi.Router) {
					r.With(readMW).Get("/", v3IssuingPoolsH.ListPools)
					r.With(writeMW).Post("/", v3IssuingPoolsH.CreatePool)
					r.With(writeMW).Patch("/{id}", v3IssuingPoolsH.UpdatePool)
					r.With(writeMW).Post("/{id}/enable", v3IssuingPoolsH.EnablePool)
					r.With(writeMW).Post("/{id}/disable", v3IssuingPoolsH.DisablePool)
					r.With(writeMW).Post("/{id}/reload", v3IssuingPoolsH.ReloadPool)
				})
				// R20 P0-5: Vault 凭证写入 API（write-only，受 credential:write 权限保护）
				// production/real 下 Encryptor 缺失时不注册路由
				acqCredH := admin.NewAcquirerCredentialHandler(deps.Pool, deps.Enc, deps.Rdb)
				r.With(readMW).Get("/channels/{id}/credentials", acqCredH.CredentialStatus)
				if deps.Enc != nil {
					vaultCredH := admin.NewVaultCredentialHandler(deps.Pool, deps.Enc)
					r.With(credentialWriteMW).Post("/issuing/vault-credentials", vaultCredH.WriteCredential)
					// 收单侧凭证写入：写入即绑定（bump control/runtime 双 version）+ Redis 重载通知
					r.With(credentialWriteMW).Post("/channels/{id}/credentials", acqCredH.WriteCredential)
				}
				// Section 5: Admin/Dashboard 配置链路 — 供 Dashboard select 控件列出 ACTIVE adapter_provider
				r.With(readMW).Get("/issuing/adapter-providers", v3IssuingPoolsH.ListAdapterProviders)
				r.Route("/bank", func(r chi.Router) {
					r.Route("/va-pool", func(r chi.Router) {
						r.With(readMW).Get("/", v3BankH.ListVAPool)
						r.With(writeMW).Post("/", v3BankH.CreateVAAccount)
						r.With(writeMW).Post("/{id}/release", v3BankH.ReleaseVA)
						r.With(writeMW).Post("/{id}/disable", v3BankH.DisableVA)
					})
					r.Route("/settlement-accounts", func(r chi.Router) {
						r.With(readMW).Get("/", v3BankH.ListSettlementAccounts)
						r.With(writeMW).Post("/", v3BankH.CreateSettlementAccount)
					})
					r.With(readMW).Get("/intents", v3BankH.ListIntents)
					r.With(readMW).Get("/webhook-logs", v3BankH.ListWebhookLogs)
					r.With(readMW).Get("/reconciliation", v3BankH.ListReconciliationLogs)
				})
				// P0-8: Kill Switch 路由要求 admin:kill_switch 权限
				// Recovery 路由要求 admin:recovery 权限
				r.Route("/system", func(r chi.Router) {
					r.With(killSwitchMW).Post("/lockdown", killSwitchH.Lockdown)
					// P0-7: 全卡安全离场端点，要求 admin:kill_switch 权限
					r.With(killSwitchMW).Post("/suspend-active-cards", killSwitchH.SuspendActiveCards)
					// G07: 解除大闸需审批（maker-checker）
					r.With(recoveryMW).Post("/recovery/request", killSwitchH.RequestRecovery)
					r.With(recoveryMW).Post("/recovery/execute", killSwitchH.ExecuteRecovery)
				})
				r.With(readMW).Get("/events/stream", killSwitchH.EventsStream)
				r.Route("/merchant-webhooks", func(r chi.Router) {
					r.With(readMW).Get("/endpoints", merchantWebhookH.ListEndpoints)
					r.With(writeMW).Post("/endpoints", merchantWebhookH.CreateEndpoint)
					r.With(writeMW).Patch("/endpoints/{id}/status", merchantWebhookH.UpdateEndpointStatus)
					r.With(writeMW).Delete("/endpoints/{id}", merchantWebhookH.DeleteEndpoint)
					r.With(readMW).Get("/deliveries", merchantWebhookH.ListDeliveries)
					r.With(writeMW).Post("/deliveries/{id}/replay", merchantWebhookH.ReplayDelivery)
				})
				r.Route("/fees", func(r chi.Router) {
					r.With(readMW).Get("/pricing", feeH.List)
					r.With(writeMW).Post("/pricing", feeH.Upsert)
				})
				r.Route("/splits", func(r chi.Router) {
					r.With(readMW).Get("/rules", splitH.List)
					r.With(writeMW).Post("/rules", splitH.Upsert)
				})
				r.Route("/statements", func(r chi.Router) {
					r.With(writeMW).Post("/generate", statementH.AdminGenerate)
					r.With(readMW).Get("/", statementH.AdminList)
				})
				r.Route("/pool", func(r chi.Router) {
					r.With(readMW).Get("/channels", poolH.ListChannels)
					r.With(writeMW).Post("/channels", poolH.UpsertChannel)
					r.With(readMW).Get("/policies", poolH.ListPolicies)
					r.With(writeMW).Post("/policies", poolH.UpsertPolicy)
					r.With(readMW).Get("/watermarks", poolH.ListWatermarks)
				})
				r.Route("/merchants", func(r chi.Router) {
					r.With(readMW).Get("/", v3MerchantH.ListMerchants)
					r.With(writeMW).Post("/", v3MerchantH.CreateMerchant)
					r.With(readMW).Get("/{id}/connection-setup", v3MerchantH.ConnectionSetup)
					r.With(writeMW).Put("/{id}/connection-setup", v3MerchantH.ConnectionSetup)
					// P0-2
					r.With(readMW).Get("/for-channel", v3MerchantH.ListMerchantsForChannel)
					// A2: 商户 API Key 生命周期（签发/查看/吊销）
					r.Route("/{id}/api-keys", func(r chi.Router) {
						r.With(credentialWriteMW).Post("/", merchantKeyH.Issue)
						r.With(readMW).Get("/", merchantKeyH.Status)
						r.With(credentialWriteMW).Delete("/", merchantKeyH.Revoke)
					})
				})
				// P0-8: 审批路由权限矩阵�?
				//   - GET（List/Get）：admin:read
				//   - POST /（Create）：admin:request_change
				//   - POST /{id}/approve|reject：admin:approve
				r.Route("/approvals", func(r chi.Router) {
					r.With(readMW).Get("/", approvalsH.List)
					r.With(requestChangeMW).Post("/", approvalsH.Create)
					r.With(readMW).Get("/{id}", approvalsH.Get)
					r.With(approveMW).Post("/{id}/approve", approvalsH.Approve)
					r.With(approveMW).Post("/{id}/reject", approvalsH.Reject)
				})
				// P0-8: Payment Link 创建要求 payment_link:create 权限�?
				r.With(httpx.RequirePermission(httpx.PermPaymentLinkCreate)).Post("/payment-links", paymentLinksHandler.CreatePaymentLink)
			})

			// P0-8: Acquirer 管理 API �?每条路由挂载 RequirePermission�?
			if deps.AcquirerHandler != nil {
				acqReadMW := httpx.RequirePermission(httpx.PermAdminRead)
				acqWriteMW := httpx.RequirePermission(httpx.PermAdminWriteConfig)
				acqCredMW := httpx.RequirePermission(httpx.PermAdminCredentialWrite)
				acqH := deps.AcquirerHandler
				r.Route("/acquirer", func(r chi.Router) {
					r.With(acqReadMW).Get("/providers", acqH.ListProviders)
					r.With(acqWriteMW).Post("/providers", acqH.CreateProvider)
					r.With(acqReadMW).Get("/channels", acqH.ListChannels)
					r.With(acqWriteMW).Post("/channels", acqH.CreateChannel)
					r.With(acqWriteMW).Patch("/channels/{id}", acqH.UpdateChannel)
					r.With(acqWriteMW).Post("/channels/{id}/enable", acqH.EnableChannel)
					r.With(acqWriteMW).Post("/channels/{id}/disable", acqH.DisableChannel)
					r.With(acqCredMW).Post("/channels/{id}/rotate-secret", acqH.RotateSecret)
					r.With(acqWriteMW).Post("/channels/{id}/reload", acqH.ReloadChannel)
				})
			}

			r.Route("/merchant/{id}", func(r chi.Router) {
				r.With(readMW).Get("/balance", ledgerH.GetBalance)
			})
			// P0-8: rotate-secret 要求 credential:write 权限�?
			r.Route("/mae", func(r chi.Router) {
				r.With(writeMW).Post("/", maeH.Create)
				r.With(writeMW).Patch("/{mae_id}", maeH.Update)
				r.With(credentialWriteMW).Post("/{mae_id}/rotate-secret", maeH.RotateSecret)
				r.With(writeMW).Post("/{mae_id}/status", maeH.ChangeStatus)
				r.With(readMW).Get("/{mae_id}/metrics", maeH.GetMetrics)
			})
			r.Route("/banking-calendar", func(r chi.Router) {
				r.With(writeMW).Post("/", treasuryH.CreateCalendar)
			})
			r.Route("/sweep-rule", func(r chi.Router) {
				r.With(writeMW).Post("/", treasuryH.CreateSweepRule)
				r.With(writeMW).Patch("/{id}", treasuryH.UpdateSweepRule)
			})
			r.With(readMW).Get("/webhook/events", webhookH.ListEvents)
			r.With(writeMW).Post("/webhook/events/{id}/replay", webhookH.Replay)

			if riskH != nil {
				r.With(readMW).Get("/risk/events", riskH.ListEvents)
				r.With(readMW).Get("/risk/events/{id}", riskH.GetEvent)
				// P6: 表化风控规则（channel / site 池化维度）
				r.With(readMW).Get("/risk/rules", riskH.ListRules)
				r.With(writeMW).Post("/risk/rules", riskH.UpsertRule)
				// P6: 池化风险检测
				r.With(readMW).Get("/risk/alerts", riskH.Alerts)
			}
		}) // 关闭受保护路�?Group
	})

	// /merchant/* �?旧式 JWT MerchantAuth
	r.Route("/merchant", func(r chi.Router) {
		r.Use(httpx.MerchantAuth(cfg.JWTSecret))
		r.Post("/payout-address", payoutH.CreateAddress)
		r.Post("/payout-request", payoutH.CreatePayoutRequest)
		// A2: 商户退款（部分退款 / 累计校验 / 幂等）
		r.Post("/refunds", refundH.Create)
		r.Get("/refunds", refundH.List)
		r.Get("/refunds/{id}", refundH.Get)
		// A2: 日终对账单
		r.Get("/statements", statementH.MerchantList)
		r.Get("/statements/{id}", statementH.MerchantGet)
		r.Get("/statements/{id}/download", statementH.Download)
		r.Get("/statements/{id}/download-link", statementH.SignDownload)
		// A2: 订单/交易查询
		r.Get("/orders/{merchant_order_id}", queryH.GetOrder)
		r.Get("/payments/{attempt_id}", queryH.GetPayment)
		r.Get("/transactions", queryH.ListTransactions)
	})

	// /internal/* �?InternalAuth
	r.Route("/internal", func(r chi.Router) {
		r.Use(httpx.InternalAuth(cfg.InternalToken))
		r.Get("/metrics", httpx.MetricsHandler().ServeHTTP)
		r.Post("/payout/{id}/freeze", payoutH.Freeze)
		r.Post("/settlement/settle-all", settlementH.SettleAll)
		// SEC-012: fx-lock/submit/reconcile 假成功端点已删除；
		// 真实路径为 Outbox Worker（fxlock/payout/reconcile）+ Provider Webhook。
		r.Post("/sweep/evaluate", treasuryH.Evaluate)
		r.Post("/sweep/{id}/submit", treasuryH.Submit)
		r.Post("/sweep/{id}/reconcile", treasuryH.Reconcile)
		r.Post("/sweep/approval/{id}/approve", treasuryH.ApproveSweep)
		r.Post("/mae/{mae_id}/circuit-break", maeH.CircuitBreak)
		r.Post("/dispute/refund", disputeH.RefundConfirmed)
		r.Post("/dispute/chargeback", disputeH.ChargebackReceived)
		r.Post("/dispute/{dispute_id}/reverse", disputeH.ChargebackReversed)
		if riskH != nil {
			r.Post("/risk/evaluate", riskH.Evaluate)
			r.Post("/risk/failure", riskH.RecordFailure)
			r.Post("/risk/success", riskH.RecordSuccess)
			r.Post("/risk/cooldown/{merchant_id}", riskH.SetCooldown)
			r.Delete("/risk/cooldown/{merchant_id}", riskH.ClearCooldown)
		}
	})

	r.Route("/webhook", func(r chi.Router) {
		r.Post("/mae/{provider_id}", webhookH.HandleMAE)
		r.Post("/bank/{provider_id}", webhookH.HandleBank)
		r.Post("/lp/{provider_id}", webhookH.HandleLP)
	})

	// P0-5: Register Unified Webhook Receiver production route
	// B7: WEBHOOK_SIGNING_KEY is for HMAC verification only; encryption key is injected independently
	// R25 A2: production/real mode missing or short WebhookSigningKey must fail-closed (panic)
	// R26 Task 1: Extract validateUnifiedWebhookConfig + mountUnifiedWebhookRoute for test reuse
	if err := validateUnifiedWebhookConfig(cfg, log); err != nil {
		panic(err.Error())
	}
	if deps.Pool != nil && cfg.WebhookSigningKey != "" {
		if err := mountUnifiedWebhookRoute(r, deps, cfg, log, ledgerSvc); err != nil {
			panic(err.Error())
		}
	}

	// Checkout 路由
	var tokenStore token.Store
	if deps.Rdb != nil && cfg.TokenStore == "redis" {
		tokenStore = token.NewRedisStore(deps.Rdb)
	} else {
		tokenStore = token.NewMemoryStore()
	}
	tokenSvc := token.NewService(tokenStore)

	if deps.CheckoutTmpl != nil && deps.AcquirerGateway != nil {
		var orderProvider checkout.OrderProvider
		switch cfg.CheckoutOrderProvider {
		case "mock":
			orderProvider = checkout.NewMockOrderProvider()
		case "disabled":
			// 不注�?checkout 路由
		case "http":
			orderProvider = checkout.NewHTTPOrderProvider(cfg.CheckoutOrderAPIURL)
		}

		if orderProvider != nil && cfg.AppEnv != "production" {
			// SEC-002: 旧匿名 Checkout 绕过统一支付生命周期（无 attempt/reservation/
			// 统一账本收敛），production 一律不注册；生产唯一路径是 v3
			// Payment Intent + Hosted Checkout（/api/v3/*）。此路由仅供 local demo。
			checkoutH := checkout.NewHandler(tokenSvc, orderProvider, deps.AcquirerGateway, deps.RiskGateway, deps.CheckoutTmpl)
			r.Get("/checkout", checkoutH.ServeCheckoutPage)
			r.Route("/api/checkout", func(r chi.Router) {
				r.Post("/token", checkoutH.HandleCreateToken)
				r.Get("/validate", checkoutH.HandleValidateToken)
				r.Post("/charge", checkoutH.HandleCharge)
			})
		}
	}

	// MerchantVerifier 解析
	merchantVerifier := deps.MerchantVerifier
	if merchantVerifier == nil && deps.Pool != nil {
		merchantVerifier = middleware.NewDBMerchantVerifier(deps.Pool)
	}

	rateLimiter := deps.RateLimiter
	if rateLimiter == nil && deps.Rdb != nil {
		// G14: 金融入口生产默认 failClosed=true；Redis 故障返回 503 RATE_LIMITER_FAIL_CLOSED�?
		rateLimiter = ratelimit.NewRedisBucket(deps.Rdb, cfg.RateLimitFailClosed)
	}

	// P0-2: 构�?checkoutService 时注�?failClosedSvc，确保支付入口经�?AdmissionCheck�?
	checkoutService := checkout.NewServiceWithFailClosed(deps.Pool, deps.AcquirerGateway, ledgerSvc, deps.RiskGateway, failClosedSvc)

	// P0-3: tokenEncryptor 已在上方提前构造，注入�?CheckoutService�?
	checkoutService.SetTokenEncryptor(tokenEncryptor)

	// P0-1: production/real 模式�?ChannelRegistry 缺失必须启动失败（fail-closed）�?
	isRealModeForRegistry := cfg.AcquirerMode == "real" || cfg.AppEnv == "production"
	if isRealModeForRegistry && deps.AcquirerReg == nil {
		log.Error("channel registry required in real/production mode, fail-closed")
		panic("channel registry required in real/production mode (R11)")
	}

	// P0-1: 注入 ChannelRegistry �?CheckoutService，确�?CreateSession �?ProcessPaymentIntent 能调�?SelectChannel�?
	checkoutService.SetChannelRegistry(deps.AcquirerReg)

	// P1: 注入支付池选路器（shadow 只记录决策，enforce 生效）
	if cfg.PoolMode != "off" {
		checkoutService.SetPoolSelector(poolSvc)
	}

	checkoutSessionHandler := checkout.NewSessionHandler(checkoutService, cfg.CheckoutPublicBaseURL)
	paymentIntentHandler := checkout.NewPaymentIntentHandler(checkoutService)

	// G10: 三入口统一 resolver handler
	// P0-C2: paymentLinkSvc 已在上方提前构造（与 Encryptor），Admin 和公开 Handler 共享同一实例。
	checkoutResolverHandler := checkout.NewCheckoutResolverHandler(checkoutService, paymentLinkSvc)
	// PAY-003: 注入 HTML 状态页模板（nil = 仅 JSON）
	checkoutResolverHandler.SetStatusTmpl(deps.StatusTmpl)

	// G10: 注册 GET /checkout/v3/{session_id} �?GET /pay/{token}
	r.Get("/checkout/v3/{session_id}", checkoutResolverHandler.ResolveSession)
	r.Get("/pay/{token}", checkoutResolverHandler.ResolvePaymentLink)
	r.Post("/pay/{token}/confirm", checkoutResolverHandler.ConfirmPaymentLink)

	if merchantVerifier != nil {
		// P0-2: 支付路由经过 AdmissionCheck 中间件�?
		// 中间件在 MerchantAuth 之后、RateLimit 之前执行�?
		// 封锁时返�?503 + SYSTEM_LOCKDOWN_FAIL_CLOSED�?
		r.Route("/api/v3/checkout", func(r chi.Router) {
			r.Use(middleware.MerchantAuth(merchantVerifier))
			r.Use(middleware.AdmissionCheck(failClosedSvc))
			r.Post("/sessions", checkoutSessionHandler.CreateSession)
		})

		r.Route("/api/v3", func(r chi.Router) {
			r.Use(middleware.MerchantAuth(merchantVerifier))
			r.Use(middleware.AdmissionCheck(failClosedSvc))
			if rateLimiter != nil {
				r.Use(middleware.RateLimit(rateLimiter))
			}
			r.Post("/payment_intents", paymentIntentHandler.CreatePaymentIntent)
		})
	}

	// P0-3: 在组合根中先构�?PipelineService，再注入 AcquirerGateway�?
	// production/real 模式�?PipelineService 缺失必须启动失败（fail-closed）�?
	// 不允许回退�?NoopRepository（G05 fail-closed）�?
	isRealMode := cfg.AcquirerMode == "real" || cfg.AppEnv == "production"
	pipelineSvc := deps.PipelineService
	if pipelineSvc == nil {
		if deps.Pool != nil {
			pipelineRepo := pipelinerepo.NewDBRepository(deps.Pool)
			pipelineSvc = pipeline.NewService(pipelineRepo, time.Now)
		} else if isRealMode {
			// production/real 模式�?PipelineService 缺失必须启动失败
			log.Error("pipeline service required in real/production mode but pool is nil, fail-closed")
			panic("pipeline service required in real/production mode (P0-3)")
		}
	}

	// P0-3: �?PipelineService 注入 AcquirerGateway（若未注入）�?
	// 这确�?frozen/RISK_HOLD/DISABLED 隔离组能阻断实际路由�?
	if pipelineSvc != nil {
		if acqGW, ok := deps.AcquirerGateway.(*checkout.AcquirerGateway); ok {
			if acqGW != nil {
				acqGW.SetPipelineService(pipelineSvc)
			}
		}
	}

	// 引用 acquirer 包以避免未使用导入（�?checkout 禁用时）�?
	_ = acquirer.Service{}

	return r
}

// Now is exported for tests that need to override time-dependent behavior.
var Now = func() time.Time { return time.Now() }

// validateUnifiedWebhookConfig validates WEBHOOK_SIGNING_KEY requirements.
// R26 Task 1: Extracted from BuildRouter for test reuse.
// production/real: empty or short key returns error (caller must panic).
// local/mock: no validation (empty key means route not registered).
func validateUnifiedWebhookConfig(cfg *config.Config, log *slog.Logger) error {
	if cfg.AppEnv != "production" && cfg.ExternalMode != "real" {
		return nil
	}
	if cfg.WebhookSigningKey == "" {
		log.Error("webhook: production/real mode requires WEBHOOK_SIGNING_KEY, fail-closed")
		return fmt.Errorf("webhook: production/real mode requires WEBHOOK_SIGNING_KEY (R25 A2)")
	}
	if len(cfg.WebhookSigningKey) < 32 {
		log.Error("webhook: production/real mode requires WEBHOOK_SIGNING_KEY >= 32 bytes, fail-closed")
		return fmt.Errorf("webhook: production/real mode requires WEBHOOK_SIGNING_KEY >= 32 bytes (R25 A2)")
	}
	return nil
}

// mountUnifiedWebhookRoute constructs the PayloadEncryptor and registers the
// POST /webhook/unified/{provider_type}/{provider_id} route on r.
// R26 Task 1: Extracted from BuildRouter for test reuse.
// Returns error on configuration failure (caller must panic).
func mountUnifiedWebhookRoute(r chi.Router, deps Deps, cfg *config.Config, log *slog.Logger, ledgerSvc checkout.LedgerConvergence) error {
	var webhookEncryptor checkout.PayloadEncryptor
	if cfg.AppEnv == "production" {
		if deps.ExternalDeps == nil || deps.ExternalDeps.KMS == nil {
			log.Error("webhook: production mode requires KMS-backed PayloadEncryptor, fail-closed")
			return fmt.Errorf("webhook: production mode requires KMS-backed PayloadEncryptor (B7)")
		}
		stableKMS, stableOK := deps.ExternalDeps.KMS.(crypto.StableDEKSource)
		if !stableOK {
			log.Error("webhook: KMS does not support stable DEK derivation, fail-closed " +
				"(webhook-payload DEK requires deterministic KMS backend)")
			return fmt.Errorf("webhook: KMS does not support stable DEK derivation (B7/SEC-001)")
		}
		dek, kmsErr := stableKMS.StableDEK(context.Background(), "webhook-payload")
		if kmsErr != nil {
			log.Error("webhook: KMS DEK derivation failed, fail-closed", "err", kmsErr)
			return fmt.Errorf("webhook: KMS DEK derivation failed (B7)")
		}
		if len(dek) < 32 {
			log.Error("webhook: KMS DEK too short, fail-closed")
			return fmt.Errorf("webhook: KMS DEK too short (B7)")
		}
		enc, aesErr := checkout.NewAESGCMPayloadEncryptor(dek[:32])
		if aesErr != nil {
			log.Error("webhook: AES-GCM encryptor creation failed, fail-closed", "err", aesErr)
			return fmt.Errorf("webhook: AES-GCM encryptor creation failed (B7)")
		}
		webhookEncryptor = enc
	} else {
		payloadKey := cfg.WebhookPayloadKeyBase64
		if payloadKey == "" {
			payloadKey = cfg.MasterKeyBase64
		}
		if payloadKey != "" {
			keyBytes, decodeErr := base64.StdEncoding.DecodeString(payloadKey)
			if decodeErr == nil && len(keyBytes) >= 32 {
				enc, aesErr := checkout.NewAESGCMPayloadEncryptor(keyBytes[:32])
				if aesErr == nil {
					webhookEncryptor = enc
				}
			}
		}
		if webhookEncryptor == nil {
			log.Error("webhook: no payload encryption key configured, fail-closed (NoopPayloadEncryptor is forbidden)")
			return fmt.Errorf("webhook: no payload encryption key configured (P1-1: NoopPayloadEncryptor is forbidden)")
		}
	}

	// SEC-007: 生产使用 per-provider 验签密钥（provider_credential_vault），
	// 不再全 Provider 共享 cfg.WebhookSigningKey。
	// SEC-010: 注入 ledgerSvc，webhook 消费时单事务收敛 attempt + capture/release。
	allowGlobalFallback := cfg.AppEnv != "production"
	var unifiedWebhookReceiver *checkout.UnifiedWebhookReceiver
	if !allowGlobalFallback && deps.Enc != nil {
		resolver := checkout.NewDBSigningKeyResolver(deps.Pool, deps.Enc)
		unifiedWebhookReceiver = checkout.NewUnifiedWebhookReceiverSecure(
			deps.Pool, resolver, false, []byte(cfg.WebhookSigningKey), webhookEncryptor, ledgerSvc, log,
		)
	} else {
		// local/test：全局密钥兜底保持本地 Mock Provider 流程兼容；
		// 若 Encryptor 可用仍升级为 per-provider 解析 + 账本收敛。
		unifiedWebhookReceiver = checkout.NewUnifiedWebhookReceiverWithEncryptor(
			deps.Pool, []byte(cfg.WebhookSigningKey), webhookEncryptor, log,
		)
		if deps.Enc != nil {
			unifiedWebhookReceiver.UseKeyResolver(checkout.NewDBSigningKeyResolver(deps.Pool, deps.Enc), ledgerSvc)
		}
	}
	registerUnifiedWebhookRoute(r, unifiedWebhookReceiver.HandleWebhook)
	return nil
}

// registerUnifiedWebhookRoute registers the POST /webhook/unified/{provider_type}/{provider_id}
// route on r with the given handler.
// R26 Task 1: Extracted for test reuse — tests can inject a fake handler and
// verify the route exists via chi.Walk without needing a real pgxpool.Pool.
func registerUnifiedWebhookRoute(r chi.Router, handler http.HandlerFunc) {
	r.Post("/webhook/unified/{provider_type}/{provider_id}", handler)
}

// ipLoginRateLimit ADM-020: 对未鉴权的登录/会话交换路由按客户端 IP 做
// 进程内固定窗口限流（每分钟 10 次），防爆破。多节点部署由网络层限流兜底。
func ipLoginRateLimit() func(http.Handler) http.Handler {
	const window = time.Minute
	const maxHits = 10
	var mu sync.Mutex
	hits := map[string][]time.Time{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil || ip == "" {
				ip = r.RemoteAddr
			}
			now := time.Now()
			mu.Lock()
			fresh := hits[ip][:0]
			for _, t := range hits[ip] {
				if now.Sub(t) < window {
					fresh = append(fresh, t)
				}
			}
			if len(fresh) >= maxHits {
				hits[ip] = fresh
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]interface{}{"code": "RATE_LIMITED", "message": "too many requests, retry later"},
				})
				return
			}
			hits[ip] = append(fresh, now)
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}
