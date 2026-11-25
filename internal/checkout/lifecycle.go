package checkout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/checkout/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// ============================================================================
// G10: 三入口统一生命周期
//
// Hosted Checkout / S2S Payment Intent / Payment Link 三个入口共享：
//   - order_id（checkout_orders）
//   - payment_attempt_id（payment_attempts）
//   - merchant、tenant、amount、currency、merchant_order_id、metadata
//   - 统一状态机（AttemptStatus）
//
// 状态机（G10）：
//   REQUIRES_ACTION → PROCESSING → AUTHORIZED → CAPTURED → SETTLED
//                   ↘ FAILED
//                   ↘ CANCELED
//   任意状态 → UNKNOWN_EXTERNAL_STATE（timeout，RED-03）
//
// PENDING 不是完成态。
// ============================================================================

// EntryChannel 三入口渠道标识。
type EntryChannel string

const (
	EntryHosted      EntryChannel = "HOSTED"
	EntryS2S         EntryChannel = "S2S"
	EntryPaymentLink EntryChannel = "PAYMENT_LINK"
)

// AttemptStatus 统一支付尝试状态机。
//
// G10: 三入口共享，PENDING 不是完成态。
type AttemptStatus string

const (
	// AttemptRequiresAction 需要 3DS 或额外操作
	AttemptRequiresAction AttemptStatus = "REQUIRES_ACTION"
	// AttemptProcessing 处理中
	AttemptProcessing AttemptStatus = "PROCESSING"
	// AttemptAuthorized 已授权（未捕获）
	AttemptAuthorized AttemptStatus = "AUTHORIZED"
	// AttemptCaptured 已捕获
	AttemptCaptured AttemptStatus = "CAPTURED"
	// AttemptSettled 已结算（终态）
	AttemptSettled AttemptStatus = "SETTLED"
	// AttemptFailed 失败（终态）
	AttemptFailed AttemptStatus = "FAILED"
	// AttemptCanceled 已取消（终态）
	AttemptCanceled AttemptStatus = "CANCELED"
	// AttemptUnknownExternalState 外部状态未知（timeout，RED-03）
	AttemptUnknownExternalState AttemptStatus = "UNKNOWN_EXTERNAL_STATE"
	// AttemptPartiallyCaptured 部分捕获
	AttemptPartiallyCaptured AttemptStatus = "PARTIALLY_CAPTURED"
	// AttemptRefunded 已退款
	AttemptRefunded AttemptStatus = "REFUNDED"
	// AttemptPartiallyRefunded 部分退款
	AttemptPartiallyRefunded AttemptStatus = "PARTIALLY_REFUNDED"
	// AttemptChargedBack 已拒付
	AttemptChargedBack AttemptStatus = "CHARGED_BACK"
)

// IsTerminal 返回状态是否为终态。
//
// G10: PENDING 不是终态。终态包括 SETTLED、FAILED、CANCELED、REFUNDED、CHARGED_BACK。
// UNKNOWN_EXTERNAL_STATE 不是终态（可通过轮询/回调/人工对账收敛）。
func (s AttemptStatus) IsTerminal() bool {
	switch s {
	case AttemptSettled, AttemptFailed, AttemptCanceled,
		AttemptRefunded, AttemptChargedBack:
		return true
	default:
		return false
	}
}

// IsCompleted 返回状态是否为成功完成态。
//
// G10: PENDING 不是完成态。完成态包括 SETTLED。
func (s AttemptStatus) IsCompleted() bool {
	return s == AttemptSettled
}

