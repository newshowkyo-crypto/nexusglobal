package outbox

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nexusglobal/mamr/internal/payout/repository"
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
}

type Worker struct {
	repo      *repository.Repository
	processor EventProcessor
	config    Config
	logger    *slog.Logger
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

func New(repo *repository.Repository, processor EventProcessor, config Config, logger *slog.Logger) *Worker {
	return &Worker{
		repo:      repo,
		processor: processor,
		config:    config,
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
	logger := w.logger.With("worker_id", workerID)

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

		events, err := w.repo.ClaimOutboxBatch(ctx, "worker", w.config.BatchSize)
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
				continue
			}

			if err := w.processor(ctx, evt); err != nil {
				logger.Error("process outbox event failed",
					"event_id", evt.ID, "event_type", evt.EventType, "err", err)
			}
		}
	}
}
