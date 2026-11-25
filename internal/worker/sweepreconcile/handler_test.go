package sweepreconcile

import (
	"context"
	"encoding/json"

	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	treasurydb "github.com/nexusglobal/mamr/internal/treasury/repository/db"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
	"github.com/nexusglobal/mamr/pkg/external/bank"
)

type mockRepo struct {
	mock.Mock
}

func (m *mockRepo) GetTransfer(ctx context.Context, id uuid.UUID) (*treasurydb.SweepTransfer, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*treasurydb.SweepTransfer), args.Error(1)
}

func (m *mockRepo) UpdateTransferStatus(ctx context.Context, id uuid.UUID, status string, version int32) error {
	args := m.Called(ctx, id, status, version)
	return args.Error(0)
}

type mockBank struct {
	mock.Mock
}

func (m *mockBank) SubmitTransfer(ctx context.Context, req bank.TransferRequest) (bank.TransferResult, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(bank.TransferResult), args.Error(1)
}

func (m *mockBank) QueryTransfer(ctx context.Context, bankRef string) (bank.TransferStatus, error) {
	args := m.Called(ctx, bankRef)
	return args.Get(0).(bank.TransferStatus), args.Error(1)
}

func newEvent(t *testing.T, transferID uuid.UUID, bankRef string) outbox.OutboxEvent {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"transfer_id": transferID.String(), "bank_ref": bankRef})
	require.NoError(t, err)
	return outbox.OutboxEvent{
		ID:        uuid.NewString(),
		EventType: outbox.EventSweepReconcileRequested,
		Payload:   payload,
	}
}

func TestSweepReconcile_Completed_Confirms(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepo)
	bankCli := new(mockBank)
	w := New(repo, bankCli, slog.Default())

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&treasurydb.SweepTransfer{
		ID: transferID, Status: "Submitted", BankRef: "ref-1", Version: 3,
	}, nil)
	bankCli.On("QueryTransfer", mock.Anything, "ref-1").Return(bank.TransferStatus{BankRef: "ref-1", Status: "completed"}, nil)
	repo.On("UpdateTransferStatus", ctx, transferID, "Confirmed", int32(3)).Return(nil)

	err := w.Handle(ctx, newEvent(t, transferID, ""))
	require.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestSweepReconcile_Failed_MarksFailed(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepo)
	bankCli := new(mockBank)
	w := New(repo, bankCli, slog.Default())

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&treasurydb.SweepTransfer{
		ID: transferID, Status: "Submitted", BankRef: "ref-2", Version: 5,
	}, nil)
	bankCli.On("QueryTransfer", mock.Anything, "ref-2").Return(bank.TransferStatus{BankRef: "ref-2", Status: "failed"}, nil)
	repo.On("UpdateTransferStatus", ctx, transferID, "Failed", int32(5)).Return(nil)

	err := w.Handle(ctx, newEvent(t, transferID, ""))
	require.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestSweepReconcile_Pending_RetriesWithoutFakeConfirm(t *testing.T) {
	// RED-03：pending 不得伪造终态；返回 error 触发退避重试。
	ctx := context.Background()
	repo := new(mockRepo)
	bankCli := new(mockBank)
	w := New(repo, bankCli, slog.Default())

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&treasurydb.SweepTransfer{
		ID: transferID, Status: "Submitted", BankRef: "ref-3", Version: 1,
	}, nil)
	bankCli.On("QueryTransfer", mock.Anything, "ref-3").Return(bank.TransferStatus{BankRef: "ref-3", Status: "pending"}, nil)

	err := w.Handle(ctx, newEvent(t, transferID, ""))
	require.Error(t, err)
	repo.AssertNotCalled(t, "UpdateTransferStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestSweepReconcile_BankTimeout_Retries(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepo)
	bankCli := new(mockBank)
	w := New(repo, bankCli, slog.Default())

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&treasurydb.SweepTransfer{
		ID: transferID, Status: "Submitted", BankRef: "ref-4", Version: 1,
	}, nil)
	bankCli.On("QueryTransfer", mock.Anything, "ref-4").Return(bank.TransferStatus{}, bank.ErrTimeout)

	err := w.Handle(ctx, newEvent(t, transferID, ""))
	require.Error(t, err)
	repo.AssertNotCalled(t, "UpdateTransferStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestSweepReconcile_AlreadyTerminal_Acks(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepo)
	bankCli := new(mockBank)
	w := New(repo, bankCli, slog.Default())

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&treasurydb.SweepTransfer{
		ID: transferID, Status: "Confirmed", BankRef: "ref-5", Version: 9,
	}, nil)

	err := w.Handle(ctx, newEvent(t, transferID, ""))
	require.NoError(t, err)
	bankCli.AssertNotCalled(t, "QueryTransfer", mock.Anything, mock.Anything)
}

func TestSweepReconcile_MissingBankRef_Errors(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepo)
	bankCli := new(mockBank)
	w := New(repo, bankCli, slog.Default())

	transferID := uuid.New()
	repo.On("GetTransfer", ctx, transferID).Return(&treasurydb.SweepTransfer{
		ID: transferID, Status: "Submitted", BankRef: "", Version: 1,
	}, nil)

	err := w.Handle(ctx, newEvent(t, transferID, ""))
	require.Error(t, err)
}
