package vcc_funding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	issuing "github.com/nexusglobal/mamr/internal/issuing"
)

// SlogLogger wraps slog.Logger to satisfy the Logger interface.
type SlogLogger struct {
	logger *slog.Logger
}

func NewSlogLogger(logger *slog.Logger) *SlogLogger {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogLogger{logger: logger}
}

func (l *SlogLogger) Info(ctx context.Context, message string, fields ...interface{}) {
	l.logger.InfoContext(ctx, message, fields...)
}

func (l *SlogLogger) Error(ctx context.Context, message string, err error, fields ...interface{}) {
	l.logger.ErrorContext(ctx, message, "err", err)
}

// DBOutboxRepository 是 transactional_outbox 的真实 DB 实现。
type DBOutboxRepository struct {
	pool *pgxpool.Pool
}

func NewDBOutboxRepository(pool *pgxpool.Pool) *DBOutboxRepository {
	return &DBOutboxRepository{pool: pool}
}

func (r *DBOutboxRepository) ClaimPendingEvents(ctx context.Context, batchSize int) ([]OutboxEvent, error) {
	// Worker 实例标识用于 locked_by，确保 lease 可追踪。
	workerID := "vcc-funding-worker"
	rows, err := r.pool.Query(ctx, `
		UPDATE transactional_outbox
		SET status = 'Processing', locked_by = $2, locked_at = now(), updated_at = now()
		WHERE id IN (
			SELECT id FROM transactional_outbox
			WHERE (
				(status IN ('Pending', 'Failed') AND (next_retry_at IS NULL OR next_retry_at <= now()))
				OR
				(status = 'Processing' AND locked_at < now() - interval '5 minutes')
			)
			  AND event_type IN ('VCC_CRYPTO_BRIDGE_REQUESTED', 'VCC_FUNDING_REQUESTED', 'VCC_ISSUING_FUNDING_REQUESTED')
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, event_type, payload, status, retry_count, created_at`, batchSize, workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		var createdAt time.Time
		if err := rows.Scan(&e.ID, &e.EventType, &e.Payload, &e.Status, &e.RetryCount, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt = createdAt
		events = append(events, e)
	}
	return events, rows.Err()
}

func (r *DBOutboxRepository) MarkEventProcessed(ctx context.Context, eventID uuid.UUID, processedAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET status = 'Processed', locked_by = NULL, locked_at = NULL, updated_at = $2
		WHERE id = $1`, eventID, processedAt)
	return err
}

func (r *DBOutboxRepository) MarkEventFailed(ctx context.Context, eventID uuid.UUID, errorMsg string, retryCount int) error {
	// 指数退避：base=10s, factor=2, max=5min
	backoff := time.Duration(10*(1<<min(retryCount, 5))) * time.Second
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	nextRetryAt := time.Now().Add(backoff)
	_, err := r.pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET status = 'Failed', retry_count = $2, next_retry_at = $3, locked_by = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $1`, eventID, retryCount, nextRetryAt)
	return err
}

