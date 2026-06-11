package bank

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockBank_SubmitTransfer_Success(t *testing.T) {
	m := NewMockBank()
	req := TransferRequest{SweepTransferID: uuid.New(), Amount: decimal.NewFromInt(1000)}
	res, err := m.SubmitTransfer(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BankRef)
	assert.Equal(t, "submitted", res.Status)
}

func TestMockBank_SubmitTransfer_Timeout(t *testing.T) {
	m := NewMockBank()
	m.SetDefaultScenario(ScenarioTimeout)
	req := TransferRequest{SweepTransferID: uuid.New()}
	_, err := m.SubmitTransfer(context.Background(), req)
	assert.ErrorIs(t, err, ErrTimeout)
}

func TestMockBank_SubmitTransfer_Failure(t *testing.T) {
	m := NewMockBank()
	m.SetDefaultScenario(ScenarioFailure)
	req := TransferRequest{SweepTransferID: uuid.New()}
	_, err := m.SubmitTransfer(context.Background(), req)
	assert.ErrorIs(t, err, ErrExplicitFailure)
}

func TestMockBank_SubmitTransfer_Deterministic(t *testing.T) {
	m := NewMockBank()
	id := uuid.New()
	req := TransferRequest{SweepTransferID: id}
	r1, _ := m.SubmitTransfer(context.Background(), req)
	r2, _ := m.SubmitTransfer(context.Background(), req)
	assert.Equal(t, r1.BankRef, r2.BankRef)
}

func TestMockBank_QueryTransfer_Success(t *testing.T) {
	m := NewMockBank()
	res, err := m.QueryTransfer(context.Background(), "bank_ref_123")
	require.NoError(t, err)
	assert.Equal(t, "confirmed", res.Status)
}

func TestMockBank_QueryTransfer_Timeout(t *testing.T) {
	m := NewMockBank()
	m.SetDefaultScenario(ScenarioTimeout)
	_, err := m.QueryTransfer(context.Background(), "bank_ref_123")
	assert.ErrorIs(t, err, ErrTimeout)
}
