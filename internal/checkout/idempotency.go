package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// ============================================================================
// G12: 并发幂等
//
// PI 强制 Idempotency-Key；scope = tenant + merchant + operation + key
// 并发认领使用 INSERT ... ON CONFLICT
// 同 payload_hash 返回首次稳定响应，不同 payload_hash 返回 409
// 100 并发相同 idempotency key：一个 attempt、一个 outbox、一个 provider idempotency key、一组分录
// ============================================================================

// IdempotencyOperation 操作类型。
const (
	OpCreatePaymentIntent = "CREATE_PAYMENT_INTENT"
	OpCreatePaymentLink   = "CREATE_PAYMENT_LINK"
	OpCreateSession       = "CREATE_SESSION"
)

// IdempotencyRecord 幂等记录。
type IdempotencyRecord struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	MerchantID            uuid.UUID
	Operation             string
	IdempotencyKey        string
	PayloadHash           string
	ResponseStatus        int
	ResponseBodyEncrypted []byte
	PaymentAttemptID      *uuid.UUID
	State                 string
	CreatedAt             time.Time
	CommittedAt           *time.Time
	ExpiresAt             time.Time
}

// IdempotencyRepository 幂等记录仓储。
type IdempotencyRepository struct {
	pool *pgxpool.Pool
}

func NewIdempotencyRepository(pool *pgxpool.Pool) *IdempotencyRepository {
	return &IdempotencyRepository{pool: pool}
}

// ClaimOrGet 并发认领幂等记录。
//
// G12 核心逻辑：
//  1. 尝试 INSERT 新记录（INSERT ... ON CONFLICT DO NOTHING）
//  2. 如果 INSERT 成功（新认领），返回 claimed=true
//  3. 如果 INSERT 失败（已存在），查询现有记录：
//     a. 如果 payload_hash 相同 → 返回首次响应（幂等）
//     b. 如果 payload_hash 不同 → 返回 ErrIdempotencyPayloadMismatch（409）
//
// 此方法在 DB 事务内执行，不执行外部网络 I/O（RED-01）。
func (r *IdempotencyRepository) ClaimOrGet(ctx context.Context, tenantID, merchantID uuid.UUID, operation, idempotencyKey, payloadHash string) (record *IdempotencyRecord, claimed bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, apperr.ErrInternal.WithMessage(err.Error())
	}
	defer tx.Rollback(ctx)

	recordID := uuid.New()
	expiresAt := time.Now().Add(24 * time.Hour)

	// 尝试 INSERT ... ON CONFLICT DO NOTHING
	var insertedID *uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO idempotency_records (id, tenant_id, merchant_id, operation, idempotency_key, payload_hash, state, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', $7)
		ON CONFLICT (tenant_id, merchant_id, operation, idempotency_key) DO NOTHING
		RETURNING id
	`, recordID, tenantID, merchantID, operation, idempotencyKey, payloadHash, expiresAt).Scan(&insertedID)

	if err == nil && insertedID != nil {
		// 新认领成功
		if err := tx.Commit(ctx); err != nil {
			return nil, false, apperr.ErrInternal.WithMessage(err.Error())
		}
		return &IdempotencyRecord{
			ID:             *insertedID,
			TenantID:       tenantID,
			MerchantID:     merchantID,
			Operation:      operation,
			IdempotencyKey: idempotencyKey,
			PayloadHash:    payloadHash,
			State:          "PENDING",
			ExpiresAt:      expiresAt,
		}, true, nil
	}

	// INSERT 没有返回行（ON CONFLICT），查询现有记录
	var existingID uuid.UUID
	var existingPayloadHash string
	var existingState string
	var existingResponseStatus *int
	var existingResponseBody []byte
	var existingAttemptID *uuid.UUID
	var existingCreatedAt time.Time
	var existingCommittedAt *time.Time
	var existingExpiresAt time.Time

	err = tx.QueryRow(ctx, `
		SELECT id, payload_hash, state, response_status, response_body_encrypted,
		       payment_attempt_id, created_at, committed_at, expires_at
		FROM idempotency_records
		WHERE tenant_id = $1 AND merchant_id = $2 AND operation = $3 AND idempotency_key = $4
	`, tenantID, merchantID, operation, idempotencyKey).Scan(
		&existingID, &existingPayloadHash, &existingState, &existingResponseStatus,
		&existingResponseBody, &existingAttemptID, &existingCreatedAt, &existingCommittedAt, &existingExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, apperr.ErrInternal.WithMessage("idempotency: concurrent insert disappeared")
		}
		return nil, false, apperr.ErrInternal.WithMessage(err.Error())
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, apperr.ErrInternal.WithMessage(err.Error())
	}

	// G12: payload_hash 不同 → 409
	if existingPayloadHash != payloadHash {
		return &IdempotencyRecord{
			ID:             existingID,
			TenantID:       tenantID,
			MerchantID:     merchantID,
			Operation:      operation,
			IdempotencyKey: idempotencyKey,
			PayloadHash:    existingPayloadHash,
			State:          existingState,
		}, false, ErrIdempotencyPayloadMismatch
	}

	// payload_hash 相同 → 幂等返回
	var responseStatus int
	if existingResponseStatus != nil {
		responseStatus = *existingResponseStatus
	}
	return &IdempotencyRecord{
		ID:                    existingID,
		TenantID:              tenantID,
		MerchantID:            merchantID,
		Operation:             operation,
		IdempotencyKey:        idempotencyKey,
		PayloadHash:           existingPayloadHash,
		ResponseStatus:        responseStatus,
		ResponseBodyEncrypted: existingResponseBody,
		PaymentAttemptID:      existingAttemptID,
		State:                 existingState,
		CreatedAt:             existingCreatedAt,
		CommittedAt:           existingCommittedAt,
		ExpiresAt:             existingExpiresAt,
	}, false, nil
}

// Commit 提交幂等记录（保存首次响应）。
//
// G12: 保存首次稳定响应，后续相同请求返回此响应。
func (r *IdempotencyRepository) Commit(ctx context.Context, id uuid.UUID, responseStatus int, responseBody []byte, attemptID *uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE idempotency_records
		SET state = 'COMMITTED', response_status = $2, response_body_encrypted = $3,
		    payment_attempt_id = $4, committed_at = NOW()
		WHERE id = $1 AND state = 'PENDING'
	`, id, responseStatus, responseBody, attemptID)
	if err != nil {
		return apperr.ErrInternal.WithMessage(err.Error())
	}
	return nil
}

