package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/pkg/external/lp"
)

type PayoutRepository interface {
	GetPayoutRequest(ctx context.Context, id uuid.UUID) (*db.PayoutRequest, error)
	UpdatePayoutRequestStatus(ctx context.Context, id uuid.UUID, status string, version int32) error
}

type Worker struct {
	payoutRepo PayoutRepository
	lpCli      lp.Client
	logger     *slog.Logger
}

func New(payoutRepo PayoutRepository, lpCli lp.Client, logger *slog.Logger) *Worker {
	return &Worker{
		payoutRepo: payoutRepo,
		lpCli:      lpCli,
		logger:     logger,
	}
}

func (w *Worker) ReconcilePayout(ctx context.Context, payoutID uuid.UUID) error {
	payoutReq, err := w.payoutRepo.GetPayoutRequest(ctx, payoutID)
	if err != nil {
		return fmt.Errorf("reconcile: get payout: %w", err)
	}
	if payoutReq == nil {
		return fmt.Errorf("reconcile: payout %s not found", payoutID)
	}

	if payoutReq.Status != string(domain.StatusUnknownExternalState) {
		w.logger.Info("reconcile: skipping non-unknown payout",
			"payout_id", payoutID, "status", payoutReq.Status)
		return nil
	}

	if payoutReq.ProviderRef == "" {
		w.logger.Warn("reconcile: no provider ref, cannot query",
			"payout_id", payoutID)
		return nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	result, err := w.lpCli.QueryStatus(queryCtx, payoutReq.ProviderRef)
	if err != nil {
		if errors.Is(err, lp.ErrTimeout) {
			w.logger.Info("reconcile: query still unknown",
				"payout_id", payoutID)
			return nil
		}
		return fmt.Errorf("reconcile: query status: %w", err)
	}

	switch result.Status {
	case "confirmed":
		w.logger.Info("reconcile: confirmed",
			"payout_id", payoutID)
		return w.payoutRepo.UpdatePayoutRequestStatus(ctx, payoutID, string(domain.StatusConfirmed), payoutReq.Version)
	case "failed":
		w.logger.Info("reconcile: failed",
			"payout_id", payoutID)
		return w.payoutRepo.UpdatePayoutRequestStatus(ctx, payoutID, string(domain.StatusFailed), payoutReq.Version)
	default:
		w.logger.Info("reconcile: still unknown",
			"payout_id", payoutID, "lp_status", result.Status)
		return nil
	}
}
