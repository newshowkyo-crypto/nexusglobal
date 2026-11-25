// Package payment_dispatch 实现 Omni-Channel 真实支付生命周期的 Worker (P0-5)。
//
// 设计目标：
//   - 从 outbox 消费 PAYMENT_DISPATCH_REQUESTED 事件
//   - 执行 ExecutionCheck（DB 强一致封锁检查）
//   - 执行风控检查（RiskGateway.Check）
//   - 调用 PaymentGateway/AcquirerAdapter.Charge
//   - 推进 payment_attempt 状态
//   - Provider timeout → UNKNOWN_EXTERNAL_STATE，不释放 reservation（RED-03）
//   - Provider 明确拒绝 → FAILED + 释放 reservation
//   - Provider 成功 → CAPTURED + Capture 复式分录
//
// 红线约束：
//   - DB 事务内禁止调用 Provider、Redis、HTTP、KMS 等外部网络（RED-01）
//   - Provider 超时必须进入 UNKNOWN/PENDING_RECONCILIATION，不得直接判失败
//   - 未实现的能力必须返回明确 UNSUPPORTED，不得伪装成功
package payment_dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/ledger/service"
	merchantwebhook "github.com/nexusglobal/mamr/internal/merchantportal/webhook"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	pooldomain "github.com/nexusglobal/mamr/internal/pool/domain"
	riskdomain "github.com/nexusglobal/mamr/internal/risk/carding/domain"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
)

// ErrUnsupportedCapability 表示未实现的能力（不得伪装成功）。
//
// 红线约束：未实现的能力必须返回明确 UNSUPPORTED，不得伪装成功。
var ErrUnsupportedCapability = errors.New("payment_dispatch: UNSUPPORTED_CAPABILITY")

// PaymentGateway 支付网关接口（与 checkout.PaymentGateway 一致）。
type PaymentGateway interface {
	Charge(ctx context.Context, req checkout.PaymentRequest) (*checkout.PaymentResult, error)
}

// LedgerService 账本服务接口（用于 Capture/Release）。
type LedgerService interface {
	CaptureTx(ctx context.Context, tx pgx.Tx, reservationID string, captureAmountMinor int64, transactionID string) (string, error)
	ReleaseTx(ctx context.Context, tx pgx.Tx, reservationID string, transactionID string) (string, error)
}

// RiskGateway 风控网关接口。
// B2: 风控必须在 Provider 调用前真实执行。
type RiskGateway interface {
	Check(ctx context.Context, req riskdomain.CheckRequest) riskdomain.CheckResult
}

// PoolRouter 支付池失败切换接口（可选）。
// 只在 Provider 明确拒绝时切换；超时/未知一律不切换（防双扣）。
type PoolRouter interface {
	Failover(ctx context.Context, attemptID uuid.UUID) (*pooldomain.Decision, error)
	Confirm(ctx context.Context, attemptID, channelID uuid.UUID, amountMinor int64) error
	Release(ctx context.Context, attemptID, channelID uuid.UUID, amountMinor int64) error
}

// Worker 消费 PAYMENT_DISPATCH_REQUESTED 事件，执行真实支付生命周期。
type Worker struct {
	pool           *pgxpool.Pool
	paymentGW      PaymentGateway
	ledgerSvc      LedgerService
	txm            *postgres.TxManager
	logger         *slog.Logger
	failClosedSvc  *failclosed.Service
	riskGateway    RiskGateway
	tokenEncryptor checkout.TokenEncryptor // B6: token 加密器
	attemptRepo    *checkout.PaymentAttemptRepository
	poolRouter     PoolRouter // P1: 失败切换（可选）
	dispatchCount  int        // 用于测试：记录 dispatch 调用次数
	releaseCount   int        // 用于测试：记录 release 调用次数
}

// New 创建 payment dispatch worker。
func New(pool *pgxpool.Pool, paymentGW PaymentGateway, ledgerSvc LedgerService, txm *postgres.TxManager, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		pool:        pool,
		paymentGW:   paymentGW,
		ledgerSvc:   ledgerSvc,
		txm:         txm,
		logger:      logger,
		attemptRepo: checkout.NewPaymentAttemptRepository(pool),
	}
}

