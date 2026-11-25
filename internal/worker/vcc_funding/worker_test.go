package vcc_funding

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	issuing "github.com/nexusglobal/mamr/internal/issuing"
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

type mockFundingRouter struct {
	mock.Mock
}

func (m *mockFundingRouter) TriggerVCCFunding(ctx context.Context, req FundingTriggerRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

type mockIssuingFundingHandler struct {
	mock.Mock
}

func (m *mockIssuingFundingHandler) FundIssuingProvider(ctx context.Context, req IssuingFundingRequest) (*IssuingFundingResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*IssuingFundingResult), args.Error(1)
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
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Info", ctx, "Crypto bridge conversion completed").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

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
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "Crypto bridge conversion failed", ErrConversionTimeout).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

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
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "Slippage exceeded threshold", ErrSlippageExceeded).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

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
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "Failed to unmarshal crypto bridge payload", mock.Anything).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

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
	mockOutbox.On("MarkEventFailed", ctx, eventID, "unknown event type for vcc_funding worker", 1).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

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

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

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

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := worker.Start(ctx)

	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	mockLogger.AssertExpectations(t)
}

// TestWorker_ProcessFundingEvent_Success 验证 VCC_FUNDING_REQUESTED 事件
// 通过 FundingRouter 成功处理。
func TestWorker_ProcessFundingEvent_Success(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	payload := VCCFundingPayload{
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
		Provenance:       map[string]string{"source": "crypto_bridge"},
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_FUNDING_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockFundingRouter := new(mockFundingRouter)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockFundingRouter.On("TriggerVCCFunding", ctx, mock.Anything).Return(nil)
	mockOutbox.On("MarkEventProcessed", ctx, eventID, mock.Anything).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Info", ctx, "VCC funding triggered successfully").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockFundingRouter, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockFundingRouter.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

// TestWorker_ProcessFundingEvent_NilFundingRouter 验证 VCC_FUNDING_REQUESTED
// 在 FundingRouter 未注入时进入 MarkEventFailed（fail-closed，不 ACK）。
func TestWorker_ProcessFundingEvent_NilFundingRouter(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	payload := VCCFundingPayload{
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_FUNDING_REQUESTED",
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
	mockOutbox.On("MarkEventFailed", ctx, eventID, "funding router not configured", 1).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "VCC_FUNDING_REQUESTED requires FundingRouter, but none injected", ErrUnsupportedCapability).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	// fundingRouter = nil
	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

// TestWorker_ProcessFundingEvent_RouterError 验证 VCC_FUNDING_REQUESTED
// 在 FundingRouter 返回错误时进入 MarkEventFailed（不 ACK）。
func TestWorker_ProcessFundingEvent_RouterError(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	payload := VCCFundingPayload{
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_FUNDING_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	routerErr := errors.New("issuing service unavailable")

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockFundingRouter := new(mockFundingRouter)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockFundingRouter.On("TriggerVCCFunding", ctx, mock.Anything).Return(routerErr)
	mockOutbox.On("MarkEventFailed", ctx, eventID, routerErr.Error(), 1).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "VCC funding trigger failed", routerErr).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, mockFundingRouter, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockFundingRouter.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

// ============================================================================
// VCC_ISSUING_FUNDING_REQUESTED 测试
// ============================================================================

// TestProcessIssuingFundingEvent_Success 验证 VCC_ISSUING_FUNDING_REQUESTED
// 事件通过 IssuingFundingHandler 成功处理。
func TestProcessIssuingFundingEvent_Success(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-issuing-123"

	payload := VCCIssuingFundingPayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockIssuingHandler := new(mockIssuingFundingHandler)
	mockLogger := new(mockLogger)

	issuingResult := &IssuingFundingResult{
		ProviderRef: "provider-ref-789",
		Status:      "SUCCESS",
	}

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockIssuingHandler.On("FundIssuingProvider", ctx, mock.Anything).Return(issuingResult, nil)
	mockOutbox.On("MarkEventProcessed", ctx, eventID, mock.Anything).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Info", ctx, "Issuing funding completed successfully").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config,
		WithIssuingFundingHandler(mockIssuingHandler))

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockIssuingHandler.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

// TestProcessIssuingFundingEvent_Timeout_UnknownState 验证 VCC_ISSUING_FUNDING_REQUESTED
// 在 Provider 调用 timeout 时进入 MarkEventUnknown（不自动重试 Provider）。
// P0-5: 使用 issuing.ErrProviderTimeout 而非 ErrConversionTimeout。
func TestProcessIssuingFundingEvent_Timeout_UnknownState(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-issuing-timeout"

	payload := VCCIssuingFundingPayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockIssuingHandler := new(mockIssuingFundingHandler)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockIssuingHandler.On("FundIssuingProvider", ctx, mock.Anything).Return(nil, issuing.ErrProviderTimeout)
	mockOutbox.On("MarkEventUnknown", ctx, eventID).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "Issuing funding provider call failed", issuing.ErrProviderTimeout).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config,
		WithIssuingFundingHandler(mockIssuingHandler))

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockIssuingHandler.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

// TestProcessIssuingFundingEvent_NilHandler_FailClosed 验证 VCC_ISSUING_FUNDING_REQUESTED
// 在 IssuingFundingHandler 未注入时进入 MarkEventFailed（fail-closed，不 ACK）。
func TestProcessIssuingFundingEvent_NilHandler_FailClosed(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-issuing-nil"

	payload := VCCIssuingFundingPayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
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
	mockOutbox.On("MarkEventFailed", ctx, eventID, "issuing funding handler not configured", 1).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "VCC_ISSUING_FUNDING_REQUESTED requires IssuingFundingHandler, but none injected", ErrUnsupportedCapability).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	// issuingFundingHandler = nil (no WithIssuingFundingHandler option)
	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config)

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

// TestProcessIssuingFundingEvent_ProviderFailed_Processed 验证 VCC_ISSUING_FUNDING_REQUESTED
// 在 Provider 明确返回 FAILED 时进入 MarkEventProcessed（不重试 Provider）。
// P0-2: Provider 明确失败 → MarkEventProcessed（不重试），funding balance 状态已由 handler 更新为 FAILED。
func TestProcessIssuingFundingEvent_ProviderFailed_Processed(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-issuing-failed"

	payload := VCCIssuingFundingPayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockIssuingHandler := new(mockIssuingFundingHandler)
	mockLogger := new(mockLogger)

	issuingResult := &IssuingFundingResult{
		ProviderRef: "provider-ref-failed",
		Status:      "FAILED",
	}

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockIssuingHandler.On("FundIssuingProvider", ctx, mock.Anything).Return(issuingResult, nil)
	mockOutbox.On("MarkEventProcessed", ctx, eventID, mock.Anything).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "Issuing funding provider returned FAILED", mock.Anything).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config,
		WithIssuingFundingHandler(mockIssuingHandler))

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockIssuingHandler.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
}

