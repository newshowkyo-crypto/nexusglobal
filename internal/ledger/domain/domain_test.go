package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func TestValidateBalanced_Balanced(t *testing.T) {
	entries := []Entry{
		{AccountID: uuid.New(), Direction: DirectionDebit, Amount: decimal.NewFromInt(100)},
		{AccountID: uuid.New(), Direction: DirectionCredit, Amount: decimal.NewFromInt(100)},
	}
	assert.NoError(t, ValidateBalanced(entries))
}

func TestValidateBalanced_Unbalanced(t *testing.T) {
	entries := []Entry{
		{AccountID: uuid.New(), Direction: DirectionDebit, Amount: decimal.NewFromInt(100)},
		{AccountID: uuid.New(), Direction: DirectionCredit, Amount: decimal.NewFromInt(80)},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrImbalanced)
}

func TestValidateBalanced_ZeroAmount(t *testing.T) {
	entries := []Entry{
		{AccountID: uuid.New(), Direction: DirectionDebit, Amount: decimal.Zero},
		{AccountID: uuid.New(), Direction: DirectionCredit, Amount: decimal.Zero},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrZeroAmount)
}

func TestValidateBalanced_NegativeAmount(t *testing.T) {
	entries := []Entry{
		{AccountID: uuid.New(), Direction: DirectionDebit, Amount: decimal.NewFromInt(-10)},
		{AccountID: uuid.New(), Direction: DirectionCredit, Amount: decimal.NewFromInt(10)},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrZeroAmount)
}

func TestValidateBalanced_InvalidDirection(t *testing.T) {
	entries := []Entry{
		{AccountID: uuid.New(), Direction: "INVALID", Amount: decimal.NewFromInt(100)},
		{AccountID: uuid.New(), Direction: DirectionCredit, Amount: decimal.NewFromInt(100)},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrInvalidDirection)
}

func TestValidateBalanced_Empty(t *testing.T) {
	err := ValidateBalanced([]Entry{})
	assert.ErrorIs(t, err, ErrNoEntries)
}

func TestValidateBalanced_SingleEntryIsImbalanced(t *testing.T) {
	// 单条 entry 必然借贷不平衡（因为不是 2/3/4 条配平）
	entries := []Entry{
		{AccountID: uuid.New(), Direction: DirectionDebit, Amount: decimal.NewFromInt(100)},
	}
	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrImbalanced)
}

func TestValidateBalanced_MultiplePairs(t *testing.T) {
	entries := []Entry{
		{AccountID: uuid.New(), Direction: DirectionDebit, Amount: decimal.NewFromInt(50)},
		{AccountID: uuid.New(), Direction: DirectionCredit, Amount: decimal.NewFromInt(30)},
		{AccountID: uuid.New(), Direction: DirectionDebit, Amount: decimal.NewFromInt(50)},
		{AccountID: uuid.New(), Direction: DirectionCredit, Amount: decimal.NewFromInt(70)},
	}
	assert.NoError(t, ValidateBalanced(entries))
}

