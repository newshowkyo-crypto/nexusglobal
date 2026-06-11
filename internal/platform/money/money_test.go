package money

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMinor_Add(t *testing.T) {
	tests := []struct {
		name     string
		a, b     Minor
		expected Minor
	}{
		{"positive", 100, 200, 300},
		{"zero", 0, 0, 0},
		{"negative result", 100, -200, -100},
		{"max safe", Minor(math.MaxInt64 / 2), Minor(math.MaxInt64 / 2), Minor(math.MaxInt64 - 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.a.Add(tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMinor_Sub(t *testing.T) {
	tests := []struct {
		name     string
		a, b     Minor
		expected Minor
	}{
		{"positive", 300, 200, 100},
		{"zero", 0, 0, 0},
		{"negative result", 100, 200, -100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.a.Sub(tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMinor_MulBps(t *testing.T) {
	tests := []struct {
		name     string
		amount   Minor
		bps      int32
		expected Minor
	}{
		{"100bps 1%", 10000, 100, 100},
		{"50bps 0.5%", 10000, 50, 50},
		{"0bps", 10000, 0, 0},
		{"250bps 2.5%", 10000, 250, 250},
		{"10000bps 100%", 10000, 10000, 10000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.amount.MulBps(tt.bps)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMinor_Decimal(t *testing.T) {
	m := NewMinor(12345)
	d := m.Decimal()
	assert.True(t, d.Equal(decimal.NewFromInt(12345)))
}

func TestMinor_Int64(t *testing.T) {
	m := NewMinor(12345)
	assert.Equal(t, int64(12345), m.Int64())
}

func TestMinorFromString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected Minor
		wantErr  bool
	}{
		{"integer", "100", 100, false},
		{"decimal truncated", "100.50", 100, false},
		{"negative", "-100", -100, false},
		{"zero", "0", 0, false},
		{"invalid", "abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MinorFromString(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestRound4(t *testing.T) {
	tests := []struct {
		name     string
		input    decimal.Decimal
		expected decimal.Decimal
	}{
		{"already 4", decimal.NewFromFloat(1.2345), decimal.NewFromFloat(1.2345)},
		{"round up", decimal.NewFromFloat(1.23456), decimal.NewFromFloat(1.2346)},
		{"round down", decimal.NewFromFloat(1.23454), decimal.NewFromFloat(1.2345)},
		{"integer", decimal.NewFromInt(1), decimal.NewFromInt(1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Round4(tt.input)
			assert.True(t, result.Equal(tt.expected), "expected %s, got %s", tt.expected, result)
		})
	}
}

func TestBpsOf(t *testing.T) {
	tests := []struct {
		name     string
		amount   decimal.Decimal
		bps      int32
		expected decimal.Decimal
	}{
		{"1% of 10000", decimal.NewFromInt(10000), 100, decimal.NewFromInt(100)},
		{"0.5% of 10000", decimal.NewFromInt(10000), 50, decimal.NewFromInt(50)},
		{"0% of 10000", decimal.NewFromInt(10000), 0, decimal.NewFromInt(0)},
		{"100% of 10000", decimal.NewFromInt(10000), 10000, decimal.NewFromInt(10000)},
		{"2.5% of 1000", decimal.NewFromInt(1000), 250, decimal.NewFromInt(25)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BpsOf(tt.amount, tt.bps)
			assert.True(t, result.Equal(tt.expected), "expected %s, got %s", tt.expected, result)
		})
	}
}

func TestToMinor(t *testing.T) {
	d := decimal.NewFromInt(12345)
	m := ToMinor(d)
	assert.Equal(t, Minor(12345), m)
}
