package checkout

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/checkout/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	pooldomain "github.com/nexusglobal/mamr/internal/pool/domain"
)

// PoolSelector 支付池选路接口（由 internal/pool.Service 实现）。
// 定义在 checkout 侧，避免 checkout 反向被 pool 包依赖。
type PoolSelector interface {
	Mode() pooldomain.Mode
	Select(ctx context.Context, req pooldomain.SelectionRequest) (*pooldomain.Decision, error)
}

type CheckoutService struct {
	pool            *pgxpool.Pool
	paymentGW       PaymentGateway
	ledgerSvc       LedgerService
	riskGateway     RiskGateway
	tokenEncryptor  TokenEncryptor            // P0-3: token 加密器，production 必须 non-nil
	channelRegistry *registry.ChannelRegistry // P0-3: 通道注册表，用于 SelectChannel
	poolSelector    PoolSelector              // P1: 支付池选路（shadow/enforce）
	// P0-2: failClosedSvc 用于 AdmissionCheck（支付入口）和 ExecutionCheck（dispatch 前）。
	// 若为 nil，则跳过封锁检查（仅用于测试场景）。
	// 生产环境必须通过 NewServiceWithFailClosed 或 SetFailClosedService 注入。
	failClosedSvc *failclosed.Service
}

// LedgerService 账本服务接口（使用 typed pgx.Tx，移除 interface{} 断言 G02）
type LedgerService interface {
	Reserve(ctx context.Context, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error)
	ReserveTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error)
}

func NewService(pool *pgxpool.Pool, paymentGW PaymentGateway, ledgerSvc LedgerService, riskGateway RiskGateway) *CheckoutService {
	return &CheckoutService{
		pool:        pool,
		paymentGW:   paymentGW,
		ledgerSvc:   ledgerSvc,
		riskGateway: riskGateway,
	}
}

// SetTokenEncryptor 注入 token 加密器（P0-3）。
// Production 模式必须注入 non-nil encryptor。
func (s *CheckoutService) SetTokenEncryptor(enc TokenEncryptor) {
	s.tokenEncryptor = enc
}

// SetChannelRegistry 注入 ChannelRegistry（P0-3）。
// 用于组合根在构造 CheckoutService 后注入 ChannelRegistry，
// 确保 CreateSession 和 ProcessPaymentIntent 能调用 SelectChannel 选择通道。
func (s *CheckoutService) SetChannelRegistry(reg *registry.ChannelRegistry) {
	s.channelRegistry = reg
}

// SetPoolSelector 注入支付池选路器（P1）。
func (s *CheckoutService) SetPoolSelector(sel PoolSelector) {
	s.poolSelector = sel
}

// resolveAcquirerChannel 返回 (acquirer_channel_id, pool_channel_id)。
//
// 规则：
//   - pool 未配置或 mode=off：走既有 SelectChannel（单通道静态绑定）。
//   - mode=shadow：调用池分配器记录决策，但实际仍走既有 SelectChannel。
//   - mode=enforce：池分配器是唯一权威；无策略/无候选 → fail-closed。
func (s *CheckoutService) resolveAcquirerChannel(ctx context.Context, merchantID uuid.UUID,
	currency string, amountMinor int64, attemptID uuid.UUID, siteID, businessLine string) (uuid.UUID, uuid.UUID, error) {
	var allowedChannels map[uuid.UUID]bool
	if siteID != "" {
		allowedChannels = map[uuid.UUID]bool{}
		var siteUUID uuid.UUID
		if err := s.pool.QueryRow(ctx, `SELECT id FROM merchant_site WHERE merchant_id=$1 AND site_code=$2 AND status='ACTIVE'`, merchantID, siteID).Scan(&siteUUID); err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: site not active: %w", err)
		}
		rows, err := s.pool.Query(ctx, `SELECT channel_id FROM site_payment_policy WHERE site_id=$1 AND merchant_id=$2 AND status='ACTIVE' AND (effective_at IS NULL OR effective_at<=now()) AND (expires_at IS NULL OR expires_at>now())`, siteUUID, merchantID)
		if err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: site policy lookup: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return uuid.Nil, uuid.Nil, err
			}
			allowedChannels[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: read site payment policy: %w", err)
		}
		if len(allowedChannels) == 0 {
			return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: no active site payment policy")
		}
	}
	if s.poolSelector != nil && s.poolSelector.Mode() != pooldomain.ModeOff {
		decision, err := s.poolSelector.Select(ctx, pooldomain.SelectionRequest{
			TenantID:                merchantID,
			MerchantID:              merchantID,
			SiteID:                  siteID,
			BusinessLine:            businessLine,
			Currency:                currency,
			AmountMinor:             amountMinor,
			AttemptID:               attemptID,
			AllowedAcquirerChannels: allowedChannels,
		})
		switch {
		case err == nil && s.poolSelector.Mode() == pooldomain.ModeEnforce:
			if decision == nil || (allowedChannels != nil && !allowedChannels[decision.AcquirerChannelID]) {
				return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: pool did not select an authorized channel")
			}
			return decision.AcquirerChannelID, decision.ChannelID, nil
		case err == nil:
			// shadow：决策只记录，实际通道仍走既有逻辑
		case s.poolSelector.Mode() == pooldomain.ModeEnforce:
			return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: pool selection failed: %w", err)
		}
	}

	if s.channelRegistry == nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: channel registry not configured, fail-closed")
	}
	if allowedChannels != nil {
		candidates, err := s.channelRegistry.SelectCandidates(ctx, merchantID, currency, amountMinor)
		if err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: select authorized acquirer channel: %w", err)
		}
		for _, candidate := range candidates {
			if allowedChannels[candidate.ID] {
				return candidate.ID, uuid.Nil, nil
			}
		}
		return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: no available channel is authorized for site")
	}
	channel, err := s.channelRegistry.SelectChannel(ctx, merchantID, currency, amountMinor)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("checkout: select acquirer channel: %w", err)
	}
	return channel.ID, uuid.Nil, nil
}

