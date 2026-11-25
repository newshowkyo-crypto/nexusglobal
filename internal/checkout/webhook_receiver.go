package checkout

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	merchantwebhook "github.com/nexusglobal/mamr/internal/merchantportal/webhook"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// ============================================================================
// Webhook 统一 Receiver
//
// 验证签名、保存加密原始事件、入队/stream
// consumer 处理幂等并推进 attempt/ledger
// 重复事件不得重复分录（RED-10）
// timeout 进入 UNKNOWN，不盲目重试（RED-03）
// ============================================================================

// ErrNoProviderKey 指定 provider/channel 没有注册 WEBHOOK_SECRET 凭证。
// 生产环境（key resolver 启用）下收到此错误的 webhook 必须 401 拒绝。
var ErrNoProviderKey = errors.New("webhook: no per-provider signing key registered")

// ErrWebhookRejected 事件未通过归属校验（provider/金额/状态回退），
// 事件被标记 REJECTED，不得推进 attempt。
var ErrWebhookRejected = errors.New("webhook: ownership verification failed")

// SigningKeyResolver 按 provider 身份解析验签密钥（SEC-007）。
// 生产实现应从 provider_credential_vault（credential_type='WEBHOOK_SECRET'，
// AES-256-GCM 加密）读取，禁止全 Provider 共享单一密钥。
type SigningKeyResolver interface {
	SigningKey(ctx context.Context, providerType, providerID string) ([]byte, error)
}

// LedgerConvergence webhook 事件驱动账本收敛（SEC-010/006）。
// 由 internal/ledger/service.Service 实现（方法签名一致）。
type LedgerConvergence interface {
	CaptureTx(ctx context.Context, tx pgx.Tx, reservationID string, captureAmountMinor int64, transactionID string) (string, error)
	ReleaseTx(ctx context.Context, tx pgx.Tx, reservationID string, transactionID string) (string, error)
	AcquireCaptureTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error)
}

// UnifiedWebhookReceiver 统一 Webhook Receiver。
//
// P0-6: 使用 PayloadEncryptor 接口（AES-256-GCM）替代 XOR 加密。
// 加密密钥缺失时 fail closed（拒绝接收 webhook）。
// SEC-007: keyResolver 非 nil 时按 provider 解析独立密钥；为 nil 时（仅测试/local）
// 回退全局 signingKey。
type UnifiedWebhookReceiver struct {
	pool        *pgxpool.Pool
	signingKey  []byte
	keyResolver SigningKeyResolver // SEC-007: per-provider key；nil = 仅测试/local 回退全局
	encryptor   PayloadEncryptor   // P0-6: AES-256-GCM payload 加密器
	ledger      LedgerConvergence  // SEC-010: capture/release 收敛；nil = 不收敛（测试）
	log         *slog.Logger
}

// NewUnifiedWebhookReceiver 创建统一 Webhook Receiver（测试/local：全局密钥验签）。
func NewUnifiedWebhookReceiver(pool *pgxpool.Pool, signingKey []byte, log *slog.Logger) *UnifiedWebhookReceiver {
	if log == nil {
		log = slog.Default()
	}
	return &UnifiedWebhookReceiver{
		pool:       pool,
		signingKey: signingKey,
		encryptor:  nil,
		log:        log,
	}
}

// NewUnifiedWebhookReceiverWithEncryptor 创建带显式 PayloadEncryptor 的 receiver（全局密钥）。
func NewUnifiedWebhookReceiverWithEncryptor(pool *pgxpool.Pool, signingKey []byte, encryptor PayloadEncryptor, log *slog.Logger) *UnifiedWebhookReceiver {
	if log == nil {
		log = slog.Default()
	}
	return &UnifiedWebhookReceiver{
		pool:       pool,
		signingKey: signingKey,
		encryptor:  encryptor,
		log:        log,
	}
}

// NewUnifiedWebhookReceiverSecure 生产构造函数（SEC-007/008/010）。
//
//   - keyResolver：per-provider 验签密钥（必填；nil 时 panic 防误用）。
//   - allowGlobalKeyFallback：仅允许在 local/test 为 true；production 必须 false，
//     此时未注册 per-provider 密钥的 webhook 一律 401。
//   - ledger：账本收敛接口（capture/release），可 nil（仅事件推进测试）。
func NewUnifiedWebhookReceiverSecure(pool *pgxpool.Pool, keyResolver SigningKeyResolver, allowGlobalKeyFallback bool, signingKey []byte, encryptor PayloadEncryptor, ledger LedgerConvergence, log *slog.Logger) *UnifiedWebhookReceiver {
	if keyResolver == nil {
		panic("webhook: NewUnifiedWebhookReceiverSecure requires a SigningKeyResolver (SEC-007)")
	}
	if log == nil {
		log = slog.Default()
	}
	return &UnifiedWebhookReceiver{
		pool:        pool,
		signingKey:  signingKey,
		keyResolver: keyResolver,
		encryptor:   encryptor,
		ledger:      ledger,
		log:         log,
	}
}

