package payment_dispatch

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// The current gateway adapters consume major units and convert to two-decimal
// provider amounts. Refuse other currencies until their adapter contract is supported.
func dispatchAmount(minor int64, currency string) (decimal.Decimal, error) {
	if minor <= 0 {
		return decimal.Zero, fmt.Errorf("payment amount must be positive")
	}
	switch strings.ToUpper(currency) {
	case "USD", "SGD", "HKD", "EUR", "GBP", "AED":
		return decimal.New(minor, -2), nil
	default:
		return decimal.Zero, fmt.Errorf("payment currency %s has no verified amount conversion", currency)
	}
}
