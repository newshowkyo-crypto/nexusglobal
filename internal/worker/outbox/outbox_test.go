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
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/payout/repository/db"
)

type mockRepo struct {
	mu             sync.Mutex
	claimed        []db.TransactionalOutbox
	ackedIDs       []uuid.UUID
	failedIDs      []uuid.UUID
	deadLetterIDs  []uuid.UUID
	claimCallCount int

	// SEC-011 测试钩子
	ackOwner        map[uuid.UUID]string // id -> 接受 ACK 的 owner；nil 表 = 全拒绝
	failOwner       map[uuid.UUID]string
	deadLetterOwner map[uuid.UUID]string
	ownersSeen      []string
	leasesSeen      []int32
}

func (m *mockRepo) ClaimOutboxBatch(_ context.Context, owner string, _ int32, leaseSeconds int32) ([]db.TransactionalOutbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimCallCount++
	m.ownersSeen = append(m.ownersSeen, owner)
	m.leasesSeen = append(m.leasesSeen, leaseSeconds)
	if len(m.claimed) == 0 {
		return nil, nil
	}
	events := m.claimed
	m.claimed = nil
	return events, nil
}

func (m *mockRepo) AckOutboxEvent(_ context.Context, id uuid.UUID, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ackOwner != nil {
		if accept, ok := m.ackOwner[id]; !ok || accept != owner {
			return ErrStaleOwner
		}
	}
	m.ackedIDs = append(m.ackedIDs, id)
	return nil
}

func (m *mockRepo) FailOutboxEvent(_ context.Context, id uuid.UUID, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failOwner != nil {
		if accept, ok := m.failOwner[id]; !ok || accept != owner {
			return ErrStaleOwner
		}
	}
	m.failedIDs = append(m.failedIDs, id)
	return nil
}

func (m *mockRepo) UpdateOutboxStatus(_ context.Context, id uuid.UUID, status, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if status != "DeadLetter" {
		return nil
	}
	if m.deadLetterOwner != nil {
		if accept, ok := m.deadLetterOwner[id]; !ok || accept != owner {
			return ErrStaleOwner
		}
	}
	m.deadLetterIDs = append(m.deadLetterIDs, id)
	return nil
}

func newTestWorker(repo OutboxRepo, processor EventProcessor) *Worker {
	return New(repo, processor, Config{Concurrency: 1, BatchSize: 1, PollInterval: 10 * time.Millisecond, MaxRetries: 5}, slog.Default())
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

	w := newTestWorker(repo, processor)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	assert.True(t, processed, "processor should have been called")
	repo.mu.Lock()
	assert.Contains(t, repo.ackedIDs, eventID, "event should be acked on success")
	assert.Empty(t, repo.failedIDs, "no events should be failed")
	// SEC-011: claim 必须携带 owner 与 lease。
	assert.NotEmpty(t, repo.ownersSeen)
	assert.True(t, len(repo.ownersSeen[0]) > len("worker"), "owner must be a unique instance id")
	assert.Equal(t, int32(300), repo.leasesSeen[0], "default lease must be 300s")
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

	w := newTestWorker(repo, processor)
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

	w := newTestWorker(repo, processor)
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

// --- SEC-011 fencing 证据测试 ---

func TestOutbox_StaleOwnerAck_IsTolerated(t *testing.T) {
	// 事件处理成功但 ACK 时 owner 已过期：worker 必须容忍（记日志继续），
	// 不崩溃、不重复 Fail；事件将由新 owner 幂等重放。
	eventID := uuid.New()
	repo := &mockRepo{
		claimed: []db.TransactionalOutbox{
			{ID: eventID, AggregateType: "test", AggregateID: uuid.New(), EventType: "TEST", RetryCount: 0},
		},
		ackOwner: map[uuid.UUID]string{}, // 全部 ACK 拒绝（stale owner）
	}

	processor := func(_ context.Context, evt OutboxEvent) error { return nil }
	w := newTestWorker(repo, processor)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	repo.mu.Lock()
	assert.Empty(t, repo.ackedIDs, "stale ACK must not be recorded")
	assert.Empty(t, repo.failedIDs, "stale ACK must not trigger Fail")
	repo.mu.Unlock()
}

func TestOutbox_StaleOwnerFail_IsTolerated(t *testing.T) {
	eventID := uuid.New()
	repo := &mockRepo{
		claimed: []db.TransactionalOutbox{
			{ID: eventID, AggregateType: "test", AggregateID: uuid.New(), EventType: "TEST", RetryCount: 0},
		},
		failOwner: map[uuid.UUID]string{}, // 全部 Fail 拒绝（stale owner）
	}

	processor := func(_ context.Context, evt OutboxEvent) error { return errors.New("boom") }
	w := newTestWorker(repo, processor)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	repo.mu.Lock()
	assert.Empty(t, repo.failedIDs, "stale Fail must not be recorded")
	repo.mu.Unlock()
}

func TestOutbox_StaleOwnerDeadLetter_IsTolerated(t *testing.T) {
	eventID := uuid.New()
	repo := &mockRepo{
		claimed: []db.TransactionalOutbox{
			{ID: eventID, AggregateType: "test", AggregateID: uuid.New(), EventType: "TEST", RetryCount: 5},
		},
		deadLetterOwner: map[uuid.UUID]string{}, // 全部 DeadLetter 拒绝
	}

	processor := func(_ context.Context, evt OutboxEvent) error {
		t.Error("processor should NOT be called")
		return nil
	}
	w := newTestWorker(repo, processor)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	repo.mu.Lock()
	assert.Empty(t, repo.deadLetterIDs, "stale DeadLetter must not be recorded")
	repo.mu.Unlock()
}

func TestOutbox_ConfigLeaseSeconds_Propagates(t *testing.T) {
	repo := &mockRepo{}
	w := New(repo, func(_ context.Context, _ OutboxEvent) error { return nil },
		Config{Concurrency: 1, BatchSize: 1, PollInterval: 10 * time.Millisecond, MaxRetries: 5, LeaseSeconds: 60},
		slog.Default())
	require.Equal(t, int32(60), w.config.LeaseSeconds, "explicit lease must be preserved")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	w.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	w.Stop()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.NotEmpty(t, repo.leasesSeen)
	assert.Equal(t, int32(60), repo.leasesSeen[0])
}