// NewWithFailClosed 创建带 Fail-Closed 服务注入的 worker（推荐生产使用）。
func NewWithFailClosed(pool *pgxpool.Pool, paymentGW PaymentGateway, ledgerSvc LedgerService, txm *postgres.TxManager, logger *slog.Logger, failClosedSvc *failclosed.Service) *Worker {
	w := New(pool, paymentGW, ledgerSvc, txm, logger)
	w.failClosedSvc = failClosedSvc
	return w
}

// NewWithRiskGateway 创建带 Fail-Closed 和风控网关注入的 worker（推荐生产使用）。
// B2: 风控必须在 Provider 调用前真实执行。
func NewWithRiskGateway(pool *pgxpool.Pool, paymentGW PaymentGateway, ledgerSvc LedgerService, txm *postgres.TxManager, logger *slog.Logger, failClosedSvc *failclosed.Service, riskGW RiskGateway) *Worker {
	w := NewWithFailClosed(pool, paymentGW, ledgerSvc, txm, logger, failClosedSvc)
	w.riskGateway = riskGW
	return w
}

// SetFailClosedService 注入 Fail-Closed 服务。
func (w *Worker) SetFailClosedService(svc *failclosed.Service) {
	w.failClosedSvc = svc
}

// SetRiskGateway 注入风控网关。
func (w *Worker) SetRiskGateway(gw RiskGateway) {
	w.riskGateway = gw
}

// SetTokenEncryptor 注入 token 加密器（B6）。
func (w *Worker) SetTokenEncryptor(enc checkout.TokenEncryptor) {
	w.tokenEncryptor = enc
	// 重建 attemptRepo 以使用新的 encryptor
	w.attemptRepo = checkout.NewPaymentAttemptRepositoryWithEncryptor(w.pool, enc)
}

// SetPool 注入支付池失败切换器（可选）。
func (w *Worker) SetPool(p PoolRouter) { w.poolRouter = p }

// DispatchCount 返回 dispatch 调用次数（用于测试）。
func (w *Worker) DispatchCount() int { return w.dispatchCount }

// ReleaseCount 返回 release 调用次数（用于测试）。
func (w *Worker) ReleaseCount() int { return w.releaseCount }

// executionCheck 是 Provider dispatch 前的统一封锁检查（P0-2）。
// 若 failClosedSvc 为 nil，回退到进程内缓存检查。
// 否则调用 ExecutionCheck(ctx)，封锁或 DB 错误时返回 ErrSystemLockdown。
func (w *Worker) executionCheck(ctx context.Context) error {
	if w.failClosedSvc != nil {
		return w.failClosedSvc.ExecutionCheck(ctx)
	}
	if failclosed.IsSystemLockedDown() {
		return failclosed.ErrSystemLockdown
	}
	return nil
}

// DispatchPayload PAYMENT_DISPATCH_REQUESTED 事件 payload。
//
// P0-4: Outbox payload 只包含 payment_attempt_id。
// Worker 根据 attempt_id 从数据库重新加载全部权威字段：
//   - order_id, merchant_id, tenant_id, amount_minor, currency
//   - ledger_reservation_id, provider_idempotency_key
//   - 加密支付令牌
//
// 不得再信任 Outbox Payload 中的金额、商户、币种、账本 ID。
type DispatchPayload struct {
	PaymentAttemptID string `json:"payment_attempt_id"`
}

// DefaultDispatchLeaseDuration 默认 dispatch lease 时长。
// lease 过期后允许其他 worker 重新领取，修复崩溃窗口。
const DefaultDispatchLeaseDuration = 5 * time.Minute

