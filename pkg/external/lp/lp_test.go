package lp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockLP_FXLock_Success(t *testing.T) {
	m := NewMockLP()
	req := FXLockRequest{PayoutRequestID: uuid.New()}
	res, err := m.FXLock(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, res.LockID)
	assert.True(t, res.Rate.GreaterThan(decimal.Zero))
}

func TestMockLP_FXLock_Timeout(t *testing.T) {
	m := NewMockLP()
	m.SetDefaultScenario(ScenarioTimeout)
	req := FXLockRequest{PayoutRequestID: uuid.New()}
	_, err := m.FXLock(context.Background(), req)
	assert.ErrorIs(t, err, ErrTimeout)
}

func TestMockLP_FXLock_Failure(t *testing.T) {
	m := NewMockLP()
	m.SetDefaultScenario(ScenarioFailure)
	req := FXLockRequest{PayoutRequestID: uuid.New()}
	_, err := m.FXLock(context.Background(), req)
	assert.ErrorIs(t, err, ErrExplicitFailure)
}

func TestMockLP_FXLock_PerKeyScenario(t *testing.T) {
	m := NewMockLP()
	specificID := uuid.New()
	m.SetScenario(specificID.String(), ScenarioTimeout)

	_, err := m.FXLock(context.Background(), FXLockRequest{PayoutRequestID: specificID})
	assert.ErrorIs(t, err, ErrTimeout)

	_, err = m.FXLock(context.Background(), FXLockRequest{PayoutRequestID: uuid.New()})
	assert.NoError(t, err)
}

func TestMockLP_FXLock_Deterministic(t *testing.T) {
	m := NewMockLP()
	id := uuid.New()
	req := FXLockRequest{PayoutRequestID: id}
	r1, _ := m.FXLock(context.Background(), req)
	r2, _ := m.FXLock(context.Background(), req)
	assert.Equal(t, r1.LockID, r2.LockID)
}

func TestMockLP_Payout_Success(t *testing.T) {
	m := NewMockLP()
	req := PayoutRequest{PayoutRequestID: uuid.New()}
	res, err := m.Payout(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, res.ProviderRef)
	assert.Equal(t, "submitted", res.Status)
}

func TestMockLP_Payout_Timeout(t *testing.T) {
	m := NewMockLP()
	m.SetDefaultScenario(ScenarioTimeout)
	req := PayoutRequest{PayoutRequestID: uuid.New()}
	_, err := m.Payout(context.Background(), req)
	assert.ErrorIs(t, err, ErrTimeout)
}

func TestMockLP_Payout_Failure(t *testing.T) {
	m := NewMockLP()
	m.SetDefaultScenario(ScenarioFailure)
	req := PayoutRequest{PayoutRequestID: uuid.New()}
	_, err := m.Payout(context.Background(), req)
	assert.ErrorIs(t, err, ErrExplicitFailure)
}

func TestMockLP_QueryStatus_Success(t *testing.T) {
	m := NewMockLP()
	res, err := m.QueryStatus(context.Background(), "ref_123")
	require.NoError(t, err)
	assert.Equal(t, "confirmed", res.Status)
}

func TestMockLP_QueryStatus_Timeout(t *testing.T) {
	m := NewMockLP()
	m.SetDefaultScenario(ScenarioTimeout)
	_, err := m.QueryStatus(context.Background(), "ref_123")
	assert.ErrorIs(t, err, ErrTimeout)
}