// ChannelRegistry 返回注入的 ChannelRegistry（可能为 nil，用于测试）。
func (s *CheckoutService) ChannelRegistry() *registry.ChannelRegistry {
	return s.channelRegistry
}

// TokenEncryptor 返回注入的 token 加密器（可能为 nil，用于测试）。
func (s *CheckoutService) TokenEncryptor() TokenEncryptor {
	return s.tokenEncryptor
}

// NewServiceWithFailClosed 创建带 Fail-Closed 服务注入的 CheckoutService（P0-2）。
//
// 生产环境应使用此构造函数，确保支付入口经过 AdmissionCheck。
// failClosedSvc != nil 时，CreateSession/ProcessPaymentIntent/ResolveSession/ConsumePaymentLink
// 会在处理请求最开始调用 AdmissionCheck(ctx)，封锁时返回 ErrSystemLockdown。
func NewServiceWithFailClosed(pool *pgxpool.Pool, paymentGW PaymentGateway, ledgerSvc LedgerService, riskGateway RiskGateway, failClosedSvc *failclosed.Service) *CheckoutService {
	return &CheckoutService{
		pool:          pool,
		paymentGW:     paymentGW,
		ledgerSvc:     ledgerSvc,
		riskGateway:   riskGateway,
		failClosedSvc: failClosedSvc,
	}
}

// SetFailClosedService 注入 Fail-Closed 服务（用于已构造的 CheckoutService）。
// 允许 router 在构造后注入，避免循环依赖。
func (s *CheckoutService) SetFailClosedService(svc *failclosed.Service) {
	s.failClosedSvc = svc
}

// FailClosedService 返回注入的 Fail-Closed 服务（可能为 nil）。
func (s *CheckoutService) FailClosedService() *failclosed.Service {
	return s.failClosedSvc
}

// admissionCheck 是支付入口的统一封锁检查（P0-2）。
// 若 failClosedSvc 为 nil（测试场景），跳过检查。
// 否则调用 AdmissionCheck(ctx)，封锁时返回 ErrSystemLockdown。
func (s *CheckoutService) admissionCheck(ctx context.Context) error {
	if s.failClosedSvc == nil {
		return nil
	}
	return s.failClosedSvc.AdmissionCheck(ctx)
}

// executionCheck 是 Provider dispatch 前的统一封锁检查（P0-2）。
// 若 failClosedSvc 为 nil（测试场景），跳过检查。
// 否则调用 ExecutionCheck(ctx)，封锁或 DB 错误时返回 ErrSystemLockdown。
func (s *CheckoutService) executionCheck(ctx context.Context) error {
	if s.failClosedSvc == nil {
		return nil
	}
	return s.failClosedSvc.ExecutionCheck(ctx)
}

