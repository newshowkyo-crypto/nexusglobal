package fxlock

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

	"github.com/nexusglobal/mamr/internal/ledger/fee"
)

type Worker struct {
	repo       *repository.Repository
	lpCli      lp.Client
	feeService fee.FeeServiceInterface
	txm        *postgres.TxManager
	pool       *pgxpool.Pool
	logger     *slog.Logger
}

func New(repo *repository.Repository, lpCli lp.Client, feeService fee.FeeServiceInterface, txm *postgres.TxManager, pool *pgxpool.Pool, logger *slog.Logger) *Worker {
	return &Worker{
		repo:       repo,
		lpCli:      lpCli,
		feeService: feeService,
		txm:        txm,
		pool:       pool,
		logger:     logger,
	}
}

func (w *Worker) Handle(ctx context.Context, event outbox.OutboxEvent) error {
	if event.EventType != "FX_LOCK_REQUESTED" {
		return fmt.Errorf("fxlock: unexpected event type %s", event.EventType)
	}

	var payload struct {
		PayoutRequestID string `json:"payout_request_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("fxlock: unmarshal payload: %w", err)
	}

	payoutID, err := uuid.Parse(payload.PayoutRequestID)
	if err != nil {
		return fmt.Errorf("fxlock: invalid payout request id: %w", err)
	}

	payoutReq, err := w.repo.GetPayoutRequest(ctx, payoutID)
	if err != nil {
		return fmt.Errorf("fxlock: get payout request: %w", err)
	}
	if payoutReq == nil {
		return fmt.Errorf("fxlock: payout request %s not found", payoutID)
	}

	if payoutReq.Status != string(domain.StatusBalanceFrozen) {
		w.logger.Info("fxlock: skipping non-frozen payout",
			"payout_id", payoutID, "status", payoutReq.Status)
		return nil
	}

	amt := money.NumericToDecimal(payoutReq.Amount)

	lockReq := lp.FXLockRequest{
		PayoutRequestID: payoutID,
		Amount:          amt,
		FiatCurrency:    payoutReq.FiatCurrency,
		TargetAsset:     payoutReq.TargetAsset,
		TargetNetwork:   payoutReq.TargetNetwork,
	}

	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	lockResult, err := w.lpCli.FXLock(lockCtx, lockReq)
	if err != nil {
		if errors.Is(err, lp.ErrTimeout) {
			w.logger.Warn("fxlock: LP timeout, setting UnknownExternalState",
				"payout_id", payoutID)
			return w.setStatus(ctx, payoutID, string(domain.StatusUnknownExternalState), payoutReq.Version)
		}
		if errors.Is(err, lp.ErrExplicitFailure) {
			w.logger.Warn("fxlock: LP explicit failure, triggering release",
				"payout_id", payoutID)
			return w.setStatus(ctx, payoutID, string(domain.StatusFailed), payoutReq.Version)
		}
		return fmt.Errorf("fxlock: lp.FXLock: %w", err)
	}

	w.logger.Info("fxlock: success",
		"payout_id", payoutID, "lock_id", lockResult.LockID)

	outboxPayload, _ := json.Marshal(map[string]string{
		"payout_request_id": payoutID.String(),
		"lock_id":           lockResult.LockID,
	})
	outboxKey := "lp_payout:" + payoutID.String()

	// 记录 GAS_FEE（如果费用服务可用且有 gas 费用）
	if w.feeService != nil && lockResult.GasFee > 0 {
		feeDetails := []fee.FeeDetail{
			{
				FeeType:    fee.FeeTypeGas,
				Amount:     lockResult.GasFee,
				SourceData: map[string]interface{}{"payout_id": payoutID.String(), "lock_id": lockResult.LockID, "network": payoutReq.TargetNetwork, "asset": payoutReq.TargetAsset},
			},
		}
		// 异步记录费用，不阻塞主流程
		go func() {
			if err := w.feeService.AllocateTransactionFees(context.Background(), payoutID, payoutReq.MerchantID, payoutReq.FiatCurrency, feeDetails); err != nil {
				w.logger.Error("failed to allocate gas fee", "payout_id", payoutID, "error", err)
			}
		}()
	}

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusFxLocked), payoutReq.Version); err != nil {
			return fmt.Errorf("update status: %w", err)
		}

		// G2: 持久化 lock_id
		if err := w.repo.UpdatePayoutRequestLockID(ctx, tx, payoutID, lockResult.LockID); err != nil {
			return fmt.Errorf("update lock_id: %w", err)
		}

		_, err := w.repo.InsertOutboxEvent(ctx, tx, db.InsertOutboxEventParams{
			AggregateType:  "payout_request",
			AggregateID:    payoutID,
			EventType:      "LP_PAYOUT_REQUESTED",
			Payload:        outboxPayload,
			IdempotencyKey: outboxKey,
		})
		if err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}

		return nil
	})
}

func (w *Worker) setStatus(ctx context.Context, payoutID uuid.UUID, status string, version int32) error {
	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx, payoutID, status, version)
	})
}
