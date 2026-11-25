package refund

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/checkout"
	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	merchantwebhook "github.com/nexusglobal/mamr/internal/merchantportal/webhook"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/settlement/projection"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
)

// Gateway 上游退款能力（由 checkout.AcquirerGateway 实现）。
type Gateway interface {
	Refund(ctx context.Context, req checkout.RefundRequest) (*checkout.RefundResult, error)
}

// Ledger 退款账务能力。
type Ledger interface {
	GetOrCreateAccountTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error)
	CreateTransaction(ctx context.Context, tx pgx.Tx, txn ledgerdomain.Transaction) error
}

// Worker 消费 REFUND_REQUESTED 事件。
type Worker struct {
	pool    *pgxpool.Pool
	txm     *postgres.TxManager
	repo    *Repository
	gateway Gateway
	ledger  Ledger
	log     *slog.Logger
}

func NewWorker(pool *pgxpool.Pool, txm *postgres.TxManager, repo *Repository,
	gateway Gateway, ledger Ledger, log *slog.Logger) *Worker {
	return &Worker{pool: pool, txm: txm, repo: repo, gateway: gateway, ledger: ledger, log: log}
}

type refundPayload struct {
	RefundRequestID string `json:"refund_request_id"`
}

// Handle 执行一次退款。
func (w *Worker) Handle(ctx context.Context, event outbox.OutboxEvent) error {
	if event.EventType != outbox.EventRefundRequested {
		return fmt.Errorf("refund: unexpected event type %s", event.EventType)
	}
	var payload refundPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("refund: unmarshal payload: %w", err)
	}
	refundID, err := uuid.Parse(payload.RefundRequestID)
	if err != nil {
		return fmt.Errorf("refund: invalid refund_request_id: %w", err)
	}

	// 1. 认领（事务内），外部调用必须在事务外（RED-01）
	var f *Refund
	var attempt *AttemptInfo
	err = w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var claimed bool
		var e error
		f, attempt, claimed, e = w.repo.ClaimTx(ctx, tx, refundID)
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
		w.log.Info("refund: already claimed or terminal, skip", "refund_id", refundID)
		return nil
	}

	if attempt.ProviderRef == "" {
		w.log.Warn("refund: payment attempt has no provider_ref, cannot refund upstream",
			"refund_id", refundID, "attempt_id", attempt.ID)
		return w.fail(ctx, f, "NO_PROVIDER_REF", "payment attempt has no provider reference")
	}

	// 2. 上游退款（事务外）
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
			return w.fail(ctx, f, "PROVIDER_DECLINED", callErr.Error())
		case errors.Is(callErr, checkout.ErrPaymentTimeout):
			w.log.Warn("refund: provider timeout, marking UNKNOWN", "refund_id", refundID)
			_ = w.repo.MarkUnknown(ctx, f.ID, "PROVIDER_TIMEOUT", callErr.Error())
			w.notify(ctx, f, merchantwebhook.EventRefundUnknown, StatusUnknown, "", callErr.Error())
			return nil
		default:
			_ = w.repo.MarkUnknown(ctx, f.ID, "PROVIDER_ERROR", callErr.Error())
			w.notify(ctx, f, merchantwebhook.EventRefundUnknown, StatusUnknown, "", callErr.Error())
			return nil
		}
	}

	// 3. 成功：同一事务内落账 + 投影 + attempt 状态 + 商户回调
	if err := w.settleSucceeded(ctx, f, attempt, result.ProviderRef); err != nil {
		return err
	}

	w.log.Info("refund: succeeded", "refund_id", f.ID, "amount_minor", f.AmountMinor)
	return nil
}

