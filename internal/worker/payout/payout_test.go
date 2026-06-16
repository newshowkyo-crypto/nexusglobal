package payout

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/worker/outbox"
	"github.com/nexusglobal/mamr/pkg/external/lp"
)

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

func TestHandle_InvalidEventType(t *testing.T) {
	ctx := context.Background()
	lpCli := new(mockLPClient)
	logger := slog.Default()

	worker := New(nil, nil, lpCli, nil, nil, nil, logger)

	event := outbox.OutboxEvent{
		EventType: "INVALID_EVENT",
		Payload:   []byte(`{}`),
	}

	err := worker.Handle(ctx, event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected event type")
}

func TestHandle_InvalidPayload(t *testing.T) {
	ctx := context.Background()
	lpCli := new(mockLPClient)
	logger := slog.Default()

	worker := New(nil, nil, lpCli, nil, nil, nil, logger)

	event := outbox.OutboxEvent{
		EventType: "LP_PAYOUT_REQUESTED",
		Payload:   []byte(`invalid json`),
	}

	err := worker.Handle(ctx, event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestHandle_InvalidPayoutID(t *testing.T) {
	ctx := context.Background()
	lpCli := new(mockLPClient)
	logger := slog.Default()

	worker := New(nil, nil, lpCli, nil, nil, nil, logger)

	event := outbox.OutboxEvent{
		EventType: "LP_PAYOUT_REQUESTED",
		Payload:   []byte(`{"payout_request_id": "invalid-uuid"}`),
	}

	err := worker.Handle(ctx, event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid id")
}
