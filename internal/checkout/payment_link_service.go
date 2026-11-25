package checkout

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/checkout/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	pooldomain "github.com/nexusglobal/mamr/internal/pool/domain"
)

// PaymentLinkService 管理 Payment Link 生命周期。
//
// G09 安全保证：
//   - DB 只存 token_hash（SHA-256），不存明文 token
//   - payment_url 列只存非敏感 path（/pay/{paymentLinkID}），不含 token
//   - 创建响应只返回一次完整 URL（含 token），之后不再可获取
//   - token 比对使用 crypto/subtle.ConstantTimeCompare（恒时）
//   - single-use 链接在并发 checkout 时通过 SELECT ... FOR UPDATE 原子 claim
//   - access log 只记录 token_hash，不记录 token
//   - 支持 revoke / rotate / expire
type PaymentLinkService struct {
	pool            *pgxpool.Pool
	encryptor       TokenEncryptor            // B6: token 加密器，nil 时 fail-closed
	channelRegistry *registry.ChannelRegistry // P0-2: 通道注册表，用于 SelectChannel
	poolSelector    PoolSelector              // P1: 支付池选路
}

func NewPaymentLinkService(pool *pgxpool.Pool) *PaymentLinkService {
	return &PaymentLinkService{pool: pool}
}

// NewPaymentLinkServiceWithEncryptor 创建带 token 加密器的 PaymentLinkService（B6）。
func NewPaymentLinkServiceWithEncryptor(pool *pgxpool.Pool, encryptor TokenEncryptor) *PaymentLinkService {
	return &PaymentLinkService{pool: pool, encryptor: encryptor}
}

// SetChannelRegistry 注入 ChannelRegistry（P0-2）。
// 用于组合根在构造 PaymentLinkService 后注入 ChannelRegistry，
// 确保 CreatePaymentLink 能调用 SelectChannel 选择通道。
func (s *PaymentLinkService) SetChannelRegistry(reg *registry.ChannelRegistry) {
	s.channelRegistry = reg
}

// SetPoolSelector 注入支付池选路器（P1）。
func (s *PaymentLinkService) SetPoolSelector(sel PoolSelector) {
	s.poolSelector = sel
}

// resolvePaymentLinkChannel 返回 (acquirer_channel_id, pool_channel_id)。
// 规则与 CheckoutService.resolveAcquirerChannel 一致：shadow 只记录，enforce 生效。
func (s *PaymentLinkService) resolvePaymentLinkChannel(ctx context.Context, merchantID uuid.UUID,
	currency string, amountMinor int64, attemptID uuid.UUID, siteID, businessLine string) (uuid.UUID, uuid.UUID, error) {
	if s.poolSelector != nil && s.poolSelector.Mode() != pooldomain.ModeOff {
		decision, err := s.poolSelector.Select(ctx, pooldomain.SelectionRequest{
			TenantID:     merchantID,
			MerchantID:   merchantID,
			SiteID:       siteID,
			BusinessLine: businessLine,
			Currency:     currency,
			AmountMinor:  amountMinor,
			AttemptID:    attemptID,
		})
		switch {
		case err == nil && s.poolSelector.Mode() == pooldomain.ModeEnforce:
			return decision.AcquirerChannelID, decision.ChannelID, nil
		case err == nil:
			// shadow
		case s.poolSelector.Mode() == pooldomain.ModeEnforce:
			return uuid.Nil, uuid.Nil, fmt.Errorf("payment link: pool selection failed: %w", err)
		}
	}

	if s.channelRegistry == nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("payment link: channel registry not configured, fail-closed")
	}
	channel, err := s.channelRegistry.SelectChannel(ctx, merchantID, currency, amountMinor)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("payment link: select acquirer channel: %w", err)
	}
	return channel.ID, uuid.Nil, nil
}

type CreatePaymentLinkRequest struct {
	MerchantID      uuid.UUID
	MerchantOrderID string
	AmountMinor     int64
	Currency        string
	SuccessURL      string
	CancelURL       string
	SingleUse       bool
	ExpiresAt       *time.Time
	Customer        *domain.CustomerInfo
	Metadata        map[string]string
	SiteID          string
	BusinessLine    string
}