// settleSucceeded 退款成功后的共享事务：标记成功 + 账本分录 + 投影 + attempt 状态推进 + 商户回调。
// Worker.Handle（首次执行）与 Reconciler（UNKNOWN 对账）共用，保证两条路径落库行为完全一致。
func (w *Worker) settleSucceeded(ctx context.Context, f *Refund, attempt *AttemptInfo, providerRef string) error {
	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := w.repo.MarkSucceededTx(ctx, tx, f.ID, providerRef); err != nil {
			return err
		}

		settledID, err := w.ledger.GetOrCreateAccountTx(ctx, tx, f.MerchantID, "SETTLED", f.Currency)
		if err != nil {
			return fmt.Errorf("refund: get settled account: %w", err)
		}
		receivableID, err := w.ledger.GetOrCreateAccountTx(ctx, tx, f.MerchantID, "PROVIDER_RECEIVABLE", f.Currency)
		if err != nil {
			return fmt.Errorf("refund: get receivable account: %w", err)
		}

		amount := money.DecimalFromInt64(f.AmountMinor)
		txn := ledgerdomain.Transaction{
			IdempotencyKey:  "refund_" + f.ID.String(),
			TransactionType: ledgerdomain.TransactionType("REFUND"),
			AggregateType:   "refund_request",
			AggregateID:     f.ID.String(),
			Description:     "merchant refund",
			TenantID:        f.MerchantID,
			AssetCode:       f.Currency,
			Amount:          amount,
			Status:          ledgerdomain.TransactionStatusConfirmed,
			SourceAccountID: settledID,
			TargetAccountID: receivableID,
			Entries: []ledgerdomain.Entry{
				{
					AccountID:   settledID,
					Direction:   ledgerdomain.DirectionDebit,
					Amount:      amount,
					AssetCode:   f.Currency,
					Description: "refund: settled decrease",
				},
				{
					AccountID:   receivableID,
					Direction:   ledgerdomain.DirectionCredit,
					Amount:      amount,
					AssetCode:   f.Currency,
					Description: "refund: provider receivable decrease",
				},
			},
		}
		if err := w.ledger.CreateTransaction(ctx, tx, txn); err != nil {
			return fmt.Errorf("refund: post ledger: %w", err)
		}

		first, err := projection.Once(ctx, tx, "refund:"+f.ID.String())
		if err != nil {
			return err
		}
		if first {
			if err := projection.RemoveSettledTx(ctx, tx, f.MerchantID, f.Currency, f.AmountMinor); err != nil {
				return err
			}
		}

		succeeded, err := w.repo.SumSucceededRefundsTx(ctx, tx, attempt.ID)
		if err != nil {
			return err
		}
		if err := w.repo.AdvanceAttemptStatusTx(ctx, tx, attempt.ID, attempt.AmountMinor, succeeded); err != nil {
			return err
		}

		return merchantwebhook.EnqueueTx(ctx, tx, f.MerchantID, merchantwebhook.EventRefundSucceeded, f.ID.String(),
			refundPayloadMap(f, StatusSucceeded, providerRef, ""))
	})
}

func (w *Worker) fail(ctx context.Context, f *Refund, code, reason string) error {
	if err := w.repo.MarkFailed(ctx, f.ID, code, reason); err != nil {
		return err
	}
	w.notify(ctx, f, merchantwebhook.EventRefundFailed, StatusFailed, "", reason)
	return nil
}

// notify 在独立事务内写商户回调（失败只记录，不影响退款结果）。
func (w *Worker) notify(ctx context.Context, f *Refund, eventType, status, providerRef, reason string) {
	err := w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return merchantwebhook.EnqueueTx(ctx, tx, f.MerchantID, eventType, f.ID.String(),
			refundPayloadMap(f, status, providerRef, reason))
	})
	if err != nil {
		w.log.Error("refund: enqueue merchant webhook failed", "refund_id", f.ID, "err", err)
	}
}

func refundPayloadMap(f *Refund, status, providerRef, reason string) map[string]any {
	return map[string]any{
		"refund_request_id":  f.ID.String(),
		"payment_attempt_id": f.PaymentAttemptID.String(),
		"merchant_refund_id": f.MerchantRefundID,
		"amount_minor":       f.AmountMinor,
		"currency":           f.Currency,
		"status":             status,
		"provider_refund_id": providerRef,
		"failure_reason":     reason,
	}
}