// IsValidTransition 校验状态转换是否合法。
//
// G10: 统一状态机，防止非法转换。
func (s AttemptStatus) IsValidTransition(next AttemptStatus) bool {
	transitions := map[AttemptStatus][]AttemptStatus{
		AttemptRequiresAction:       {AttemptProcessing, AttemptFailed, AttemptCanceled},
		AttemptProcessing:           {AttemptAuthorized, AttemptFailed, AttemptCanceled, AttemptUnknownExternalState, AttemptCaptured},
		AttemptAuthorized:           {AttemptCaptured, AttemptPartiallyCaptured, AttemptFailed, AttemptCanceled, AttemptUnknownExternalState},
		AttemptCaptured:             {AttemptSettled, AttemptRefunded, AttemptPartiallyRefunded, AttemptChargedBack, AttemptUnknownExternalState},
		AttemptPartiallyCaptured:    {AttemptCaptured, AttemptSettled, AttemptRefunded, AttemptPartiallyRefunded, AttemptChargedBack},
		AttemptSettled:              {AttemptRefunded, AttemptPartiallyRefunded, AttemptChargedBack},
		AttemptUnknownExternalState: {AttemptProcessing, AttemptAuthorized, AttemptCaptured, AttemptSettled, AttemptFailed, AttemptCanceled, AttemptPartiallyCaptured, AttemptRefunded, AttemptPartiallyRefunded, AttemptChargedBack},
		AttemptRefunded:             {}, // 终态
		AttemptPartiallyRefunded:    {AttemptRefunded, AttemptChargedBack},
		AttemptChargedBack:          {}, // 终态
		AttemptFailed:               {}, // 终态
		AttemptCanceled:             {}, // 终态
	}
	allowed, ok := transitions[s]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == next {
			return true
		}
	}
	return false
}

