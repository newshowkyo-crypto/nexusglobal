package payout

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

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
	"github.com/nexusglobal/mamr/pkg/external/lp"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
)

type Worker struct {
	repo       *repository.Repository
	ledgerRepo *repository.LedgerRepo
	lpCli      lp.Client
	txm        *postgres.TxManager
	pool       *pgxpool.Pool
	logger     *slog.Logger
}

func New(repo *repository.Repository, ledgerRepo *repository.LedgerRepo, lpCli lp.Client, txm *postgres.TxManager, pool *pgxpool.Pool, logger *slog.Logger) *Worker {
	return &Worker{
		repo:       repo,
		ledgerRepo: ledgerRepo,
		lpCli:      lpCli,
		txm:        txm,
		pool:       pool,
		logger:     logger,
	}
}

func (w *Worker) Handle(ctx context.Context, event outbox.OutboxEvent) error {
	if event.EventType != "LP_PAYOUT_REQUESTED" {
		return fmt.Errorf("payout worker: unexpected event type %s", event.EventType)
	}

	var payload struct {
		PayoutRequestID string `json:"payout_request_id"`
		LockID          string `json:"lock_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("payout worker: unmarshal: %w", err)
	}

	payoutID, err := uuid.Parse(payload.PayoutRequestID)
	if err != nil {
		return fmt.Errorf("payout worker: invalid id: %w", err)
	}

	payoutReq, err := w.repo.GetPayoutRequest(ctx, payoutID)
	if err != nil {
		return fmt.Errorf("payout worker: get request: %w", err)
	}
	if payoutReq == nil {
		return fmt.Errorf("payout worker: request %s not found", payoutID)
	}

	if payoutReq.Status != string(domain.StatusFxLocked) {
		w.logger.Info("payout worker: skipping non-fxlocked",
			"payout_id", payoutID, "status", payoutReq.Status)
		return nil
	}

	amt := money.NumericToDecimal(payoutReq.Amount)

	payoutReq2 := lp.PayoutRequest{
		PayoutRequestID: payoutID,
		LockID:          payload.LockID,
		Amount:          amt,
		FiatCurrency:    payoutReq.FiatCurrency,
		TargetAsset:     payoutReq.TargetAsset,
		TargetNetwork:   payoutReq.TargetNetwork,
	}

	payoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := w.lpCli.Payout(payoutCtx, payoutReq2)
	if err != nil {
		if errors.Is(err, lp.ErrTimeout) {
			w.logger.Warn("payout worker: LP timeout",
				"payout_id", payoutID)
			return w.setStatus(ctx, payoutID, string(domain.StatusUnknownExternalState), payoutReq.Version)
		}
		if errors.Is(err, lp.ErrExplicitFailure) {
			w.logger.Warn("payout worker: LP failure, releasing",
				"payout_id", payoutID)
			return w.releaseFrozen(ctx, payoutID, payoutReq)
		}
		return fmt.Errorf("payout worker: lp.Payout: %w", err)
	}

	w.logger.Info("payout worker: submitted",
		"payout_id", payoutID, "provider_ref", result.ProviderRef)

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusLPProcessing), payoutReq.Version); err != nil {
			return fmt.Errorf("update status: %w", err)
		}

		// G2: 持久化 provider_ref
		if err := w.repo.UpdatePayoutRequestProviderRef(ctx, tx, payoutID, result.ProviderRef); err != nil {
			return fmt.Errorf("update provider_ref: %w", err)
		}

		return nil
	})
}

func (w *Worker) releaseFrozen(ctx context.Context, payoutID uuid.UUID, payoutReq *db.PayoutRequest) error {
	err := w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusFailed), payoutReq.Version)
	})
	if err != nil {
		return err
	}
	return w.Release(ctx, payoutID)
}

func (w *Worker) setStatus(ctx context.Context, payoutID uuid.UUID, status string, version int32) error {
	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx, payoutID, status, version)
	})
}

func (w *Worker) Confirm(ctx context.Context, payoutID uuid.UUID, providerRef string) error {
	idempotencyKey := "payout_confirm:" + payoutID.String()

	existing, err := w.ledgerRepo.GetTransactionByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return fmt.Errorf("confirm: idempotency check: %w", err)
	}
	if existing != nil {
		w.logger.Info("confirm: already posted (idempotent)", "payout_id", payoutID)
		return nil
	}

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		payoutReq, err := w.repo.LockPayoutRequestForUpdate(ctx, tx, payoutID)
		if err != nil {
			return fmt.Errorf("confirm: lock payout: %w", err)
		}
		if payoutReq == nil {
			return fmt.Errorf("confirm: request %s not found", payoutID)
		}

		if payoutReq.Status == string(domain.StatusConfirmed) || payoutReq.Status == string(domain.StatusReconciled) {
			return nil
		}

		if !domain.CanTransition(domain.PayoutStatus(payoutReq.Status), domain.StatusConfirmed) {
			return fmt.Errorf("confirm: cannot transition from %s to Confirmed", payoutReq.Status)
		}

		amount := money.NumericToDecimal(payoutReq.Amount)
		if amount.IsZero() {
			return fmt.Errorf("confirm: payout amount is zero")
		}

		bal, err := w.repo.LockBalanceForUpdate(ctx, tx, payoutReq.MerchantID, payoutReq.FiatCurrency)
		if err != nil {
			return fmt.Errorf("confirm: lock balance: %w", err)
		}
		if bal == nil {
			return fmt.Errorf("confirm: no treasury ledger for merchant %s currency %s", payoutReq.MerchantID, payoutReq.FiatCurrency)
		}

		frozenAccID, err := w.ledgerRepo.GetOrCreateAccount(ctx, tx, payoutReq.MerchantID, "FROZEN_PAYOUT", payoutReq.FiatCurrency)
		if err != nil {
			return fmt.Errorf("confirm: get frozen account: %w", err)
		}
		externalAccID, err := w.ledgerRepo.GetOrCreateAccount(ctx, tx, payoutReq.MerchantID, "EXTERNAL_PAYOUT_SETTLEMENT", payoutReq.FiatCurrency)
		if err != nil {
			return fmt.Errorf("confirm: get external account: %w", err)
		}

		_, err = w.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  idempotencyKey,
			TransactionType: ledgerdomain.TxnTypePayoutConfirm,
			AggregateType:   "payout_request",
			AggregateID:     payoutID.String(),
			Description:     fmt.Sprintf("Payout confirm %s %s", amount.String(), payoutReq.FiatCurrency),
			Entries: []ledgerdomain.Entry{
				{AccountID: frozenAccID.String(), Direction: ledgerdomain.DirectionDebit, Amount: amount},
				{AccountID: externalAccID.String(), Direction: ledgerdomain.DirectionCredit, Amount: amount},
			},
		})
		if err != nil {
			return fmt.Errorf("confirm: ledger post: %w", err)
		}

		bal.FrozenPayoutBalance = bal.FrozenPayoutBalance.Sub(amount)
		bal.AvailablePayoutBalance = bal.SettledBalance.
			Sub(bal.ReserveBalance).
			Sub(bal.FrozenPayoutBalance).
			Sub(bal.DisputeHoldBalance)

		if err := w.repo.UpdateBalance(ctx, tx, bal); err != nil {
			return fmt.Errorf("confirm: update balance: %w", err)
		}

		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusConfirmed), payoutReq.Version)
	})
}

func (w *Worker) Release(ctx context.Context, payoutID uuid.UUID) error {
	idempotencyKey := "payout_release:" + payoutID.String()

	existing, err := w.ledgerRepo.GetTransactionByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return fmt.Errorf("release: idempotency check: %w", err)
	}
	if existing != nil {
		w.logger.Info("release: already posted (idempotent)", "payout_id", payoutID)
		return nil
	}

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		payoutReq, err := w.repo.LockPayoutRequestForUpdate(ctx, tx, payoutID)
		if err != nil {
			return fmt.Errorf("release: lock payout: %w", err)
		}
		if payoutReq == nil {
			return fmt.Errorf("release: request %s not found", payoutID)
		}

		if payoutReq.Status == string(domain.StatusBalanceReleased) {
			return nil
		}

		// T5-2 资金红线：人工调单期间 frozen 绝不释放。
		// 必须等人工核销清除 manual_review 标记后才允许走释放/核销分录。
		if payoutReq.ManualReview {
			w.logger.Warn("release: blocked, payout under manual review",
				"payout_id", payoutID, "reason", payoutReq.ManualReviewReason)
			return fmt.Errorf("release: payout %s is under manual review, frozen balance held", payoutID)
		}

		if !domain.CanTransition(domain.PayoutStatus(payoutReq.Status), domain.StatusBalanceReleased) {
			return fmt.Errorf("release: cannot transition from %s to BalanceReleased", payoutReq.Status)
		}

		amount := money.NumericToDecimal(payoutReq.Amount)
		if amount.IsZero() {
			return fmt.Errorf("release: payout amount is zero")
		}

		bal, err := w.repo.LockBalanceForUpdate(ctx, tx, payoutReq.MerchantID, payoutReq.FiatCurrency)
		if err != nil {
			return fmt.Errorf("release: lock balance: %w", err)
		}
		if bal == nil {
			return fmt.Errorf("release: no treasury ledger for merchant %s currency %s", payoutReq.MerchantID, payoutReq.FiatCurrency)
		}

		frozenAccID, err := w.ledgerRepo.GetOrCreateAccount(ctx, tx, payoutReq.MerchantID, "FROZEN_PAYOUT", payoutReq.FiatCurrency)
		if err != nil {
			return fmt.Errorf("release: get frozen account: %w", err)
		}
		settledAccID, err := w.ledgerRepo.GetOrCreateAccount(ctx, tx, payoutReq.MerchantID, "SETTLED", payoutReq.FiatCurrency)
		if err != nil {
			return fmt.Errorf("release: get settled account: %w", err)
		}

		_, err = w.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  idempotencyKey,
			TransactionType: ledgerdomain.TxnTypePayoutRelease,
			AggregateType:   "payout_request",
			AggregateID:     payoutID.String(),
			Description:     fmt.Sprintf("Payout release %s %s", amount.String(), payoutReq.FiatCurrency),
			Entries: []ledgerdomain.Entry{
				{AccountID: frozenAccID.String(), Direction: ledgerdomain.DirectionDebit, Amount: amount},
				{AccountID: settledAccID.String(), Direction: ledgerdomain.DirectionCredit, Amount: amount},
			},
		})
		if err != nil {
			return fmt.Errorf("release: ledger post: %w", err)
		}

		bal.FrozenPayoutBalance = bal.FrozenPayoutBalance.Sub(amount)
		bal.SettledBalance = bal.SettledBalance.Add(amount)
		bal.AvailablePayoutBalance = bal.SettledBalance.
			Sub(bal.ReserveBalance).
			Sub(bal.FrozenPayoutBalance).
			Sub(bal.DisputeHoldBalance)

		if err := w.repo.UpdateBalance(ctx, tx, bal); err != nil {
			return fmt.Errorf("release: update balance: %w", err)
		}

		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusBalanceReleased), payoutReq.Version)
	})
}