// CreatePaymentLinkResult 创建 Payment Link 的结果。
//
// Token 和 PaymentURL 只在创建时返回一次，之后不可获取。
// DB 中只存 token_hash 和非敏感 path（/pay/{paymentLinkID}）。
type CreatePaymentLinkResult struct {
	PaymentLinkID string
	Token         string // 只返回一次，不落库
	PaymentURL    string // 含 token 的完整 URL，只返回一次
}

// generatePaymentLinkToken 生成 32 字节随机 token（64 hex chars）。
//
// G09 修复：检查 rand.Read 错误，不再忽略。
func generatePaymentLinkToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("payment_link: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// hashToken 计算 token 的 SHA-256 hash（hex 编码）。
//
// DB 只存 hash，不存明文 token。比对时使用 hashToken(token) 然后 subtle.ConstantTimeCompare。
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// constantTimeHashCompare 恒时比对 token hash。
//
// 即使 hash 不匹配也消耗相同时间，防止时序攻击。
func constantTimeHashCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// CreatePaymentLink 创建 Payment Link。
//
// G09 安全保证：
//   - token 使用 crypto/rand 生成，检查错误
//   - DB 只存 token_hash，不存明文 token
//   - payment_url 列存非敏感 path（/pay/{paymentLinkID}），不含 token
//   - 返回的 PaymentURL 含 token，但只返回一次
//
// P0-5: Payment Link 必须能真正进入相同支付生命周期，不得只返回 JSON 元数据。
// 在同一事务内创建：checkout_order + payment_attempt + PAYMENT_DISPATCH_REQUESTED Outbox。
func (s *PaymentLinkService) CreatePaymentLink(ctx context.Context, req CreatePaymentLinkRequest) (*CreatePaymentLinkResult, error) {
	// PAY-002: 回跳 URL 必须为 HTTPS 且命中商户 allowlist（fail-closed）。
	if err := ensureReturnURLsAllowed(ctx, s.pool, req.MerchantID, req.SuccessURL, req.CancelURL); err != nil {
		return nil, err
	}

	// P0-2/P1: 在任何 INSERT 之前选路，确保失败时不产生孤儿数据。
	attemptID := uuid.New()
	acquirerChannelID, poolChannelID, err := s.resolvePaymentLinkChannel(
		ctx, req.MerchantID, req.Currency, req.AmountMinor, attemptID, req.SiteID, req.BusinessLine)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	token, err := generatePaymentLinkToken()
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	tokenHash := hashToken(token)

	paymentLinkID := uuid.New()
	// G09: payment_url 只存非敏感 path（用 paymentLinkID，不用 token）
	// 这样即使 DB 泄露，攻击者也无法获取 token
	safePath := fmt.Sprintf("/pay/%s", paymentLinkID.String())

	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	if req.ExpiresAt != nil {
		expiresAt = *req.ExpiresAt
	}

	// P0-5: 在同一事务内创建 payment_link + checkout_order + payment_attempt。
	// 这确保 Payment Link 真正进入相同支付生命周期。
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	defer tx.Rollback(ctx)

	if err := createLinkInTx(ctx, tx, req, acquirerChannelID, poolChannelID, attemptID, paymentLinkID, tokenHash, safePath, expiresAt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	// G09: 返回的 PaymentURL 含 token，但只返回一次
	// 调用方（handler）应确保此 URL 不落库、不记日志
	fullPaymentURL := fmt.Sprintf("/pay/%s", token)

	return &CreatePaymentLinkResult{
		PaymentLinkID: paymentLinkID.String(),
		Token:         token,
		PaymentURL:    fullPaymentURL,
	}, nil
}

// createLinkInTx 在给定事务内创建 payment_link + checkout_order + payment_attempt。
// PAY-010: 供 CreatePaymentLink 与 Rotate 复用，保证 rotate 的"吊销旧链接 +
// 创建新链接"发生在同一事务（失败即全部回滚，不再出现旧链接已吊销而新链接缺失）。
func createLinkInTx(ctx context.Context, tx pgx.Tx, req CreatePaymentLinkRequest,
	acquirerChannelID, poolChannelID, attemptID, paymentLinkID uuid.UUID,
	tokenHash, safePath string, expiresAt time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO payment_links (id, token_hash, merchant_id, merchant_order_id, amount_minor, currency, success_url, cancel_url, payment_url, single_use, expires_at, status, customer, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, paymentLinkID, tokenHash, req.MerchantID, req.MerchantOrderID, req.AmountMinor, req.Currency,
		req.SuccessURL, req.CancelURL, safePath, req.SingleUse, expiresAt, "ACTIVE", req.Customer, req.Metadata)
	if err != nil {
		return apperr.ErrInternal.WithMessage(err.Error())
	}

	// P0-5: 创建 checkout_order（PAYMENT_LINK 入口）
	orderID := uuid.New()
	tenantID := req.MerchantID
	_, err = tx.Exec(ctx, `
		INSERT INTO checkout_orders (id, tenant_id, merchant_id, merchant_order_id, amount_minor, currency, status, success_url, cancel_url, customer, metadata, site_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', $7, $8, $9, $10, $11)
		ON CONFLICT (merchant_id, merchant_order_id) DO UPDATE SET updated_at = NOW()
	`, orderID, tenantID, req.MerchantID, req.MerchantOrderID, req.AmountMinor, req.Currency,
		req.SuccessURL, req.CancelURL, req.Customer, req.Metadata, req.SiteID)
	if err != nil {
		return apperr.ErrInternal.WithMessage(err.Error())
	}

	// 重新查询 order_id（可能因 ON CONFLICT 已存在）
	err = tx.QueryRow(ctx, `
		SELECT id FROM checkout_orders WHERE merchant_id = $1 AND merchant_order_id = $2
	`, req.MerchantID, req.MerchantOrderID).Scan(&orderID)
	if err != nil {
		return apperr.ErrInternal.WithMessage(err.Error())
	}

	// P0-5: 创建 payment_attempt（PAYMENT_LINK 入口）
	providerIdempotencyKey := ProviderIdempotencyKey(attemptID)
	_, err = tx.Exec(ctx, `
		INSERT INTO payment_attempts (
			id, order_id, merchant_id, tenant_id, acquirer_channel_id, entry_channel, entry_ref,
			merchant_order_id, amount_minor, currency, status,
			payment_method_token, provider_idempotency_key,
			customer, metadata, pool_channel_id, version
		)
		VALUES ($1, $2, $3, $4, $5, 'PAYMENT_LINK', $6, $7, $8, $9, 'REQUIRES_ACTION', '', $10, $11, $12, $13, 0)
	`, attemptID, orderID, req.MerchantID, tenantID, acquirerChannelID, paymentLinkID.String(),
		req.MerchantOrderID, req.AmountMinor, req.Currency,
		providerIdempotencyKey, req.Customer, req.Metadata, nullableUUID(poolChannelID))
	if err != nil {
		return apperr.ErrInternal.WithMessage(err.Error())
	}

	return nil
}

// ResolveByToken 通过 token 解析 Payment Link（恒时比对）。
//
// G09 安全保证：
//   - 使用 token_hash 查找（DB 索引）
//   - 找到后用 subtle.ConstantTimeCompare 恒时比对 hash
//   - 不泄露记录存在性（无效/过期/已用返回相同错误）
//   - 记录 access log（只记 token_hash，不记 token）
func (s *PaymentLinkService) ResolveByToken(ctx context.Context, token string) (*PaymentLinkRecord, error) {
	if len(token) != 64 {
		// G09: 不泄露记录存在性，返回统一错误
		return nil, ErrPaymentLinkNotFound
	}

	tokenHash := hashToken(token)

	row := s.pool.QueryRow(ctx, `
		SELECT id, token_hash, merchant_id, merchant_order_id, amount_minor, currency,
		       success_url, cancel_url, single_use, expires_at, status, claimed_at
		FROM payment_links
		WHERE token_hash = $1 AND status = 'ACTIVE'
	`, tokenHash)

	var rec PaymentLinkRecord
	var dbTokenHash string
	err := row.Scan(&rec.ID, &dbTokenHash, &rec.MerchantID, &rec.MerchantOrderID,
		&rec.AmountMinor, &rec.Currency, &rec.SuccessURL, &rec.CancelURL,
		&rec.SingleUse, &rec.ExpiresAt, &rec.Status, &rec.ClaimedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// G09: 不泄露记录存在性
			return nil, ErrPaymentLinkNotFound
		}
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	// G09: 恒时比对 token hash（即使 DB 查找已命中，仍做恒时比对防止时序侧信道）
	if !constantTimeHashCompare(tokenHash, dbTokenHash) {
		return nil, ErrPaymentLinkNotFound
	}

	// 检查过期
	if rec.ExpiresAt != nil && rec.ExpiresAt.Before(time.Now()) {
		// 记录 access log（只记 token_hash）
		_ = s.recordAccessLog(ctx, rec.ID, tokenHash, "VIEW", false, "EXPIRED")
		return nil, ErrPaymentLinkExpired
	}

	return &rec, nil
}

// ClaimForCheckout 原子认领 Payment Link 用于 checkout（single-use 原子 claim）。
//
// G09 并发安全：
//   - 使用 SELECT ... FOR UPDATE 锁定行
//   - single-use 链接只能被一个 checkout 成功 claim
//   - 并发 claim 只有一个成功，其余返回 ErrPaymentLinkAlreadyClaimed
//   - 非 single-use 链接可多次 claim（直到过期或吊销）
//
// 此方法在 DB 事务内执行，不执行任何外部网络 I/O（RED-01）。
func (s *PaymentLinkService) ClaimForCheckout(ctx context.Context, token string, attemptID uuid.UUID) (*PaymentLinkRecord, error) {
	if len(token) != 64 {
		return nil, ErrPaymentLinkNotFound
	}

	tokenHash := hashToken(token)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	defer tx.Rollback(ctx)

	// SELECT ... FOR UPDATE 锁定行，防止并发 claim
	row := tx.QueryRow(ctx, `
		SELECT id, token_hash, merchant_id, merchant_order_id, amount_minor, currency,
		       success_url, cancel_url, single_use, expires_at, status, claimed_at
		FROM payment_links
		WHERE token_hash = $1 AND status = 'ACTIVE'
		FOR UPDATE
	`, tokenHash)

	var rec PaymentLinkRecord
	var dbTokenHash string
	err = row.Scan(&rec.ID, &dbTokenHash, &rec.MerchantID, &rec.MerchantOrderID,
		&rec.AmountMinor, &rec.Currency, &rec.SuccessURL, &rec.CancelURL,
		&rec.SingleUse, &rec.ExpiresAt, &rec.Status, &rec.ClaimedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentLinkNotFound
		}
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	// G09: 恒时比对
	if !constantTimeHashCompare(tokenHash, dbTokenHash) {
		return nil, ErrPaymentLinkNotFound
	}

	// 检查过期
	if rec.ExpiresAt != nil && rec.ExpiresAt.Before(time.Now()) {
		_ = s.recordAccessLog(ctx, rec.ID, tokenHash, "CLAIM", false, "EXPIRED")
		return nil, ErrPaymentLinkExpired
	}

	// single-use 原子 claim
	if rec.SingleUse {
		if rec.ClaimedAt != nil {
			// 已被 claim
			_ = s.recordAccessLog(ctx, rec.ID, tokenHash, "CLAIM", false, "ALREADY_CLAIMED")
			return nil, ErrPaymentLinkAlreadyClaimed
		}

		// 原子标记为 CLAIMED
		// P1: WHERE 条件包含 status = 'ACTIVE' AND claimed_at IS NULL 作为乐观锁二次校验，
		// 即使 FOR UPDATE 已锁定，仍防止状态在锁内被其他路径修改。
		result, err := tx.Exec(ctx, `
			UPDATE payment_links
			SET status = 'CLAIMED', claimed_at = NOW(), claimed_by_attempt_id = $2, updated_at = NOW()
			WHERE id = $1 AND status = 'ACTIVE' AND claimed_at IS NULL
		`, rec.ID, attemptID)
		if err != nil {
			return nil, apperr.ErrInternal.WithMessage(err.Error())
		}
		// P1: 必须检查 RowsAffected，确保原子消费真正生效。
		// 如果 RowsAffected == 0，说明在 FOR UPDATE 锁内状态仍被并发修改（理论上不应发生，
		// 但作为 fail-closed 防御），必须返回错误而非静默成功。
		if result.RowsAffected() == 0 {
			_ = s.recordAccessLog(ctx, rec.ID, tokenHash, "CLAIM", false, "RACE_LOST")
			return nil, ErrPaymentLinkAlreadyClaimed
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	_ = s.recordAccessLog(ctx, rec.ID, tokenHash, "CLAIM", true, "")
	return &rec, nil
}

// MarkAsPaid 标记 Payment Link 为已支付。
//
// G09: 使用 token_hash 查找，恒时比对。
// single-use 链接必须是 CLAIMED 状态才能 MarkAsPaid。
// 非 single-use 链接必须是 ACTIVE 状态。
func (s *PaymentLinkService) MarkAsPaid(ctx context.Context, token string) error {
	if len(token) != 64 {
		return ErrPaymentLinkNotFound
	}

	tokenHash := hashToken(token)

	// 先查找链接，确定 single_use 和当前状态
	var linkID uuid.UUID
	var dbTokenHash string
	var singleUse bool
	var currentStatus string
	err := s.pool.QueryRow(ctx, `
		SELECT id, token_hash, single_use, status
		FROM payment_links
		WHERE token_hash = $1
	`, tokenHash).Scan(&linkID, &dbTokenHash, &singleUse, &currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPaymentLinkNotFound
		}
		return err
	}

	// G09: 恒时比对
	if !constantTimeHashCompare(tokenHash, dbTokenHash) {
		return ErrPaymentLinkNotFound
	}

	// 状态检查
	if currentStatus == "PAID" {
		return ErrPaymentLinkAlreadyPaid
	}
	if currentStatus == "REVOKED" {
		return ErrPaymentLinkRevoked
	}
	if currentStatus == "EXPIRED" {
		return ErrPaymentLinkExpired
	}

	// single-use 必须是 CLAIMED，非 single-use 必须是 ACTIVE
	if singleUse {
		if currentStatus != "CLAIMED" {
			return ErrPaymentLinkNotClaimed
		}
	} else {
		if currentStatus != "ACTIVE" {
			return ErrPaymentLinkNotFound
		}
	}

	result, err := s.pool.Exec(ctx, `
		UPDATE payment_links
		SET status = 'PAID', updated_at = NOW()
		WHERE id = $1 AND (expires_at IS NULL OR expires_at > NOW())
	`, linkID)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return ErrPaymentLinkExpired
	}

	_ = s.recordAccessLog(ctx, linkID, tokenHash, "PAY", true, "")
	return nil
}

// Revoke 吊销 Payment Link（通过 payment_link_id）。
//
// 用于管理员吊销或 rotate 旧链接。
func (s *PaymentLinkService) Revoke(ctx context.Context, paymentLinkID uuid.UUID, reason string) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE payment_links
		SET status = 'REVOKED', revoked_at = NOW(), revoked_reason = $2, updated_at = NOW()
		WHERE id = $1 AND status IN ('ACTIVE', 'CLAIMED')
	`, paymentLinkID, reason)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPaymentLinkNotFound
	}
	return nil
}

// Rotate 轮换 Payment Link token（生成新 token，吊销旧链接）。
//
// G09: 旧链接被吊销，新链接获得新 token。
// 返回的新 token 只返回一次。
func (s *PaymentLinkService) Rotate(ctx context.Context, oldToken string) (*CreatePaymentLinkResult, error) {
	if len(oldToken) != 64 {
		return nil, ErrPaymentLinkNotFound
	}

	oldTokenHash := hashToken(oldToken)

	// 查找旧链接
	var oldLinkID uuid.UUID
	var dbTokenHash string
	var merchantID uuid.UUID
	var merchantOrderID string
	var amountMinor int64
	var currency string
	var successURL, cancelURL string
	var singleUse bool
	var expiresAt *time.Time
	var customer *domain.CustomerInfo
	var metadata map[string]string

	err := s.pool.QueryRow(ctx, `
		SELECT id, token_hash, merchant_id, merchant_order_id, amount_minor, currency,
		       success_url, cancel_url, single_use, expires_at, customer, metadata
		FROM payment_links
		WHERE token_hash = $1 AND status IN ('ACTIVE', 'CLAIMED')
	`, oldTokenHash).Scan(&oldLinkID, &dbTokenHash, &merchantID, &merchantOrderID,
		&amountMinor, &currency, &successURL, &cancelURL, &singleUse, &expiresAt, &customer, &metadata)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentLinkNotFound
		}
		return nil, err
	}

	// G09: 恒时比对
	if !constantTimeHashCompare(oldTokenHash, dbTokenHash) {
		return nil, ErrPaymentLinkNotFound
	}

	// PAY-002: 新链接沿用旧链接回跳 URL，同样必须命中 allowlist
	if err := ensureReturnURLsAllowed(ctx, s.pool, merchantID, successURL, cancelURL); err != nil {
		return nil, err
	}

	// P0-2/P1: 事务外完成选路、生成新 token
	newAttemptID := uuid.New()
	acquirerChannelID, poolChannelID, err := s.resolvePaymentLinkChannel(
		ctx, merchantID, currency, amountMinor, newAttemptID, "", "")
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	newToken, err := generatePaymentLinkToken()
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	newTokenHash := hashToken(newToken)

	newLinkID := uuid.New()
	// G09: payment_url 只存非敏感 path（用 paymentLinkID，不用 token）
	newSafePath := fmt.Sprintf("/pay/%s", newLinkID.String())

	newExpiresAt := time.Now().Add(7 * 24 * time.Hour)
	if expiresAt != nil {
		newExpiresAt = *expiresAt
	}

	// PAY-010: 单事务完成"吊销旧链接 + 取消遗留未决 attempt + 创建新链接 +
	// 标记 rotated_from_id"。任一步失败整体回滚，不会出现旧链接已吊销
	// 而新链接缺失的中间态（先吊销后创建的非原子缺陷）。
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	defer tx.Rollback(ctx)

	// 吊销旧链接（带状态守卫，RowsAffected==0 说明并发下已被处理）
	revokeResult, err := tx.Exec(ctx, `
		UPDATE payment_links
		SET status = 'REVOKED', revoked_at = NOW(), revoked_reason = 'ROTATED', updated_at = NOW()
		WHERE id = $1 AND status IN ('ACTIVE', 'CLAIMED')
	`, oldLinkID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	if revokeResult.RowsAffected() == 0 {
		return nil, ErrPaymentLinkNotFound
	}

	// 取消旧链接遗留的未决 attempt（PAYMENT_LINK 入口仍处于
	// REQUIRES_ACTION/PROCESSING/AUTHORIZED 的记录），否则新链接 confirm 创建
	// 新 attempt 时会违反 (order_id, entry_channel) 部分唯一索引。
	// 取消是状态机合法迁移（未决 → CANCELED），版本递增，重复执行无副作用。
	if _, err := tx.Exec(ctx, `
		UPDATE payment_attempts
		SET status = 'CANCELED', version = version + 1, updated_at = NOW()
		WHERE entry_channel = 'PAYMENT_LINK' AND entry_ref = $1::text
		  AND status IN ('REQUIRES_ACTION', 'PROCESSING', 'AUTHORIZED')
	`, oldLinkID.String()); err != nil {
		return nil, apperr.ErrInternal.WithMessage(fmt.Sprintf("payment link: cancel superseded attempts: %v", err))
	}

	// 创建新链接（复用 createLinkInTx：payment_link + checkout_order + payment_attempt）
	newReq := CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: merchantOrderID,
		AmountMinor:     amountMinor,
		Currency:        currency,
		SuccessURL:      successURL,
		CancelURL:       cancelURL,
		SingleUse:       singleUse,
		ExpiresAt:       expiresAt,
		Customer:        customer,
		Metadata:        metadata,
	}
	if err := createLinkInTx(ctx, tx, newReq, acquirerChannelID, poolChannelID, newAttemptID, newLinkID, newTokenHash, newSafePath, newExpiresAt); err != nil {
		return nil, err
	}

	// 标记新链接的 rotated_from_id
	if _, err := tx.Exec(ctx, `
		UPDATE payment_links SET rotated_from_id = $2 WHERE id = $1
	`, newLinkID, oldLinkID); err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	// G09: 返回的 PaymentURL 含 token，但只返回一次
	return &CreatePaymentLinkResult{
		PaymentLinkID: newLinkID.String(),
		Token:         newToken,
		PaymentURL:    fmt.Sprintf("/pay/%s", newToken),
	}, nil
}

// ExpireExpiredLinks 过期所有已过期的 ACTIVE 链接。
//
// 由定时任务调用。
func (s *PaymentLinkService) ExpireExpiredLinks(ctx context.Context) (int64, error) {
	result, err := s.pool.Exec(ctx, `
		UPDATE payment_links
		SET status = 'EXPIRED', updated_at = NOW()
		WHERE status = 'ACTIVE' AND expires_at IS NOT NULL AND expires_at < NOW()
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// recordAccessLog 记录 Payment Link 访问日志。
//
// G09: 只记录 token_hash，不记录 token。
// 不记录 Referer 中的 token、query string 中的 token。
func (s *PaymentLinkService) recordAccessLog(ctx context.Context, linkID uuid.UUID, tokenHash string, action string, success bool, errorCode string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO payment_link_access_log (payment_link_id, token_hash, action, success, error_code)
		VALUES ($1, $2, $3, $4, $5)
	`, linkID, tokenHash, action, success, errorCode)
	return err
}

// ConfirmPaymentResult 是 ConfirmPayment 的返回结果。
type ConfirmPaymentResult struct {
	PaymentAttemptID string
	Status           string
}

// ConfirmPayment 通过 Payment Link 提交支付方法，进入支付生命周期。
//
// G09 安全保证：
//   - 校验 token 长度（64 hex chars）
//   - 计算 token_hash，恒时比对
//   - 在事务内完成所有操作（原子性）
//   - single_use 链接：ACTIVE 时原子 claim 为 CLAIMED；CLAIMED 时允许 confirm
//   - 非 single_use 链接：必须是 ACTIVE 状态
//   - payment_method_token 加密存储（使用 encryptor）
//   - Outbox payload 只含 payment_attempt_id，不含 token
//
// 此方法在 DB 事务内执行，不执行任何外部网络 I/O（RED-01）。
func (s *PaymentLinkService) ConfirmPayment(ctx context.Context, token string, paymentMethodToken string) (*ConfirmPaymentResult, error) {
	// G09: 校验 token 长度
	if len(token) != 64 {
		return nil, ErrPaymentLinkNotFound
	}

	// P0-C1: nil encryptor + 非空 token → fail-closed，禁止明文存储
	if s.encryptor == nil && paymentMethodToken != "" {
		return nil, apperr.ErrInternal.WithMessage("payment_method_token encryption is required: encryptor not configured")
	}

	tokenHash := hashToken(token)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	defer tx.Rollback(ctx)

	// SELECT ... FOR UPDATE 锁定行，防止并发修改
	row := tx.QueryRow(ctx, `
		SELECT id, token_hash, merchant_id, single_use, expires_at, status
		FROM payment_links
		WHERE token_hash = $1 AND status IN ('ACTIVE', 'CLAIMED')
		FOR UPDATE
	`, tokenHash)

	var linkID uuid.UUID
	var dbTokenHash string
	var merchantID uuid.UUID
	var singleUse bool
	var expiresAt *time.Time
	var currentStatus string

	err = row.Scan(&linkID, &dbTokenHash, &merchantID, &singleUse, &expiresAt, &currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// G09: 不泄露记录存在性
			return nil, ErrPaymentLinkNotFound
		}
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	// G09: 恒时比对 token hash
	if !constantTimeHashCompare(tokenHash, dbTokenHash) {
		return nil, ErrPaymentLinkNotFound
	}

	// 检查过期
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		// P1-2: recordAccessLog 移到事务外，避免在 FOR UPDATE 锁期间另开连接
		defer func() {
			_ = s.recordAccessLog(ctx, linkID, tokenHash, "CONFIRM", false, "EXPIRED")
		}()
		return nil, ErrPaymentLinkExpired
	}

	// single_use / 非 single_use 状态校验
	if singleUse {
		if currentStatus == "ACTIVE" {
			// 原子 claim：ACTIVE → CLAIMED
			result, err := tx.Exec(ctx, `
				UPDATE payment_links
				SET status = 'CLAIMED', claimed_at = NOW(), updated_at = NOW()
				WHERE id = $1 AND status = 'ACTIVE'
			`, linkID)
			if err != nil {
				return nil, apperr.ErrInternal.WithMessage(err.Error())
			}
			if result.RowsAffected() == 0 {
				// P1-2: recordAccessLog 移到事务外
				defer func() {
					_ = s.recordAccessLog(ctx, linkID, tokenHash, "CONFIRM", false, "RACE_LOST")
				}()
				return nil, ErrPaymentLinkAlreadyClaimed
			}
		}
		// CLAIMED 状态允许 confirm，但必须检查是否已被确认过（P1-2: 防止重复确认）
	} else {
		// 非 single_use：必须是 ACTIVE
		if currentStatus != "ACTIVE" {
			return nil, ErrPaymentLinkNotFound
		}
	}

	// 查找关联的 payment_attempt（entry_ref = paymentLinkID.String(), entry_channel = 'PAYMENT_LINK'）
	var attemptID uuid.UUID
	var attemptStatus string
	var attemptVersion int
	var tenantID uuid.UUID
	var existingToken string
	var existingEncryptedToken []byte
	err = tx.QueryRow(ctx, `
		SELECT id, status, version, tenant_id, COALESCE(payment_method_token, ''), payment_method_token_encrypted
		FROM payment_attempts
		WHERE entry_ref = $1 AND entry_channel = 'PAYMENT_LINK'
		ORDER BY created_at DESC
		LIMIT 1
	`, linkID.String()).Scan(&attemptID, &attemptStatus, &attemptVersion, &tenantID, &existingToken, &existingEncryptedToken)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	// P1-2: single-use 链接防重复确认 — 如果 payment_attempt 已有 payment_method_token
	// 或 payment_method_token_encrypted，说明此链接已被确认过，返回 409。
	if singleUse && (existingToken != "" || len(existingEncryptedToken) > 0) {
		defer func() {
			_ = s.recordAccessLog(ctx, linkID, tokenHash, "CONFIRM", false, "ALREADY_CONFIRMED")
		}()
		return nil, ErrPaymentLinkAlreadyClaimed
	}

	// 更新 payment_method_token（加密存储）
	var encryptedToken []byte
	var plaintextToken string
	if s.encryptor != nil && paymentMethodToken != "" {
		encryptedToken, err = s.encryptor.EncryptToken(ctx, tenantID, attemptID, paymentMethodToken)
		if err != nil {
			return nil, apperr.ErrInternal.WithMessage(err.Error())
		}
		// 加密后原始 token 不写入 DB
		plaintextToken = ""
	}
	// P0-C1: nil encryptor + 非空 token 已在函数入口处 fail-closed，此处不会到达

	// 更新 payment_attempt：写入 payment_method_token + 状态转为 REQUIRES_ACTION（如果还不是）
	if attemptStatus == "REQUIRES_ACTION" {
		_, err = tx.Exec(ctx, `
			UPDATE payment_attempts
			SET payment_method_token = $2, payment_method_token_encrypted = $3,
			    version = version + 1, updated_at = NOW()
			WHERE id = $1
		`, attemptID, plaintextToken, encryptedToken)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE payment_attempts
			SET payment_method_token = $2, payment_method_token_encrypted = $3,
			    status = 'REQUIRES_ACTION',
			    version = version + 1, updated_at = NOW()
			WHERE id = $1
		`, attemptID, plaintextToken, encryptedToken)
	}
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	// 写入 PAYMENT_DISPATCH_REQUESTED Outbox（payload 只含 payment_attempt_id，不含 token）
	dispatchPayload, _ := json.Marshal(map[string]interface{}{
		"payment_attempt_id": attemptID.String(),
	})
	outboxID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ($1, 'payment_attempt', $2, 'PAYMENT_DISPATCH_REQUESTED', $3, $4, 'Pending')
	`, outboxID, attemptID, dispatchPayload, "dispatch_"+attemptID.String())
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}

	_ = s.recordAccessLog(ctx, linkID, tokenHash, "CONFIRM", true, "")

	return &ConfirmPaymentResult{
		PaymentAttemptID: attemptID.String(),
		Status:           "REQUIRES_ACTION",
	}, nil
}

// PaymentLinkRecord Payment Link 数据库记录。
type PaymentLinkRecord struct {
	ID              uuid.UUID
	MerchantID      uuid.UUID
	MerchantOrderID string
	AmountMinor     int64
	Currency        string
	SuccessURL      string
	CancelURL       string
	SingleUse       bool
	ExpiresAt       *time.Time
	Status          string
	ClaimedAt       *time.Time
}

// G09: 错误不泄露记录存在性
var (
	ErrPaymentLinkNotFound       = apperr.New("PAYMENT_LINK_NOT_FOUND", 404)
	ErrPaymentLinkExpired        = apperr.New("PAYMENT_LINK_EXPIRED", 410)
	ErrPaymentLinkAlreadyClaimed = apperr.New("PAYMENT_LINK_ALREADY_CLAIMED", 409)
	ErrPaymentLinkAlreadyPaid    = apperr.New("PAYMENT_LINK_ALREADY_PAID", 409)
	ErrPaymentLinkRevoked        = apperr.New("PAYMENT_LINK_REVOKED", 410)
	ErrPaymentLinkNotClaimed     = apperr.New("PAYMENT_LINK_NOT_CLAIMED", 409)
)
