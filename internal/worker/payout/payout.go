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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
	"github.com/nexusglobal/mamr/pkg/external/lp"
)

type Worker struct {
	repo   *repository.Repository
	lpCli  lp.Client
	txm    *postgres.TxManager
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func New(repo *repository.Repository, lpCli lp.Client, txm *postgres.TxManager, pool *pgxpool.Pool, logger *slog.Logger) *Worker {
	return &Worker{
		repo:   repo,
		lpCli:  lpCli,
		txm:    txm,
		pool:   pool,
		logger: logger,
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

	amt := numericToDecimal(payoutReq.Amount)

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
		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusLPProcessing), payoutReq.Version)
	})
}

func (w *Worker) releaseFrozen(ctx context.Context, payoutID uuid.UUID, payoutReq *db.PayoutRequest) error {
	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusFailed), payoutReq.Version)
	})
}

func (w *Worker) setStatus(ctx context.Context, payoutID uuid.UUID, status string, version int32) error {
	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx, payoutID, status, version)
	})
}

func (w *Worker) Confirm(ctx context.Context, payoutID uuid.UUID, providerRef string) error {
	payoutReq, err := w.repo.GetPayoutRequest(ctx, payoutID)
	if err != nil {
		return err
	}
	if payoutReq == nil {
		return fmt.Errorf("confirm: request %s not found", payoutID)
	}

	if payoutReq.Status == string(domain.StatusConfirmed) || payoutReq.Status == string(domain.StatusReconciled) {
		w.logger.Info("confirm: already confirmed/reconciled", "payout_id", payoutID)
		return nil
	}

	if payoutReq.Status != string(domain.StatusLPProcessing) && payoutReq.Status != string(domain.StatusUnknownExternalState) {
		return fmt.Errorf("confirm: invalid status %s for confirm", payoutReq.Status)
	}

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusConfirmed), payoutReq.Version)
	})
}

func (w *Worker) Release(ctx context.Context, payoutID uuid.UUID) error {
	payoutReq, err := w.repo.GetPayoutRequest(ctx, payoutID)
	if err != nil {
		return err
	}
	if payoutReq == nil {
		return fmt.Errorf("release: request %s not found", payoutID)
	}

	if payoutReq.Status == string(domain.StatusBalanceReleased) {
		w.logger.Info("release: already released", "payout_id", payoutID)
		return nil
	}

	if payoutReq.Status != string(domain.StatusFailed) {
		return fmt.Errorf("release: invalid status %s for release", payoutReq.Status)
	}

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusBalanceReleased), payoutReq.Version)
	})
}

func numericToDecimal(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid {
		return decimal.Zero
	}
	d, _ := decimal.NewFromString(n.Int.String())
	if n.Exp < 0 {
		divisor := decimal.NewFromInt(1)
		for i := int32(0); i < -n.Exp; i++ {
			divisor = divisor.Mul(decimal.NewFromInt(10))
		}
		return d.Div(divisor)
	}
	return d
}