// PaymentAttempt 统一支付尝试记录。
type PaymentAttempt struct {
	ID                     uuid.UUID
	OrderID                uuid.UUID
	MerchantID             uuid.UUID
	TenantID               uuid.UUID
	AcquirerChannelID      uuid.UUID // P0-3: 唯一权威通道 ID，由 SelectChannel 选择
	PoolChannelID          uuid.UUID // P1: 支付池成员 ID（租约与失败切换）
	EntryChannel           EntryChannel
	EntryRef               string
	MerchantOrderID        string
	AmountMinor            int64
	Currency               string
	Status                 AttemptStatus
	ProviderIdempotencyKey string
	ProviderRef            string
	ProviderTransactionID  string
	RequiresActionURL      string
	RequiresActionType     string
	FailureCode            string
	FailureReason          string
	PaymentMethodToken     string
	LedgerReservationID    string
	Customer               *domain.CustomerInfo
	Metadata               map[string]string
	Version                int
	// B2: Dispatch lease 字段 — 修复崩溃窗口
	DispatchStartedAt    *time.Time
	DispatchLeaseUntil   *time.Time
	DispatchOwner        string
	DispatchAttemptCount int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// UnifiedCheckoutContext 三入口共享的 checkout 上下文。
//
// G10: Hosted / S2S / Payment Link 共享此上下文。
type UnifiedCheckoutContext struct {
	OrderID          uuid.UUID
	PaymentAttemptID uuid.UUID
	MerchantID       uuid.UUID
	TenantID         uuid.UUID
	MerchantOrderID  string
	AmountMinor      int64
	Currency         string
	Metadata         map[string]string
	Customer         *domain.CustomerInfo
}

// PaymentAttemptRepository payment_attempts 表仓储。
type PaymentAttemptRepository struct {
	pool      *pgxpool.Pool
	encryptor TokenEncryptor // B6: token 加密器，nil 时 fail-closed
}

// TokenEncryptor token 加密/解密接口（B6）。
// 生产使用 KMS/Envelope Encryption，mock/local 使用 AES-GCM。
type TokenEncryptor interface {
	EncryptToken(ctx context.Context, tenantID uuid.UUID, attemptID uuid.UUID, plaintextToken string) ([]byte, error)
	DecryptToken(ctx context.Context, tenantID uuid.UUID, attemptID uuid.UUID, ciphertext []byte) (string, error)
}

func NewPaymentAttemptRepository(pool *pgxpool.Pool) *PaymentAttemptRepository {
	return &PaymentAttemptRepository{pool: pool}
}

// NewPaymentAttemptRepositoryWithEncryptor 创建带 token 加密器的 repository（B6）。
func NewPaymentAttemptRepositoryWithEncryptor(pool *pgxpool.Pool, encryptor TokenEncryptor) *PaymentAttemptRepository {
	return &PaymentAttemptRepository{pool: pool, encryptor: encryptor}
}

// CreatePaymentAttempt 创建统一 payment attempt。
//
// G10: 三入口都调用此方法，生成共享 order_id 和 payment_attempt_id。
// B6: 如果 encryptor 存在，payment_method_token 加密后存储到 payment_method_token_encrypted 列，
// 原始 payment_method_token 列留空。如果 encryptor 为 nil 且 token 非空，fail-closed 返回错误。
// 此方法在传入的 ctx 内执行，不执行外部网络 I/O（RED-01）。
func (r *PaymentAttemptRepository) CreatePaymentAttempt(ctx context.Context, attempt *PaymentAttempt) error {
	return r.CreatePaymentAttemptTx(ctx, r.pool, attempt)
}

// CreatePaymentAttemptTx 事务内版本（PAY-004）：exec 可为 *pgxpool.Pool 或 pgx.Tx。
func (r *PaymentAttemptRepository) CreatePaymentAttemptTx(ctx context.Context, exec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, attempt *PaymentAttempt) error {
	var encryptedToken []byte
	var plaintextToken string

	if r.encryptor != nil && attempt.PaymentMethodToken != "" {
		var encErr error
		encryptedToken, encErr = r.encryptor.EncryptToken(ctx, attempt.TenantID, attempt.ID, attempt.PaymentMethodToken)
		if encErr != nil {
			return fmt.Errorf("encrypt payment_method_token: %w", encErr)
		}
		// B6: 加密后原始 token 不写入 DB
		plaintextToken = ""
	} else if r.encryptor == nil && attempt.PaymentMethodToken != "" {
		// P0-C1: nil encryptor + 非空 token → fail-closed，禁止明文存储
		return fmt.Errorf("payment_method_token encryption is required: encryptor not configured")
	}

	_, err := exec.Exec(ctx, `
		INSERT INTO payment_attempts (
			id, order_id, merchant_id, tenant_id, acquirer_channel_id, entry_channel, entry_ref,
			merchant_order_id, amount_minor, currency, status,
			provider_idempotency_key, provider_ref, provider_transaction_id,
			requires_action_url, requires_action_type,
			failure_code, failure_reason,
			payment_method_token, payment_method_token_encrypted, ledger_reservation_id,
			customer, metadata, pool_channel_id, version
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, 0)
	`, attempt.ID, attempt.OrderID, attempt.MerchantID, attempt.TenantID, attempt.AcquirerChannelID,
		string(attempt.EntryChannel), attempt.EntryRef,
		attempt.MerchantOrderID, attempt.AmountMinor, attempt.Currency, string(attempt.Status),
		attempt.ProviderIdempotencyKey, attempt.ProviderRef, attempt.ProviderTransactionID,
		attempt.RequiresActionURL, attempt.RequiresActionType,
		attempt.FailureCode, attempt.FailureReason,
		plaintextToken, encryptedToken, attempt.LedgerReservationID,
		attempt.Customer, attempt.Metadata, nullableUUID(attempt.PoolChannelID))
	if err != nil {
		return apperr.ErrInternal.WithMessage(err.Error())
	}
	return nil
}

// nullableUUID 把零值 UUID 转为 SQL NULL，避免写入 00000000-...。
func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// SwitchChannelTx 在失败切换时更新 attempt 的通道绑定（乐观锁 version）。
// 返回新的 version。version 不匹配说明并发推进，调用方不得继续切换。
func (r *PaymentAttemptRepository) SwitchChannelTx(ctx context.Context, tx pgx.Tx,
	attemptID, acquirerChannelID, poolChannelID uuid.UUID, version int) (int, error) {
	var newVersion int
	err := tx.QueryRow(ctx, `
		UPDATE payment_attempts
		SET acquirer_channel_id = $2,
		    pool_channel_id = $3,
		    version = version + 1,
		    updated_at = now()
		WHERE id = $1 AND version = $4
		RETURNING version
	`, attemptID, acquirerChannelID, nullableUUID(poolChannelID), version).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("payment_attempt %s: version conflict, cannot switch channel", attemptID)
		}
		return 0, err
	}
	return newVersion, nil
}

