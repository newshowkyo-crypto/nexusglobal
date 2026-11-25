package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"os"
	"os/signal"

	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	acquireradapter "github.com/nexusglobal/mamr/internal/acquirer/adapter"
	acquirerdomain "github.com/nexusglobal/mamr/internal/acquirer/domain"
	acquirerregistry "github.com/nexusglobal/mamr/internal/acquirer/registry"
	acquirerrepo "github.com/nexusglobal/mamr/internal/acquirer/repository"
	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/fee"
	"github.com/nexusglobal/mamr/internal/issuing"
	ledgerrepo "github.com/nexusglobal/mamr/internal/ledger/repository"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	maedomain "github.com/nexusglobal/mamr/internal/mae/domain"
	maemetric "github.com/nexusglobal/mamr/internal/mae/metric"
	maerepo "github.com/nexusglobal/mamr/internal/mae/repository"
	maeservice "github.com/nexusglobal/mamr/internal/mae/service"
	merchantwebhook "github.com/nexusglobal/mamr/internal/merchantportal/webhook"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	payoutservice "github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/pipeline"
	pipelinerepo "github.com/nexusglobal/mamr/internal/pipeline/repository"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/factories"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	"github.com/nexusglobal/mamr/internal/platform/otel"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	poolpkg "github.com/nexusglobal/mamr/internal/pool"
	pooldomain "github.com/nexusglobal/mamr/internal/pool/domain"
	"github.com/nexusglobal/mamr/internal/refund"
	riskcarding "github.com/nexusglobal/mamr/internal/risk/carding"
	settlementservice "github.com/nexusglobal/mamr/internal/settlement"
	"github.com/nexusglobal/mamr/internal/split"
	"github.com/nexusglobal/mamr/internal/statement"
	"github.com/nexusglobal/mamr/internal/treasury"
	treasuryrepo "github.com/nexusglobal/mamr/internal/treasury/repository"
	"github.com/nexusglobal/mamr/internal/treasury/scheduler"
	treasuryservice "github.com/nexusglobal/mamr/internal/treasury/service"
	"github.com/nexusglobal/mamr/internal/webhook/consumer"
	webhookrepo "github.com/nexusglobal/mamr/internal/webhook/repository"
	"github.com/nexusglobal/mamr/internal/worker/fxlock"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
	paymentdispatch "github.com/nexusglobal/mamr/internal/worker/payment_dispatch"
	payoutworker "github.com/nexusglobal/mamr/internal/worker/payout"
	"github.com/nexusglobal/mamr/internal/worker/reconcile"
	"github.com/nexusglobal/mamr/internal/worker/sweepreconcile"
	vccfunding "github.com/nexusglobal/mamr/internal/worker/vcc_funding"
)

// newOutboxProcessor wraps a Registry into an outbox.EventProcessor closure.
// R26 Task 2: Extracted from main() so tests can verify the same Registry
// instance is reused across all events (not reconstructed per event).
func newOutboxProcessor(registry *outbox.Registry) func(context.Context, outbox.OutboxEvent) error {
	return func(ctx context.Context, event outbox.OutboxEvent) error {
		return registry.Process(ctx, event)
	}
}