// Encryptor 返回底层 encryptor（供 consumer 复用）。
func (h *UnifiedWebhookReceiver) Encryptor() PayloadEncryptor { return h.encryptor }

// UseKeyResolver 为已构造的 receiver 升级 per-provider 密钥解析与账本收敛
// （local/test 装配路径；production 应直接使用 NewUnifiedWebhookReceiverSecure）。
func (h *UnifiedWebhookReceiver) UseKeyResolver(resolver SigningKeyResolver, ledger LedgerConvergence) {
	h.keyResolver = resolver
	if ledger != nil {
		h.ledger = ledger
	}
}

// WebhookEventPayload webhook 事件 payload 结构。
type WebhookEventPayload struct {
	EventType       string          `json:"event_type"`
	ProviderEventID string          `json:"provider_event_id"`
	AttemptID       string          `json:"attempt_id"`
	Status          string          `json:"status"`
	ProviderRef     string          `json:"provider_ref"`
	AmountMinor     int64           `json:"amount_minor"`
	Currency        string          `json:"currency"`
	RedirectURL     string          `json:"redirect_url,omitempty"`
	ActionType      string          `json:"action_type,omitempty"`
	FailureCode     string          `json:"failure_code,omitempty"`
	FailureReason   string          `json:"failure_reason,omitempty"`
	Sequence        int64           `json:"sequence,omitempty"`
	Raw             json.RawMessage `json:"raw,omitempty"`
}

// HandleWebhook POST /webhook/unified/{provider_type}/{provider_id}
//
// 统一 Webhook Receiver（P0-5）：
//  1. 验证 HMAC-SHA256 签名
//  2. 保存加密原始事件（idempotency_key UNIQUE 去重）
//  3. 入队/stream（consumer 异步处理）
//  4. 重复事件返回 200 + DUPLICATED（不重复处理）
//  5. 立即返回 2xx，不在 HTTP Handler 同步推进支付状态
//
// RED-01: HTTP Handler 内不调用 Provider、不推进 attempt 状态（由 consumer 异步处理）
// RED-10: 幂等键 UNIQUE，重复事件不重复入账
// RED-15: 敏感字段加密 + json:"-"
func (h *UnifiedWebhookReceiver) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	providerType := r.PathValue("provider_type")
	providerID := r.PathValue("provider_id")
	if providerType == "" || providerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "INVALID_PROVIDER"})
		return
	}

	signature := r.Header.Get("X-Signature")
	if signature == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "MISSING_SIGNATURE"})
		return
	}

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "INVALID_BODY"})
		return
	}

	// 1. 验证 HMAC-SHA256 签名（SEC-007：优先 per-provider 密钥）
	if !h.verifySignatureFor(r.Context(), providerType, providerID, payload, signature) {
		h.log.Warn("webhook: signature verification failed",
			"provider_type", providerType, "provider_id", providerID)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "INVALID_SIGNATURE"})
		return
	}

	// 2. 解析 payload
	var event WebhookEventPayload
	if err := json.Unmarshal(payload, &event); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "INVALID_JSON"})
		return
	}

	if event.ProviderEventID == "" {
		event.ProviderEventID = r.Header.Get("X-Provider-Event-Id")
	}

	// 3. 计算幂等键
	payloadHash := HashPayload(payload)
	idempotencyKey := h.computeWebhookIdempotencyKey(providerType, providerID, event.ProviderEventID, event.EventType, payloadHash)

	// 4. 保存加密原始事件（去重）+ 入队（事务内）
	eventID, duplicated, err := h.saveEventAndEnqueue(r.Context(), providerType, providerID, &event, payload, payloadHash, idempotencyKey, signature)
	if err != nil {
		// P0-6: 日志中不输出原始敏感 payload，仅记录 provider_type 和错误类型
		h.log.Error("webhook: save event failed",
			"err", err.Error(),
			"provider_type", providerType,
			"provider_id", providerID,
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "INTERNAL_ERROR"})
		return
	}

	if duplicated {
		// RED-10: 重复事件不重复处理，返回 200
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"received":        true,
			"ingress_id":      eventID.String(),
			"idempotency_key": idempotencyKey,
			"duplicated":      true,
		})
		return
	}

	// P0-5: 立即返回 2xx，不在 HTTP Handler 同步推进支付状态。
	// Consumer 会异步处理事件（验签已通过、已持久化、已入队）。
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"received":        true,
		"ingress_id":      eventID.String(),
		"idempotency_key": idempotencyKey,
		"duplicated":      false,
	})
}