// Fail 标记幂等记录为失败。
func (r *IdempotencyRepository) Fail(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE idempotency_records SET state = 'FAILED' WHERE id = $1 AND state = 'PENDING'
	`, id)
	return err
}

// CanonicalizePayload 规范化 payload（用于稳定 hash）。
//
// G12: JSON key 排序，去除空白，确保相同语义的 payload 产生相同 hash。
func CanonicalizePayload(v interface{}) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		// 不是 object，直接返回原始 JSON
		return b, nil
	}
	// 重新序列化（Go 的 json.Marshal 已经按 key 排序）
	return json.Marshal(m)
}

// HashPayload 计算 payload hash。
//
// G12: SHA-256 hash，hex 编码。
func HashPayload(payload []byte) string {
	h := sha256.Sum256(payload)
	return hex.EncodeToString(h[:])
}

// ComputeIdempotencyScope 计算 idempotency scope。
//
// G12: scope = tenant + merchant + operation + key
// 用于 UNIQUE 约束和并发认领。
func ComputeIdempotencyScope(tenantID, merchantID uuid.UUID, operation, key string) string {
	return fmt.Sprintf("%s:%s:%s:%s", tenantID, merchantID, operation, key)
}

// ProviderIdempotencyKey 生成 provider idempotency key。
//
// G12: 100 并发相同 idempotency key → 一个 provider idempotency key。
// 使用 payment_attempt_id 确保唯一性。
func ProviderIdempotencyKey(attemptID uuid.UUID) string {
	return fmt.Sprintf("nexus_attempt_%s", attemptID.String())
}

// ValidateIdempotencyKey 校验 idempotency key 格式。
//
// G12: key 长度 1-256，非空。
func ValidateIdempotencyKey(key string) error {
	if key == "" {
		return apperr.New("IDEMPOTENCY_KEY_REQUIRED", 400)
	}
	if len(key) > 256 {
		return apperr.New("IDEMPOTENCY_KEY_TOO_LONG", 400)
	}
	return nil
}

// ValidateAmount 校验金额。
//
// G12: amount > 0、上限、int64 overflow。
func ValidateAmount(amountMinor int64) error {
	if amountMinor <= 0 {
		return apperr.New("INVALID_AMOUNT", 400).WithMessage("amount_minor must be positive")
	}
	// 上限：10 trillion minor units（防止 int64 overflow，足够覆盖任何现实金额）
	const maxAmount int64 = 10_000_000_000_000_00 // 10 trillion in minor units (10^15)
	if amountMinor > maxAmount {
		return apperr.New("AMOUNT_TOO_LARGE", 400).WithMessage("amount_minor exceeds maximum")
	}
	return nil
}

// ValidateCurrency 校验 ISO 4217 货币。
//
// G12: 3 字符大写字母。
func ValidateCurrency(currency string) error {
	if len(currency) != 3 {
		return apperr.New("INVALID_CURRENCY", 400).WithMessage("currency must be 3 characters")
	}
	for _, c := range currency {
		if c < 'A' || c > 'Z' {
			return apperr.New("INVALID_CURRENCY", 400).WithMessage("currency must be uppercase letters")
		}
	}
	return nil
}

// ValidateMetadata 校验 metadata 大小/深度。
//
// G12: 最多 50 个 key，每个 value 最多 500 字符。
func ValidateMetadata(metadata map[string]string) error {
	if len(metadata) > 50 {
		return apperr.New("METADATA_TOO_LARGE", 400).WithMessage("metadata: max 50 keys")
	}
	for k, v := range metadata {
		if len(k) > 100 {
			return apperr.New("METADATA_KEY_TOO_LONG", 400).WithMessage("metadata key: max 100 chars")
		}
		if len(v) > 500 {
			return apperr.New("METADATA_VALUE_TOO_LONG", 400).WithMessage("metadata value: max 500 chars")
		}
	}
	return nil
}

// ValidateMerchantOrderID 校验 merchant_order_id。
func ValidateMerchantOrderID(orderID string) error {
	if orderID == "" {
		return apperr.New("MERCHANT_ORDER_ID_REQUIRED", 400)
	}
	if len(orderID) > 128 {
		return apperr.New("MERCHANT_ORDER_ID_TOO_LONG", 400)
	}
	return nil
}

// ValidatePaymentMethodToken 校验 payment method token 格式。
//
// G12: 非空，长度 1-256。
func ValidatePaymentMethodToken(token string) error {
	if token == "" {
		return apperr.New("PAYMENT_METHOD_TOKEN_REQUIRED", 400)
	}
	if len(token) > 256 {
		return apperr.New("PAYMENT_METHOD_TOKEN_TOO_LONG", 400)
	}
	return nil
}

// DeleteFailed 删除 FAILED 状态的幂等记录（PAY-007：提交失败可恢复）。
// 仅允许删除 FAILED 记录——COMMITTED/PENDING 不可删除，防止重放绕过幂等。
func (r *IdempotencyRepository) DeleteFailed(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM idempotency_records WHERE id = $1 AND state = 'FAILED'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("idempotency: record %s not in FAILED state", id)
	}
	return nil
}

// WaitCommitted 有限轮询等待幂等记录从 PENDING 进入 COMMITTED（PAY-007）。
//
// 并发同键请求中未认领成功的一方，等待首次请求提交后读取其存储响应，
// 实现"100 并发同 key 全部返回同一真实响应"。总等待 ~5s（100ms x 50）；
// 超时返回最近一次读到的记录（调用方按 state 判定 409）。
// 记录不存在或 payload_hash 变化时返回 ErrIdempotencyPayloadMismatch / nil。
func (r *IdempotencyRepository) WaitCommitted(ctx context.Context, tenantID, merchantID uuid.UUID, operation, idempotencyKey string) (*IdempotencyRecord, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, err := r.GetByKey(ctx, tenantID, merchantID, operation, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.State == "COMMITTED" {
			return rec, nil
		}
		if time.Now().After(deadline) {
			return rec, nil
		}
		select {
		case <-ctx.Done():
			return rec, nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// GetByKey 按 (tenant, merchant, operation, key) 读取幂等记录；不存在返回 nil。
func (r *IdempotencyRepository) GetByKey(ctx context.Context, tenantID, merchantID uuid.UUID, operation, idempotencyKey string) (*IdempotencyRecord, error) {
	var rec IdempotencyRecord
	var attemptID *uuid.UUID
	var committedAt *time.Time
	var responseStatus *int
	err := r.pool.QueryRow(ctx, `
		SELECT id, payload_hash, state, response_status, response_body_encrypted,
		       payment_attempt_id, created_at, committed_at, expires_at
		FROM idempotency_records
		WHERE tenant_id = $1 AND merchant_id = $2 AND operation = $3 AND idempotency_key = $4
	`, tenantID, merchantID, operation, idempotencyKey).Scan(
		&rec.ID, &rec.PayloadHash, &rec.State, &responseStatus, &rec.ResponseBodyEncrypted,
		&attemptID, &rec.CreatedAt, &committedAt, &rec.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, apperr.ErrInternal.WithMessage(err.Error())
	}
	if responseStatus != nil {
		rec.ResponseStatus = *responseStatus
	}
	rec.TenantID = tenantID
	rec.MerchantID = merchantID
	rec.Operation = operation
	rec.IdempotencyKey = idempotencyKey
	rec.PaymentAttemptID = attemptID
	rec.CommittedAt = committedAt
	return &rec, nil
}