// Handle 处理 PAYMENT_DISPATCH_REQUESTED 事件。
//
// B2 修复后的流程：
//  1. ExecutionCheck（DB 强一致封锁检查）
//  2. 原子领取 dispatch lease（ClaimDispatchLease）
//  3. 风控检查（RiskGateway.Check）— 必须在 Provider 调用前真实执行
//  4. 调用 PaymentGateway.Charge（外部网络 I/O，在事务外执行，RED-01）
//  5. 根据 Provider 返回结果在同一 DB 事务内推进状态：
//     - timeout/unknown → UNKNOWN_EXTERNAL_STATE，不释放 reservation（RED-03）
//     - 明确拒绝 → FAILED + 释放 reservation（同一事务）
//     - 成功 → CAPTURED + Capture 复式分录（同一事务）
//
// 幂等：重复消费同一事件不产生副作用。
// B2: 不再看到 PROCESSING 就永久跳过；lease 过期后允许重新领取。
func (w *Worker) Handle(ctx context.Context, event outbox.OutboxEvent) error {
	if event.EventType != outbox.EventPaymentDispatchRequested {
		return fmt.Errorf("payment_dispatch: unexpected event type %s", event.EventType)
	}

	var payload DispatchPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("payment_dispatch: unmarshal: %w", err)
	}

	attemptID, err := uuid.Parse(payload.PaymentAttemptID)
	if err != nil {
		return fmt.Errorf("payment_dispatch: invalid attempt_id: %w", err)
	}

	// 幂等检查：获取当前 attempt 状态
	attempt, err := w.attemptRepo.GetPaymentAttempt(ctx, attemptID)
	if err != nil {
		return fmt.Errorf("payment_dispatch: get attempt: %w", err)
	}

	// 已终态：幂等返回
	switch attempt.Status {
	case checkout.AttemptCaptured, checkout.AttemptSettled,
		checkout.AttemptFailed, checkout.AttemptCanceled:
		w.logger.Info("payment_dispatch: attempt already in terminal state, skip",
			"attempt_id", attemptID, "status", attempt.Status)
		return nil
	}

	// 1. ExecutionCheck（DB 强一致封锁检查）
	if err := w.executionCheck(ctx); err != nil {
		w.logger.Warn("payment_dispatch: blocked by system lockdown, will retry",
			"attempt_id", attemptID, "err", err)
		return fmt.Errorf("payment_dispatch: system lockdown: %w", err)
	}

	// 2. 原子领取 dispatch lease（B2: 修复崩溃窗口）
	// REQUIRES_ACTION → PROCESSING 或 PROCESSING + lease_expired → PROCESSING
	owner := fmt.Sprintf("worker-%s", event.ID)
	attempt, err = w.attemptRepo.ClaimDispatchLease(ctx, attemptID, owner, DefaultDispatchLeaseDuration)
	if err != nil {
		if errors.Is(err, checkout.ErrDispatchLeaseHeld) {
			w.logger.Info("payment_dispatch: dispatch lease held by another worker, skip",
				"attempt_id", attemptID)
			return nil // lease 未过期，其他 worker 正在处理，幂等返回
		}
		return fmt.Errorf("payment_dispatch: claim dispatch lease: %w", err)
	}

	// 3. 风控检查（B2: 必须在 Provider 调用前真实执行）
	// P0-4: 使用 attempt.MerchantID（从 DB 加载的权威字段），不信任 payload。
	if w.riskGateway != nil {
		riskResult := w.riskGateway.Check(ctx, riskdomain.CheckRequest{
			TenantID:   attempt.TenantID,
			MerchantID: attempt.MerchantID,
		})
		if riskResult.Decision == riskdomain.DecisionBlock || riskResult.Decision == riskdomain.DecisionCooldown {
			w.logger.Warn("payment_dispatch: risk rejected, marking FAILED",
				"attempt_id", attemptID, "decision", riskResult.Decision, "reason", riskResult.Reason)
			// 风控拒绝 → FAILED + 释放 reservation（同一事务）
			return w.finalizeFailed(ctx, attempt, "RISK_REJECTED", riskResult.Reason)
		}
	}

	// 4. 调用 PaymentGateway.Charge（外部网络 I/O，在事务外执行，RED-01）
	amount, amountErr := dispatchAmount(attempt.AmountMinor, attempt.Currency)
	if amountErr != nil {
		return w.finalizeFailed(ctx, attempt, "UNSUPPORTED_CURRENCY", amountErr.Error())
	}
	// P1: Provider 明确拒绝时允许按支付池候选切换；超时/未知一律不切换。
	const maxDispatchTries = 3
	for try := 0; ; try++ {
		payReq := checkout.PaymentRequest{
			OrderID:                attempt.OrderID.String(),
			MerchantID:             attempt.MerchantID.String(),
			AcquirerChannelID:      attempt.AcquirerChannelID, // P0-3: 从 attempt 读取
			Amount:                 amount,
			Currency:               attempt.Currency,
			PaymentMethodToken:     attempt.PaymentMethodToken,     // B6: 从 DB 加载并解密
			ProviderIdempotencyKey: attempt.ProviderIdempotencyKey, // B3: 从 DB 加载
		}

		w.dispatchCount++
		result, err := w.paymentGW.Charge(ctx, payReq)

		if err == nil {
			// 成功：确认池租约（失败不影响支付结果，只记录）
			if w.poolRouter != nil && attempt.PoolChannelID != uuid.Nil {
				if cerr := w.poolRouter.Confirm(ctx, attempt.ID, attempt.PoolChannelID, attempt.AmountMinor); cerr != nil {
					w.logger.Warn("payment_dispatch: pool confirm failed",
						"attempt_id", attemptID, "err", cerr)
				}
			}
			w.logger.Info("payment_dispatch: provider success, marking CAPTURED",
				"attempt_id", attemptID, "status", result.Status, "provider_ref", result.ProviderRef)
			return w.finalizeCaptured(ctx, attempt, result)
		}

		// 4a. Provider 超时 → UNKNOWN_EXTERNAL_STATE，不释放 reservation（RED-03）
		if errors.Is(err, checkout.ErrPaymentTimeout) {
			w.logger.Warn("payment_dispatch: provider timeout, marking UNKNOWN_EXTERNAL_STATE",
				"attempt_id", attemptID)
			return w.finalizeUnknown(ctx, attempt, "PROVIDER_TIMEOUT", err.Error())
		}

		// 4b. Provider 明确拒绝 → 尝试池内切换；无候选则 FAILED
		if errors.Is(err, checkout.ErrPaymentFailed) {
			if w.tryFailover(ctx, attempt, try, maxDispatchTries) {
				continue
			}
			if w.poolRouter != nil && attempt.PoolChannelID != uuid.Nil {
				if rerr := w.poolRouter.Release(ctx, attempt.ID, attempt.PoolChannelID, attempt.AmountMinor); rerr != nil {
					w.logger.Warn("payment_dispatch: pool release failed",
						"attempt_id", attemptID, "err", rerr)
				}
			}
			w.logger.Info("payment_dispatch: provider explicit failure, marking FAILED",
				"attempt_id", attemptID)
			return w.finalizeFailed(ctx, attempt, "PROVIDER_DECLINED", err.Error())
		}

		// 4c. 其他错误（包括 ErrUnsupportedCapability）→ UNKNOWN_EXTERNAL_STATE
		w.logger.Error("payment_dispatch: provider error, marking UNKNOWN_EXTERNAL_STATE",
			"attempt_id", attemptID)
		return w.finalizeUnknown(ctx, attempt, "PROVIDER_ERROR", err.Error())
	}
}

