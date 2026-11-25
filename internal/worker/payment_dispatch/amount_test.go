package payment_dispatch

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDispatchAmountUsesMajorUnitsWithoutRounding(t *testing.T) {
	for _, currency := range []string{"USD", "SGD", "HKD", "EUR", "GBP", "AED"} {
		for _, tc := range []struct {
			minor int64
			want  string
		}{{1, "0.01"}, {12345, "123.45"}, {math.MaxInt64, "92233720368547758.07"}} {
			got, err := dispatchAmount(tc.minor, currency)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.String(), currency)
		}
	}
}

func TestDispatchAmountRefusesUnverifiedCurrencyAndInvalidAmount(t *testing.T) {
	for _, currency := range []string{"JPY", "KWD", "", "UNKNOWN"} {
		_, err := dispatchAmount(100, currency)
		require.Error(t, err)
	}
	for _, amount := range []int64{0, -1} {
		_, err := dispatchAmount(amount, "USD")
		require.Error(t, err)
	}
}