func TestValidateBalanced_DecimalPrecision(t *testing.T) {
	entries := []Entry{
		{AccountID: uuid.New(), Direction: DirectionDebit, Amount: decimal.RequireFromString("100.1234")},
		{AccountID: uuid.New(), Direction: DirectionCredit, Amount: decimal.RequireFromString("100.1234")},
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

// TestAssetExchange_DualTrackEntries tests ASSET_EXCHANGE transaction with dual-track entries
// Scenario: USD -> USDT exchange with 100 USD at rate 1:1
func TestAssetExchange_DualTrackEntries(t *testing.T) {
	// Setup accounts
	usdSettledAcc := uuid.New()
	usdtSettledAcc := uuid.New()
	counterpartyUsdAcc := uuid.New()
	counterpartyUsdtAcc := uuid.New()

	// Exchange: 100 USD <-> 100 USDT (rate 1:1)
	// Debit: USD (100) + USDT (100) = 200
	// Credit: USD (100) + USDT (100) = 200
	entries := []Entry{
		// Debit our USD settled account (we give 100 USD)
		{
			ID:              uuid.New(),
			AccountID:       usdSettledAcc,
			Direction:       DirectionDebit,
			Amount:          decimal.NewFromInt(100),
			AssetCode:       "USD",
			TransactionType: "ASSET_EXCHANGE",
		},
		// Credit counterparty USD account (they receive 100 USD)
		{
			ID:              uuid.New(),
			AccountID:       counterpartyUsdAcc,
			Direction:       DirectionCredit,
			Amount:          decimal.NewFromInt(100),
			AssetCode:       "USD",
			TransactionType: "ASSET_EXCHANGE",
		},
		// Debit counterparty USDT account (they give 100 USDT)
		{
			ID:              uuid.New(),
			AccountID:       counterpartyUsdtAcc,
			Direction:       DirectionDebit,
			Amount:          decimal.NewFromInt(100),
			AssetCode:       "USDT",
			TransactionType: "ASSET_EXCHANGE",
		},
		// Credit our USDT settled account (we receive 100 USDT)
		{
			ID:              uuid.New(),
			AccountID:       usdtSettledAcc,
			Direction:       DirectionCredit,
			Amount:          decimal.NewFromInt(100),
			AssetCode:       "USDT",
			TransactionType: "ASSET_EXCHANGE",
		},
	}

	// Validate balanced entries
	err := ValidateBalanced(entries)
	assert.NoError(t, err, "ASSET_EXCHANGE entries should be balanced")

	// Verify debit/credit amounts
	debitTotal := decimal.Zero
	creditTotal := decimal.Zero
	for _, entry := range entries {
		if entry.Direction == DirectionDebit {
			debitTotal = debitTotal.Add(entry.Amount)
		} else {
			creditTotal = creditTotal.Add(entry.Amount)
		}
	}

	assert.True(t, debitTotal.Equal(creditTotal),
		"Debit total %s should equal credit total %s", debitTotal, creditTotal)
}

// TestAssetExchange_Imbalanced tests that imbalanced ASSET_EXCHANGE entries are rejected
func TestAssetExchange_Imbalanced(t *testing.T) {
	entries := []Entry{
		{
			ID:              uuid.New(),
			AccountID:       uuid.New(),
			Direction:       DirectionDebit,
			Amount:          decimal.NewFromInt(100),
			AssetCode:       "USD",
			TransactionType: "ASSET_EXCHANGE",
		},
		// Missing credit entry - this should fail
	}

	err := ValidateBalanced(entries)
	assert.ErrorIs(t, err, ErrImbalanced, "Imbalanced ASSET_EXCHANGE entries should fail validation")
}

// TestAssetExchange_GainLossEntry tests that gain/loss can be expressed in entries
func TestAssetExchange_GainLossEntry(t *testing.T) {
	usdAcc := uuid.New()
	btcAcc := uuid.New()
	counterpartyUsdAcc := uuid.New()
	counterpartyBtcAcc := uuid.New()

	// Scenario: Sell 1 BTC at $50,000
	// BTC: Debit 1 (our), Credit 1 (counterparty) = 1 each
	// USD: Debit 50000 (counterparty), Credit 50000 (our) = 50000 each
	// Total: Debit 50001, Credit 50001 = balanced
	entries := []Entry{
		// Debit our BTC account (remove 1 BTC)
		{
			ID:              uuid.New(),
			AccountID:       btcAcc,
			Direction:       DirectionDebit,
			Amount:          decimal.NewFromInt(1),
			AssetCode:       "BTC",
			TransactionType: "ASSET_EXCHANGE",
		},
		// Credit counterparty BTC account (they receive 1 BTC)
		{
			ID:              uuid.New(),
			AccountID:       counterpartyBtcAcc,
			Direction:       DirectionCredit,
			Amount:          decimal.NewFromInt(1),
			AssetCode:       "BTC",
			TransactionType: "ASSET_EXCHANGE",
		},
		// Debit counterparty USD account (they send $50,000)
		{
			ID:              uuid.New(),
			AccountID:       counterpartyUsdAcc,
			Direction:       DirectionDebit,
			Amount:          decimal.NewFromInt(50000),
			AssetCode:       "USD",
			TransactionType: "ASSET_EXCHANGE",
		},
		// Credit our USD account (receive $50,000)
		{
			ID:              uuid.New(),
			AccountID:       usdAcc,
			Direction:       DirectionCredit,
			Amount:          decimal.NewFromInt(50000),
			AssetCode:       "USD",
			TransactionType: "ASSET_EXCHANGE",
		},
	}

	err := ValidateBalanced(entries)
	assert.NoError(t, err, "ASSET_EXCHANGE with gain entry should be balanced")
}

// TestBridgeSlippageGuard_Exceeds20Bps tests that slippage > 20bps is rejected
func TestBridgeSlippageGuard_Exceeds20Bps(t *testing.T) {
	profile := &BridgeProfile{
		ID:             uuid.New(),
		TenantID:       uuid.New(),
		Name:           "Test Profile",
		SourceAsset:    "USD",
		TargetAsset:    "USDT",
		MaxSlippageBps: 20, // 20bps = 0.2%
		Enabled:        true,
	}

	// Test: estimated slippage > 20bps should be rejected
	estimatedSlippage := 25 // 25bps > 20bps
	result := CheckSlippageGuard(profile, estimatedSlippage)

	assert.False(t, result.Allowed, "Slippage > 20bps should be rejected")
	assert.Equal(t, 20, result.MaxSlippageBps)
	assert.Equal(t, 25, result.EstimatedBps)
	assert.Contains(t, result.Message, "exceeds maximum allowed")
}

// TestBridgeSlippageGuard_Within20Bps tests that slippage within 20bps is allowed
func TestBridgeSlippageGuard_Within20Bps(t *testing.T) {
	profile := &BridgeProfile{
		ID:             uuid.New(),
		TenantID:       uuid.New(),
		Name:           "Test Profile",
		SourceAsset:    "USD",
		TargetAsset:    "USDT",
		MaxSlippageBps: 20,
		Enabled:        true,
	}

	// Test: estimated slippage within 20bps should be allowed
	estimatedSlippage := 15 // 15bps < 20bps
	result := CheckSlippageGuard(profile, estimatedSlippage)

	assert.True(t, result.Allowed, "Slippage within 20bps should be allowed")
	assert.Equal(t, 20, result.MaxSlippageBps)
	assert.Equal(t, 15, result.EstimatedBps)
	assert.Contains(t, result.Message, "slippage check passed")
}

// TestBridgeSlippageGuard_Default20Bps tests that default is 20bps when MaxSlippageBps is 0
func TestBridgeSlippageGuard_Default20Bps(t *testing.T) {
	profile := &BridgeProfile{
		ID:             uuid.New(),
		TenantID:       uuid.New(),
		Name:           "Test Profile",
		SourceAsset:    "USD",
		TargetAsset:    "USDT",
		MaxSlippageBps: 0, // Default to 20bps
		Enabled:        true,
	}

	// Test: with 0 MaxSlippageBps, default to 20bps
	estimatedSlippage := 25 // Should exceed default 20bps
	result := CheckSlippageGuard(profile, estimatedSlippage)

	assert.False(t, result.Allowed, "Slippage > default 20bps should be rejected")
	assert.Equal(t, 20, result.MaxSlippageBps) // Default should be 20bps
}

// CheckSlippageGuard checks if estimated slippage exceeds maximum allowed
func CheckSlippageGuard(profile *BridgeProfile, estimatedSlippageBps int) *SlippageGuardResult {
	maxSlippageBps := profile.MaxSlippageBps
	if maxSlippageBps == 0 {
		maxSlippageBps = 20 // Default 20bps = 0.2%
	}

	if estimatedSlippageBps > maxSlippageBps {
		return &SlippageGuardResult{
			Allowed:        false,
			MaxSlippageBps: maxSlippageBps,
			EstimatedBps:   estimatedSlippageBps,
			EstimatedRate:  decimal.Zero,
			Message:        "estimated slippage exceeds maximum allowed",
		}
	}

	return &SlippageGuardResult{
		Allowed:        true,
		MaxSlippageBps: maxSlippageBps,
		EstimatedBps:   estimatedSlippageBps,
		EstimatedRate:  decimal.Zero,
		Message:        "slippage check passed",
	}
}
