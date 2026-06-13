package repository

import (
	"testing"

	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/shopspring/decimal"
)

// TestDecimalToNumeric_Fractional guards against the historical exponent-negation
// bug that corrupted every fractional ledger entry amount.
func TestDecimalToNumeric_Fractional(t *testing.T) {
	for _, s := range []string{"100.5", "100.1234", "3000.00", "0.01", "100", "1234567.89"} {
		d := decimal.RequireFromString(s)
		n, err := decimalToNumeric(d)
		if err != nil {
			t.Fatalf("decimalToNumeric(%s): %v", s, err)
		}
		got := money.NumericToDecimal(n)
		if !got.Equal(d) {
			t.Errorf("decimalToNumeric(%s) round-tripped to %s, want %s", s, got, d)
		}
	}
}