// verifySignature 全局密钥验签（仅 keyResolver == nil 的测试/local 场景）。
func (h *UnifiedWebhookReceiver) verifySignature(payload []byte, signature string) bool {
	return h.verifySignatureWith(h.signingKey, payload, signature)
}

// verifySignatureFor 按 provider 解析密钥后验签（SEC-007）。
// keyResolver 已配置：必须存在该 provider 的独立密钥，否则拒绝（ErrNoProviderKey）；
// 无任何密钥回退（fail closed）。keyResolver 为 nil（测试/local）：回退全局密钥。
func (h *UnifiedWebhookReceiver) verifySignatureFor(ctx context.Context, providerType, providerID string, payload []byte, signature string) bool {
	if h.keyResolver != nil {
		key, err := h.keyResolver.SigningKey(ctx, providerType, providerID)
		if err != nil {
			h.log.Warn("webhook: per-provider signing key unavailable",
				"provider_type", providerType, "provider_id", providerID, "err", err.Error())
			return false
		}
		return h.verifySignatureWith(key, payload, signature)
	}
	return h.verifySignature(payload, signature)
}

// verifySignatureWith 用指定密钥做恒时 HMAC-SHA256 比对。
func (h *UnifiedWebhookReceiver) verifySignatureWith(key []byte, payload []byte, signature string) bool {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	// 恒时比对
	return hmac.Equal([]byte(expectedMAC), []byte(signature))
}

// computeWebhookIdempotencyKey 计算 webhook 幂等键。
func (h *UnifiedWebhookReceiver) computeWebhookIdempotencyKey(providerType, providerID, providerEventID, eventType, payloadHash string) string {
	return fmt.Sprintf("wh:%s:%s:%s:%s:%s", providerType, providerID, providerEventID, eventType, payloadHash)
}

// saveEvent 保存事件（去重）。
//
// RED-10: idempotency_key UNIQUE，重复事件不重复入账。
// RED-15: payload 加密存储（AES-256-GCM）。
// P0-6: 加密密钥缺失时 fail closed（拒绝接收，禁止明文回退）。
func (h *UnifiedWebhookReceiver) saveEvent(ctx context.Context, providerType, providerID string, event *WebhookEventPayload, payload []byte, payloadHash, idempotencyKey, signature string) (eventID uuid.UUID, duplicated bool, err error) {
	// P0-6: 加密密钥缺失时 fail closed
	if h.encryptor == nil {
		return uuid.Nil, false, fmt.Errorf("%w: webhook payload encryptor not configured, refusing to store plaintext", ErrEncryptionKeyMissing)
	}

	signatureHash := HashPayload([]byte(signature))

	// P0-6: 使用 AES-256-GCM 加密 payload（替代 XOR）
	encryptedPayload, err := h.encryptor.Encrypt(payload)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("encrypt webhook payload: %w", err)
	}

	// INSERT ... ON CONFLICT DO NOTHING（去重）
	var insertedID *uuid.UUID
	err = h.pool.QueryRow(ctx, `
		INSERT INTO unified_webhook_events (
			provider_type, provider_id, provider_event_id, event_type,
			signature_hash, payload_encrypted, payload_hash, idempotency_key,
			status, provider_sequence
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'RECEIVED', $9)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`, providerType, providerID, event.ProviderEventID, event.EventType,
		signatureHash, encryptedPayload, payloadHash, idempotencyKey, event.Sequence).Scan(&insertedID)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT：重复事件
			var existingID uuid.UUID
			err = h.pool.QueryRow(ctx, `
				SELECT id FROM unified_webhook_events WHERE idempotency_key = $1
			`, idempotencyKey).Scan(&existingID)
			if err != nil {
				return uuid.Nil, false, err
			}
			return existingID, true, nil
		}
		return uuid.Nil, false, err
	}

	return *insertedID, false, nil
}

