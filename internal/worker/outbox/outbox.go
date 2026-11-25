package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
)

type EventProcessor func(ctx context.Context, event OutboxEvent) error

type OutboxEvent struct {
	ID             string
	AggregateType  string
	AggregateID    string
	EventType      string
	Payload        []byte
	IdempotencyKey string
	RetryCount     int32
}

type Config struct {
	Concurrency  int
	BatchSize    int32
	PollInterval time.Duration
	MaxRetries   int
	// LeaseSeconds 是 Processing 状态的租约时长；超过该时长未 ACK 的事件
	// 会被其他 worker 重新认领（SEC-011 lease recovery）。0 = 默认 300 秒。
	LeaseSeconds int32
}

// ErrStaleOwner 由 repository 返回：事件已被其他 worker 重新认领，
// 当前 owner 的 ACK/Fail 被拒绝。这是正常竞争场景，worker 只需记日志。
var ErrStaleOwner = errors.New("outbox: stale owner")

type OutboxRepo interface {
	ClaimOutboxBatch(ctx context.Context, lockedBy string, limit int32, leaseSeconds int32) ([]db.TransactionalOutbox, error)
	AckOutboxEvent(ctx context.Context, id uuid.UUID, owner string) error
	FailOutboxEvent(ctx context.Context, id uuid.UUID, owner string) error
	UpdateOutboxStatus(ctx context.Context, id uuid.UUID, status, owner string) error
}

type Worker struct {
	repo      OutboxRepo
	processor EventProcessor
	config    Config
	ownerID   string
	logger    *slog.Logger
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

func New(repo OutboxRepo, processor EventProcessor, config Config, logger *slog.Logger) *Worker {
	if config.LeaseSeconds <= 0 {
		config.LeaseSeconds = 300
	}
	return &Worker{
		repo:      repo,
		processor: processor,
		config:    config,
		ownerID:   "outbox-" + uuid.NewString(),
		logger:    logger,
		stopCh:    make(chan struct{}),
	}
}

func (w *Worker) Start(ctx context.Context) {
	for i := 0; i < w.config.Concurrency; i++ {
		w.wg.Add(1)
		go w.run(ctx, i)
	}
}

func (w *Worker) Stop() {
	close(w.stopCh)
	w.wg.Wait()
}

func (w *Worker) run(ctx context.Context, workerID int) {
	defer w.wg.Done()
	logger := w.logger.With("worker_id", workerID, "owner", w.ownerID)

	for {
		select {
		case <-w.stopCh:
			logger.Info("outbox worker stopping")
			return
		case <-ctx.Done():
			logger.Info("outbox worker context cancelled")
			return
		default:
		}

		events, err := w.repo.ClaimOutboxBatch(ctx, w.ownerID, w.config.BatchSize, w.config.LeaseSeconds)
		if err != nil {
			logger.Error("claim outbox batch failed", "err", err)
			time.Sleep(w.config.PollInterval)
			continue
		}

		if len(events) == 0 {
			time.Sleep(w.config.PollInterval)
			continue
		}

		for _, event := range events {
			evt := OutboxEvent{
				ID:             event.ID.String(),
				AggregateType:  event.AggregateType,
				AggregateID:    event.AggregateID.String(),
				EventType:      event.EventType,
				Payload:        event.Payload,
				IdempotencyKey: event.IdempotencyKey,
				RetryCount:     event.RetryCount,
			}

			if int(evt.RetryCount) >= w.config.MaxRetries {
				logger.Warn("max retries reached, dead letter",
					"event_id", evt.ID, "event_type", evt.EventType,
					"retry_count", evt.RetryCount)
				if err := w.repo.UpdateOutboxStatus(ctx, event.ID, "DeadLetter", w.ownerID); err != nil {
					if errors.Is(err, ErrStaleOwner) {
						logger.Warn("dead letter rejected: event re-claimed by another owner",
							"event_id", evt.ID)
						continue
					}
					logger.Error("dead letter event failed", "event_id", evt.ID, "err", err)
				}
				continue
			}

			if err := w.processor(ctx, evt); err != nil {
				logger.Error("process outbox event failed",
					"event_id", evt.ID, "event_type", evt.EventType, "err", err)
				if failErr := w.repo.FailOutboxEvent(ctx, event.ID, w.ownerID); failErr != nil {
					if errors.Is(failErr, ErrStaleOwner) {
						// SEC-011 fencing：事件已被重新认领，本 owner 的 Fail 被拒绝是
						// 预期行为——不得覆盖新 owner 的进度。
						logger.Warn("fail rejected: event re-claimed by another owner",
							"event_id", evt.ID)
						continue
					}
					logger.Error("fail outbox event failed", "event_id", evt.ID, "err", failErr)
				}
			} else {
				if ackErr := w.repo.AckOutboxEvent(ctx, event.ID, w.ownerID); ackErr != nil {
					if errors.Is(ackErr, ErrStaleOwner) {
						// 事件处理成功但 ACK 前被重新认领：事件会被新 owner 幂等重放，
						// 处理器必须幂等（registry 约束），这里记日志即可。
						logger.Warn("ack rejected: event re-claimed by another owner (will replay idempotently)",
							"event_id", evt.ID)
						continue
					}
					logger.Error("ack outbox event failed", "event_id", evt.ID, "err", ackErr)
				}
			}
		}
	}
}
