package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
)

// TestValidateBalanced_Unbalanced 测试借贷不平衡
func TestValidateBalanced_Unbalanced(t *testing.T) {
	entries := []domain.LedgerEntry{
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100)},
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(50)},
	}

	err := domain.ValidateBalanced(entries)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrImbalanced)
}

// TestValidateBalanced_Balanced 测试借贷平衡
func TestValidateBalanced_Balanced(t *testing.T) {
	entries := []domain.LedgerEntry{
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100)},
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(100)},
	}

	err := domain.ValidateBalanced(entries)

	require.NoError(t, err)
}

// TestValidateBalanced_Empty 测试空条目
func TestValidateBalanced_Empty(t *testing.T) {
	entries := []domain.LedgerEntry{}

	err := domain.ValidateBalanced(entries)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrNoEntries)
}

// TestValidateBalanced_SingleEntry 测试单条目不平衡
func TestValidateBalanced_SingleEntry(t *testing.T) {
	entries := []domain.LedgerEntry{
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100)},
	}

	err := domain.ValidateBalanced(entries)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrImbalanced)
}

// TestValidateBalanced_MultiplePairs 测试多对平衡
func TestValidateBalanced_MultiplePairs(t *testing.T) {
	entries := []domain.LedgerEntry{
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100)},
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(100)},
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(50)},
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(50)},
	}

	err := domain.ValidateBalanced(entries)

	require.NoError(t, err)
}

// TestValidateBalanced_ZeroAmount 测试零金额
func TestValidateBalanced_ZeroAmount(t *testing.T) {
	entries := []domain.LedgerEntry{
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(0)},
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(0)},
	}

	err := domain.ValidateBalanced(entries)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrZeroAmount)
}

// TestValidateBalanced_NegativeAmount 测试负金额
func TestValidateBalanced_NegativeAmount(t *testing.T) {
	entries := []domain.LedgerEntry{
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(-100)},
		{ID: uuid.New(), AccountID: uuid.New(), Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(-100)},
	}

	err := domain.ValidateBalanced(entries)

	require.Error(t, err)
}

// TestDirectionConstants 测试方向常量
func TestDirectionConstants(t *testing.T) {
	assert.Equal(t, domain.Direction("DEBIT"), domain.DirectionDebit)
	assert.Equal(t, domain.Direction("CREDIT"), domain.DirectionCredit)
}

// TestTransactionTypeConstants 测试交易类型常量
func TestTransactionTypeConstants(t *testing.T) {
	assert.Equal(t, domain.TransactionType("ORDER_REFUND"), domain.TxnTypeOrderRefund)
	assert.Equal(t, domain.TransactionType("CHARGEBACK_RECEIVED"), domain.TxnTypeChargebackReceived)
	assert.Equal(t, domain.TransactionType("CHARGEBACK_FEE"), domain.TxnTypeChargebackFee)
	assert.Equal(t, domain.TransactionType("CHARGEBACK_REVERSED"), domain.TxnTypeChargebackReversed)
}

// TestAccountTypeConstants 测试账户类型常量
func TestAccountTypeConstants(t *testing.T) {
	assert.Equal(t, domain.AccountType("SETTLED"), domain.AccountTypeSettled)
	assert.Equal(t, domain.AccountType("CHARGEBACK"), domain.AccountTypeChargeback)
	assert.Equal(t, domain.AccountType("FROZEN_PAYOUT"), domain.AccountTypeFrozenPayout)
	assert.Equal(t, domain.AccountType("RESERVE"), domain.AccountTypeReserve)
}
