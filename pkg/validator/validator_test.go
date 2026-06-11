package validator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateAddress_TRON_Valid(t *testing.T) {
	valid := []string{
		"TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9",
		"TQ9a31x8LmA8yp8xGL8R8HQz8xLmaDUnBj",
	}
	for _, addr := range valid {
		ok, msg := ValidateAddress(NetworkTRONTRC20, addr)
		assert.True(t, ok, "expected valid TRON address: %s, got: %s", addr, msg)
	}
}

func TestValidateAddress_TRON_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"0x742d35Cc6634C0532925a3b844Bc9e7595f2bD18",
		"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
		"abc",
		"X9YnFQHwKENnHjoxb3m9",
	}
	for _, addr := range invalid {
		ok, _ := ValidateAddress(NetworkTRONTRC20, addr)
		assert.False(t, ok, "expected invalid TRON address: %s", addr)
	}
}

func TestValidateAddress_ETH_Valid(t *testing.T) {
	valid := []string{
		"0x742d35Cc6634C0532925a3b844Bc9e7595f2bD18",
		"0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B",
	}
	for _, addr := range valid {
		ok, msg := ValidateAddress(NetworkETHERC20, addr)
		assert.True(t, ok, "expected valid ETH address: %s, got: %s", addr, msg)
	}
}

func TestValidateAddress_ETH_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"742d35Cc6634C0532925a3b844Bc9e7595f2bD18",
		"0xGGG",
		"TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9",
	}
	for _, addr := range invalid {
		ok, _ := ValidateAddress(NetworkETHERC20, addr)
		assert.False(t, ok, "expected invalid ETH address: %s", addr)
	}
}

func TestValidateAddress_BTC_Valid(t *testing.T) {
	valid := []string{
		"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
		"3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
		"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
	}
	for _, addr := range valid {
		ok, msg := ValidateAddress(NetworkBTC, addr)
		assert.True(t, ok, "expected valid BTC address: %s, got: %s", addr, msg)
	}
}

func TestValidateAddress_BTC_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"0x742d35Cc6634C0532925a3b844Bc9e7595f2bD18",
		"TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9",
		"bc1invalid",
	}
	for _, addr := range invalid {
		ok, _ := ValidateAddress(NetworkBTC, addr)
		assert.False(t, ok, "expected invalid BTC address: %s", addr)
	}
}

func TestValidateAddress_SOL_Valid(t *testing.T) {
	valid := []string{
		"7EcDhSYGxXyscszYEp35KHN8vvw3svAuLKTzXwCFLtV",
		"9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM",
	}
	for _, addr := range valid {
		ok, msg := ValidateAddress(NetworkSOL, addr)
		assert.True(t, ok, "expected valid SOL address: %s, got: %s", addr, msg)
	}
}

func TestValidateAddress_SOL_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"0x742d35Cc6634C0532925a3b844Bc9e7595f2bD18",
		"abc",
	}
	for _, addr := range invalid {
		ok, _ := ValidateAddress(NetworkSOL, addr)
		assert.False(t, ok, "expected invalid SOL address: %s", addr)
	}
}

func TestValidateAddress_UnsupportedNetwork(t *testing.T) {
	ok, msg := ValidateAddress("UNKNOWN", "someaddr")
	assert.False(t, ok)
	assert.Contains(t, msg, "unsupported network")
}

func TestValidateAddress_Whitespace(t *testing.T) {
	ok, _ := ValidateAddress(NetworkETHERC20, "  ")
	assert.False(t, ok)
}
