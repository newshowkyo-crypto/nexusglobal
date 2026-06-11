package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func TestValidateBalanced_Balanced(t *testing.T) {
	entries := []Entry{
		{AccountID: "acc_settled", Direction: DirectionDebit, Amount: decimal.NewFromInt(100)},
		{AccountID: "acc_frozen", Direction: DirectionCredit, Amount: decimal.NewFromInt(100)},
	}
	assert.NoError(t, ValidateBalanced(entries))
}

func TestValidateBalanced_Unbalanced(t *testing.T) {
	entries := []Entry{
		{AccountID: "acc_settled", Direction: DirectionDebit, Amount: decimal.NewFromInt(100)},
		{AccountID: "acc_frozen", Direction: DirectionCredit, Amount: decimal.NewFromInt(80)},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrImbalanced)
}

func TestValidateBalanced_ZeroAmount(t *testing.T) {
	entries := []Entry{
		{AccountID: "acc_settled", Direction: DirectionDebit, Amount: decimal.Zero},
		{AccountID: "acc_frozen", Direction: DirectionCredit, Amount: decimal.Zero},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrZeroAmount)
}

func TestValidateBalanced_NegativeAmount(t *testing.T) {
	entries := []Entry{
		{AccountID: "acc_settled", Direction: DirectionDebit, Amount: decimal.NewFromInt(-10)},
		{AccountID: "acc_frozen", Direction: DirectionCredit, Amount: decimal.NewFromInt(10)},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrZeroAmount)
}

func TestValidateBalanced_InvalidDirection(t *testing.T) {
	entries := []Entry{
		{AccountID: "acc_settled", Direction: "INVALID", Amount: decimal.NewFromInt(100)},
		{AccountID: "acc_frozen", Direction: DirectionCredit, Amount: decimal.NewFromInt(100)},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrInvalidDirection)
}

func TestValidateBalanced_Empty(t *testing.T) {
	err := ValidateBalanced([]Entry{})
	assert.ErrorIs(t, err, ErrNoEntries)
}

func TestValidateBalanced_OddEntries(t *testing.T) {
	entries := []Entry{
		{AccountID: "acc", Direction: DirectionDebit, Amount: decimal.NewFromInt(100)},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrOddEntries)
}

func TestValidateBalanced_MultiplePairs(t *testing.T) {
	entries := []Entry{
		{AccountID: "acc1_dr", Direction: DirectionDebit, Amount: decimal.NewFromInt(50)},
		{AccountID: "acc1_cr", Direction: DirectionCredit, Amount: decimal.NewFromInt(30)},
		{AccountID: "acc2_dr", Direction: DirectionDebit, Amount: decimal.NewFromInt(50)},
		{AccountID: "acc2_cr", Direction: DirectionCredit, Amount: decimal.NewFromInt(70)},
	}
	assert.NoError(t, ValidateBalanced(entries))
}

func TestValidateBalanced_DecimalPrecision(t *testing.T) {
	entries := []Entry{
		{AccountID: "acc_dr", Direction: DirectionDebit, Amount: decimal.RequireFromString("100.1234")},
		{AccountID: "acc_cr", Direction: DirectionCredit, Amount: decimal.RequireFromString("100.1234")},
	}
	assert.NoError(t, ValidateBalanced(entries))
}

func TestDirection_Constants(t *testing.T) {
	assert.Equal(t, Direction("DEBIT"), DirectionDebit)
	assert.Equal(t, Direction("CREDIT"), DirectionCredit)
}

func TestAccountType_Constants(t *testing.T) {
	assert.Equal(t, AccountType("SETTLED"), AccountTypeSettled)
	assert.Equal(t, AccountType("FROZEN_PAYOUT"), AccountTypeFrozenPayout)
	assert.Equal(t, AccountType("EXTERNAL_PAYOUT_SETTLEMENT"), AccountTypeExternalPayout)
}