// decodeMasterKey 从 base64 解码 master key，返回 32 字节 AES-256 key。
func decodeMasterKey(masterKeyBase64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(masterKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("decode master key base64: %w", err)
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("master key must be at least 32 bytes, got %d", len(key))
	}
	return key[:32], nil
}

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

	// G07: 构造持久化 Kill Switch Service 并从 DB 加载状态到进程内缓存。
	// Worker 也需要知道封锁状态，以便在封锁期间停止发起外部调用。
	failClosedSvc := failclosed.NewService(pool, rdb, log)
	if rec, err := failClosedSvc.LoadFromDB(ctx); err != nil {
		log.Error("failclosed: load from DB failed, fail-closed", "err", err)
		os.Exit(1)
	} else if rec.IsLocked() {
		log.Warn("failclosed: worker loaded in LOCKED state from DB; payout/sweep workers will block new flows")
	}

	// G07: 启动多节点同步订阅器
	failClosedSub := failclosed.NewSubscriber(failClosedSvc, rdb, log)
	if err := failClosedSub.Start(ctx); err != nil {
		log.Warn("failclosed: subscriber start failed, continuing without pub/sub sync", "err", err)
	}
	defer failClosedSub.Stop()

	deps, err := factories.NewExternalDeps(cfg)
	if err != nil {
		log.Error("init external deps", "err", err)
		os.Exit(1)
	}

	// 构造凭证加密器（用于 AcquirerRegistry 解密通道凭证）
	var enc *crypto.Encryptor
	if deps.KMS != nil {
		enc = crypto.NewEncryptor(deps.KMS)
	}

	txm := postgres.NewTxManager(pool)
	repo := payoutrepo.New(pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(pool)

	// 初始化费用摊销服务
	feeRepo := ledgerrepo.NewFeeRepo(pool)
	feeService := ledgerservice.NewFeeService(feeRepo)

	fxlockWorker := fxlock.New(repo, deps.LP, feeService, txm, pool, log)
	payoutWorker := payoutworker.New(repo, ledgerRepo, deps.LP, feeService, txm, pool, log)

	// P0-2: 注入 Fail-Closed 服务到 payout/fxlock worker，
	// 确保 LP dispatch 前经过 ExecutionCheck（DB 强一致）。
	fxlockWorker.SetFailClosedService(failClosedSvc)
	payoutWorker.SetFailClosedService(failClosedSvc)

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

	// SEC-013: Sweep 对账 Worker —— 查询银行真实状态后写终态，禁止同步假 Confirmed。
	sweepReconcileWorker := sweepreconcile.New(treasuryRepoInst, deps.Bank, log)

	// P0-7: 构造专用 Provider 冻结同步服务（Task A）。
	// 不再使用完整 issuing.Service 和 NoopFraudGuard。
	// ProviderCardFreezeService 只依赖 CardRepo、Registry、Vault、Factory、SyncRepo。
	issuingCardRepo := issuing.NewDBCardRepository(pool)
	issuingProviderRepo := issuing.NewDBProviderRepository(pool)
	issuingRegistry := issuing.NewChannelRegistry(issuingProviderRepo)

	// Task E: 从 DB 加载 ACTIVE issuing providers 并注册到 Factory
	issuingFactory := issuing.NewHTTPAdapterFactory()

	// R20 P0-2: 使用 ResolveRuntimeMode pure resolver 替代手动判断。
	// 未知值直接报错，禁止默认落入 LocalMock。
	issuingRuntimeMode, modeErr := issuing.ResolveRuntimeMode(cfg.AppEnv, cfg.ExternalMode)
	if modeErr != nil {
		log.Error("issuing: invalid runtime mode configuration, fail-closed", "err", modeErr)
		os.Exit(1)
	}
	isProduction := issuingRuntimeMode.IsProduction()
	isLocalMock := !isProduction

	if isProduction && enc == nil {
		log.Error("issuing: production/real mode requires KMS-backed Encryptor for Vault, fail-closed")
		os.Exit(1)
	}

	var issuingVaultSvc issuing.VaultService
	if isLocalMock {
		// F-13: local/mock 模式使用 MockVaultService
		issuingVaultSvc = issuing.NewMockVaultService(map[string]string{"api_key": "mock-local-credential"})
		log.Info("issuing: using MockVaultService (local/mock mode)")
	} else {
		issuingVaultSvc = issuing.NewCryptoVaultService(enc, pool)
	}

	// Task E: 从 DB 加载 ACTIVE issuing providers，通过统一组合根函数注册到 Factory。
	// F-11: 调用 BuildWorkerIssuingRuntime（与测试使用同一函数）。
	issuingProviders, loadErr := issuingProviderRepo.ListProviders(ctx, nil)
	if loadErr != nil {
		log.Error("issuing: failed to load providers from DB, fail-closed", "err", loadErr)
		os.Exit(1)
	}

	// R20 P0-3: 创建 OwnershipChecker 用于 Worker 启动 DB 预检
	var ownershipChecker issuing.OwnershipChecker
	if isProduction {
		ownershipChecker = issuing.NewDBOwnershipChecker(pool)
	}

	activeProviderCount, regErr := issuing.BuildWorkerIssuingRuntime(issuingFactory, issuingProviders, issuingRuntimeMode, ownershipChecker)
	if regErr != nil {
		log.Error("issuing: failed to register providers to factory, fail-closed", "err", regErr)
		os.Exit(1)
	}

	// R18 P0-5: MOCK_LOCAL 注册由 BuildWorkerIssuingRuntime 统一处理。
	// 删除 main.go 中固定代码 "MOCK_LOCAL" 的旁路注册。
	// local/mock 下，每个 adapter_type=MOCK_LOCAL 的 Provider 按 provider_code 注册到 Factory。

	// Task E: production/real 下 Factory 未注册时启动失败
	if isProduction {
		if activeProviderCount == 0 {
			log.Error("issuing: production/real mode has no ACTIVE issuing providers, fail-closed")
			os.Exit(1)
		}
		if vErr := issuingFactory.Validate(); vErr != nil {
			log.Error("issuing: production/real mode adapter factory validation failed, fail-closed", "err", vErr)
			os.Exit(1)
		}
	}

	// 刷新 Registry 缓存
	if err := issuingRegistry.Refresh(ctx); err != nil {
		log.Error("issuing: failed to refresh provider registry, fail-closed", "err", err)
		os.Exit(1)
	}

	// Task B: 构造 Provider 冻结同步状态机仓库
	issuingSyncRepo := issuing.NewDBProviderFreezeSyncRepository(pool)

	// Task A: 构造专用 Provider 冻结同步服务
	providerFreezeSvc := issuing.NewProviderCardFreezeService(
		issuingCardRepo,
		issuingRegistry,
		issuingVaultSvc,
		issuingFactory,
		issuingSyncRepo,
		log,
	)

	log.Info("issuing: provider freeze sync service initialized",
		"active_providers", activeProviderCount,
		"registered_codes", issuingFactory.RegisteredCodes(),
	)

	// R25 B: 在 outboxProcessor 闭包外部构造 Registry，确保启动时只构造一次。
	// 全部 MustRegister 在 Worker 启动前完成，outboxProcessor 只调用 registry.Process。
	registry := outbox.NewRegistry(log)

	// ── 支付分发事件 ──
	// FX_LOCK_REQUESTED: 调用 LP FXLock
	registry.MustRegister(outbox.EventFXLockRequested, func(ctx context.Context, event outbox.OutboxEvent) error {
		return fxlockWorker.Handle(ctx, event)
	})
	// LP_PAYOUT_REQUESTED: 调用 LP Payout
	registry.MustRegister(outbox.EventLPPayoutRequested, func(ctx context.Context, event outbox.OutboxEvent) error {
		return payoutWorker.Handle(ctx, event)
	})
	// SWEEP_RECONCILE_REQUESTED: SEC-013 —— 查询银行核对 Sweep 转账真实状态。
	// completed → Confirmed；failed → Failed；pending/unknown 返回 error 退避重试（RED-03）。
	registry.MustRegister(outbox.EventSweepReconcileRequested, sweepReconcileWorker.Handle)

	// ── 系统控制事件 ──
	// SYSTEM_LOCKDOWN_PUBLISH: publish 封锁事件到 Redis（通知其他节点）
	registry.MustRegister(outbox.EventSystemLockdownPublish, func(ctx context.Context, event outbox.OutboxEvent) error {
		return failClosedSvc.PublishLockdownEventFromOutbox(ctx, event.Payload)
	})
	// SYSTEM_RECOVERY_PUBLISH: publish 解除事件到 Redis
	registry.MustRegister(outbox.EventSystemRecoveryPublish, func(ctx context.Context, event outbox.OutboxEvent) error {
		return failClosedSvc.PublishRecoveryEventFromOutbox(ctx, event.Payload)
	})

	// ── 卡片冻结事件（SuspendActiveCards 触发）──
	// P0-7: CARD_FROZEN_SUSPEND 调用 ProviderCardFreezeService.SyncProviderFreeze 同步 Provider 侧冻结。
	// 超时/不确定结果（ErrUnknownExternalState）返回可重试错误，绝不能 return nil ACK。
	// 普通失败返回 error 进入重试/DLQ。成功 return nil ACK。
	// 已 SUCCEEDED 重放由持久化状态机返回成功，不再次调用 Provider。
	cardFrozenHandler := outbox.NewCardFrozenSuspendHandler(providerFreezeSvc, log)
	registry.MustRegister(outbox.EventCardFrozenSuspend, cardFrozenHandler.Handle)

	// ── 账本事件 ──
	// B5: Ledger 通知事件明确分类为审计事件（无下游消费者），返回 nil ACK。
	// 这些事件不是资金事件，仅用于通知下游（如风控、报表）。
	// 当前无下游消费者，明确标记为审计 ACK（不是 no-op，而是有意为之的审计通知）。
	ledgerAuditAckHandler := func(ctx context.Context, event outbox.OutboxEvent) error {
		log.Debug("ledger audit event acknowledged (no downstream consumer, audit-only)",
			"event_id", event.ID,
			"event_type", event.EventType,
		)
		return nil
	}
	registry.MustRegister(outbox.EventLedgerTransactionCreated, ledgerAuditAckHandler)
	registry.MustRegister(outbox.EventLedgerReservationCreated, ledgerAuditAckHandler)
	registry.MustRegister(outbox.EventLedgerReservationCaptured, ledgerAuditAckHandler)
	registry.MustRegister(outbox.EventLedgerReservationReleased, ledgerAuditAckHandler)

	// ── 配置重载事件 ──
	// CONFIG_RELOAD/ACQUIRER_CHANNEL_RELOAD/ISSUING_POOL_RELOAD:
	// publish 到 Redis 配置重载频道，通知 API 节点刷新内存缓存。
	// Redis 发布失败返回 error（不 ACK），触发重试。
	configReloadHandler := func(channel string) outbox.Handler {
		return func(ctx context.Context, event outbox.OutboxEvent) error {
			if rdb == nil {
				return errors.New("redis unavailable, cannot publish config reload")
			}
			if err := rdb.Publish(ctx, channel, string(event.Payload)).Err(); err != nil {
				return fmt.Errorf("publish config reload to %s: %w", channel, err)
			}
			log.Debug("config reload published",
				"event_id", event.ID,
				"event_type", event.EventType,
				"channel", channel,
			)
			return nil
		}
	}
	registry.MustRegister(outbox.EventConfigReload, configReloadHandler("config:reload"))
	registry.MustRegister(outbox.EventAcquirerChannelReload, configReloadHandler("config:acquirer_channel:reload"))
	registry.MustRegister(outbox.EventIssuingPoolReload, configReloadHandler("config:issuing_pool:reload"))

	// ── 配置变更事件（admin approval 触发）──
	// 这些事件由 admin approval service 写入 outbox，需 publish 到 Redis 通知 API 节点。
	// 每个 EventType 对应一个 Redis 频道。
	configChangeHandler := func(channel string) outbox.Handler {
		return func(ctx context.Context, event outbox.OutboxEvent) error {
			if rdb == nil {
				return errors.New("redis unavailable, cannot publish config change")
			}
			if err := rdb.Publish(ctx, channel, string(event.Payload)).Err(); err != nil {
				return fmt.Errorf("publish config change to %s: %w", channel, err)
			}
			return nil
		}
	}
	registry.MustRegister(outbox.EventAcquirerChannelStatusChanged, configChangeHandler("channel.config.updated"))
	registry.MustRegister(outbox.EventAcquirerChannelConfigUpdated, configChangeHandler("config:acquirer_channel:reload"))
	registry.MustRegister(outbox.EventIssuingPoolStatusChanged, configChangeHandler("issuing_pool.status_changed"))
	registry.MustRegister(outbox.EventIssuingPoolConfigUpdated, configChangeHandler("config:issuing_pool:reload"))
	registry.MustRegister(outbox.EventMerchantReserveUpdated, configChangeHandler("config:merchant:reload"))
	registry.MustRegister(outbox.EventWalletPolicyDisabled, configChangeHandler("config:wallet_policy:reload"))
	registry.MustRegister(outbox.EventVAAccountDisabled, configChangeHandler("config:va_account:reload"))
	registry.MustRegister(outbox.EventVAAccountReleased, configChangeHandler("config:va_account:reload"))
	registry.MustRegister(outbox.EventChannelRiskUpdated, configChangeHandler("config:acquirer_channel:reload"))
	registry.MustRegister(outbox.EventSystemRecoveryExecuted, configChangeHandler("config:system:reload"))
	registry.MustRegister(outbox.EventAdapterChannelDisabled, configChangeHandler("config:adapter_channel:reload"))
	registry.MustRegister(outbox.EventAdapterChannelEnabled, configChangeHandler("config:adapter_channel:reload"))
	registry.MustRegister(outbox.EventBridgeProfileDisabled, configChangeHandler("config:bridge_profile:reload"))
	registry.MustRegister(outbox.EventBridgeProfileEnabled, configChangeHandler("config:bridge_profile:reload"))
	registry.MustRegister(outbox.EventCorporateEntityRiskHold, configChangeHandler("config:corporate_entity:reload"))
	registry.MustRegister(outbox.EventCorporateEntityRiskHoldCleared, configChangeHandler("config:corporate_entity:reload"))
	registry.MustRegister(outbox.EventCorporateEntityEnabled, configChangeHandler("config:corporate_entity:reload"))
	registry.MustRegister(outbox.EventIsolationGroupFrozen, configChangeHandler("config:isolation_group:reload"))
	registry.MustRegister(outbox.EventIsolationGroupUnfrozen, configChangeHandler("config:isolation_group:reload"))
	// ADM-009: 审批服务产生的其余事件必须可被 Worker 识别，否则进入死信
	registry.MustRegister(outbox.EventAdapterProviderUpdated, configChangeHandler("config:adapter_provider:reload"))
	registry.MustRegister(outbox.EventAdapterProviderDisabled, configChangeHandler("config:adapter_provider:reload"))
	registry.MustRegister(outbox.EventAdapterProviderEnabled, configChangeHandler("config:adapter_provider:reload"))
	registry.MustRegister(outbox.EventWalletPolicyEnabled, configChangeHandler("config:wallet_policy:reload"))
	registry.MustRegister(outbox.EventMerchantRoutingPolicyCreated, configChangeHandler("config:merchant_routing_policy:reload"))
	registry.MustRegister(outbox.EventMerchantRoutingPolicyUpdated, configChangeHandler("config:merchant_routing_policy:reload"))
	registry.MustRegister(outbox.EventPaymentLinkRevoked, configChangeHandler("config:payment_link:reload"))
	registry.MustRegister(outbox.EventCorporateEntityUpdated, configChangeHandler("config:corporate_entity:reload"))
	registry.MustRegister(outbox.EventBridgeProfileUpdated, configChangeHandler("config:bridge_profile:reload"))
	registry.MustRegister(outbox.EventWalletPolicyUpdated, configChangeHandler("config:wallet_policy:reload"))

	// ── VCC 资金事件 ──
	// B5: 删除 VCC no-op ACK handler。
	// VCC 事件由 vcc_funding worker 独立处理，使用独立的 ClaimPendingEvents（过滤 event_type）。
	// 主 outbox worker 不应拾取 VCC 事件；若被拾取，返回 error（不 ACK），进入重试/死信。
	// 一个 EventType 只能有一个 Owner：VCC 事件的 Owner 是 vcc_funding worker。
	// 不允许任何资金事件使用 no-op ACK。
	registry.MustRegister(outbox.EventVCCFundingRequested, func(ctx context.Context, event outbox.OutboxEvent) error {
		return fmt.Errorf("VCC_FUNDING_REQUESTED must be handled by vcc_funding worker, not main outbox worker")
	})
	registry.MustRegister(outbox.EventVCCCryptoBridgeRequested, func(ctx context.Context, event outbox.OutboxEvent) error {
		return fmt.Errorf("VCC_CRYPTO_BRIDGE_REQUESTED must be handled by vcc_funding worker, not main outbox worker")
	})
	// P0-3: VCC_ISSUING_FUNDING_REQUESTED 必须由 vcc_funding worker 处理，主 outbox worker 不应拾取。
	registry.MustRegister(outbox.EventVCCIssuingFundingRequested, func(ctx context.Context, event outbox.OutboxEvent) error {
		return fmt.Errorf("VCC_ISSUING_FUNDING_REQUESTED must be handled by vcc_funding worker, not main outbox worker")
	})
	// P0-3: VCC_ISSUING_FUNDING_COMPLETED 审计 handler
	// 该事件仅用于审计记录，不移动资金。写入持久审计记录后 ACK。
	registry.MustRegister(outbox.EventVCCIssuingFundingCompleted, func(ctx context.Context, event outbox.OutboxEvent) error {
		log.Info("VCC issuing funding completed (audit event)",
			"event_id", event.ID,
			"event_type", event.EventType,
		)
		// 审计事件：仅记录日志，不移动资金，不触发后续事件
		return nil
	})

	// ── PAYMENT_DISPATCH_REQUESTED ──
	// B1: 装配真实 payment_dispatch worker，注入全部依赖。
	// production/real 模式缺少 PaymentGateway、LedgerService 或 FailClosedService 时启动失败。
	// mock/local 使用确定性的 MockPaymentGateway，但仍走真实 DB、Outbox、Ledger。
	var paymentDispatchGW paymentdispatch.PaymentGateway
	var paymentDispatchRiskGW paymentdispatch.RiskGateway
	var refundGW refund.Gateway

	if cfg.AppEnv == "production" || cfg.ExternalMode == "real" || cfg.AcquirerMode == "real" {
		// production/real 模式：通过 AcquirerGateway + ChannelRegistry 构造真实 PaymentGateway。
		// 禁止使用 MockPaymentGateway 作为 production 占位。
		if deps.LP == nil {
			log.Error("payment_dispatch: production/real mode requires LP client, fail-closed")
			os.Exit(1)
		}
		// 构造 AcquirerGateway（真实模式，isReal=true）
		// ChannelRegistry 从 DB 加载通道配置，AcquirerGateway 通过 registry 获取 AcquirerClient。
		acqRepo := acquirerrepo.New(pool)

		// P0-3: 创建 DefaultAdapterFactory 并注册 provider 构造器
		factory := acquirerregistry.NewDefaultAdapterFactory(log)
		// P0-2: real/production 模式下不注册 mock constructor
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

		acqReg := acquirerregistry.NewChannelRegistry(acquirerregistry.RegistryConfig{
			Repo:       acqRepo,
			Encryptor:  enc,
			RDB:        rdb,
			Logger:     log,
			FailMode:   acquirerregistry.FailModeClosed,
			IsRealMode: true,
			Factory:    factory, // P0-3: 注入 Factory
		})
		if loadErr := acqReg.LoadAll(ctx); loadErr != nil {
			log.Error("payment_dispatch: production/real mode failed to load channel registry, fail-closed", "err", loadErr)
			os.Exit(1)
		}
		pipelineRepo := pipelinerepo.NewDBRepository(pool)
		pipelineSvc := pipeline.NewService(pipelineRepo, time.Now)
		acqGW := checkout.NewAcquirerGatewayWithFailClosed(acqReg, pipelineSvc, true, failClosedSvc)
		paymentDispatchGW = acqGW
		refundGW = acqGW
		log.Info("payment_dispatch: production/real mode using AcquirerGateway",
			"channels", len(acqReg.ListAll()))

		// P0-2: production/real 模式必须注入真实 RiskGateway，禁止 nil。
		// riskcarding.Gateway 使用 Redis 滑动窗口，production 默认 fail-closed。
		if rdb == nil {
			log.Error("payment_dispatch: production/real mode requires Redis for RiskGateway, fail-closed")
			os.Exit(1)
		}
		paymentDispatchRiskGW = riskcarding.NewGateway(riskcarding.GatewayConfig{
			RDB:      rdb,
			Logger:   log,
			FailOpen: false, // production 必须 fail-closed
		})
		log.Info("payment_dispatch: production/real mode using real RiskGateway (fail-closed)")
	} else {
		// mock/local 模式：使用确定性的 MockPaymentGateway
		paymentDispatchGW = checkout.NewMockPaymentGateway()
		refundGW = refund.MockGateway{}
		log.Info("payment_dispatch: using MockPaymentGateway (mock/local mode)")
		// mock/local 模式也注入 RiskGateway（fail-open 允许 Redis 不可用时放行）
		if rdb != nil {
			paymentDispatchRiskGW = riskcarding.NewGateway(riskcarding.GatewayConfig{
				RDB:      rdb,
				Logger:   log,
				FailOpen: true, // local/mock 允许 fail-open
			})
			log.Info("payment_dispatch: using RiskGateway (local/mock mode, fail-open)")
		}
	}
	if gw, ok := paymentDispatchRiskGW.(*riskcarding.Gateway); ok {
		gw.SetRuleStore(riskcarding.NewRuleStore(pool))
	}

	// 构造 payment_dispatch worker
	// LedgerService 使用 ledgerservice.Service（实现 CaptureTx/ReleaseTx）
	dispatchLedgerSvc := ledgerservice.New(ledgerRepo, txm, pool)

	// P0 4.5: 结算结转 Worker —— 将已捕获的在途资金 PENDING 结转为 SETTLED，
	// 并同步 merchant_treasury_ledger 投影，使 A 面收款可以用于 B 面出金。
	settlementSvc := settlementservice.New(pool, txm, dispatchLedgerSvc)
	settlementSvc.SetFeeEngine(fee.NewRepository(pool))
	settlementSvc.SetSplitEngine(split.NewRepository(pool))
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				results, err := settlementSvc.SettleAll(ctx)
				if err != nil {
					log.Error("settlement tick failed", "err", err)
					continue
				}
				if len(results) > 0 {
					log.Info("settlement tick completed", "batches", len(results))
				}
			}
		}
	}()
	// P0-2: production/real 模式下 RiskGateway 为 nil 必须启动失败。
	if cfg.AppEnv == "production" || cfg.ExternalMode == "real" || cfg.AcquirerMode == "real" {
		if paymentDispatchRiskGW == nil {
			log.Error("payment_dispatch: production/real mode requires RiskGateway, fail-closed")
			os.Exit(1)
		}
	}
	paymentDispatchWorker := paymentdispatch.NewWithRiskGateway(
		pool, paymentDispatchGW, dispatchLedgerSvc, txm, log, failClosedSvc, paymentDispatchRiskGW,
	)

	// P1: 支付池失败切换（Provider 明确拒绝时切下一候选；超时不切换）
	poolRepo := poolpkg.NewRepository(pool)
	poolSvc := poolpkg.New(poolRepo, pooldomain.ParseMode(cfg.PoolMode), log)
	if rdb != nil {
		poolSvc.SetRedis(rdb)
	}
	paymentDispatchWorker.SetPool(poolSvc)

	// A2: 商户出站回调投递 Worker
	merchantWebhookRepo := merchantwebhook.NewRepository(pool, enc)
	merchantWebhookWorker := merchantwebhook.NewWorker(merchantWebhookRepo, log)
	go merchantWebhookWorker.Start(ctx)

	// A2: 商户退款执行 Worker
	refundRepo := refund.NewRepository(pool)
	refundWorker := refund.NewWorker(pool, txm, refundRepo, refundGW, dispatchLedgerSvc, log)
	registry.MustRegister(outbox.EventRefundRequested, refundWorker.Handle)

	// A2: 退款 UNKNOWN 自动对账 Worker：以相同幂等键重放上游退款，
	// 只对账窗口内（默认 24h，对齐渠道幂等键保留期）的 UNKNOWN，超窗留给人工。
	reconcileInterval := 5 * time.Minute
	if v := os.Getenv("REFUND_RECONCILE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			reconcileInterval = d
		} else {
			log.Warn("refund_reconcile: invalid REFUND_RECONCILE_INTERVAL, using default", "value", v, "default", reconcileInterval)
		}
	}
	reconcileMaxAge := 24 * time.Hour
	if v := os.Getenv("REFUND_RECONCILE_MAX_AGE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			reconcileMaxAge = d
		} else {
			log.Warn("refund_reconcile: invalid REFUND_RECONCILE_MAX_AGE, using default", "value", v, "default", reconcileMaxAge)
		}
	}
	refundReconciler := refund.NewReconciler(refundWorker, reconcileMaxAge, log)
	go func() {
		ticker := time.NewTicker(reconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := refundReconciler.RunOnce(ctx)
				if err != nil {
					log.Error("refund reconcile tick failed", "err", err)
					continue
				}
				if n > 0 {
					log.Info("refund reconcile tick completed", "processed", n)
				}
			}
		}
	}()

	// A2: 日终对账单（启动补跑昨日 + 每日 00 点生成）
	statementRepo := statement.NewRepository(pool, txm)
	statementSvc := statement.NewService(statementRepo)
	go func() {
		generateYesterday := func() {
			now := time.Now().UTC()
			y := now.AddDate(0, 0, -1)
			day := time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, time.UTC)
			res, err := statementSvc.GenerateAll(ctx, day)
			if err != nil {
				log.Error("statement: daily generation failed", "date", day.Format("2006-01-02"), "err", err)
				return
			}
			log.Info("statement: daily generation completed",
				"date", day.Format("2006-01-02"), "statements", len(res))
		}
		generateYesterday()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if now.Hour() == 0 {
					generateYesterday()
				}
			}
		}
	}()

	// P0-3: 构造 TokenEncryptor 并注入到 payment_dispatch worker。
	// Production 使用 KMS，local/mock 使用 MASTER_KEY_BASE64。
	var tokenEnc checkout.TokenEncryptor
	if cfg.AppEnv == "production" {
		if deps.KMS == nil {
			log.Error("payment_dispatch: production mode requires KMS for TokenEncryptor, fail-closed")
			os.Exit(1)
		}
		te, teErr := checkout.NewTokenEncryptorFromKMS(ctx, deps.KMS)
		if teErr != nil {
			log.Error("payment_dispatch: KMS DEK derivation for token encryptor failed, fail-closed", "err", teErr)
			os.Exit(1)
		}
		tokenEnc = te
	} else {
		masterKey := cfg.MasterKeyBase64
		if masterKey == "" {
			log.Error("payment_dispatch: MASTER_KEY_BASE64 is empty, fail-closed")
			os.Exit(1)
		}
		te, teErr := checkout.NewTokenEncryptorFromMasterKey(masterKey)
		if teErr != nil {
			log.Error("payment_dispatch: master key invalid for token encryptor, fail-closed", "err", teErr)
			os.Exit(1)
		}
		tokenEnc = te
	}
	paymentDispatchWorker.SetTokenEncryptor(tokenEnc)
	log.Info("payment_dispatch: TokenEncryptor injected")

	registry.MustRegister(outbox.EventPaymentDispatchRequested, func(ctx context.Context, event outbox.OutboxEvent) error {
		return paymentDispatchWorker.Handle(ctx, event)
	})

	// ── WEBHOOK_EVENT_RECEIVED ──
	// B4: 接通 Webhook Outbox Consumer。
	// B7: 加密密钥独立于签名密钥，使用 WEBHOOK_PAYLOAD_KEY_BASE64 或 MASTER_KEY_BASE64。
	// handler 根据 event_id 加载 encrypted payload, 解密, 幂等推进 payment_attempt,
	// 必要时执行 Ledger Capture/Release, 成功后才 ACK。
	if cfg.WebhookSigningKey != "" {
		// B7: 构造独立的 PayloadEncryptor
		var webhookEncryptor checkout.PayloadEncryptor
		if cfg.AppEnv == "production" {
			// production: 必须通过 KMS/Envelope Encryptor
			if deps.KMS == nil {
				log.Error("webhook outbox: production mode requires KMS-backed PayloadEncryptor, fail-closed")
				os.Exit(1)
			}
			stableKMS, stableOK := deps.KMS.(crypto.StableDEKSource)
			if !stableOK {
				log.Error("webhook outbox: KMS does not support stable DEK derivation, fail-closed " +
					"(webhook-payload DEK requires deterministic KMS backend)")
				os.Exit(1)
			}
			dek, kmsErr := stableKMS.StableDEK(ctx, "webhook-payload")
			if kmsErr != nil {
				log.Error("webhook outbox: KMS DEK derivation failed, fail-closed", "err", kmsErr)
				os.Exit(1)
			}
			if len(dek) < 32 {
				log.Error("webhook outbox: KMS DEK too short, fail-closed")
				os.Exit(1)
			}
			enc, aesErr := checkout.NewAESGCMPayloadEncryptor(dek[:32])
			if aesErr != nil {
				log.Error("webhook outbox: AES-GCM encryptor creation failed, fail-closed", "err", aesErr)
				os.Exit(1)
			}
			webhookEncryptor = enc
		} else {
			// local/mock: 使用 WEBHOOK_PAYLOAD_KEY_BASE64 或 MASTER_KEY_BASE64
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
				log.Error("webhook outbox: no payload encryption key configured, fail-closed (NoopPayloadEncryptor is forbidden)")
				os.Exit(1)
			}
		}

		webhookReceiver := checkout.NewUnifiedWebhookReceiverWithEncryptor(pool, []byte(cfg.WebhookSigningKey), webhookEncryptor, log)
		webhookConsumer := checkout.NewWebhookConsumer(webhookReceiver, 10*time.Second)

		registry.MustRegister(outbox.EventWebhookReceived, func(ctx context.Context, event outbox.OutboxEvent) error {
			// 从 outbox payload 解析 event_id
			var whPayload struct {
				EventID string `json:"event_id"`
			}
			if err := json.Unmarshal(event.Payload, &whPayload); err != nil {
				return fmt.Errorf("webhook outbox: unmarshal: %w", err)
			}
			eventUUID, err := uuid.Parse(whPayload.EventID)
			if err != nil {
				return fmt.Errorf("webhook outbox: invalid event_id: %w", err)
			}
			return webhookConsumer.HandleOutboxEvent(ctx, eventUUID)
		})
		log.Info("webhook outbox consumer registered")
	} else {
		log.Warn("webhook outbox consumer not registered: WEBHOOK_SIGNING_KEY not configured")
	}

	// R25 B: 启动前验证 Registry（production/real 模式 strict=true，缺失 Handler 拒绝启动）
	isStrictMode := cfg.AppEnv == "production" || cfg.ExternalMode == "real"
	if err := outbox.ValidateOutboxRegistry(registry, isStrictMode); err != nil {
		log.Error("outbox: registry validation failed, fail-closed", "err", err)
		os.Exit(1)
	}

	outboxProcessor := newOutboxProcessor(registry)

	outboxCfg := outbox.Config{
		Concurrency:  cfg.OutboxWorkerConcurrency,
		BatchSize:    int32(cfg.OutboxClaimBatch),
		PollInterval: cfg.SweepTickInterval,
		MaxRetries:   cfg.StreamMaxDeliveries,
	}
	outboxWorker := outbox.New(repo, outboxProcessor, outboxCfg, log)
	outboxWorker.Start(ctx)
	log.Info("outbox worker started", "concurrency", cfg.OutboxWorkerConcurrency)

	if cfg.VCCFundingProvider != "disabled" {
		// P0-A2: production 没有真实 VCC Provider Adapter 时必须启动失败。
		// VCCAdapterFactory 只注册了 mock/local（本地闭环），production 禁止使用。
		if cfg.AppEnv == "production" {
			log.Error("vcc_funding worker: no real VCC provider adapter available in production (only mock/local registered), fail-closed",
				"provider", cfg.VCCFundingProvider, "capability", "VCC_FUNDING")
			os.Exit(1)
		}

		vccOutboxRepo := vccfunding.NewDBOutboxRepository(pool)

		// G15: VCC provider capability 边界。
		// 使用 VCCAdapterFactory 查找明确 adapter；未知 kind 启动报 UNSUPPORTED_CAPABILITY。
		// 严禁自动回退 mock（旧逻辑在非 mock provider 下静默回退 MockCryptoBridge + NoopLedgerService，
		// 导致账本断流且违反 G15 红线）。
		vccFactory := vccfunding.NewDefaultVCCAdapterFactory()
		vccBundle, vccErr := vccFactory.Create(cfg.VCCFundingProvider, pool, log)
		if vccErr != nil {
			// 未知 provider 或未实现 capability：fail-closed，拒绝启动。
			// 错误可能是 ErrUnsupportedCapability 或 ErrBlockedExternalCredential。
			log.Error("vcc_funding worker: provider capability not available, fail-closed",
				"provider", cfg.VCCFundingProvider, "err", vccErr)
			os.Exit(1)
		}

		// P0-A2: 构造真实 DB-backed FundingRouter（禁止 nil / Noop）。
		// 链路：vcc_funding.FundingRouter(适配器) → treasury.VCCFundingRouter → VCCFundingDBRepository
		vccFundingRepo := treasury.NewVCCFundingDBRepository(pool)
		// P7: 归集资金改动走账本（SETTLED → VCC_BRIDGE_PENDING → ISSUING_POOL），
		// 投影由 internal/settlement/projection 统一写入。
		vccFundingRepo.SetLedger(ledgerservice.New(ledgerRepo, txm, pool))
		treasuryFundingRouter := treasury.NewVCCFundingRouter(vccFundingRepo, 50)
		vccFundingRouter, vccRouterErr := treasury.NewTreasuryFundingRouterAdapter(treasuryFundingRouter, vccFundingRepo)
		if vccRouterErr != nil {
			log.Error("vcc_funding worker: funding router assembly failed, fail-closed", "err", vccRouterErr)
			os.Exit(1)
		}

		// P0-1: 构造 DB-backed IssuingFundingHandler
		// mock/local 模式使用 mockMode=true；production 没有1真实 issuing adapter 时 fail-closed
		var issuingFundingHandler vccfunding.IssuingFundingHandler
		if cfg.AppEnv == "production" {
			// production 模式：没有真实 issuing adapter，fail-closed
			log.Error("vcc_funding worker: production mode requires real IssuingFundingHandler, fail-closed")
			os.Exit(1)
		} else {
			issuingFundingHandler = vccfunding.NewDBIssuingFundingHandler(pool, log, true)
			log.Info("vcc_funding worker: DB-backed IssuingFundingHandler injected (mock/local mode)")
		}

		vccWorker := vccfunding.NewWorker(vccOutboxRepo, vccBundle.CryptoBridge, vccBundle.LedgerService, vccFundingRouter, vccfunding.NewSlogLogger(log), vccfunding.WorkerConfig{
			PollInterval:   10 * time.Second,
			BatchSize:      10,
			MaxRetries:     cfg.StreamMaxDeliveries,
			MaxSlippageBps: 50,
		}, vccfunding.WithIssuingFundingHandler(issuingFundingHandler))
		go func() {
			if err := vccWorker.Start(ctx); err != nil {
				log.Error("vcc funding worker error", "err", err)
			}
		}()
		log.Info("vcc funding worker started", "provider", cfg.VCCFundingProvider,
			"ledger_implementation", "LocalLedgerService",
			"funding_router", "treasury.TreasuryFundingRouterAdapter")
	}

	// G15: cryptopay ChainListener capability 边界。
	// repository DB 实现已就绪 (internal/cryptopay/repository/db_repository.go)。
	// 启动 listener 需要: (1) UniversalAdapterRegistry 实例 (2) 从 crypto_pay_listener_config 表加载链配置 (3) 真实链 RPC/indexer 端点。
	// 当前未接通真实链 RPC：明确标记 UNSUPPORTED_CAPABILITY，不静默跳过。
	if cfg.AppEnv == "production" {
		log.Warn("cryptopay chain listener: UNSUPPORTED_CAPABILITY in production (requires UniversalAdapterRegistry instance and real chain RPC config); chain confirmations/reorgs will not be processed",
			"capability", "ONCHAIN_CONFIRMATION_LISTENER")
	} else {
		log.Info("cryptopay chain listener not started: requires UniversalAdapterRegistry instance and real chain RPC config",
			"capability", "ONCHAIN_CONFIRMATION_LISTENER",
			"status", "UNSUPPORTED_CAPABILITY")
	}

	if rdb != nil {
		webhookRepo := webhookrepo.New(pool)

		// P0-6: production/real 模式下 KMS Encryptor 必须可用，否则启动失败（fail-closed）。
		if (cfg.AppEnv == "production" || cfg.ExternalMode == "real") && enc == nil {
			log.Error("webhook consumer: production/real mode requires KMS-backed Encryptor, fail-closed")
			os.Exit(1)
		}

		// G5: 启动 LP、MAE、Bank 三个消费者
		lpConsumer := consumer.NewLPConsumer(rdb, webhookRepo, repo, ledgerRepo, enc, payoutWorker, log)
		go func() {
			if err := lpConsumer.Start(ctx); err != nil {
				log.Error("lp consumer error", "err", err)
			}
		}()
		log.Info("lp webhook consumer started")

		maeConsumer := consumer.NewMAEConsumer(rdb, webhookRepo, repo, ledgerRepo, enc, log)
		go func() {
			if err := maeConsumer.Start(ctx); err != nil {
				log.Error("mae consumer error", "err", err)
			}
		}()
		log.Info("mae webhook consumer started")

		bankConsumer := consumer.NewBankConsumer(rdb, webhookRepo, repo, ledgerRepo, enc, log)
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
					if result.Decision == "SUBMIT" && result.TransferID != "" {
						transferID, parseErr := uuid.Parse(result.TransferID)
						if parseErr != nil {
							log.Error("sweep tick: parse transfer id", "err", parseErr)
							continue
						}
						if submitErr := treasurySvc.Submit(ctx, transferID); submitErr != nil {
							log.Error("sweep tick: submit transfer", "rule_id", rule.ID, "transfer_id", result.TransferID, "err", submitErr)
						} else {
							log.Info("sweep tick: submit transfer succeeded", "rule_id", rule.ID, "transfer_id", result.TransferID)
						}
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
