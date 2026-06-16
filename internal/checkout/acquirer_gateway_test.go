package checkout

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/pipeline"
)

func TestAcquirerGateway_RealModeRejectsRawCard(t *testing.T) {
	// Create a mock registry
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeStaleServing,
	})
	pipelineSvc := pipeline.NewService(nil, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, true) // isReal = true

	// Test: Real mode should reject raw card number
	_, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:    "order-123",
		MerchantID: uuid.New().String(),
		Amount:     decimal.NewFromInt(100),
		Currency:   "USD",
		CardNumber: "4242424242424242", // Raw PAN
		CardExpiry: "12/25",
		CardCVV:    "123",
	})

	require.Error(t, err)
	assert.Equal(t, ErrRawCardNotAllowed, err)
}

func TestAcquirerGateway_RealModeRejectsRawCVV(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeStaleServing,
	})
	pipelineSvc := pipeline.NewService(nil, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, true) // isReal = true

	// Test: Real mode should reject raw CVV even without PAN
	_, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:            "order-123",
		MerchantID:         uuid.New().String(),
		Amount:             decimal.NewFromInt(100),
		Currency:           "USD",
		CardNumber:         "",    // No PAN
		CardCVV:            "123", // Raw CVV
		PaymentMethodToken: "pm_token_123",
	})

	require.Error(t, err)
	assert.Equal(t, ErrRawCardNotAllowed, err)
}

func TestAcquirerGateway_RealModeRequiresPaymentMethodToken(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeStaleServing,
	})
	pipelineSvc := pipeline.NewService(nil, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, true) // isReal = true

	// Test: Real mode requires payment_method_token
	_, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:    "order-123",
		MerchantID: uuid.New().String(),
		Amount:     decimal.NewFromInt(100),
		Currency:   "USD",
		// No card data, but also no token
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "payment_method_token required in real mode")
}

func TestAcquirerGateway_MockModeAllowsRawCard(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeStaleServing,
	})
	pipelineSvc := pipeline.NewService(nil, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, false) // isReal = false (mock)

	// Test: Mock mode should allow raw card data
	// Note: This will fail because registry has no channels, but the raw card check should pass
	_, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:    "order-123",
		MerchantID: uuid.New().String(),
		Amount:     decimal.NewFromInt(100),
		Currency:   "USD",
		CardNumber: "4242424242424242",
		CardExpiry: "12/25",
		CardCVV:    "123",
	})

	// Should not be ErrRawCardNotAllowed
	if err != nil {
		assert.NotEqual(t, ErrRawCardNotAllowed, err)
	}
}

func TestAcquirerGateway_ChannelNotFound(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeStaleServing,
	})
	pipelineSvc := pipeline.NewService(nil, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, false) // mock mode

	// Test: Should return ErrChannelNotFound when registry has no channels
	_, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:    "order-123",
		MerchantID: uuid.New().String(),
		Amount:     decimal.NewFromInt(100),
		Currency:   "USD",
		CardNumber: "4242424242424242",
	})

	require.Error(t, err)
	assert.Equal(t, ErrChannelNotFound, err)
}

func TestAcquirerGateway_RealModeFailClosed(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeClosed, // fail-closed
	})
	pipelineSvc := pipeline.NewService(nil, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, true) // isReal = true

	// Test: Real mode with empty registry should fail-closed
	_, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:            "order-123",
		MerchantID:         uuid.New().String(),
		Amount:             decimal.NewFromInt(100),
		Currency:           "USD",
		PaymentMethodToken: "pm_token_123",
	})

	require.Error(t, err)
	// Should be ErrChannelNotFound (fail-closed)
	assert.Equal(t, ErrChannelNotFound, err)
}