// TestProcessIssuingFundingEvent_DoesNotGenerateCryptoBridge 验证
// VCC_ISSUING_FUNDING_REQUESTED 事件处理不会生成第二个 VCC_CRYPTO_BRIDGE_REQUESTED。
// 即：IssuingFundingHandler 只调用 FundIssuingProvider，不触发 CryptoBridge.Convert。
func TestProcessIssuingFundingEvent_DoesNotGenerateCryptoBridge(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-issuing-no-cb"

	payload := VCCIssuingFundingPayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockIssuingHandler := new(mockIssuingFundingHandler)
	mockLogger := new(mockLogger)

	issuingResult := &IssuingFundingResult{
		ProviderRef: "provider-ref-no-cb",
		Status:      "SUCCESS",
	}

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockIssuingHandler.On("FundIssuingProvider", ctx, mock.Anything).Return(issuingResult, nil)
	mockOutbox.On("MarkEventProcessed", ctx, eventID, mock.Anything).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Info", ctx, "Issuing funding completed successfully").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config,
		WithIssuingFundingHandler(mockIssuingHandler))

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockIssuingHandler.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
	// 关键断言：CryptoBridge.Convert 从未被调用
	mockCryptoBridge.AssertNotCalled(t, "Convert")
}

// ============================================================================
// ClaimPendingEvents lease 机制测试已移至 adapters_integration_test.go
// 真实 DB 集成测试需要 TEST_DATABASE_URL 环境变量
// ============================================================================

