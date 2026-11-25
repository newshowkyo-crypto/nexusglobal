package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexusglobal/mamr/internal/checkout/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/internal/platform/httpx/middleware"
)

type PaymentIntentHandler struct {
	service *CheckoutService
}

func NewPaymentIntentHandler(service *CheckoutService) *PaymentIntentHandler {
	return &PaymentIntentHandler{service: service}
}

// CreatePaymentIntent POST /api/v3/payment_intents
//
// G12: 并发幂等
//   - 强制 Idempotency-Key
//   - scope = tenant + merchant + operation + key
//   - 并发认领使用 INSERT ... ON CONFLICT
//   - 同 payload 返回首次稳定响应，不同 payload 返回 409
//   - 100 并发相同 idempotency key：一个 attempt、一个 outbox、一个 provider idempotency key
func (h *PaymentIntentHandler) CreatePaymentIntent(w http.ResponseWriter, r *http.Request) {
	// P0-2: 支付入口封锁检查（HTTP 层）。
	// 封锁时返回 503 + SYSTEM_LOCKDOWN_FAIL_CLOSED，不进入业务逻辑。
	if err := h.service.admissionCheck(r.Context()); err != nil {
		httpx.WriteError(w, apperr.New(failclosed.SystemLockdownCode, http.StatusServiceUnavailable))
		return
	}

	mc, ok := middleware.GetMerchantContext(r.Context())
	if !ok {
		httpx.WriteError(w, apperr.New("MERCHANT_AUTH_REQUIRED", http.StatusUnauthorized))
		return
	}

	// G12: 强制 Idempotency-Key
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if err := ValidateIdempotencyKey(idempotencyKey); err != nil {
		httpx.WriteError(w, err.(*apperr.Error))
		return
	}

	var req struct {
		MerchantOrderID    string               `json:"merchant_order_id"`
		AmountMinor        int64                `json:"amount_minor"`
		Currency           string               `json:"currency"`
		PaymentMethodToken string               `json:"payment_method_token"`
		Customer           *domain.CustomerInfo `json:"customer,omitempty"`
		Metadata           map[string]string    `json:"metadata,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, apperr.New("INVALID_REQUEST", http.StatusBadRequest))
		return
	}

	// G12: 校验所有输入
	if err := ValidateMerchantOrderID(req.MerchantOrderID); err != nil {
		httpx.WriteError(w, err.(*apperr.Error))
		return
	}
	if err := ValidateAmount(req.AmountMinor); err != nil {
		httpx.WriteError(w, err.(*apperr.Error))
		return
	}
	if err := ValidateCurrency(req.Currency); err != nil {
		httpx.WriteError(w, err.(*apperr.Error))
		return
	}
	if err := ValidatePaymentMethodToken(req.PaymentMethodToken); err != nil {
		httpx.WriteError(w, err.(*apperr.Error))
		return
	}
	if err := ValidateMetadata(req.Metadata); err != nil {
		httpx.WriteError(w, err.(*apperr.Error))
		return
	}

	// G12: 规范化 payload 并计算 hash
	canonicalPayload, err := CanonicalizePayload(req)
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal)
		return
	}
	payloadHash := HashPayload(canonicalPayload)

	result, err := h.service.ProcessPaymentIntent(r.Context(), ProcessPaymentIntentRequest{
		MerchantID:         mc.MerchantID,
		IdempotencyKey:     idempotencyKey,
		PayloadHash:        payloadHash,
		MerchantOrderID:    req.MerchantOrderID,
		AmountMinor:        req.AmountMinor,
		Currency:           req.Currency,
		PaymentMethodToken: req.PaymentMethodToken,
		Customer:           req.Customer,
		Metadata:           req.Metadata,
	})
	if err != nil {
		if err == ErrIdempotencyPayloadMismatch {
			httpx.WriteError(w, apperr.New("IDEMPOTENCY_PAYLOAD_MISMATCH", http.StatusConflict))
		} else if appErr, ok := apperr.AsAPIError(err); ok {
			httpx.WriteError(w, appErr)
		} else {
			httpx.WriteError(w, apperr.ErrInternal)
		}
		return
	}

	// G09: 禁止缓存
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"payment_intent_id":  result.PaymentIntentID,
		"payment_attempt_id": result.PaymentAttemptID,
		"status":             result.Status,
	})
}

type ProcessPaymentIntentRequest struct {
	MerchantID         uuid.UUID
	IdempotencyKey     string
	PayloadHash        string
	MerchantOrderID    string
	AmountMinor        int64
	Currency           string
	PaymentMethodToken string
	Customer           *domain.CustomerInfo
	Metadata           map[string]string
	SiteID             string
	BusinessLine       string
}

type ProcessPaymentIntentResult struct {
	PaymentIntentID  string
	PaymentAttemptID string
	Status           string
}

var ErrIdempotencyPayloadMismatch = apperr.New("IDEMPOTENCY_PAYLOAD_MISMATCH", http.StatusConflict)

// ErrIdempotencyInFlight 幂等记录存在但无可用首次响应（原请求处理中或崩溃遗留，
// 或旧记录未存储响应体）。客户端应稍后用查询接口获取结果（PAY-007）。
var ErrIdempotencyInFlight = apperr.New("IDEMPOTENCY_REQUEST_IN_PROGRESS", http.StatusConflict)

// ProcessPaymentIntent 处理支付意图（G12 并发幂等）。
//
// G12 核心逻辑：
//  1. ClaimOrGet 并发认领（INSERT ... ON CONFLICT）
//  2. 如果新认领：创建 payment_intent + payment_attempt，提交响应
//  3. 如果已存在且 payload_hash 相同：返回首次响应（幂等）
//  4. 如果已存在且 payload_hash 不同：返回 409
//
// P0-2: 在处理请求最开始调用 AdmissionCheck(ctx)，封锁时返回 ErrSystemLockdown。
//
// RED-01: DB 事务内禁止外部网络 I/O（provider 调用在事务外由 worker 执行）
// RED-10: 幂等键 UNIQUE，重复事件不重复入账
func (s *CheckoutService) ProcessPaymentIntent(ctx context.Context, req ProcessPaymentIntentRequest) (*ProcessPaymentIntentResult, error) {
	// P0-A4: Token Encryptor 缺失时硬拒绝。不得创建 payment_intent、payment_attempt、
	// ledger reservation 或 Outbox。
	if req.PaymentMethodToken != "" && s.tokenEncryptor == nil {
		return nil, errors.New("payment_method_token encryption is required")
	}

	// P0-2: 支付入口封锁检查。封锁时返回 ErrSystemLockdown，HTTP 503。
	if err := s.admissionCheck(ctx); err != nil {
		return nil, err
	}

	// P0-3: channelRegistry 必须非 nil，fail-closed。在 ClaimOrGet 之前检查，确保不产生幂等记录。
	if s.channelRegistry == nil {
		return nil, fmt.Errorf("checkout: channel registry not configured, fail-closed")
	}

	if err := ValidatePaymentMethodToken(req.PaymentMethodToken); err != nil {
		return nil, err
	}

	tenantID := req.MerchantID // 简化：tenant = merchant

	idempotencyRepo := NewIdempotencyRepository(s.pool)

	// G12: 并发认领
	record, claimed, err := idempotencyRepo.ClaimOrGet(ctx, tenantID, req.MerchantID, OpCreatePaymentIntent, req.IdempotencyKey, req.PayloadHash)
	if err != nil {
		if err == ErrIdempotencyPayloadMismatch {
			return nil, ErrIdempotencyPayloadMismatch
		}
		return nil, err
	}

	// PAY-007: FAILED 记录可恢复——上次提交失败，删除后重新认领执行。
	if !claimed && record.State == "FAILED" {
		if delErr := idempotencyRepo.DeleteFailed(ctx, record.ID); delErr == nil {
			record, claimed, err = idempotencyRepo.ClaimOrGet(ctx, tenantID, req.MerchantID, OpCreatePaymentIntent, req.IdempotencyKey, req.PayloadHash)
			if err != nil {
				if err == ErrIdempotencyPayloadMismatch {
					return nil, ErrIdempotencyPayloadMismatch
				}
				return nil, err
			}
		}
	}

	if !claimed {
		// PAY-006/007: 幂等语义按记录状态区分。
		//   COMMITTED → 返回首次真实响应（解密存储体，含真实 intent ID）。
		//   PENDING（原请求仍在处理中）→ 有限等待其提交后返回同一响应；
		//     超时仍未提交（崩溃遗留）→ 409，客户端可安全重试。
		if record.State != "COMMITTED" {
			record, err = idempotencyRepo.WaitCommitted(ctx, tenantID, req.MerchantID, OpCreatePaymentIntent, req.IdempotencyKey)
			if err != nil || record == nil || record.State != "COMMITTED" || len(record.ResponseBodyEncrypted) == 0 {
				return nil, ErrIdempotencyInFlight
			}
		}
		if len(record.ResponseBodyEncrypted) == 0 || s.tokenEncryptor == nil {
			return nil, ErrIdempotencyInFlight
		}
		bodyStr, decErr := s.tokenEncryptor.DecryptToken(ctx, tenantID, record.ID, record.ResponseBodyEncrypted)
		if decErr != nil {
			return nil, apperr.ErrInternal.WithMessage("idempotency: decrypt stored response failed")
		}
		var firstResponse map[string]string
		if json.Unmarshal([]byte(bodyStr), &firstResponse) != nil {
			return nil, apperr.ErrInternal.WithMessage("idempotency: stored response unreadable")
		}
		attemptIDStr := ""
		if record.PaymentAttemptID != nil {
			attemptIDStr = record.PaymentAttemptID.String()
		}
		return &ProcessPaymentIntentResult{
			PaymentIntentID:  firstResponse["payment_intent_id"],
			PaymentAttemptID: attemptIDStr,
			Status:           firstResponse["status"],
		}, nil
	}

	// 新认领：创建 payment_intent + payment_attempt
	// 使用 mutex 保护事务（虽然 ClaimOrGet 已经原子认领，但创建过程需要事务）
	var result *ProcessPaymentIntentResult
	var processErr error

	processOnce := sync.Once{}
	processOnce.Do(func() {
		result, processErr = s.createPaymentIntentInternal(ctx, req, record.ID)
	})

	if processErr != nil {
		// 标记幂等记录为失败（PAY-007：FAILED 可被后续同键请求恢复重试）
		if failErr := idempotencyRepo.Fail(ctx, record.ID); failErr != nil {
			// Fail 失败不阻断主流程：记录留在 PENDING 会由 409 语义兜底
			slog.Warn("payment intent: mark idempotency record failed", "record_id", record.ID, "err", failErr)
		}
		return nil, processErr
	}

	// 提交幂等记录
	attemptUUID, _ := uuid.Parse(result.PaymentAttemptID)
	var attemptPtr *uuid.UUID
	if attemptUUID != uuid.Nil {
		attemptPtr = &attemptUUID
	}
	responseBody, _ := json.Marshal(map[string]string{
		"payment_intent_id":  result.PaymentIntentID,
		"payment_attempt_id": result.PaymentAttemptID,
		"status":             result.Status,
	})

	// PAY-008: 响应体必须加密后存入 response_body_encrypted（列名即契约），
	// 旧实现存明文。Commit 失败显式返回 500——记录停在 PENDING 且业务已创建，
	// 客户端重放会得到 409 并可安全查询，不能伪装成功。
	storedBody := responseBody
	if s.tokenEncryptor != nil {
		enc, encErr := s.tokenEncryptor.EncryptToken(ctx, tenantID, record.ID, string(responseBody))
		if encErr != nil {
			return nil, apperr.ErrInternal.WithMessage("idempotency: encrypt response failed")
		}
		storedBody = []byte(enc)
	}
	if commitErr := idempotencyRepo.Commit(ctx, record.ID, http.StatusOK, storedBody, attemptPtr); commitErr != nil {
		slog.Error("payment intent: commit idempotency record failed",
			"record_id", record.ID, "err", commitErr)
		return result, nil // 业务创建已成功；记录未提交则重放走 409 恢复路径
	}

	return result, nil
}

// createPaymentIntentInternal 内部创建 payment_intent + payment_attempt。
//
// P0-3: 加密必须在开启数据库事务前完成；事务内只写加密结果。
// P0-5: 在同一事务内写入：
//   - checkout_order
//   - payment_attempt
//   - ledger reservation
//   - idempotency record
//   - PAYMENT_DISPATCH_REQUESTED Outbox
//
// RED-01: DB 事务内禁止外部网络 I/O。
func (s *CheckoutService) createPaymentIntentInternal(ctx context.Context, req ProcessPaymentIntentRequest, idempotencyRecordID uuid.UUID) (*ProcessPaymentIntentResult, error) {
	// P0-A4: Token Encryptor 缺失时硬拒绝。不得创建 payment_intent、payment_attempt、
	// ledger reservation 或 Outbox。
	if req.PaymentMethodToken != "" && s.tokenEncryptor == nil {
		return nil, errors.New("payment_method_token encryption is required")
	}

	// P0-1 (R5): attemptID 必须在加密之前生成。
	// AES-GCM AAD 绑定 attempt ID：加密与落库必须使用同一个 ID，
	// 否则解密时以 attempt.ID 作为 AAD 永远失败（AAD mismatch）。
	attemptID := uuid.New()

	// P0-3: 加密 token 在事务外完成（加密可能涉及 KMS 网络调用，禁止在事务内执行）。
	var encryptedToken []byte
	if s.tokenEncryptor != nil && req.PaymentMethodToken != "" {
		var encErr error
		encryptedToken, encErr = s.tokenEncryptor.EncryptToken(ctx, req.MerchantID, attemptID, req.PaymentMethodToken)
		if encErr != nil {
			return nil, fmt.Errorf("encrypt payment_method_token: %w", encErr)
		}
	}

	// P0-3/P1: 通过支付池或 ChannelRegistry 选择通道（事务外执行，可能涉及 DB 查询）
	acquirerChannelID, poolChannelID, err := s.resolveAcquirerChannel(
		ctx, req.MerchantID, req.Currency, req.AmountMinor, attemptID, req.SiteID, req.BusinessLine)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// 检查是否已存在（双重保护，防止并发 race）
	var existingID string
	var existingStatus string
	var existingAttemptID *uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id::text, status, payment_attempt_id FROM payment_intents
		WHERE merchant_id = $1 AND idempotency_key = $2
	`, req.MerchantID, req.IdempotencyKey).Scan(&existingID, &existingStatus, &existingAttemptID)

	switch {
	case err == nil:
		// 已存在，幂等返回
		var attemptStr string
		if existingAttemptID != nil {
			attemptStr = existingAttemptID.String()
		}
		return &ProcessPaymentIntentResult{
			PaymentIntentID:  existingID,
			PaymentAttemptID: attemptStr,
			Status:           existingStatus,
		}, nil
	case err != pgx.ErrNoRows:
		return nil, err
	}

	paymentIntentID := uuid.New()
	// P0-1 (R5): attemptID 已在函数开头（加密前）生成，此处不再重复生成。
	orderID := uuid.New()
	tenantID := req.MerchantID

	// G10: 创建 checkout_order（S2S 入口）
	_, err = tx.Exec(ctx, `
		INSERT INTO checkout_orders (id, tenant_id, merchant_id, merchant_order_id, amount_minor, currency, status, success_url, cancel_url, customer, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', '', '', $7, $8)
		ON CONFLICT (merchant_id, merchant_order_id) DO UPDATE SET updated_at = NOW()
	`, orderID, tenantID, req.MerchantID, req.MerchantOrderID, req.AmountMinor, req.Currency, req.Customer, req.Metadata)
	if err != nil {
		return nil, err
	}

	// 重新查询 order_id（可能因 ON CONFLICT 已存在）
	err = tx.QueryRow(ctx, `
		SELECT id FROM checkout_orders WHERE merchant_id = $1 AND merchant_order_id = $2
	`, req.MerchantID, req.MerchantOrderID).Scan(&orderID)
	if err != nil {
		return nil, err
	}

	// G12: 生成 provider idempotency key（一个 attempt → 一个 provider key）
	providerIdempotencyKey := ProviderIdempotencyKey(attemptID)

	// G10: 创建 payment_attempt（S2S 入口）
	// P0-3: payment_method_token 列只写空字符串，加密 token 写入 payment_method_token_encrypted。
	// P0-3: acquirer_channel_id 由 SelectChannel 选择，写入 attempt。
	_, err = tx.Exec(ctx, `
		INSERT INTO payment_attempts (
			id, order_id, merchant_id, tenant_id, acquirer_channel_id, entry_channel, entry_ref,
			merchant_order_id, amount_minor, currency, status,
			payment_method_token, payment_method_token_encrypted, provider_idempotency_key,
			customer, metadata, pool_channel_id, version
		)
		VALUES ($1, $2, $3, $4, $5, 'S2S', $6, $7, $8, $9, 'REQUIRES_ACTION', '', $10, $11, $12, $13, $14, 0)
	`, attemptID, orderID, req.MerchantID, tenantID, acquirerChannelID, req.IdempotencyKey,
		req.MerchantOrderID, req.AmountMinor, req.Currency,
		encryptedToken, providerIdempotencyKey,
		req.Customer, req.Metadata, nullableUUID(poolChannelID))
	if err != nil {
		return nil, err
	}

	// SEC-006: 收单（acquiring）不再在 intent 创建时冻结商户既有 SETTLED 余额。
	// 旧 ReserveTx 语义（SETTLED→FROZEN_PAYOUT）是出金预留，不是收单应收。
	// 收单正确事件：captured 时由统一 Webhook consumer 执行 AcquireCaptureTx
	//（Dr PROVIDER_RECEIVABLE / Cr SETTLED，商户无需预存余额）；failed 无资金移动。
	ledgerReservationID := ""

	_, err = tx.Exec(ctx, `
		INSERT INTO payment_intents (id, merchant_id, merchant_order_id, idempotency_key, payload_hash, amount_minor, currency, status, payment_method_token, customer, metadata, ledger_reservation_id, payment_attempt_id, tenant_id, provider_idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '', $9, $10, $11, $12, $13, $14)
	`, paymentIntentID, req.MerchantID, req.MerchantOrderID, req.IdempotencyKey, req.PayloadHash,
		req.AmountMinor, req.Currency, "REQUIRES_ACTION", req.Customer, req.Metadata,
		ledgerReservationID, attemptID, tenantID, providerIdempotencyKey)
	if err != nil {
		return nil, err
	}

	// P0-4: 在同一事务内写入 PAYMENT_DISPATCH_REQUESTED Outbox。
	// Outbox payload 只包含 payment_attempt_id，不包含金额、商户、币种、账本 ID、token。
	// Worker 根据 attempt_id 从数据库重新加载全部权威字段。
	// token 不得进入：普通日志、Outbox JSON、error message、audit snapshot。
	// RED-01: 事务内不调用 Provider，仅写 outbox。
	dispatchPayload, _ := json.Marshal(map[string]interface{}{
		"payment_attempt_id": attemptID.String(),
	})
	outboxID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ($1, 'payment_attempt', $2, 'PAYMENT_DISPATCH_REQUESTED', $3, $4, 'Pending')
	`, outboxID, attemptID, dispatchPayload, "dispatch_"+attemptID.String())
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &ProcessPaymentIntentResult{
		PaymentIntentID:  paymentIntentID.String(),
		PaymentAttemptID: attemptID.String(),
		Status:           "REQUIRES_ACTION",
	}, nil
}