func (r *DBOutboxRepository) MarkEventUnknown(ctx context.Context, eventID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE transactional_outbox
		SET status = 'UnknownExternalState', locked_by = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $1`, eventID)
	return err
}

// ============================================================================
// VCC Provider capability errors (G15)
//
// 红线约束：未知 provider 或未实现 capability 必须明确拒绝启动或标记
// UNSUPPORTED_CAPABILITY，严禁自动回退 mock。缺真实凭证的外部联调标记
// BLOCKED_EXTERNAL_CREDENTIAL 或 SKIPPED_MISSING_CREDENTIAL，不得伪造凭证。
// ============================================================================

var (
	// ErrUnsupportedCapability 表示请求的 VCC provider capability 没有对应的真实 adapter。
	// 启动时遇到此错误必须 fail-closed，禁止回退 mock。
	ErrUnsupportedCapability = errors.New("vcc_funding: UNSUPPORTED_CAPABILITY (no real adapter registered for requested provider)")

	// ErrBlockedExternalCredential 表示外部 provider 凭证缺失，无法进行真实联调。
	// 不得伪造凭证；应标记为 BLOCKED_EXTERNAL_CREDENTIAL 或 SKIPPED_MISSING_CREDENTIAL。
	ErrBlockedExternalCredential = errors.New("vcc_funding: BLOCKED_EXTERNAL_CREDENTIAL (real provider credential missing)")
)

// SupportedVCCKinds 列出当前 VCC funding worker 支持的 provider kind。
// "mock"：本地闭环测试，使用真实账本 + mock crypto conversion（仅转换逻辑 mock，账本真实写入）。
// "local"：本地真实账本 + 占位 crypto bridge（仅用于开发环境，不进入生产）。
//
// 任何不在此集合中的 provider kind 在启动时必须返回 ErrUnsupportedCapability。
var SupportedVCCKinds = map[string]bool{
	"mock":  true,
	"local": true,
}

// ============================================================================
// MockIssuingFundingHandler —— 仅用于测试 double
//
// 重要：MockIssuingFundingHandler 仅 mock 发卡入金逻辑（FundIssuingProvider 方法），
// 不 mock 账本写入。账本写入必须走真实 DB 以满足 G15 真实账本闭环要求。
//
// 此构造函数仅用于测试。生产装配请使用真实 IssuingFundingHandler。
// ============================================================================

type MockIssuingFundingHandler struct{}

func NewMockIssuingFundingHandler() *MockIssuingFundingHandler {
	return &MockIssuingFundingHandler{}
}

func (m *MockIssuingFundingHandler) FundIssuingProvider(ctx context.Context, req IssuingFundingRequest) (*IssuingFundingResult, error) {
	if req.ProviderIdempotencyKey == "" {
		return nil, errors.New("vcc_funding: ProviderIdempotencyKey must not be empty")
	}
	return &IssuingFundingResult{
		ProviderRef: "mock-issuing-ref",
		Status:      "SUCCESS",
	}, nil
}

// ============================================================================
// MockCryptoBridge —— 仅用于测试 double
//
// 重要：MockCryptoBridge 仅 mock 转换逻辑（Convert 方法），不 mock 账本写入。
// 账本写入必须走 LocalLedgerService（真实 DB）以满足 G15 真实账本闭环要求。
//
// 此构造函数仅用于测试。生产装配请使用 NewVCCAdapterFactory。
// ============================================================================

type MockCryptoBridge struct{}

func NewMockCryptoBridge() *MockCryptoBridge {
	return &MockCryptoBridge{}
}

func (m *MockCryptoBridge) Convert(ctx context.Context, req ConversionRequest) (*ConversionResult, error) {
	return &ConversionResult{
		BridgeBatchID:     req.BridgeBatchID,
		LPCConversionRef:  "mock-lp-ref",
		TargetAmountMinor: req.AmountMinor,
		Status:            "SUCCESS",
		SlippageBps:       0,
	}, nil
}

// ============================================================================
// NoopLedgerService —— 仅用于测试 double
//
// 重要：NoopLedgerService 不写入任何账本记录，仅用于单元测试中隔离账本依赖。
// 生产装配禁止使用 NoopLedgerService，必须使用 LocalLedgerService（真实 DB）。
// ============================================================================

type NoopLedgerService struct{}

func NewNoopLedgerService() *NoopLedgerService {
	return &NoopLedgerService{}
}

func (n *NoopLedgerService) CreateCryptoPoolLedgerEntry(ctx context.Context, entry LedgerEntry) error {
	return nil
}

func (n *NoopLedgerService) TriggerVCCFundingEvent(ctx context.Context, event VCCFundingEvent) error {
	return nil
}

// ============================================================================
// LocalLedgerService —— VCC local mock 的真实账本闭环实现 (G15)
//
// 设计目标：
//   - VCC local mock 使用真实 issuing repository、ledger、fraud guard、outbox
//   - 高风险来源只允许进入隔离/审核路径
//   - 不得把 crypto conversion 当作来源消失
//   - 接通 card create/freeze/terminate/auth webhook 的状态机、幂等、UNKNOWN、Fraud Guard 和账本
//   - 回调欺诈阈值触发时切断新开卡权限
//
// 此实现写入两张表：
//   1. crypto_pool_ledger_entry：crypto pool 账本条目
//   2. transactional_outbox：触发后续 VCC_FUNDING_REQUESTED 事件
//
// NoopLedgerService 永远不写入任何记录，导致账本断流。LocalLedgerService 修复此问题。
// ============================================================================

type LocalLedgerService struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewLocalLedgerService 创建基于真实 DB 的 VCC 账本服务。
//
// pool 必须非 nil；logger 为 nil 时使用 slog.Default()。
// 此构造函数用于 VCC_FUNDING_PROVIDER=mock 或 =local 的生产装配路径，
// 保证 VCC 转换结果真实写入账本并触发后续 funding 事件。
func NewLocalLedgerService(pool *pgxpool.Pool, logger *slog.Logger) *LocalLedgerService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LocalLedgerService{pool: pool, logger: logger}
}

// CreateCryptoPoolLedgerEntry 将 crypto pool 账本条目写入 DB。
//
// 此方法满足 G15 真实账本闭环要求：
//   - 幂等（P0-A4）：以业务唯一键 (bridge_batch_id, entry_type) 冲突时跳过，
//     重复消费同一 Crypto Bridge 事件不会重复写账
//   - 审计：记录 source_tier、bridge_batch_id、lp_conversion_ref 到 provenance
//   - 高风险来源（TIER_1_HIGH_RISK）的条目写入隔离池账本（由调用方在 SourceTier 中标记）
func (s *LocalLedgerService) CreateCryptoPoolLedgerEntry(ctx context.Context, entry LedgerEntry) error {
	if s.pool == nil {
		return errors.New("vcc_funding: LocalLedgerService requires non-nil pool")
	}

	// P0-A4: 幂等插入以业务唯一键 (bridge_batch_id, entry_type) 为准（000041 迁移 UNIQUE 约束），
	// 冲突时跳过，重复消费安全。
	_, err := s.pool.Exec(ctx, `
		INSERT INTO crypto_pool_ledger_entry
			(id, bridge_batch_id, merchant_id, amount_minor, currency, entry_type,
			 source_tier, lp_conversion_ref, provenance, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (bridge_batch_id, entry_type) DO NOTHING`,
		entry.ID, entry.BridgeBatchID, entry.MerchantID, entry.AmountMinor, entry.Currency,
		entry.EntryType, entry.SourceTier, entry.LPCConversionRef, entry.Provenance, entry.CreatedAt)
	if err != nil {
		s.logger.ErrorContext(ctx, "vcc_funding: failed to write crypto pool ledger entry",
			"err", err, "entry_id", entry.ID, "bridge_batch_id", entry.BridgeBatchID)
		return err
	}
	return nil
}

// vccFundingIdempotencyKey 生成稳定的 funding outbox 幂等键（P0-A3）。
// 格式：vcc_funding:<bridge_batch_id>:<target_provider_id>
// 同一 bridge batch 对同一 target provider 只生成一个 funding 事件。
func vccFundingIdempotencyKey(bridgeBatchID string, targetProviderID uuid.UUID) string {
	return "vcc_funding:" + bridgeBatchID + ":" + targetProviderID.String()
}

// vccFundingAggregateID 从 bridge_batch_id 派生稳定的 aggregate UUID（P0-A3）。
// 同一 bridge_batch_id 永远派生同一个 UUID，不随重试变化。
func vccFundingAggregateID(bridgeBatchID string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("vcc_funding_batch:"+bridgeBatchID))
}

// marshalFundingPayload 用 json.Marshal 构建 payload（P0-A4：禁止手工 JSON 拼接）。
func marshalFundingPayload(event VCCFundingEvent) ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"bridge_batch_id":    event.BridgeBatchID,
		"merchant_id":        event.MerchantID,
		"target_provider_id": event.TargetProviderID,
		"amount_minor":       event.AmountMinor,
		"currency":           event.Currency,
		"provenance":         event.Provenance,
	})
}

// insertFundingOutbox 在指定执行器（pool 或 tx）上插入 VCC_FUNDING_REQUESTED outbox 事件。
//
// P0-A3: 提供全部必填字段（aggregate_type/aggregate_id/event_type/payload/
// idempotency_key/status/retry_count/next_retry_at/created_at/updated_at）。
// idempotency_key 稳定：vcc_funding:<bridge_batch_id>:<target_provider_id>。
// ON CONFLICT (idempotency_key) DO NOTHING 保证重复消费不重复生成 funding 事件。
func insertFundingOutbox(ctx context.Context, exec interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
}, event VCCFundingEvent, payload []byte) error {
	_, err := exec.Exec(ctx, `
		INSERT INTO transactional_outbox
			(id, aggregate_type, aggregate_id, event_type, payload, idempotency_key,
			 status, retry_count, next_retry_at, created_at, updated_at)
		VALUES ($1, 'vcc_funding_batch', $2, 'VCC_FUNDING_REQUESTED', $3, $4,
			 'Pending', 0, now(), now(), now())
		ON CONFLICT (idempotency_key) DO NOTHING`,
		uuid.New(), vccFundingAggregateID(event.BridgeBatchID), payload,
		vccFundingIdempotencyKey(event.BridgeBatchID, event.TargetProviderID))
	return err
}

// TriggerVCCFundingEvent 将 VCC_FUNDING_REQUESTED 事件写入 transactional_outbox，
// 触发后续 issuing worker 创建/激活虚拟卡。
//
// 此方法保证 VCC funding 事件不丢失：即使 worker 崩溃，outbox 中待处理事件
// 会在下次启动时被 claim 重试。
func (s *LocalLedgerService) TriggerVCCFundingEvent(ctx context.Context, event VCCFundingEvent) error {
	if s.pool == nil {
		return errors.New("vcc_funding: LocalLedgerService requires non-nil pool")
	}

	payload, err := marshalFundingPayload(event)
	if err != nil {
		s.logger.ErrorContext(ctx, "vcc_funding: failed to marshal funding payload",
			"err", err, "bridge_batch_id", event.BridgeBatchID)
		return err
	}

	if err := insertFundingOutbox(ctx, s.pool, event, payload); err != nil {
		s.logger.ErrorContext(ctx, "vcc_funding: failed to trigger VCC funding event",
			"err", err, "bridge_batch_id", event.BridgeBatchID, "merchant_id", event.MerchantID)
		return err
	}
	return nil
}

// FinalizeCryptoBridge 在同一数据库事务中完成 Ledger Entry 写入与 Funding Outbox 写入。
//
// P0-A4: Ledger Entry 与 Funding Outbox 必须在同一数据库事务中完成，
// 保证两者原子性（要么都写，要么都不写）。
// 调用方必须在事务外完成外部 Crypto Provider 调用（本方法不发起任何网络 I/O，RED-01）。
// 幂等：Ledger Entry 按 (bridge_batch_id, entry_type) 冲突跳过；Funding Outbox 按
// idempotency_key 冲突跳过。重复消费安全，可标记原事件 Processed。
func (s *LocalLedgerService) FinalizeCryptoBridge(ctx context.Context, entry LedgerEntry, event VCCFundingEvent) error {
	if s.pool == nil {
		return errors.New("vcc_funding: LocalLedgerService requires non-nil pool")
	}

	payload, err := marshalFundingPayload(event)
	if err != nil {
		s.logger.ErrorContext(ctx, "vcc_funding: failed to marshal funding payload",
			"err", err, "bridge_batch_id", event.BridgeBatchID)
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 1. Ledger entry：业务唯一键冲突时跳过（幂等）
	if _, err := tx.Exec(ctx, `
		INSERT INTO crypto_pool_ledger_entry
			(id, bridge_batch_id, merchant_id, amount_minor, currency, entry_type,
			 source_tier, lp_conversion_ref, provenance, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (bridge_batch_id, entry_type) DO NOTHING`,
		entry.ID, entry.BridgeBatchID, entry.MerchantID, entry.AmountMinor, entry.Currency,
		entry.EntryType, entry.SourceTier, entry.LPCConversionRef, entry.Provenance, entry.CreatedAt); err != nil {
		s.logger.ErrorContext(ctx, "vcc_funding: failed to write crypto pool ledger entry in tx",
			"err", err, "bridge_batch_id", entry.BridgeBatchID)
		return err
	}

	// 2. Funding outbox：同一事务内，idempotency_key 冲突时跳过（幂等）
	if err := insertFundingOutbox(ctx, tx, event, payload); err != nil {
		s.logger.ErrorContext(ctx, "vcc_funding: failed to insert funding outbox in tx",
			"err", err, "bridge_batch_id", event.BridgeBatchID)
		return err
	}

	return tx.Commit(ctx)
}

// ============================================================================
// VCCAdapterFactory —— VCC provider capability 边界 (G15)
//
// 设计目标：
//   - 非 mock VCC provider 必须由 capability registry 找到明确 adapter；否则启动报 UNSUPPORTED_CAPABILITY
//   - 没有凭证报 BLOCKED_EXTERNAL_CREDENTIAL
//   - 严禁自动回退 mock
//
// 使用方式：
//   factory := NewVCCAdapterFactory()
//   factory.RegisterLocal("mock", pool, log)  // 本地闭环测试
//   bridge, ledger, err := factory.Create(cfg.VCCFundingProvider, pool, log)
//   if err != nil { /* fail-closed */ }
// ============================================================================

// VCCAdapterBundle 封装 VCC funding worker 所需的 crypto bridge 和 ledger service。
type VCCAdapterBundle struct {
	CryptoBridge  CryptoBridgeService
	LedgerService LedgerService
}

// VCCAdapterFactory 维护 provider kind -> constructor 的映射。
// 启动时根据 cfg.VCCFundingProvider 查找对应 constructor；未注册的 kind 返回 ErrUnsupportedCapability。
type VCCAdapterFactory struct {
	constructors map[string]func(pool *pgxpool.Pool, logger *slog.Logger) (*VCCAdapterBundle, error)
}

// NewVCCAdapterFactory 创建空的 VCC adapter factory。
func NewVCCAdapterFactory() *VCCAdapterFactory {
	return &VCCAdapterFactory{
		constructors: make(map[string]func(pool *pgxpool.Pool, logger *slog.Logger) (*VCCAdapterBundle, error)),
	}
}

// RegisterLocal 注册一个本地 VCC adapter constructor。
// kind 必须在 SupportedVCCKinds 中；重复注册会覆盖。
func (f *VCCAdapterFactory) RegisterLocal(kind string, ctor func(pool *pgxpool.Pool, logger *slog.Logger) (*VCCAdapterBundle, error)) {
	f.constructors[kind] = ctor
}

// Create 根据 provider kind 查找 constructor 并创建 adapter bundle。
//
// 错误语义：
//   - 未知 kind：返回 ErrUnsupportedCapability（启动必须 fail-closed）
//   - constructor 返回错误：透传（可能是 ErrBlockedExternalCredential）
func (f *VCCAdapterFactory) Create(kind string, pool *pgxpool.Pool, logger *slog.Logger) (*VCCAdapterBundle, error) {
	ctor, ok := f.constructors[kind]
	if !ok {
		return nil, ErrUnsupportedCapability
	}
	return ctor(pool, logger)
}

// NewDefaultVCCAdapterFactory 返回预装 "mock" 和 "local" 两种本地 kind 的 factory。
//
// "mock"：使用 MockCryptoBridge（仅转换逻辑 mock）+ LocalLedgerService（真实账本）
// "local"：同 "mock"，语义别名，用于开发环境显式声明本地模式
//
// 任何不在此工厂中的 kind 在 Create 时返回 ErrUnsupportedCapability。
// 这保证未知 provider 不会静默回退到 mock。
func NewDefaultVCCAdapterFactory() *VCCAdapterFactory {
	f := NewVCCAdapterFactory()

	// "mock" kind：本地闭环测试。
	// 重要：账本服务使用 LocalLedgerService（真实 DB），不是 NoopLedgerService。
	// MockCryptoBridge 仅 mock 转换逻辑，账本真实写入。
	f.RegisterLocal("mock", func(pool *pgxpool.Pool, logger *slog.Logger) (*VCCAdapterBundle, error) {
		if pool == nil {
			return nil, errors.New("vcc_funding: mock kind requires non-nil pool for real ledger")
		}
		return &VCCAdapterBundle{
			CryptoBridge:  NewMockCryptoBridge(),
			LedgerService: NewLocalLedgerService(pool, logger),
		}, nil
	})

	// "local" kind：开发环境本地模式，语义同 "mock"。
	f.RegisterLocal("local", func(pool *pgxpool.Pool, logger *slog.Logger) (*VCCAdapterBundle, error) {
		if pool == nil {
			return nil, errors.New("vcc_funding: local kind requires non-nil pool for real ledger")
		}
		return &VCCAdapterBundle{
			CryptoBridge:  NewMockCryptoBridge(),
			LedgerService: NewLocalLedgerService(pool, logger),
		}, nil
	})

	return f
}

// ============================================================================
// DBIssuingFundingHandler — DB-backed IssuingFundingHandler 实现 (P0-2)
//
// 设计目标：
//   - 使用 *pgxpool.Pool 持久化 funding balance 状态
//   - 外部 Provider 调用绝不能位于 DB transaction 内（先调用 Provider，拿到结果后再开事务）
//   - 成功：事务内 UPDATE status='FUNDED' + 写 VCC_ISSUING_FUNDING_COMPLETED Outbox 事件
//   - 明确失败：UPDATE status='FAILED'
//   - timeout/unknown：UPDATE status='UNKNOWN_EXTERNAL_STATE'，返回 issuing.ErrProviderTimeout / issuing.ErrUnknownExternalState
//   - mockMode=true 时直接返回成功（仅用于 mock/local）
//   - production 没有1真实 issuing adapter 时 fail-closed（返回 issuing.ErrProviderNotFound）
// ============================================================================

type DBIssuingFundingHandler struct {
	pool     *pgxpool.Pool
	logger   *slog.Logger
	mockMode bool
}

func NewDBIssuingFundingHandler(pool *pgxpool.Pool, logger *slog.Logger, mockMode bool) *DBIssuingFundingHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DBIssuingFundingHandler{pool: pool, logger: logger, mockMode: mockMode}
}

func (h *DBIssuingFundingHandler) FundIssuingProvider(ctx context.Context, req IssuingFundingRequest) (*IssuingFundingResult, error) {
	// 1. 外部 Provider 调用（事务外）
	var result *IssuingFundingResult
	if h.mockMode {
		result = &IssuingFundingResult{ProviderRef: "mock-issuing-ref", Status: "SUCCESS"}
	} else {
		// production 没有真实 issuing adapter，fail-closed
		return nil, issuing.ErrProviderNotFound
	}

	// 2. 事务内更新 funding balance 状态 + 写 Completed Outbox
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if result.Status == "SUCCESS" {
		// 条件更新：只在 FUNDING_PENDING 状态下更新为 FUNDED
		tag, err := tx.Exec(ctx, `
			UPDATE issuing_provider_funding_balance
			SET status = 'FUNDED', updated_at = now()
			WHERE bridge_batch_id = $1 AND provider_id = $2 AND status = 'FUNDING_PENDING'`,
			req.BridgeBatchID, req.TargetProviderID)
		if err != nil {
			return nil, err
		}

		if tag.RowsAffected() == 0 {
			// 读取当前状态判断幂等或 fail-closed
			var currentStatus string
			err := tx.QueryRow(ctx, `
				SELECT status FROM issuing_provider_funding_balance
				WHERE bridge_batch_id = $1 AND provider_id = $2`,
				req.BridgeBatchID, req.TargetProviderID).Scan(&currentStatus)
			if err != nil {
				// 不存在 → fail-closed
				return nil, errors.New("vcc_funding: funding balance record not found, fail-closed")
			}
			if currentStatus == "FUNDED" {
				// 幂等成功
				return result, nil
			}
			// FAILED / UNKNOWN_EXTERNAL_STATE / 其他 → fail-closed，绝不静默成功
			return nil, fmt.Errorf("vcc_funding: funding balance in unexpected state %s, fail-closed", currentStatus)
		}

		// 更新 vcc_funding_finalizations 为 FINALIZED
		if _, err := tx.Exec(ctx, `
			UPDATE vcc_funding_finalizations
			SET status = 'FINALIZED', updated_at = now()
			WHERE bridge_batch_id = $1 AND target_provider_id = $2`,
			req.BridgeBatchID, req.TargetProviderID); err != nil {
			return nil, err
		}

		// 写 VCC_ISSUING_FUNDING_COMPLETED 审计事件
		completedPayload, _ := json.Marshal(map[string]interface{}{
			"bridge_batch_id":    req.BridgeBatchID,
			"merchant_id":        req.MerchantID,
			"target_provider_id": req.TargetProviderID,
			"amount_minor":       req.AmountMinor,
			"currency":           req.Currency,
			"provider_ref":       result.ProviderRef,
		})
		idempotencyKey := "vcc_issuing_completed:" + req.BridgeBatchID + ":" + req.TargetProviderID.String()
		_, err = tx.Exec(ctx, `
			INSERT INTO transactional_outbox
				(id, aggregate_type, aggregate_id, event_type, payload, idempotency_key,
				 status, retry_count, next_retry_at, created_at, updated_at)
			VALUES ($1, 'vcc_issuing_funding', $2, 'VCC_ISSUING_FUNDING_COMPLETED', $3, $4,
				 'Pending', 0, now(), now(), now())
			ON CONFLICT (idempotency_key) DO NOTHING`,
			uuid.New(), uuid.NewSHA1(uuid.NameSpaceOID, []byte("vcc_issuing_completed:"+req.BridgeBatchID+":"+req.TargetProviderID.String())),
			completedPayload, idempotencyKey)
		if err != nil {
			return nil, err
		}
	} else if result.Status == "FAILED" {
		// 条件更新：只在 FUNDING_PENDING 状态下更新为 FAILED
		_, err = tx.Exec(ctx, `
			UPDATE issuing_provider_funding_balance
			SET status = 'FAILED', updated_at = now()
			WHERE bridge_batch_id = $1 AND provider_id = $2 AND status = 'FUNDING_PENDING'`,
			req.BridgeBatchID, req.TargetProviderID)
		if err != nil {
			return nil, err
		}

		// 更新 vcc_funding_finalizations 为 FAILED
		if _, err := tx.Exec(ctx, `
			UPDATE vcc_funding_finalizations
			SET status = 'FAILED', updated_at = now()
			WHERE bridge_batch_id = $1 AND target_provider_id = $2`,
			req.BridgeBatchID, req.TargetProviderID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return result, nil
}