// GetPaymentAttempt 获取 payment attempt。
// B6: 如果 encryptor 存在且 payment_method_token_encrypted 非空，解密后填充 PaymentMethodToken。
func (r *PaymentAttemptRepository) GetPaymentAttempt(ctx context.Context, id uuid.UUID) (*PaymentAttempt, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, order_id, merchant_id, tenant_id, acquirer_channel_id, entry_channel, entry_ref,
		       merchant_order_id, amount_minor, currency, status,
		       COALESCE(provider_idempotency_key, ''), COALESCE(provider_ref, ''), COALESCE(provider_transaction_id, ''),
		       COALESCE(requires_action_url, ''), COALESCE(requires_action_type, ''),
		       COALESCE(failure_code, ''), COALESCE(failure_reason, ''),
		       COALESCE(payment_method_token, ''), payment_method_token_encrypted, COALESCE(ledger_reservation_id, ''),
		       COALESCE(customer, 'null'::jsonb), COALESCE(metadata, '{}'::jsonb), version,
		       dispatch_started_at, dispatch_lease_until, COALESCE(dispatch_owner, ''), dispatch_attempt_count,
		       COALESCE(pool_channel_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       created_at, updated_at
		FROM payment_attempts WHERE id = $1
	`, id)

	var a PaymentAttempt
	var entryChannel, status string
	var encryptedToken []byte
	err := row.Scan(&a.ID, &a.OrderID, &a.MerchantID, &a.TenantID, &a.AcquirerChannelID, &entryChannel, &a.EntryRef,
		&a.MerchantOrderID, &a.AmountMinor, &a.Currency, &status,
		&a.ProviderIdempotencyKey, &a.ProviderRef, &a.ProviderTransactionID,
		&a.RequiresActionURL, &a.RequiresActionType,
		&a.FailureCode, &a.FailureReason,
		&a.PaymentMethodToken, &encryptedToken, &a.LedgerReservationID,
		&a.Customer, &a.Metadata, &a.Version,
		&a.DispatchStartedAt, &a.DispatchLeaseUntil, &a.DispatchOwner, &a.DispatchAttemptCount,
		&a.PoolChannelID,
		&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.EntryChannel = EntryChannel(entryChannel)
	a.Status = AttemptStatus(status)

	// B6: 如果加密 token 存在，解密后填充 PaymentMethodToken
	if len(encryptedToken) > 0 && r.encryptor != nil {
		decrypted, decErr := r.encryptor.DecryptToken(ctx, a.TenantID, a.ID, encryptedToken)
		if decErr != nil {
			return nil, fmt.Errorf("decrypt payment_method_token: %w", decErr)
		}
		a.PaymentMethodToken = decrypted
	}

	return &a, nil
}

// GetPaymentAttemptByOrder 按 order_id 和 entry_channel 获取 attempt。
func (r *PaymentAttemptRepository) GetPaymentAttemptByOrder(ctx context.Context, orderID uuid.UUID, channel EntryChannel) (*PaymentAttempt, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, order_id, merchant_id, tenant_id, entry_channel, entry_ref,
		       merchant_order_id, amount_minor, currency, status,
		       COALESCE(provider_idempotency_key, ''), COALESCE(provider_ref, ''), COALESCE(provider_transaction_id, ''),
		       COALESCE(requires_action_url, ''), COALESCE(requires_action_type, ''),
		       COALESCE(failure_code, ''), COALESCE(failure_reason, ''),
		       COALESCE(payment_method_token, ''), COALESCE(ledger_reservation_id, ''),
		       COALESCE(customer, 'null'::jsonb), COALESCE(metadata, '{}'::jsonb), version, created_at, updated_at
		FROM payment_attempts
		WHERE order_id = $1 AND entry_channel = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, orderID, string(channel))

	var a PaymentAttempt
	var entryChannel, status string
	err := row.Scan(&a.ID, &a.OrderID, &a.MerchantID, &a.TenantID, &entryChannel, &a.EntryRef,
		&a.MerchantOrderID, &a.AmountMinor, &a.Currency, &status,
		&a.ProviderIdempotencyKey, &a.ProviderRef, &a.ProviderTransactionID,
		&a.RequiresActionURL, &a.RequiresActionType,
		&a.FailureCode, &a.FailureReason,
		&a.PaymentMethodToken, &a.LedgerReservationID,
		&a.Customer, &a.Metadata, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.EntryChannel = EntryChannel(entryChannel)
	a.Status = AttemptStatus(status)
	return &a, nil
}

// GetPaymentAttemptByEntryRef 按 entry_ref 和 entry_channel 获取 attempt。
//
// P1 修复：Payment Link 关联查询必须用 entry_ref（= paymentLinkID），
// 而不是 order_id。Payment Link 创建时 entry_ref = paymentLinkID.String()，
// order_id 是独立的 checkout_orders.id，两者不同。
// 旧代码 GetPaymentAttemptByOrder(rec.ID, EntryPaymentLink) 把 PaymentLinkID
// 误当 order_id，导致关联查询永远找不到对应的 attempt。
func (r *PaymentAttemptRepository) GetPaymentAttemptByEntryRef(ctx context.Context, entryRef string, channel EntryChannel) (*PaymentAttempt, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, order_id, merchant_id, tenant_id, entry_channel, entry_ref,
		       merchant_order_id, amount_minor, currency, status,
		       COALESCE(provider_idempotency_key, ''), COALESCE(provider_ref, ''), COALESCE(provider_transaction_id, ''),
		       COALESCE(requires_action_url, ''), COALESCE(requires_action_type, ''),
		       COALESCE(failure_code, ''), COALESCE(failure_reason, ''),
		       COALESCE(payment_method_token, ''), COALESCE(ledger_reservation_id, ''),
		       COALESCE(customer, 'null'::jsonb), COALESCE(metadata, '{}'::jsonb), version, created_at, updated_at
		FROM payment_attempts
		WHERE entry_ref = $1 AND entry_channel = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, entryRef, string(channel))

	var a PaymentAttempt
	var entryChannel, status string
	err := row.Scan(&a.ID, &a.OrderID, &a.MerchantID, &a.TenantID, &entryChannel, &a.EntryRef,
		&a.MerchantOrderID, &a.AmountMinor, &a.Currency, &status,
		&a.ProviderIdempotencyKey, &a.ProviderRef, &a.ProviderTransactionID,
		&a.RequiresActionURL, &a.RequiresActionType,
		&a.FailureCode, &a.FailureReason,
		&a.PaymentMethodToken, &a.LedgerReservationID,
		&a.Customer, &a.Metadata, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.EntryChannel = EntryChannel(entryChannel)
	a.Status = AttemptStatus(status)
	return &a, nil
}

// UpdateAttemptStatus 更新 attempt 状态（带版本号乐观锁）。
//
// G10: 校验状态转换合法性。使用乐观锁防止并发更新。
func (r *PaymentAttemptRepository) UpdateAttemptStatus(ctx context.Context, id uuid.UUID, next AttemptStatus, version int) error {
	// 先获取当前状态
	var currentStatus string
	var currentVersion int
	err := r.pool.QueryRow(ctx, `
		SELECT status, version FROM payment_attempts WHERE id = $1
	`, id).Scan(&currentStatus, &currentVersion)
	if err != nil {
		return err
	}

	current := AttemptStatus(currentStatus)
	if !current.IsValidTransition(next) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalStateTransition, current, next)
	}

	result, err := r.pool.Exec(ctx, `
		UPDATE payment_attempts
		SET status = $2, version = version + 1, updated_at = NOW()
		WHERE id = $1 AND version = $3
	`, id, string(next), version)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

// SetRequiresAction 设置 attempt 为 REQUIRES_ACTION 状态（3DS redirect）。
func (r *PaymentAttemptRepository) SetRequiresAction(ctx context.Context, id uuid.UUID, actionURL, actionType string, version int) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE payment_attempts
		SET status = 'REQUIRES_ACTION', requires_action_url = $2, requires_action_type = $3,
		    version = version + 1, updated_at = NOW()
		WHERE id = $1 AND version = $4 AND status IN ('PROCESSING', 'REQUIRES_ACTION')
	`, id, actionURL, actionType, version)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

// SetProviderResult 设置 provider 返回结果。
//
// RED-03: timeout 走 UnknownExternalState，无盲重试。
func (r *PaymentAttemptRepository) SetProviderResult(ctx context.Context, id uuid.UUID, result ProviderResult, version int) error {
	return r.SetProviderResultTx(ctx, r.pool, id, result, version)
}

// SetProviderResultTx 事务内版本（SEC-010）：允许 webhook consumer 在同一事务
// 内推进 attempt 并收敛账本。exec 可为 *pgxpool.Pool 或 pgx.Tx。
func (r *PaymentAttemptRepository) SetProviderResultTx(ctx context.Context, exec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, id uuid.UUID, result ProviderResult, version int) error {
	var nextStatus AttemptStatus
	switch result.Status {
	case "authorized":
		nextStatus = AttemptAuthorized
	case "captured":
		nextStatus = AttemptCaptured
	case "settled":
		nextStatus = AttemptSettled
	case "failed":
		nextStatus = AttemptFailed
	case "requires_action":
		nextStatus = AttemptRequiresAction
	case "processing":
		nextStatus = AttemptProcessing
	case "unknown", "timeout":
		// RED-03: timeout 走 UnknownExternalState，无盲重试
		nextStatus = AttemptUnknownExternalState
	default:
		nextStatus = AttemptUnknownExternalState
	}

	if nextStatus == AttemptRequiresAction {
		_, err := exec.Exec(ctx, `
			UPDATE payment_attempts
			SET status = 'REQUIRES_ACTION', requires_action_url = $2, requires_action_type = $3,
			    provider_ref = $4, provider_transaction_id = $5,
			    version = version + 1, updated_at = NOW()
			WHERE id = $1 AND version = $6
		`, id, result.RedirectURL, result.ActionType, result.ProviderRef, result.ProviderTransactionID, version)
		return err
	}

	if nextStatus == AttemptFailed {
		_, err := exec.Exec(ctx, `
			UPDATE payment_attempts
			SET status = 'FAILED', failure_code = $2, failure_reason = $3,
			    provider_ref = $4, provider_transaction_id = $5,
			    version = version + 1, updated_at = NOW()
			WHERE id = $1 AND version = $6
		`, id, result.FailureCode, result.FailureReason, result.ProviderRef, result.ProviderTransactionID, version)
		return err
	}

	_, err := exec.Exec(ctx, `
		UPDATE payment_attempts
		SET status = $2::varchar, provider_ref = $3, provider_transaction_id = $4,
		    captured_at = CASE WHEN $2::text = 'CAPTURED' AND captured_at IS NULL THEN NOW() ELSE captured_at END,
		    version = version + 1, updated_at = NOW()
		WHERE id = $1 AND version = $5
	`, id, string(nextStatus), result.ProviderRef, result.ProviderTransactionID, version)
	return err
}

// ClaimDispatchLease 原子领取 dispatch lease（B2: 修复崩溃窗口）。
//
// 规则：
//   - REQUIRES_ACTION → PROCESSING：直接领取
//   - PROCESSING 且 dispatch_lease_until < NOW()：lease 过期，允许重新领取
//   - PROCESSING 且 lease 未过期：返回 ErrDispatchLeaseHeld
//   - 其他状态：返回 ErrIllegalStateTransition
//
// 领取成功后设置 dispatch_started_at, dispatch_lease_until, dispatch_owner,
// dispatch_attempt_count += 1, status = PROCESSING, version += 1。
func (r *PaymentAttemptRepository) ClaimDispatchLease(ctx context.Context, id uuid.UUID, owner string, leaseDuration time.Duration) (*PaymentAttempt, error) {
	leaseUntil := time.Now().Add(leaseDuration)

	// 原子 CAS：REQUIRES_ACTION → PROCESSING 或 PROCESSING + lease_expired → PROCESSING
	result, err := r.pool.Exec(ctx, `
		UPDATE payment_attempts
		SET status = 'PROCESSING',
		    dispatch_started_at = NOW(),
		    dispatch_lease_until = $2,
		    dispatch_owner = $3,
		    dispatch_attempt_count = dispatch_attempt_count + 1,
		    version = version + 1,
		    updated_at = NOW()
		WHERE id = $1
		  AND (
		    (status = 'REQUIRES_ACTION')
		    OR
		    (status = 'PROCESSING' AND (dispatch_lease_until IS NULL OR dispatch_lease_until < NOW()))
		  )
	`, id, leaseUntil, owner)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		// 领取失败：可能是状态不允许或 lease 未过期
		var status string
		var leaseUntilVal *time.Time
		err := r.pool.QueryRow(ctx, `
			SELECT status, dispatch_lease_until FROM payment_attempts WHERE id = $1
		`, id).Scan(&status, &leaseUntilVal)
		if err != nil {
			return nil, err
		}
		if status == "PROCESSING" && leaseUntilVal != nil && leaseUntilVal.After(time.Now()) {
			return nil, ErrDispatchLeaseHeld
		}
		return nil, ErrIllegalStateTransition
	}

	// 返回更新后的 attempt
	return r.GetPaymentAttempt(ctx, id)
}