// ============================================================================
// P0-2/P0-5: processIssuingFundingEvent 状态机修复后的测试
// ============================================================================

// TestProcessIssuingFundingEvent_Success_FundedStatus 验证成功后
// IssuingFundingHandler 返回 SUCCESS → MarkEventProcessed（不重试）。
// funding balance 状态更新和 Completed Outbox 事件由 DBIssuingFundingHandler 在事务内完成。
func TestProcessIssuingFundingEvent_Success_FundedStatus(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-funded-success"

	payload := VCCIssuingFundingPayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockIssuingHandler := new(mockIssuingFundingHandler)
	mockLogger := new(mockLogger)

	issuingResult := &IssuingFundingResult{
		ProviderRef: "provider-ref-funded",
		Status:      "SUCCESS",
	}

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockIssuingHandler.On("FundIssuingProvider", ctx, mock.Anything).Return(issuingResult, nil)
	mockOutbox.On("MarkEventProcessed", ctx, eventID, mock.Anything).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Info", ctx, "Issuing funding completed successfully").Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config,
		WithIssuingFundingHandler(mockIssuingHandler))

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	// 关键断言：成功后 MarkEventProcessed（不是 MarkEventFailed）
	mockOutbox.AssertExpectations(t)
	mockIssuingHandler.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
	// 不应调用 MarkEventFailed 或 MarkEventUnknown
	mockOutbox.AssertNotCalled(t, "MarkEventFailed")
	mockOutbox.AssertNotCalled(t, "MarkEventUnknown")
}

// TestProcessIssuingFundingEvent_UnknownExternalState 验证 VCC_ISSUING_FUNDING_REQUESTED
// 在 Provider 返回 issuing.ErrUnknownExternalState 时进入 MarkEventUnknown。
func TestProcessIssuingFundingEvent_UnknownExternalState(t *testing.T) {
	ctx := context.Background()
	eventID := uuid.New()
	merchantID := uuid.New()
	targetProviderID := uuid.New()
	bridgeBatchID := "bridge-batch-unknown-state"

	payload := VCCIssuingFundingPayload{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := OutboxEvent{
		ID:         eventID,
		EventType:  "VCC_ISSUING_FUNDING_REQUESTED",
		Payload:    payloadBytes,
		Status:     "PENDING",
		RetryCount: 0,
		CreatedAt:  time.Now(),
	}

	mockOutbox := new(mockOutboxRepository)
	mockCryptoBridge := new(mockCryptoBridgeService)
	mockLedger := new(mockLedgerService)
	mockIssuingHandler := new(mockIssuingFundingHandler)
	mockLogger := new(mockLogger)

	mockOutbox.On("ClaimPendingEvents", ctx, 10).Return([]OutboxEvent{event}, nil)
	mockIssuingHandler.On("FundIssuingProvider", ctx, mock.Anything).Return(nil, issuing.ErrUnknownExternalState)
	mockOutbox.On("MarkEventUnknown", ctx, eventID).Return(nil)
	mockLogger.On("Info", ctx, "VCC funding worker started").Return()
	mockLogger.On("Info", ctx, "Processing VCC event").Return()
	mockLogger.On("Error", ctx, "Issuing funding provider call failed", issuing.ErrUnknownExternalState).Return()
	mockLogger.On("Info", ctx, "VCC funding worker stopping").Return()

	config := WorkerConfig{
		PollInterval:   100 * time.Millisecond,
		BatchSize:      10,
		MaxRetries:     3,
		MaxSlippageBps: 50,
	}

	worker := NewWorker(mockOutbox, mockCryptoBridge, mockLedger, nil, mockLogger, config,
		WithIssuingFundingHandler(mockIssuingHandler))

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = worker.Stop()
	}()

	err := worker.Start(ctx)

	assert.NoError(t, err)

	mockOutbox.AssertExpectations(t)
	mockIssuingHandler.AssertExpectations(t)
	mockLogger.AssertExpectations(t)
	// 不应调用 MarkEventFailed
	mockOutbox.AssertNotCalled(t, "MarkEventFailed")
}