// saveEventAndEnqueue 在同一事务内保存事件并写入 outbox（P0-5）。
//
// 流程：
//  1. 加密 payload（AES-256-GCM）
//  2. INSERT unified_webhook_events（ON CONFLICT DO NOTHING 去重）
//  3. 若新事件：INSERT transactional_outbox（WEBHOOK_EVENT_RECEIVED）
//  4. 提交事务
//
// RED-01: 事务内不调用外部网络（仅 DB 写入）
// RED-10: idempotency_key UNIQUE，重复事件不重复入账
func (h *UnifiedWebhookReceiver) saveEventAndEnqueue(ctx context.Context, providerType, providerID string, event *WebhookEventPayload, payload []byte, payloadHash, idempotencyKey, signature string) (eventID uuid.UUID, duplicated bool, err error) {
	// P0-6: 加密密钥缺失时 fail closed
	if h.encryptor == nil {
		return uuid.Nil, false, fmt.Errorf("%w: webhook payload encryptor not configured, refusing to store plaintext", ErrEncryptionKeyMissing)
	}

	signatureHash := HashPayload([]byte(signature))

	// P0-6: 使用 AES-256-GCM 加密 payload
	encryptedPayload, err := h.encryptor.Encrypt(payload)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("encrypt webhook payload: %w", err)
	}

	// 开启事务：保存事件 + 写 outbox
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer tx.Rollback(ctx)

	// INSERT ... ON CONFLICT DO NOTHING（去重）
	var insertedID *uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO unified_webhook_events (
			provider_type, provider_id, provider_event_id, event_type,
			signature_hash, payload_encrypted, payload_hash, idempotency_key,
			status, provider_sequence
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'RECEIVED', $9)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`, providerType, providerID, event.ProviderEventID, event.EventType,
		signatureHash, encryptedPayload, payloadHash, idempotencyKey, event.Sequence).Scan(&insertedID)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT：重复事件，回滚事务（不写 outbox）。
			// 注意：必须用同一事务连接查询已存在行——若用 h.pool 会在持有一条
			// 连接的同时再取第二条，并发重复事件时耗尽连接池造成死锁。
			var existingID uuid.UUID
			err = tx.QueryRow(ctx, `
				SELECT id FROM unified_webhook_events WHERE idempotency_key = $1
			`, idempotencyKey).Scan(&existingID)
			if err != nil {
				return uuid.Nil, false, err
			}
			return existingID, true, nil
		}
		return uuid.Nil, false, err
	}

	// 写入 outbox：WEBHOOK_EVENT_RECEIVED（consumer 异步处理）
	outboxID := uuid.New()
	attemptID, _ := uuid.Parse(event.AttemptID)
	outboxPayload, _ := json.Marshal(map[string]interface{}{
		"event_id":      insertedID.String(),
		"provider_type": providerType,
		"provider_id":   providerID,
		"attempt_id":    event.AttemptID,
		"status":        event.Status,
		"provider_ref":  event.ProviderRef,
	})
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ($1, 'webhook_event', $2, 'WEBHOOK_EVENT_RECEIVED', $3, $4, 'Pending')
	`, outboxID, attemptID, outboxPayload, "wh_evt_"+insertedID.String())
	if err != nil {
		return uuid.Nil, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}

	return *insertedID, false, nil
}

