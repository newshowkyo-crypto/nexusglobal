package vcc_funding

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockOutboxRepository struct {
	mock.Mock
}

func (m *mockOutboxRepository) ClaimPendingEvents(ctx context.Context, batchSize int) ([]OutboxEvent, error) {
	args := m.Called(ctx, batchSize)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]OutboxEvent), args.Error(1)
}

func (m *mockOutboxRepository) MarkEventProcessed(ctx context.Context, eventID uuid.UUID, processedAt time.Time) error {
	args := m.Called(ctx, eventID, processedAt)
	return args.Error(0)
}

func (m *mockOutboxRepository) MarkEventFailed(ctx context.Context, eventID uuid.UUID, errorMsg string, retryCount int) error {
	args := m.Called(ctx, eventID, errorMsg, retryCount)
	return args.Error(0)
}

func (m *mockOutboxRepository) MarkEventUnknown(ctx context.Context, eventID uuid.UUID) error {
	args := m.Called(ctx, eventID)
	return args.Error(0)
}

type mockCryptoBridgeService struct {
	mock.Mock
}

func (m *mockCryptoBridgeService) Convert(ctx context.Context, req ConversionRequest) (*ConversionResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ConversionResult), args.Error(1)
}

type mockLedgerService struct {
	mock.Mock
}

func (m *mockLedgerService) CreateCryptoPoolLedgerEntry(ctx context.Context, entry LedgerEntry) error {
	args := m.Called(ctx, entry)
	return args.Error(0)
}

func (m *mockLedgerService) TriggerVCCFundingEvent(ctx context.Context, event VCCFundingEvent) error {
	args := m.Called(ctx, event)
	return args.Error(0)
}

type mockLogger struct {
	mock.Mock
}

func (m *mockLogger) Info(ctx context.Context, message string, fields ...interface{}) {
	m.Called(ctx, message)
}

func (m *mockLogger) Error(ctx context.Context, message string, err error, fields ...interface{}) {
	m.Called(ctx, message, err)
}

func TestWorker_ProcessCryptoBridgeEvent_Success(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-123"

	payload := CryptoBridgePayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		AmountMinor:      100000,
		Currency:         "USD",
		SourceTier:       "TIER_1_HIGH_RISK",
		TargetProviderID: targetProviderID,
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_CRYPTO_BRIDGE_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockLogger := new(mockLogger)

	conversionResult := &ConversionResult{
		BridgeBatchID:     bridgeBatchID,
		LPCConversionRef:  "lp-ref-456",
		TargetAmountMinor: 99500,
		Status:            "SUCCESS",
		SlippageBps:       50,
	}

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockCryptoBridge.On("Convert", ctx, mock.Anything).Return(conversionResult, nil)
	mockLedger.On("CreateCryptoPoolLedgerEntry", ctx, mock.Anything).Return(nil)
	mockLedger.On("TriggerVCCFundingEvent", ctx, mock.Anything).Return(nil)
	mockOutbox.On("MarkEventProcessed", ctx, eventID, mock.Anything).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing crypto bridge event").Return()
	mockLogger.On("Info", ctx, "Crypto bridge conversion completed").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockCryptoBridge.AssertExpectations(t)
	mockLedger.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

func TestWorker_ProcessCryptoBridgeEvent_ConversionTimeout(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-123"

	payload := CryptoBridgePayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		AmountMinor:      100000,
		Currency:         "USD",
		SourceTier:       "TIER_1_HIGH_RISK",
		TargetProviderID: targetProviderID,
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_CRYPTO_BRIDGE_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockCryptoBridge.On("Convert", ctx, mock.Anything).Return(nil, ErrConversionTimeout)
	mockOutbox.On("MarkEventUnknown", ctx, eventID).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing crypto bridge event").Return()
	mockLogger.On("Error", ctx, "Crypto bridge conversion failed", ErrConversionTimeout).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockCryptoBridge.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

func TestWorker_ProcessCryptoBridgeEvent_SlippageExceeded(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-123"

	payload := CryptoBridgePayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		AmountMinor:      100000,
		Currency:         "USD",
		SourceTier:       "TIER_1_HIGH_RISK",
		TargetProviderID: targetProviderID,
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_CRYPTO_BRIDGE_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockLogger := new(mockLogger)

	conversionResult := &ConversionResult{
		BridgeBatchID:     bridgeBatchID,
		LPCConversionRef:  "lp-ref-456",
		TargetAmountMinor: 90000,
		Status:            "SUCCESS",
		SlippageBps:       100,
	}

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockCryptoBridge.On("Convert", ctx, mock.Anything).Return(conversionResult, nil)
	mockOutbox.On("MarkEventFailed", ctx, eventID, ErrSlippageExceeded.Error(), 1).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing crypto bridge event").Return()
	mockLogger.On("Error", ctx, "Slippage exceeded threshold", ErrSlippageExceeded).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockCryptoBridge.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

func TestWorker_ProcessCryptoBridgeEvent_InvalidPayload(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_CRYPTO_BRIDGE_REQUESTED",
		Payload:    []byte("invalid json"),
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockOutbox.On("MarkEventFailed", ctx, eventID, "invalid payload", 1).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing crypto bridge event").Return()
	mockLogger.On("Error", ctx, "Failed to unmarshal crypto bridge payload", mock.Anything).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

func TestWorker_ProcessNonBridgeEvent(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "OTHER_EVENT_TYPE",
		Payload:    []byte(`{"test": "data"}`),
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockOutbox.On("MarkEventProcessed", ctx, eventID, mock.Anything).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing crypto bridge event").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

func TestWorker_Stop(t *testing.T) {
	ctx := context.Background()

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{}, nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockLogger, config)

	go func() {
		time.Sleep(100 * time.Millisecond)
		err := worker.Stop()
		assert.NoError(t, err)
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockLogger.AssertExpectations(t)
}

func TestWorker_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{}, nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "VCC funding worker context cancelled").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockLogger, config)

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := worker.Start(ctx)

	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	mockLogger.AssertExpectations(t)
}
