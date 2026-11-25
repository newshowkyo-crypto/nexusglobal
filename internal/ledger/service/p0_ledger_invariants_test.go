package service

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
)

func TestNormalSideFor(t *testing.T) {
	cases := map[string]domain.Direction{
		"SETTLED":                  domain.DirectionCredit,
		"PENDING":                  domain.DirectionCredit,
		"FROZEN_PAYOUT":            domain.DirectionCredit,
		"RESERVE":                  domain.DirectionCredit,
		"FEE":                      domain.DirectionCredit,
		"INTERCOMPANY_PAYABLE":     domain.DirectionCredit,
		"PROVIDER_RECEIVABLE":      domain.DirectionDebit,
		"ACQUIRER_RECEIVABLE":      domain.DirectionDebit,
		"PLATFORM_DISPUTE_RESERVE": domain.DirectionDebit,
		"UNKNOWN_TYPE":             domain.DirectionDebit,
	}
	for accountType, want := range cases {
		assert.Equal(t, want, normalSideFor(accountType), "normal side for %s", accountType)
	}
}

func TestHashTransactionPayload_DistinguishesAmounts(t *testing.T) {
	base := domain.Transaction{
		TransactionType: "TEST",
		AssetCode:       "USD",
		Amount:          decimal.NewFromInt(100),
		AggregateType:   "payment_attempt",
		AggregateID:     "attempt-1",
		Entries: []domain.Entry{
			{AccountID: [16]byte{}, Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100), AssetCode: "USD"},
		},
	}
	h1 := hashTransactionPayload(base)

	base.Amount = decimal.NewFromInt(101)
	h2 := hashTransactionPayload(base)
	assert.NotEqual(t, h1, h2, "different amount must produce different payload hash")

	base.Amount = decimal.NewFromInt(100)
	assert.Equal(t, h1, hashTransactionPayload(base), "same payload must be stable")

	// 分录顺序不影响哈希
	txnA := base
	txnA.Entries = []domain.Entry{
		{AccountID: [16]byte{1}, Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(100), AssetCode: "USD"},
		{AccountID: [16]byte{2}, Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100), AssetCode: "USD"},
	}
	txnB := base
	txnB.Entries = []domain.Entry{
		{AccountID: [16]byte{2}, Direction: domain.DirectionDebit, Amount: decimal.NewFromInt(100), AssetCode: "USD"},
		{AccountID: [16]byte{1}, Direction: domain.DirectionCredit, Amount: decimal.NewFromInt(100), AssetCode: "USD"},
	}
	assert.Equal(t, hashTransactionPayload(txnA), hashTransactionPayload(txnB))
}

func TestValidateEntryPrecision(t *testing.T) {
	precisions := map[string]int32{"USD": 2, "BTC": 8}

	assert.NoError(t, validateEntryPrecision([]domain.Entry{
		{Amount: decimal.NewFromInt(9999), AssetCode: "USD"},
		{Amount: decimal.RequireFromString("0.00000001"), AssetCode: "BTC"},
	}, precisions))

	assert.Error(t, validateEntryPrecision([]domain.Entry{
		{Amount: decimal.RequireFromString("99.999"), AssetCode: "USD"},
	}, precisions), "USD must reject 3 decimal places")

	assert.Error(t, validateEntryPrecision([]domain.Entry{
		{Amount: decimal.RequireFromString("0.000000001"), AssetCode: "BTC"},
	}, precisions), "BTC must reject 9 decimal places")
}