// tryFailover 在明确拒绝后切换到下一个候选通道。
// 返回 true 表示已切换成功，调用方可重试 Charge。
func (w *Worker) tryFailover(ctx context.Context, attempt *checkout.PaymentAttempt, try, maxTries int) bool {
	if w.poolRouter == nil || attempt.PoolChannelID == uuid.Nil || try >= maxTries-1 {
		return false
	}
	next, err := w.poolRouter.Failover(ctx, attempt.ID)
	if err != nil || next == nil {
		return false
	}

	// 释放旧租约额度（即使失败也继续切换：旧通道已明确拒绝）
	if rerr := w.poolRouter.Release(ctx, attempt.ID, attempt.PoolChannelID, attempt.AmountMinor); rerr != nil {
		w.logger.Warn("payment_dispatch: release old lease failed",
			"attempt_id", attempt.ID, "err", rerr)
	}

	var newVersion int
	err = w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var e error
		newVersion, e = w.attemptRepo.SwitchChannelTx(ctx, tx, attempt.ID,
			next.AcquirerChannelID, next.ChannelID, attempt.Version)
		return e
	})
	if err != nil {
		w.logger.Warn("payment_dispatch: switch channel failed",
			"attempt_id", attempt.ID, "err", err)
		// 切换失败必须释放刚为新通道预占的额度，避免泄漏
		if rerr := w.poolRouter.Release(ctx, attempt.ID, next.ChannelID, attempt.AmountMinor); rerr != nil {
			w.logger.Warn("payment_dispatch: release next lease failed",
				"attempt_id", attempt.ID, "err", rerr)
		}
		return false
	}

	oldChannel := attempt.AcquirerChannelID
	attempt.AcquirerChannelID = next.AcquirerChannelID
	attempt.PoolChannelID = next.ChannelID
	attempt.Version = newVersion
	w.logger.Info("payment_dispatch: failover to next candidate",
		"attempt_id", attempt.ID, "from", oldChannel, "to", next.AcquirerChannelID, "try", try+1)
	return true
}

