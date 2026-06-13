package reconcile

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/pkg/external/lp"
)

type mockPayoutRepo struct {
	mock.Mock
}

func (m *mockPayoutRepo) GetPayoutRequest(ctx context.Context, id uuid.UUID) (*db.PayoutRequest, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.PayoutRequest), args.Error(1)
}

func (m *mockPayoutRepo) ListPayoutRequestsByStatus(ctx context.Context, status string, limit int32) ([]db.PayoutRequest, error) {
	args := m.Called(ctx, status, limit)
	return args.Get(0).([]db.PayoutRequest), args.Error(1)
}

func (m *mockPayoutRepo) UpdateReconcileAttempt(ctx context.Context, id uuid.UUID, attempts int) error {
	args := m.Called(ctx, id, attempts)
	return args.Error(0)
}

func (m *mockPayoutRepo) MarkManualReview(ctx context.Context, id uuid.UUID, reason string) error {
	args := m.Called(ctx, id, reason)
	return args.Error(0)
}

type mockConfirmer struct {
	mock.Mock
}

func (m *mockConfirmer) Confirm(ctx context.Context, payoutID uuid.UUID, providerRef string) error {
	args := m.Called(ctx, payoutID, providerRef)
	return args.Error(0)
}

func (m *mockConfirmer) Release(ctx context.Context, payoutID uuid.UUID) error {
	args := m.Called(ctx, payoutID)
	return args.Error(0)
}

type mockLPClient struct {
	mock.Mock
}

func (m *mockLPClient) FXLock(ctx context.Context, req lp.FXLockRequest) (lp.FXLockResult, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(lp.FXLockResult), args.Error(1)
}

func (m *mockLPClient) Payout(ctx context.Context, req lp.PayoutRequest) (lp.PayoutResult, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(lp.PayoutResult), args.Error(1)
}

func (m *mockLPClient) QueryStatus(ctx context.Context, providerRef string) (lp.StatusResult, error) {
	args := m.Called(ctx, providerRef)
	return args.Get(0).(lp.StatusResult), args.Error(1)
}

type mockLockProvider struct {
	mock.Mock
}

func (m *mockLockProvider) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	args := m.Called(ctx, key, ttl)
	return args.Get(0).(bool), args.Error(1)
}

func (m *mockLockProvider) ReleaseLock(ctx context.Context, key string) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func TestReconcileAll_Success(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	payoutRepo.On("ListPayoutRequestsByStatus", ctx, "UnknownExternalState", int32(100)).Return([]db.PayoutRequest{
		{ID: payoutID, Status: "UnknownExternalState", ProviderRef: "ref-123"},
	}, nil)
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(true, nil)
	locker.On("ReleaseLock", ctx, mock.Anything).Return(nil)
	payoutRepo.On("GetPayoutRequest", ctx, payoutID).Return(&db.PayoutRequest{
		ID:          payoutID,
		Status:      "UnknownExternalState",
		ProviderRef: "ref-123",
	}, nil)
	lpCli.On("QueryStatus", mock.Anything, "ref-123").Return(lp.StatusResult{
		Status: "confirmed",
	}, nil)
	confirmer.On("Confirm", ctx, payoutID, "ref-123").Return(nil)

	err := worker.ReconcileAll(ctx)

	require.NoError(t, err)
}

func TestReconcileAll_EmptyList(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutRepo.On("ListPayoutRequestsByStatus", ctx, "UnknownExternalState", int32(100)).Return([]db.PayoutRequest{}, nil)

	err := worker.ReconcileAll(ctx)

	require.NoError(t, err)
}

// T5-3: 测试 confirmed 分支
func TestReconcilePayout_Confirmed(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(true, nil)
	locker.On("ReleaseLock", ctx, mock.Anything).Return(nil)
	payoutRepo.On("GetPayoutRequest", ctx, payoutID).Return(&db.PayoutRequest{
		ID:          payoutID,
		Status:      "UnknownExternalState",
		ProviderRef: "ref-123",
	}, nil)
	lpCli.On("QueryStatus", mock.Anything, "ref-123").Return(lp.StatusResult{
		Status: "confirmed",
	}, nil)
	confirmer.On("Confirm", ctx, payoutID, "ref-123").Return(nil)

	err := worker.ReconcilePayout(ctx, payoutID)

	require.NoError(t, err)
	confirmer.AssertCalled(t, "Confirm", ctx, payoutID, "ref-123")
}