// processEvent 处理 webhook 事件（SEC-008/009/010 加固版）。
//
//  1. 从 unified_webhook_events 行加载事件（payload、provider_type、provider_id）。
//  2. 解密 payload。
//  3. 从 DB 反查 attempt，并验证归属：attempt.acquirer_channel_id 对应通道的
//     provider_code 必须与 URL provider_id 一致；payload 声明的金额/币种
//     （若非零/非空）必须与 attempt 一致。任一不匹配 → 事件标记 REJECTED，
//     绝不推进 attempt（跨 provider 推进被阻断）。
//  4. 状态单调校验（SEC-009）：终态/回退事件被忽略并直接标记 PROCESSED（消费但不变更）。
//  5. 单事务内：CAS 推进 attempt + 账本收敛（captured→CaptureTx，
//     failed→ReleaseTx，SEC-010）+ 事件标记 PROCESSED。
//
// 幂等（RED-10）：重复投递要么被 idempotency 去重（receiver 侧），要么被
// 状态单调校验吸收（同状态 no-op）。
func (h *UnifiedWebhookReceiver) processEvent(ctx context.Context, eventID uuid.UUID) error {
	// 加载事件行（含 provider 身份与加密 payload）
	var encryptedPayload []byte
	var eventStatus, providerType, providerID string
	err := h.pool.QueryRow(ctx, `
		SELECT payload_encrypted, status, provider_type, provider_id
		FROM unified_webhook_events WHERE id = $1
	`, eventID).Scan(&encryptedPayload, &eventStatus, &providerType, &providerID)
	if err != nil {
		return fmt.Errorf("webhook consumer: load event: %w", err)
	}

	// 幂等：已处理的事件不重复处理
	if eventStatus == "PROCESSED" || eventStatus == "REJECTED" {
		return nil
	}

	encryptor := h.Encryptor()
	if encryptor == nil {
		_ = h.markEventStatus(ctx, eventID, "FAILED", "payload encryptor not configured")
		return errors.New("webhook consumer: encryptor not configured")
	}

	payload, err := encryptor.Decrypt(encryptedPayload)
	if err != nil {
		_ = h.markEventStatus(ctx, eventID, "FAILED", "decrypt error: "+err.Error())
		return fmt.Errorf("webhook consumer: decrypt: %w", err)
	}

	var event WebhookEventPayload
	if err := json.Unmarshal(payload, &event); err != nil {
		_ = h.markEventStatus(ctx, eventID, "FAILED", err.Error())
		return fmt.Errorf("webhook consumer: unmarshal: %w", err)
	}

	if event.AttemptID == "" {
		// 无关联 attempt，仅记录事件
		return h.markEventStatus(ctx, eventID, "PROCESSED", "")
	}

	attemptID, err := uuid.Parse(event.AttemptID)
	if err != nil {
		return fmt.Errorf("invalid attempt_id: %w", err)
	}

	attemptRepo := NewPaymentAttemptRepository(h.pool)
	attempt, err := attemptRepo.GetPaymentAttempt(ctx, attemptID)
	if err != nil {
		return fmt.Errorf("get attempt: %w", err)
	}

	// ── SEC-008: 归属校验（provider 绑定 + 金额/币种一致）──
	if err := h.verifyAttemptOwnership(ctx, attempt, providerID, &event); err != nil {
		h.log.Warn("webhook: ownership verification failed, rejecting event",
			"event_id", eventID, "provider_type", providerType,
			"provider_id", providerID, "attempt_id", attemptID,
			"err", err.Error())
		_ = h.markEventStatus(ctx, eventID, "REJECTED", err.Error())
		return nil // 事件已消费（REJECTED），不进入重试
	}

	// ── SEC-009: 状态单调校验 ──
	nextStatus, ok := mapProviderStatusToAttemptStatus(event.Status)
	if !ok {
		_ = h.markEventStatus(ctx, eventID, "REJECTED", "unknown provider status: "+event.Status)
		return nil
	}
	if !WebhookTransitionAllowed(string(attempt.Status), string(nextStatus)) {
		// 乱序/迟到/重复终态事件：消费但不变更（状态不可回退）。
		h.log.Info("webhook: transition not allowed, event consumed without state change",
			"event_id", eventID, "attempt_id", attemptID,
			"from", string(attempt.Status), "to", string(nextStatus))
		return h.markEventStatus(ctx, eventID, "PROCESSED", "transition_not_allowed")
	}

	providerResult := ProviderResult{
		Status:        event.Status,
		ProviderRef:   event.ProviderRef,
		RedirectURL:   event.RedirectURL,
		ActionType:    event.ActionType,
		FailureCode:   event.FailureCode,
		FailureReason: event.FailureReason,
	}

	// ── SEC-010: 单事务内收敛 attempt + ledger + 事件状态 ──
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("webhook consumer: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := attemptRepo.SetProviderResultTx(ctx, tx, attemptID, providerResult, attempt.Version); err != nil {
		return fmt.Errorf("update attempt status: %w", err)
	}

	// 账本收敛（SEC-010/006）：
	//   - attempt 有 ledger_reservation_id（预留型，如出金/既有预留流程）→
	//     captured→CaptureTx、failed→ReleaseTx。
	//   - 无 reservation（收单型）→ captured→AcquireCaptureTx
	//     （Dr PROVIDER_RECEIVABLE / Cr merchant SETTLED，金额取 DB 权威值）；
	//     failed→无资金移动。
	if h.ledger != nil {
		switch {
		case nextStatus == AttemptCaptured && attempt.LedgerReservationID != "":
			if _, err := h.ledger.CaptureTx(ctx, tx, attempt.LedgerReservationID,
				attempt.AmountMinor, eventID.String()); err != nil {
				return fmt.Errorf("ledger capture: %w", err)
			}
		case nextStatus == AttemptCaptured && attempt.LedgerReservationID == "":
			if _, err := h.ledger.AcquireCaptureTx(ctx, tx, attempt.MerchantID,
				attempt.AmountMinor, attempt.Currency, eventID.String()); err != nil {
				return fmt.Errorf("ledger acquire capture: %w", err)
			}
		case nextStatus == AttemptFailed && attempt.LedgerReservationID != "":
			if _, err := h.ledger.ReleaseTx(ctx, tx, attempt.LedgerReservationID,
				eventID.String()); err != nil {
				return fmt.Errorf("ledger release: %w", err)
			}
		}
	}

	// PAY-009: PAYMENT_LINK 入口的 attempt 捕获成功时，同一事务内将
	// 关联 payment_link 标记为 PAID（链接状态与资金状态原子一致）。
	if nextStatus == AttemptCaptured {
		if err := h.markPaymentLinkPaidTx(ctx, tx, attempt); err != nil {
			return fmt.Errorf("mark payment link paid: %w", err)
		}
	}

	// A2: 商户出站回调——终态事件与业务同事务写入投递表。
	if nextStatus == AttemptCaptured || nextStatus == AttemptFailed {
		eventType := merchantwebhook.EventPaymentSucceeded
		if nextStatus == AttemptFailed {
			eventType = merchantwebhook.EventPaymentFailed
		}
		payload := map[string]any{
			"payment_attempt_id": attempt.ID.String(),
			"order_id":           attempt.OrderID.String(),
			"merchant_order_id":  attempt.MerchantOrderID,
			"amount_minor":       attempt.AmountMinor,
			"currency":           attempt.Currency,
			"status":             string(nextStatus),
			"provider_ref":       event.ProviderRef,
			"failure_code":       event.FailureCode,
		}
		if err := merchantwebhook.EnqueueTx(ctx, tx, attempt.MerchantID,
			eventType, attempt.ID.String(), payload); err != nil {
			return fmt.Errorf("enqueue merchant webhook: %w", err)
		}
	}

	if err := h.markEventStatusTx(ctx, tx, eventID, "PROCESSED", ""); err != nil {
		return fmt.Errorf("mark event processed: %w", err)
	}

	return tx.Commit(ctx)
}

