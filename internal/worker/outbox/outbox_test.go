package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/nexusglobal/mamr/internal/payout/repository/db"
)

type mockRepo struct {
	mu             sync.Mutex
	claimed        []db.TransactionalOutbox
	ackedIDs       []uuid.UUID
	failedIDs      []uuid.UUID
	deadLetterIDs  []uuid.UUID
	claimCallCount int
}

func (m *mockRepo) ClaimOutboxBatch(_ context.Context, _ string, _ int32) ([]db.TransactionalOutbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimCallCount++
	if len(m.claimed) == 0 {
		return nil, nil
	}
	events := m.claimed
	m.claimed = nil
	return events, nil
}

func (m *mockRepo) AckOutboxEvent(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackedIDs = append(m.ackedIDs, id)
	return nil
}

func (m *mockRepo) FailOutboxEvent(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failedIDs = append(m.failedIDs, id)
	return nil
}

func (m *mockRepo) UpdateOutboxStatus(_ context.Context, id uuid.UUID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if status == "DeadLetter" {
		m.deadLetterIDs = append(m.deadLetterIDs, id)
	}
	return nil
}

func TestOutbox_SuccessfulProcess_Acks(t *testing.T) {
	eventID := uuid.New()
	repo := &mockRepo{
		claimed: []db.TransactionalOutbox{
			{ID: eventID, AggregateType: "test", AggregateID: uuid.New(), EventType: "TEST", RetryCount: 0},
		},
	}

	processed := false
	processor := func(_ context.Context, evt OutboxEvent) error {
		processed = true
		return nil
	}

	w := New(repo, processor, Config{Concurrency: 1, BatchSize: 1, PollInterval: 10 * time.Millisecond, MaxRetries: 5}, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	assert.True(t, processed, "processor should have been called")
	repo.mu.Lock()
	assert.Contains(t, repo.ackedIDs, eventID, "event should be acked on success")
	assert.Empty(t, repo.failedIDs, "no events should be failed")
	repo.mu.Unlock()
}

func TestOutbox_FailedProcess_Fails(t *testing.T) {
	eventID := uuid.New()
	repo := &mockRepo{
		claimed: []db.TransactionalOutbox{
			{ID: eventID, AggregateType: "test", AggregateID: uuid.New(), EventType: "TEST", RetryCount: 0},
		},
	}

	processor := func(_ context.Context, evt OutboxEvent) error {
		return errors.New("process failed")
	}

	w := New(repo, processor, Config{Concurrency: 1, BatchSize: 1, PollInterval: 10 * time.Millisecond, MaxRetries: 5}, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	repo.mu.Lock()
	assert.Contains(t, repo.failedIDs, eventID, "event should be marked failed")
	assert.Empty(t, repo.ackedIDs, "failed event should not be acked")
	repo.mu.Unlock()
}

func TestOutbox_MaxRetries_DeadLetter(t *testing.T) {
	eventID := uuid.New()
	repo := &mockRepo{
		claimed: []db.TransactionalOutbox{
			{ID: eventID, AggregateType: "test", AggregateID: uuid.New(), EventType: "TEST", RetryCount: 5},
		},
	}

	processor := func(_ context.Context, evt OutboxEvent) error {
		t.Error("processor should NOT be called for dead-lettered event")
		return nil
	}

	w := New(repo, processor, Config{Concurrency: 1, BatchSize: 1, PollInterval: 10 * time.Millisecond, MaxRetries: 5}, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	repo.mu.Lock()
	assert.Contains(t, repo.deadLetterIDs, eventID, "event should be dead-lettered")
	assert.NotContains(t, repo.ackedIDs, eventID, "dead-lettered event should NOT be acked as Completed")
	assert.NotContains(t, repo.failedIDs, eventID, "dead-lettered event should NOT be marked as Failed")
	repo.mu.Unlock()
}
