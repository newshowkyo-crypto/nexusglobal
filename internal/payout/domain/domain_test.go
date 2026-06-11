package domain

import (
	"testing"

	"github.com/nexusglobal/mamr/pkg/validator"
	"github.com/stretchr/testify/assert"
)

func TestCanTransition_Valid(t *testing.T) {
	tests := []struct {
		from, to PayoutStatus
	}{
		{StatusDraft, StatusBalanceFrozen},
		{StatusBalanceFrozen, StatusFxLocked},
		{StatusBalanceFrozen, StatusFailed},
		{StatusFxLocked, StatusLPProcessing},
		{StatusFxLocked, StatusFailed},
		{StatusFxLocked, StatusUnknownExternalState},
		{StatusLPProcessing, StatusConfirmed},
		{StatusLPProcessing, StatusFailed},
		{StatusLPProcessing, StatusUnknownExternalState},
		{StatusConfirmed, StatusReconciled},
		{StatusFailed, StatusBalanceReleased},
		{StatusUnknownExternalState, StatusConfirmed},
		{StatusUnknownExternalState, StatusFailed},
	}
	for _, tt := range tests {
		assert.True(t, CanTransition(tt.from, tt.to), "%s -> %s should be valid", tt.from, tt.to)
	}
}

func TestCanTransition_Invalid(t *testing.T) {
	tests := []struct {
		from, to PayoutStatus
	}{
		{StatusDraft, StatusConfirmed},
		{StatusDraft, StatusFailed},
		{StatusBalanceFrozen, StatusDraft},
		{StatusBalanceFrozen, StatusConfirmed},
		{StatusConfirmed, StatusDraft},
		{StatusReconciled, StatusDraft},
		{StatusBalanceReleased, StatusDraft},
		{StatusFailed, StatusDraft},
		{StatusFailed, StatusConfirmed},
		{StatusLPProcessing, StatusDraft},
		{StatusUnknownExternalState, StatusBalanceFrozen},
	}
	for _, tt := range tests {
		assert.False(t, CanTransition(tt.from, tt.to), "%s -> %s should be invalid", tt.from, tt.to)
	}
}

func TestTransition_Valid(t *testing.T) {
	newStatus, err := Transition(StatusDraft, StatusBalanceFrozen)
	assert.NoError(t, err)
	assert.Equal(t, StatusBalanceFrozen, newStatus)
}

func TestTransition_Invalid(t *testing.T) {
	_, err := Transition(StatusDraft, StatusConfirmed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ILLEGAL_STATE_TRANSITION")
}

func TestTransition_TerminalStates(t *testing.T) {
	_, err := Transition(StatusReconciled, StatusDraft)
	assert.Error(t, err)

	_, err = Transition(StatusBalanceReleased, StatusDraft)
	assert.Error(t, err)
}

func TestCleanAddress_ValidTRON(t *testing.T) {
	result := CleanAddress("TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", validator.NetworkTRONTRC20)
	assert.True(t, result.IsValid)
	assert.Empty(t, result.ValidationError)
	assert.NotEmpty(t, result.AddressHash)
	assert.NotEmpty(t, result.TargetAddressMasked)
	assert.Equal(t, "TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", result.TargetAddress)
}

func TestCleanAddress_InvalidAddress(t *testing.T) {
	result := CleanAddress("invalid-addr", validator.NetworkTRONTRC20)
	assert.False(t, result.IsValid)
	assert.NotEmpty(t, result.ValidationError)
}

func TestCleanAddress_Whitespace(t *testing.T) {
	result := CleanAddress("  TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9  ", validator.NetworkTRONTRC20)
	assert.Equal(t, "TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", result.TargetAddress)
	assert.True(t, result.IsValid)
}

func TestCleanAddress_Masked(t *testing.T) {
	result := CleanAddress("TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", validator.NetworkTRONTRC20)
	assert.True(t, len(result.TargetAddressMasked) > 8)
	assert.Contains(t, result.TargetAddressMasked, "TN3W")
	assert.Contains(t, result.TargetAddressMasked, "b3m9")
	assert.Contains(t, result.TargetAddressMasked, "*")
}

func TestCleanAddress_ShortAddress(t *testing.T) {
	result := CleanAddress("abc", validator.NetworkTRONTRC20)
	assert.Equal(t, "***", result.TargetAddressMasked)
}

func TestCleanAddress_HashDeterministic(t *testing.T) {
	r1 := CleanAddress("TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", validator.NetworkTRONTRC20)
	r2 := CleanAddress("TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", validator.NetworkTRONTRC20)
	assert.Equal(t, r1.AddressHash, r2.AddressHash)
}

func TestCleanAddress_DifferentAddresses(t *testing.T) {
	r1 := CleanAddress("TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", validator.NetworkTRONTRC20)
	r2 := CleanAddress("TQ9a31x8LmA8yp8xGL8R8HQz8xLmaDUnBj", validator.NetworkTRONTRC20)
	assert.NotEqual(t, r1.AddressHash, r2.AddressHash)
}

func TestPayoutStatus_Constants(t *testing.T) {
	assert.Equal(t, PayoutStatus("Draft"), StatusDraft)
	assert.Equal(t, PayoutStatus("BalanceFrozen"), StatusBalanceFrozen)
	assert.Equal(t, PayoutStatus("UnknownExternalState"), StatusUnknownExternalState)
}