// finalizeCaptured 在同一 DB 事务内：设置 CAPTURED + Ledger Capture。
// B2: Provider 成功后同一 DB 事务内推进状态和账本。
// P0-4: 使用 attempt 中的 reservation ID 和金额（从 DB 加载的权威字段）。
func (w *Worker) finalizeCaptured(ctx context.Context, attempt *checkout.PaymentAttempt, result *checkout.PaymentResult) error {
	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		// 设置 attempt → CAPTURED
		// provider_transaction_id 存渠道侧交易单号（PaymentResult.TransactionID 已由
		// 网关映射为 captureResp.ProviderRef），内部关联用 payment_attempt ID 本身。
		providerResult := checkout.ProviderResult{
			Status:                "captured",
			ProviderRef:           result.ProviderRef,
			ProviderTransactionID: result.TransactionID,
		}
		if err := w.setProviderResultTx(ctx, tx, attempt.ID, providerResult, attempt.Version); err != nil {
			return fmt.Errorf("set captured state: %w", err)
		}

		// Ledger Capture（同一事务）
		// P0-4: 使用 attempt 中的 reservation ID 和金额（权威字段）。
		if attempt.LedgerReservationID != "" && w.ledgerSvc != nil {
			if _, err := w.ledgerSvc.CaptureTx(ctx, tx, attempt.LedgerReservationID, attempt.AmountMinor, "capture_"+attempt.ID.String()); err != nil {
				return fmt.Errorf("capture reservation: %w", err)
			}
		}

		// A2: 商户出站回调（与状态推进同事务）
		payload := map[string]any{
			"payment_attempt_id": attempt.ID.String(),
			"order_id":           attempt.OrderID.String(),
			"merchant_order_id":  attempt.MerchantOrderID,
			"amount_minor":       attempt.AmountMinor,
			"currency":           attempt.Currency,
			"status":             string(checkout.AttemptCaptured),
			"provider_ref":       result.ProviderRef,
		}
		if err := merchantwebhook.EnqueueTx(ctx, tx, attempt.MerchantID,
			merchantwebhook.EventPaymentSucceeded, attempt.ID.String(), payload); err != nil {
			return fmt.Errorf("enqueue merchant webhook: %w", err)
		}
		return nil
	})
}

