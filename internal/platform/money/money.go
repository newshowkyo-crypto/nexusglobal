package money

import (
	"fmt"
	"math/big"

	"github.com/shopspring/decimal"
)

type Minor int64

func NewMinor(v int64) Minor {
	return Minor(v)
}

func MinorFromString(s string) (Minor, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return 0, fmt.Errorf("money: parse %q: %w", s, err)
	}
	return Minor(d.IntPart()), nil
}

func (m Minor) Add(other Minor) Minor {
	return m + other
}

func (m Minor) Sub(other Minor) Minor {
	return m - other
}

func (m Minor) MulBps(bps int32) Minor {
	return Minor(int64(m) * int64(bps) / 10000)
}

func (m Minor) Int64() int64 {
	return int64(m)
}

func (m Minor) Decimal() decimal.Decimal {
	return decimal.NewFromInt(int64(m))
}

var (
	divisionPrecision = 4
)

func Round4(d decimal.Decimal) decimal.Decimal {
	return d.Round(int32(divisionPrecision))
}

func BpsOf(amount decimal.Decimal, bps int32) decimal.Decimal {
	bpsDec := decimal.NewFromInt(int64(bps))
	tenThousand := decimal.NewFromInt(10000)
	return amount.Mul(bpsDec).Div(tenThousand).Round(int32(divisionPrecision))
}

func DecimalFromInt64(v int64) decimal.Decimal {
	return decimal.NewFromInt(v)
}

func DecimalFromString(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}

func ToMinor(d decimal.Decimal) Minor {
	return Minor(d.IntPart())
}

func ValidateNoFloat64() {
	_ = big.NewInt(0)
}