// dbExecutor 由 *pgxpool.Pool 与 pgx.Tx 同时满足，允许同一写路径在
// 事务内外复用（PAY-004：Hosted Session 创建必须单事务）。
type dbExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *CheckoutService) CreateCheckoutOrder(ctx context.Context, req CreateOrderRequest) (*domain.CheckoutOrder, error) {
	var order *domain.CheckoutOrder
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		order, err = createOrderInTx(ctx, tx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// withTx 在独立事务中执行 fn；CheckoutService 没有注入 TxManager，这里直接
// 用 pool 管理 BEGIN/COMMIT，回调内只做 DB 操作（RED-01）。
func (s *CheckoutService) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("checkout: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func createOrderInTx(ctx context.Context, exec dbExecutor, req CreateOrderRequest) (*domain.CheckoutOrder, error) {
	order := &domain.CheckoutOrder{
		ID:              uuid.New(),
		MerchantOrderID: req.MerchantOrderID,
		MerchantID:      req.MerchantID,
		AmountMinor:     req.AmountMinor,
		Currency:        req.Currency,
		Status:          domain.OrderStatusPending,
		SuccessURL:      req.SuccessURL,
		CancelURL:       req.CancelURL,
		Customer:        req.Customer,
		Metadata:        req.Metadata,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	_, err := exec.Exec(ctx, `
		INSERT INTO checkout_orders (id, tenant_id, merchant_id, merchant_order_id, amount_minor, currency, status, success_url, cancel_url, customer, metadata, site_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, order.ID, req.MerchantID, order.MerchantID, order.MerchantOrderID, order.AmountMinor, order.Currency,
		order.Status, order.SuccessURL, order.CancelURL, req.Customer, req.Metadata, req.SiteID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	return order, nil
}

type CreateOrderRequest struct {
	MerchantOrderID string
	MerchantID      uuid.UUID
	AmountMinor     int64
	Currency        string
	SuccessURL      string
	CancelURL       string
	Customer        *domain.CustomerInfo
	SiteID          string
	Metadata        map[string]string
}

func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("checkout: generate session id: %w", err)
	}
	return hex.EncodeToString(b)[:16], nil
}

func hashPayload(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func isValidHTTPS(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https"
}

func (s *CheckoutService) CreateSession(ctx context.Context, req CreateSessionRequest) (*domain.CheckoutSession, error) {
	// P0-2: 支付入口封锁检查。封锁时返回 ErrSystemLockdown，HTTP 503。
	if err := s.admissionCheck(ctx); err != nil {
		return nil, err
	}

	if !isValidHTTPS(req.SuccessURL) || !isValidHTTPS(req.CancelURL) {
		return nil, apperr.New("INVALID_URL", 400)
	}

	// PAY-002: 回跳 URL 必须命中商户 allowlist（fail-closed）
	if err := ensureReturnURLsAllowed(ctx, s.pool, req.MerchantID, req.SuccessURL, req.CancelURL); err != nil {
		return nil, err
	}

	// P1: 先分配 attempt_id，便于支付池写租约（失败切换需要候选顺序）
	attemptID := uuid.New()

	// P0-3/P1: 选路在任何 INSERT 之前完成，确保失败时不产生孤儿数据。
	acquirerChannelID, poolChannelID, err := s.resolveAcquirerChannel(
		ctx, req.MerchantID, req.Currency, req.AmountMinor, attemptID, req.SiteID, req.BusinessLine)
	if err != nil {
		return nil, err
	}

	orderReq := CreateOrderRequest{
		MerchantOrderID: req.MerchantOrderID,
		MerchantID:      req.MerchantID,
		AmountMinor:     req.AmountMinor,
		Currency:        req.Currency,
		SuccessURL:      req.SuccessURL,
		CancelURL:       req.CancelURL,
		Customer:        req.Customer,
		Metadata:        req.Metadata,
		SiteID:          req.SiteID,
	}

	sessionID, err := generateSessionID()
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	checkoutURL := fmt.Sprintf("%s/checkout/v3/%s", req.BaseURL, sessionID)

	session := &domain.CheckoutSession{
		ID:          uuid.New(),
		SessionID:   sessionID,
		CheckoutURL: checkoutURL,
		Status:      domain.SessionStatusActive,
		ExpiresAt:   time.Now().Add(24 * time.Hour),
		CreatedAt:   time.Now(),
	}

	// G10: 创建统一 payment_attempt（Hosted 入口）
	tenantID := req.MerchantID // 简化：tenant = merchant

	// PAY-004: order / attempt / session 必须在同一本地事务中建立。
	// 旧实现三步独立提交，任一步失败留下孤儿 order/attempt/session。
	attemptRepo := NewPaymentAttemptRepository(s.pool)
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		order, err := createOrderInTx(ctx, tx, orderReq)
		if err != nil {
			return err
		}
		session.OrderID = order.ID

		if err := attemptRepo.CreatePaymentAttemptTx(ctx, tx, &PaymentAttempt{
			ID:                attemptID,
			OrderID:           order.ID,
			MerchantID:        req.MerchantID,
			TenantID:          tenantID,
			AcquirerChannelID: acquirerChannelID,
			PoolChannelID:     poolChannelID,
			EntryChannel:      EntryHosted,
			EntryRef:          sessionID,
			MerchantOrderID:   req.MerchantOrderID,
			AmountMinor:       req.AmountMinor,
			Currency:          req.Currency,
			Status:            AttemptRequiresAction,
			Customer:          req.Customer,
			Metadata:          req.Metadata,
		}); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO checkout_sessions (id, session_id, merchant_id, order_id, amount_minor, currency, success_url, cancel_url, checkout_url, status, expires_at, metadata, payment_attempt_id, tenant_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		`, session.ID, session.SessionID, req.MerchantID, order.ID, req.AmountMinor, req.Currency,
			req.SuccessURL, req.CancelURL, session.CheckoutURL, session.Status, session.ExpiresAt, req.Metadata,
			attemptID, tenantID)
		return err
	})
	if err != nil {
		return nil, err
	}

	return session, nil
}

type CreateSessionRequest struct {
	MerchantOrderID string
	MerchantID      uuid.UUID
	AmountMinor     int64
	Currency        string
	SuccessURL      string
	CancelURL       string
	Customer        *domain.CustomerInfo
	Metadata        map[string]string
	BaseURL         string
	SiteID          string
	BusinessLine    string
}