// finalizeFailed 在同一 DB 事务内：设置 FAILED + Ledger Release。
// B2: Provider 明确拒绝后同一 DB 事务内推进状态和账本。
// P0-4: 使用 attempt 中的 reservation ID（从 DB 加载的权威字段）。
func (w *Worker) finalizeFailed(ctx context.Context, attempt *checkout.PaymentAttempt, failureCode, failureReason string) error {
	w.releaseCount++
	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		// 设置 attempt → FAILED
		providerResult := checkout.ProviderResult{
			Status:        "failed",
			FailureCode:   failureCode,
			FailureReason: failureReason,
		}
		if err := w.setProviderResultTx(ctx, tx, attempt.ID, providerResult, attempt.Version); err != nil {
			return fmt.Errorf("set failed state: %w", err)
		}

		// Ledger Release（同一事务）
		// P0-4: 使用 attempt 中的 reservation ID（权威字段）。
		if attempt.LedgerReservationID != "" && w.ledgerSvc != nil {
			if _, err := w.ledgerSvc.ReleaseTx(ctx, tx, attempt.LedgerReservationID, "release_"+attempt.ID.String()); err != nil {
				return fmt.Errorf("release reservation: %w", err)
			}
		}

		payload := map[string]any{
			"payment_attempt_id": attempt.ID.String(),
			"order_id":           attempt.OrderID.String(),
			"merchant_order_id":  attempt.MerchantOrderID,
			"amount_minor":       attempt.AmountMinor,
			"currency":           attempt.Currency,
			"status":             string(checkout.AttemptFailed),
			"failure_code":       failureCode,
			"failure_reason":     failureReason,
		}
		if err := merchantwebhook.EnqueueTx(ctx, tx, attempt.MerchantID,
			merchantwebhook.EventPaymentFailed, attempt.ID.String(), payload); err != nil {
			return fmt.Errorf("enqueue merchant webhook: %w", err)
		}
		return nil
	})
}

// finalizeUnknown 设置 UNKNOWN_EXTERNAL_STATE，不释放 reservation。
// B2/RED-03: Provider 超时或未知错误后进入 reconciliation，不自动再次扣款。
func (w *Worker) finalizeUnknown(ctx context.Context, attempt *checkout.PaymentAttempt, failureCode, failureReason string) error {
	providerResult := checkout.ProviderResult{
		Status:        "unknown",
		FailureCode:   failureCode,
		FailureReason: failureReason,
	}
	if err := w.attemptRepo.SetProviderResult(ctx, attempt.ID, providerResult, attempt.Version); err != nil {
		return fmt.Errorf("set unknown state: %w", err)
	}
	// 不释放 reservation，等待 webhook 或 reconciliation
	return nil
}

// setProviderResultTx 在事务内设置 provider 结果。
func (w *Worker) setProviderResultTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, result checkout.ProviderResult, version int) error {
	var nextStatus checkout.AttemptStatus
	switch result.Status {
	case "authorized":
		nextStatus = checkout.AttemptAuthorized
	case "captured":
		nextStatus = checkout.AttemptCaptured
	case "settled":
		nextStatus = checkout.AttemptSettled
	case "failed":
		nextStatus = checkout.AttemptFailed
	case "requires_action":
		nextStatus = checkout.AttemptRequiresAction
	case "processing":
		nextStatus = checkout.AttemptProcessing
	case "unknown", "timeout":
		nextStatus = checkout.AttemptUnknownExternalState
	default:
		nextStatus = checkout.AttemptUnknownExternalState
	}

	if nextStatus == checkout.AttemptFailed {
		_, err := tx.Exec(ctx, `
			UPDATE payment_attempts
			SET status = 'FAILED', failure_code = $2, failure_reason = $3,
			    provider_ref = $4, provider_transaction_id = $5,
			    version = version + 1, updated_at = NOW()
			WHERE id = $1 AND version = $6
		`, id, result.FailureCode, result.FailureReason, result.ProviderRef, result.ProviderTransactionID, version)
		return err
	}

	_, err := tx.Exec(ctx, `
		UPDATE payment_attempts
		SET status = $2, provider_ref = $3, provider_transaction_id = $4,
		    version = version + 1, updated_at = NOW()
		WHERE id = $1 AND version = $5
	`, id, string(nextStatus), result.ProviderRef, result.ProviderTransactionID, version)
	return err
}

// Ensure service package is referenced.
var _ = service.New
