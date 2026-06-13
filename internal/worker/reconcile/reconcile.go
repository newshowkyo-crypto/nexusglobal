package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/pkg/external/lp"
)

const (
	MaxReconcileAttempts = 5
	ManualReviewHours    = 24
	LockTTL              = 30 * time.Second
)

type PayoutConfirmer interface {
	Confirm(ctx context.Context, payoutID uuid.UUID, providerRef string) error
	Release(ctx context.Context, payoutID uuid.UUID) error
}

type PayoutRepository interface {
	GetPayoutRequest(ctx context.Context, id uuid.UUID) (*db.PayoutRequest, error)
	ListPayoutRequestsByStatus(ctx context.Context, status string, limit int32) ([]db.PayoutRequest, error)
	UpdateReconcileAttempt(ctx context.Context, id uuid.UUID, attempts int) error
	MarkManualReview(ctx context.Context, id uuid.UUID, reason string) error
}

type LockProvider interface {
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
}

type Worker struct {
	payoutRepo PayoutRepository
	confirmer  PayoutConfirmer
	lpCli      lp.Client
	locker     LockProvider
	logger     *slog.Logger
}

func New(payoutRepo PayoutRepository, confirmer PayoutConfirmer, lpCli lp.Client, locker LockProvider, logger *slog.Logger) *Worker {
	return &Worker{
		payoutRepo: payoutRepo,
		confirmer:  confirmer,
		lpCli:      lpCli,
		locker:     locker,
		logger:     logger,
	}
}

func (w *Worker) ReconcileAll(ctx context.Context) error {
	payouts, err := w.payoutRepo.ListPayoutRequestsByStatus(ctx, string(domain.StatusUnknownExternalState), 100)
	if err != nil {
		return fmt.Errorf("reconcile: list unknown payouts: %w", err)
	}

	for _, payout := range payouts {
		if err := w.ReconcilePayout(ctx, payout.ID); err != nil {
			w.logger.Error("reconcile: failed to reconcile payout",
				"payout_id", payout.ID, "error", err)
		}
	}
	return nil
}

func (w *Worker) ReconcilePayout(ctx context.Context, payoutID uuid.UUID) error {
	// T5-1: 分布式锁，防止多 Worker 并发查同一单。
	// 资金路径不允许无锁运行：locker 缺失直接报错，绝不静默降级为无锁。
	if w.locker == nil {
		return fmt.Errorf("reconcile: lock provider not configured, refusing to reconcile without distributed lock")
	}
	lockKey := fmt.Sprintf("LOCK:reconcile:%s", payoutID.String())

	acquired, err := w.locker.AcquireLock(ctx, lockKey, LockTTL)
	if err != nil {
		return fmt.Errorf("reconcile: acquire lock: %w", err)
	}
	if !acquired {
		w.logger.Info("reconcile: lock not acquired, skipping",
			"payout_id", payoutID)
		return nil
	}
	defer w.locker.ReleaseLock(ctx, lockKey)

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

	// T5-2: 检查是否应该转人工调单
	if w.shouldTransferToManualReview(payoutReq) {
		reason := fmt.Sprintf("exceeded max reconcile attempts (%d) or time threshold (%d hours)",
			MaxReconcileAttempts, ManualReviewHours)
		w.logger.Warn("reconcile: transferring to manual review",
			"payout_id", payoutID, "reason", reason)

		if err := w.payoutRepo.MarkManualReview(ctx, payoutID, reason); err != nil {
			return fmt.Errorf("reconcile: mark manual review: %w", err)
		}
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
			// 更新 reconcile attempts
			if updateErr := w.payoutRepo.UpdateReconcileAttempt(ctx, payoutID, int(payoutReq.ReconcileAttempts)+1); updateErr != nil {
				w.logger.Error("reconcile: failed to update attempts",
					"payout_id", payoutID, "error", updateErr)
			}
			return nil
		}
		return fmt.Errorf("reconcile: query status: %w", err)
	}

	switch result.Status {
	case "confirmed":
		w.logger.Info("reconcile: confirmed, running Confirm",
			"payout_id", payoutID)
		return w.confirmer.Confirm(ctx, payoutID, payoutReq.ProviderRef)
	case "failed":
		w.logger.Info("reconcile: failed, running Release",
			"payout_id", payoutID)
		return w.confirmer.Release(ctx, payoutID)
	default:
		w.logger.Info("reconcile: still unknown",
			"payout_id", payoutID, "lp_status", result.Status)
		// 更新 reconcile attempts
		if updateErr := w.payoutRepo.UpdateReconcileAttempt(ctx, payoutID, int(payoutReq.ReconcileAttempts)+1); updateErr != nil {
			w.logger.Error("reconcile: failed to update attempts",
				"payout_id", payoutID, "error", updateErr)
		}
		return nil
	}
}

func (w *Worker) shouldTransferToManualReview(payoutReq *db.PayoutRequest) bool {
	// 超过最大重试次数
	if int(payoutReq.ReconcileAttempts) >= MaxReconcileAttempts {
		return true
	}

	// 超过时间阈值
	if !payoutReq.LastReconcileAt.Time.IsZero() {
		hoursSinceLastReconcile := time.Since(payoutReq.LastReconcileAt.Time).Hours()
		if hoursSinceLastReconcile >= ManualReviewHours {
			return true
		}
	}

	return false
}

// RedisLockProvider implements LockProvider using Redis
type RedisLockProvider struct {
	rdb *redis.Client
}

func NewRedisLockProvider(rdb *redis.Client) *RedisLockProvider {
	return &RedisLockProvider{rdb: rdb}
}

func (p *RedisLockProvider) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return p.rdb.SetNX(ctx, key, "locked", ttl).Result()
}

func (p *RedisLockProvider) ReleaseLock(ctx context.Context, key string) error {
	return p.rdb.Del(ctx, key).Err()
}
