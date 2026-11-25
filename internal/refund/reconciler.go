package refund

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/checkout"
)

// Reconciler 对 UNKNOWN 退款做自动对账。
//
// 机制：UNKNOWN 意味着上游调用结果不明确（超时/网络错误）。以与首次调用完全相同的
// 幂等键（"refund_" + 退款单 ID）重放 gateway.Refund：
//   - 支持 Idempotency-Key 语义的渠道（如 Stripe，24h 内）会重放原始结果，不会重复退款；
//   - 明确成功 → 复用 Worker.settleSucceeded 成功事务（落账/投影/attempt 推进/商户回调）；
//   - 明确拒绝 → FAILED（与首次调用即被拒一致，释放退款额度）；
//   - 仍不明确 → 保持 UNKNOWN 并刷新 updated_at 形成退避，等待下一轮。
//
// 安全窗口：只对账 maxAge（默认 24h，对齐 Stripe 幂等键保留期）内的 UNKNOWN；
// 超窗的保持 UNKNOWN 留给人工对账，避免渠道侧不可去重时重复退款。
//
// 已知边界：对账认领（UNKNOWN→PROCESSING）后若进程崩溃，退款单会停留在 PROCESSING
// 成为孤儿（outbox 事件已 ACK，不会重投），需人工介入；批量大小与频率受调用方控制。
type Reconciler struct {
	worker *Worker
	maxAge time.Duration
	limit  int
	log    *slog.Logger
}

func NewReconciler(worker *Worker, maxAge time.Duration, log *slog.Logger) *Reconciler {
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	return &Reconciler{worker: worker, maxAge: maxAge, limit: 100, log: log}
}

// RunOnce 扫描一批窗口内 UNKNOWN 退款并逐条对账，返回处理条数。
func (r *Reconciler) RunOnce(ctx context.Context) (int, error) {
	ids, err := r.worker.repo.ListUnknownForReconcile(ctx, r.maxAge, r.limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		if err := r.reconcileOne(ctx, id); err != nil {
			r.log.Error("refund_reconcile: failed", "refund_id", id, "err", err)
			continue
		}
		processed++
	}
	return processed, nil
}

func (r *Reconciler) reconcileOne(ctx context.Context, refundID uuid.UUID) error {
	w := r.worker

	// 1. 认领 UNKNOWN → PROCESSING（单行原子 UPDATE，并发安全）
	var f *Refund
	var attempt *AttemptInfo
	err := w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var claimed bool
		var e error
		f, attempt, claimed, e = w.repo.ClaimUnknownTx(ctx, tx, refundID)
		if e != nil {
			return e
		}
		if !claimed {
			f = nil
		}
		return nil
	})
	if err != nil {
		return err
	}
	if f == nil {
		return nil // 已被并发处理或已终态
	}

	if attempt.ProviderRef == "" {
		w.log.Warn("refund_reconcile: attempt has no provider_ref, failing", "refund_id", f.ID)
		return w.fail(ctx, f, "NO_PROVIDER_REF", "payment attempt has no provider reference")
	}

	// 2. 以相同幂等键重放上游退款（对账语义，事务外）
	result, callErr := w.gateway.Refund(ctx, checkout.RefundRequest{
		MerchantID:        f.MerchantID.String(),
		AcquirerChannelID: attempt.AcquirerChannelID,
		ProviderRef:       attempt.ProviderRef,
		Amount:            decimal.New(f.AmountMinor, -2),
		Currency:          f.Currency,
		Reason:            f.Reason,
		IdempotencyKey:    "refund_" + f.ID.String(),
	})
	if callErr != nil {
		switch {
		case errors.Is(callErr, checkout.ErrPaymentFailed):
			// 重放得到明确拒绝：与首次调用即被拒一致，转 FAILED 释放退款额度
			return w.fail(ctx, f, "PROVIDER_DECLINED", callErr.Error())
		case errors.Is(callErr, checkout.ErrPaymentTimeout):
			// 仍不明确：保持 UNKNOWN，刷新 updated_at 形成退避，下轮再来（不重复发商户回调）
			r.log.Warn("refund_reconcile: still unknown (timeout)", "refund_id", f.ID, "err", callErr.Error())
			return w.repo.MarkUnknown(ctx, f.ID, "PROVIDER_TIMEOUT", callErr.Error())
		default:
			r.log.Warn("refund_reconcile: still unknown (error)", "refund_id", f.ID, "err", callErr.Error())
			return w.repo.MarkUnknown(ctx, f.ID, "PROVIDER_ERROR", callErr.Error())
		}
	}

	// 3. 明确成功：复用 Worker 成功事务（落账 + 投影 + attempt 推进 + 商户回调）
	if err := w.settleSucceeded(ctx, f, attempt, result.ProviderRef); err != nil {
		return err
	}
	r.log.Info("refund_reconcile: recovered", "refund_id", f.ID, "amount_minor", f.AmountMinor)
	return nil
}