// markPaymentLinkPaidTx 在同一事务内将捕获成功的 PAYMENT_LINK attempt 关联
// 链接标记为 PAID。非 PAYMENT_LINK 入口或 entry_ref 非 link id 时不动作。
func (h *UnifiedWebhookReceiver) markPaymentLinkPaidTx(ctx context.Context, tx pgx.Tx, attempt *PaymentAttempt) error {
	if attempt == nil || attempt.EntryChannel != EntryPaymentLink {
		return nil
	}
	linkID, err := uuid.Parse(attempt.EntryRef)
	if err != nil {
		// entry_ref 不是 link id（历史数据），不阻断捕获收敛
		return nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE payment_links
		SET status = 'PAID', updated_at = NOW()
		WHERE id = $1 AND status IN ('ACTIVE', 'CLAIMED')
		  AND (expires_at IS NULL OR expires_at > NOW())
	`, linkID)
	return err
}

// verifyAttemptOwnership 校验 attempt 与 webhook 来源 provider 的绑定关系（SEC-008）。
//   - attempt.acquirer_channel_id 必须非空；
//   - 该通道所属 payment_provider.provider_code 必须等于 URL 的 provider_id
//     （provider_id 是 dispatch 时的 provider 身份，payload 内自声明字段一律不信任）；
//   - payload 金额/币种若声明，必须与 attempt 一致。
func (h *UnifiedWebhookReceiver) verifyAttemptOwnership(ctx context.Context, attempt *PaymentAttempt, providerID string, event *WebhookEventPayload) error {
	if attempt.AcquirerChannelID == uuid.Nil {
		return fmt.Errorf("%w: attempt has no acquirer_channel binding", ErrWebhookRejected)
	}
	var providerCode string
	err := h.pool.QueryRow(ctx, `
		SELECT p.provider_code
		FROM tenant_acquirer_channel c
		JOIN payment_provider p ON p.id = c.provider_id
		WHERE c.id = $1
	`, attempt.AcquirerChannelID).Scan(&providerCode)
	if err != nil {
		return fmt.Errorf("%w: resolve attempt channel provider: %v", ErrWebhookRejected, err)
	}
	if providerCode != providerID {
		return fmt.Errorf("%w: attempt channel provider %q does not match webhook provider %q",
			ErrWebhookRejected, providerCode, providerID)
	}
	if event.AmountMinor != 0 && event.AmountMinor != attempt.AmountMinor {
		return fmt.Errorf("%w: amount mismatch (event=%d attempt=%d)",
			ErrWebhookRejected, event.AmountMinor, attempt.AmountMinor)
	}
	if event.Currency != "" && event.Currency != attempt.Currency {
		return fmt.Errorf("%w: currency mismatch (event=%s attempt=%s)",
			ErrWebhookRejected, event.Currency, attempt.Currency)
	}
	return nil
}

// mapProviderStatusToAttemptStatus 把 provider 状态字符串映射到 attempt 状态。
// 未知状态返回 false（消费为 REJECTED，不做 UNKNOWN 兜底推进）。
func mapProviderStatusToAttemptStatus(status string) (AttemptStatus, bool) {
	switch status {
	case "authorized":
		return AttemptAuthorized, true
	case "captured":
		return AttemptCaptured, true
	case "settled":
		return AttemptSettled, true
	case "failed":
		return AttemptFailed, true
	case "requires_action":
		return AttemptRequiresAction, true
	case "processing":
		return AttemptProcessing, true
	case "unknown", "timeout":
		// RED-03: timeout 走 UnknownExternalState，无盲重试
		return AttemptUnknownExternalState, true
	default:
		return "", false
	}
}

// WebhookTransitionAllowed 状态单调校验（SEC-009）。
// 状态不可回退；同状态为 no-op（幂等重放）；终态拒绝一切 webhook 驱动变更。
func WebhookTransitionAllowed(from, to string) bool {
	if from == to {
		return false // 同状态重放：no-op
	}
	if webhookTerminalStatuses[AttemptStatus(from)] {
		return false
	}
	allowed, ok := webhookTransitions[AttemptStatus(from)]
	if !ok {
		return false
	}
	return allowed[AttemptStatus(to)]
}

var webhookTerminalStatuses = map[AttemptStatus]bool{
	AttemptSettled:     true,
	AttemptFailed:      true,
	AttemptCanceled:    true,
	AttemptRefunded:    true,
	AttemptChargedBack: true,
}

var webhookTransitions = map[AttemptStatus]map[AttemptStatus]bool{
	AttemptRequiresAction: {AttemptProcessing: true, AttemptAuthorized: true, AttemptCaptured: true,
		AttemptFailed: true, AttemptUnknownExternalState: true, AttemptCanceled: true},
	AttemptProcessing: {AttemptAuthorized: true, AttemptCaptured: true,
		AttemptFailed: true, AttemptUnknownExternalState: true},
	AttemptAuthorized: {AttemptCaptured: true, AttemptPartiallyCaptured: true,
		AttemptFailed: true, AttemptUnknownExternalState: true},
	AttemptUnknownExternalState: {AttemptAuthorized: true, AttemptCaptured: true, AttemptFailed: true},
	AttemptCaptured: {AttemptSettled: true, AttemptPartiallyCaptured: true,
		AttemptPartiallyRefunded: true, AttemptRefunded: true, AttemptChargedBack: true,
		AttemptUnknownExternalState: true},
	AttemptPartiallyCaptured: {AttemptCaptured: true, AttemptSettled: true},
	AttemptPartiallyRefunded: {AttemptRefunded: true},
}

// markEventStatus 标记事件状态。
func (h *UnifiedWebhookReceiver) markEventStatus(ctx context.Context, eventID uuid.UUID, status, errorMessage string) error {
	_, err := h.pool.Exec(ctx, `
		UPDATE unified_webhook_events
		SET status = $2, error_message = $3, processed_at = NOW()
		WHERE id = $1
	`, eventID, status, errorMessage)
	return err
}

// markEventStatusTx 事务内标记事件状态（SEC-010：与 attempt/账本同事务提交）。
func (h *UnifiedWebhookReceiver) markEventStatusTx(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, status, errorMessage string) error {
	_, err := tx.Exec(ctx, `
		UPDATE unified_webhook_events
		SET status = $2, error_message = $3, processed_at = NOW()
		WHERE id = $1
	`, eventID, status, errorMessage)
	return err
}

// GetEventStatus 获取事件状态（用于测试和 admin 查询）。
func (h *UnifiedWebhookReceiver) GetEventStatus(ctx context.Context, eventID uuid.UUID) (string, error) {
	var status string
	err := h.pool.QueryRow(ctx, `
		SELECT status FROM unified_webhook_events WHERE id = $1
	`, eventID).Scan(&status)
	return status, err
}

// ListPendingEvents 列出待处理事件（用于 consumer）。
func (h *UnifiedWebhookReceiver) ListPendingEvents(ctx context.Context, limit int) ([]uuid.UUID, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT id FROM unified_webhook_events
		WHERE status IN ('RECEIVED', 'PROCESSING')
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ============================================================================
// Webhook Consumer（异步处理）
// ============================================================================

// WebhookConsumer webhook 事件 consumer。
type WebhookConsumer struct {
	receiver *UnifiedWebhookReceiver
	interval time.Duration
	stopCh   chan struct{}
}

func NewWebhookConsumer(receiver *UnifiedWebhookReceiver, interval time.Duration) *WebhookConsumer {
	return &WebhookConsumer{
		receiver: receiver,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动 consumer。
func (c *WebhookConsumer) Start(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.processBatch(ctx)
		}
	}
}

// Stop 停止 consumer。
func (c *WebhookConsumer) Stop() {
	close(c.stopCh)
}

// HandleOutboxEvent 处理单个 WEBHOOK_EVENT_RECEIVED Outbox 事件（B4）。
//
// 流程：
//  1. 从 outbox payload 解析 event_id
//  2. 根据 event_id 加载 encrypted payload
//  3. 解密 payload
//  4. 幂等推进 payment_attempt 状态
//  5. 必要时执行 Ledger Capture/Release
//  6. 成功后才 ACK（返回 nil）
//
// 重复、乱序事件不得重复记账（RED-10）。
func (c *WebhookConsumer) HandleOutboxEvent(ctx context.Context, eventID uuid.UUID) error {
	// 加载 encrypted payload
	var encryptedPayload []byte
	var eventStatus string
	err := c.receiver.pool.QueryRow(ctx, `
		SELECT payload_encrypted, status FROM unified_webhook_events WHERE id = $1
	`, eventID).Scan(&encryptedPayload, &eventStatus)
	if err != nil {
		return fmt.Errorf("webhook consumer: load event: %w", err)
	}

	// 幂等：已处理的事件不重复处理
	if eventStatus == "PROCESSED" {
		c.receiver.log.Debug("webhook consumer: event already processed, skip",
			"event_id", eventID)
		return nil
	}

	// 解密 payload
	encryptor := c.receiver.Encryptor()
	if encryptor == nil {
		_ = c.receiver.markEventStatus(ctx, eventID, "FAILED", "payload encryptor not configured")
		return fmt.Errorf("webhook consumer: encryptor not configured")
	}

	payload, err := encryptor.Decrypt(encryptedPayload)
	if err != nil {
		_ = c.receiver.markEventStatus(ctx, eventID, "FAILED", "decrypt error: "+err.Error())
		return fmt.Errorf("webhook consumer: decrypt: %w", err)
	}

	var event WebhookEventPayload
	if err := json.Unmarshal(payload, &event); err != nil {
		_ = c.receiver.markEventStatus(ctx, eventID, "FAILED", err.Error())
		return fmt.Errorf("webhook consumer: unmarshal: %w", err)
	}

	// 处理事件（推进 attempt 状态 + Ledger Capture/Release）
	if err := c.receiver.processEvent(ctx, eventID); err != nil {
		c.receiver.log.Error("webhook consumer: process failed",
			"event_id", eventID,
			"err", err.Error(),
		)
		_ = c.receiver.markEventStatus(ctx, eventID, "UNKNOWN", err.Error())
		return fmt.Errorf("webhook consumer: process: %w", err)
	}

	return nil
}

func (c *WebhookConsumer) processBatch(ctx context.Context) {
	eventIDs, err := c.receiver.ListPendingEvents(ctx, 100)
	if err != nil {
		return
	}

	for _, eventID := range eventIDs {
		// 获取事件并处理
		var event WebhookEventPayload
		var encryptedPayload []byte
		err := c.receiver.pool.QueryRow(ctx, `
			SELECT payload_encrypted FROM unified_webhook_events WHERE id = $1
		`, eventID).Scan(&encryptedPayload)
		if err != nil {
			continue
		}

		// P0-6: 使用 AES-256-GCM 解密（替代 XOR）
		// 加密器缺失时标记为 FAILED（fail closed，不处理）
		encryptor := c.receiver.Encryptor()
		if encryptor == nil {
			_ = c.receiver.markEventStatus(ctx, eventID, "FAILED", "payload encryptor not configured")
			continue
		}
		payload, err := encryptor.Decrypt(encryptedPayload)
		if err != nil {
			_ = c.receiver.markEventStatus(ctx, eventID, "FAILED", "decrypt error: "+err.Error())
			continue
		}

		if err := json.Unmarshal(payload, &event); err != nil {
			_ = c.receiver.markEventStatus(ctx, eventID, "FAILED", err.Error())
			continue
		}

		if err := c.receiver.processEvent(ctx, eventID); err != nil {
			// P0-6: 日志中不输出原始敏感 payload，仅记录 event_id 和错误
			c.receiver.log.Error("webhook consumer: process failed",
				"event_id", eventID,
				"err", err.Error(),
			)
			_ = c.receiver.markEventStatus(ctx, eventID, "UNKNOWN", err.Error())
		}
	}
}

// Ensure apperr is used
var _ = apperr.ErrInternal