// T5-3: 测试 failed 分支
func TestReconcilePayout_Failed(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(true, nil)
	locker.On("ReleaseLock", ctx, mock.Anything).Return(nil)
	payoutRepo.On("GetPayoutRequest", ctx, payoutID).Return(&db.PayoutRequest{
		ID:          payoutID,
		Status:      "UnknownExternalState",
		ProviderRef: "ref-123",
	}, nil)
	lpCli.On("QueryStatus", mock.Anything, "ref-123").Return(lp.StatusResult{
		Status: "failed",
	}, nil)
	confirmer.On("Release", ctx, payoutID).Return(nil)

	err := worker.ReconcilePayout(ctx, payoutID)

	require.NoError(t, err)
	confirmer.AssertCalled(t, "Release", ctx, payoutID)
}

// T5-3: 测试仍 unknown 分支
func TestReconcilePayout_StillUnknown(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(true, nil)
	locker.On("ReleaseLock", ctx, mock.Anything).Return(nil)
	payoutRepo.On("GetPayoutRequest", ctx, payoutID).Return(&db.PayoutRequest{
		ID:                payoutID,
		Status:            "UnknownExternalState",
		ProviderRef:       "ref-123",
		ReconcileAttempts: 1,
	}, nil)
	payoutRepo.On("UpdateReconcileAttempt", ctx, payoutID, 2).Return(nil)
	lpCli.On("QueryStatus", mock.Anything, "ref-123").Return(lp.StatusResult{
		Status: "pending",
	}, nil)

	err := worker.ReconcilePayout(ctx, payoutID)

	require.NoError(t, err)
	payoutRepo.AssertCalled(t, "UpdateReconcileAttempt", ctx, payoutID, 2)
}

// T5-3: 测试超阈值转人工分支
func TestReconcilePayout_TransferToManualReview(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(true, nil)
	locker.On("ReleaseLock", ctx, mock.Anything).Return(nil)
	payoutRepo.On("GetPayoutRequest", ctx, payoutID).Return(&db.PayoutRequest{
		ID:                payoutID,
		Status:            "UnknownExternalState",
		ProviderRef:       "ref-123",
		ReconcileAttempts: MaxReconcileAttempts,
		LastReconcileAt:   pgtype.Timestamptz{Time: time.Now().Add(-48 * time.Hour), Valid: true},
	}, nil)
	payoutRepo.On("MarkManualReview", ctx, payoutID, mock.Anything).Return(nil)

	err := worker.ReconcilePayout(ctx, payoutID)

	require.NoError(t, err)
	payoutRepo.AssertCalled(t, "MarkManualReview", ctx, payoutID, mock.Anything)
}

func TestReconcilePayout_PayoutNotFound(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(true, nil)
	locker.On("ReleaseLock", ctx, mock.Anything).Return(nil)
	payoutRepo.On("GetPayoutRequest", ctx, payoutID).Return(nil, nil)

	err := worker.ReconcilePayout(ctx, payoutID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestReconcilePayout_NonUnknownStatus(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(true, nil)
	locker.On("ReleaseLock", ctx, mock.Anything).Return(nil)
	payoutRepo.On("GetPayoutRequest", ctx, payoutID).Return(&db.PayoutRequest{
		ID:     payoutID,
		Status: "Confirmed",
	}, nil)

	err := worker.ReconcilePayout(ctx, payoutID)

	require.NoError(t, err)
}

func TestReconcilePayout_NoProviderRef(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(true, nil)
	locker.On("ReleaseLock", ctx, mock.Anything).Return(nil)
	payoutRepo.On("GetPayoutRequest", ctx, payoutID).Return(&db.PayoutRequest{
		ID:          payoutID,
		Status:      "UnknownExternalState",
		ProviderRef: "",
	}, nil)

	err := worker.ReconcilePayout(ctx, payoutID)

	require.NoError(t, err)
}

// T5-1: 测试分布式锁未获取到时跳过
func TestReconcilePayout_LockNotAcquired(t *testing.T) {
	ctx := context.Background()
	payoutRepo := new(mockPayoutRepo)
	confirmer := new(mockConfirmer)
	lpCli := new(mockLPClient)
	locker := new(mockLockProvider)
	logger := slog.Default()

	worker := New(payoutRepo, confirmer, lpCli, locker, logger)

	payoutID := uuid.New()
	locker.On("AcquireLock", ctx, mock.Anything, mock.Anything).Return(false, nil)

	err := worker.ReconcilePayout(ctx, payoutID)

	require.NoError(t, err)
	payoutRepo.AssertNotCalled(t, "GetPayoutRequest")
}