// ProviderResult provider 返回结果。
type ProviderResult struct {
	Status                string
	ProviderRef           string
	ProviderTransactionID string
	RedirectURL           string
	ActionType            string
	FailureCode           string
	FailureReason         string
}

// 统一生命周期错误
var (
	ErrIllegalStateTransition = apperr.New("ILLEGAL_STATE_TRANSITION", 409)
	ErrVersionConflict        = apperr.New("VERSION_CONFLICT", 409)
	ErrAttemptNotFound        = apperr.New("ATTEMPT_NOT_FOUND", 404)
	ErrDispatchLeaseHeld      = apperr.New("DISPATCH_LEASE_HELD", 409)
)

// ResolveSession resolves a checkout session by session_id for GET /checkout/v3/{session_id}.
//
// G10: 返回统一 checkout 上下文，不泄露记录存在性。
// 无效/过期/已用返回稳定页面和状态。
func (s *CheckoutService) ResolveSession(ctx context.Context, sessionID string) (*UnifiedCheckoutContext, *PaymentAttempt, error) {
	if sessionID == "" {
		return nil, nil, ErrSessionNotFound
	}

	var orderID, merchantID uuid.UUID
	var amountMinor int64
	var currency, merchantOrderID, status string
	var metadata map[string]string
	var customer *domain.CustomerInfo
	var expiresAt time.Time
	var attemptID *uuid.UUID

	err := s.pool.QueryRow(ctx, `
		SELECT order_id, merchant_id, amount_minor, currency, status, metadata, expires_at, payment_attempt_id
		FROM checkout_sessions
		WHERE session_id = $1
	`, sessionID).Scan(&orderID, &merchantID, &amountMinor, &currency, &status, &metadata, &expiresAt, &attemptID)
	if err != nil {
		// G10: 不泄露记录存在性
		return nil, nil, ErrSessionNotFound
	}

	// 检查过期
	if expiresAt.Before(time.Now()) {
		return nil, nil, ErrSessionExpired
	}

	// 获取关联的 payment_attempt
	var attempt *PaymentAttempt
	if attemptID != nil {
		repo := NewPaymentAttemptRepository(s.pool)
		var attemptErr error
		attempt, attemptErr = repo.GetPaymentAttempt(ctx, *attemptID)
		if attemptErr != nil {
			// attempt 查询失败不应导致 resolve 失败（会话仍可展示），
			// 但必须留下诊断日志，不允许静默吞掉。
			slog.Warn("checkout: resolve session attempt load failed",
				"session_id", sessionID, "attempt_id", *attemptID, "err", attemptErr)
		}
	}

	// 获取 order 的 merchant_order_id 和 customer
	_ = s.pool.QueryRow(ctx, `
		SELECT merchant_order_id, customer FROM checkout_orders WHERE id = $1
	`, orderID).Scan(&merchantOrderID, &customer)

	tenantID := merchantID // 简化：tenant = merchant

	return &UnifiedCheckoutContext{
		OrderID: orderID,
		PaymentAttemptID: func() uuid.UUID {
			if attemptID != nil {
				return *attemptID
			}
			return uuid.Nil
		}(),
		MerchantID:      merchantID,
		TenantID:        tenantID,
		MerchantOrderID: merchantOrderID,
		AmountMinor:     amountMinor,
		Currency:        currency,
		Metadata:        metadata,
		Customer:        customer,
	}, attempt, nil
}

// Session resolver 错误（不泄露记录存在性）
var (
	ErrSessionNotFound = apperr.New("SESSION_NOT_FOUND", 404)
	ErrSessionExpired  = apperr.New("SESSION_EXPIRED", 410)
	ErrSessionUsed     = apperr.New("SESSION_USED", 410)
)

// Ensure errors are wrapped properly
var _ = errors.Is
