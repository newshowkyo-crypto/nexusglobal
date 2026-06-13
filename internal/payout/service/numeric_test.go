package service

import (
	"testing"

	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/shopspring/decimal"
)

// TestDecimalToNumeric_Fractional is a regression guard against the historical
// bug where the hand-rolled converter negated the exponent, turning 100.5 into
// 10050 and 3000.00 into 30000000. Every fractional amount must round-trip exactly.
func TestDecimalToNumeric_Fractional(t *testing.T) {
	for _, s := range []string{"100.5", "100.1234", "3000.00", "0.01", "100", "1234567.89"} {
		d := decimal.RequireFromString(s)
		got := money.NumericToDecimal(decimalToNumeric(d))
		if !got.Equal(d) {
			t.Errorf("decimalToNumeric(%s) round-tripped to %s, want %s", s, got, d)
		}
	}
}
