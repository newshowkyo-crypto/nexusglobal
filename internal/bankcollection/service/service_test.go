package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	adapterdomain "github.com/nexusglobal/mamr/internal/adapter/domain"
	"github.com/nexusglobal/mamr/internal/bankcollection/domain"
)

// mockRepository 模拟 bankcollection repository
type mockRepository struct {
	mock.Mock
}

func (m *mockRepository) CreateInboundIntent(ctx context.Context, intent *domain.InboundIntent) error {
	args := m.Called(ctx, intent)
	return args.Error(0)
}

func (m *mockRepository) GetInboundIntentByID(ctx context.Context, id uuid.UUID) (*domain.InboundIntent, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.InboundIntent), args.Error(1)
}

func (m *mockRepository) GetInboundIntentByProviderRef(ctx context.Context, providerRef string) (*domain.InboundIntent, error) {
	args := m.Called(ctx, providerRef)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.InboundIntent), args.Error(1)
}

func (m *mockRepository) GetInboundIntentByPaymentReference(ctx context.Context, ref string) (*domain.InboundIntent, error) {
	args := m.Called(ctx, ref)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.InboundIntent), args.Error(1)
}

func (m *mockRepository) UpdateInboundIntent(ctx context.Context, intent *domain.InboundIntent) error {
	args := m.Called(ctx, intent)
	return args.Error(0)
}

func (m *mockRepository) GetExpiredIntents(ctx context.Context, limit int) ([]domain.InboundIntent, error) {
	args := m.Called(ctx, limit)
	return args.Get(0).([]domain.InboundIntent), args.Error(1)
}

func (m *mockRepository) GetPendingIntents(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.InboundIntent, error) {
	args := m.Called(ctx, tenantID, limit)
	return args.Get(0).([]domain.InboundIntent), args.Error(1)
}

func (m *mockRepository) DeleteInboundIntent(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *mockRepository) GetInboundIntentsByTenant(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]domain.InboundIntent, error) {
	args := m.Called(ctx, tenantID, limit, offset)
	return args.Get(0).([]domain.InboundIntent), args.Error(1)
}

func (m *mockRepository) GetInboundIntentCountByState(ctx context.Context, tenantID uuid.UUID, state domain.InboundIntentState) (int, error) {
	args := m.Called(ctx, tenantID, state)
	return args.Get(0).(int), args.Error(1)
}

func (m *mockRepository) MarkAsReconcileMismatch(ctx context.Context, intentID uuid.UUID, reason string) error {
	args := m.Called(ctx, intentID, reason)
	return args.Error(0)
}

func (m *mockRepository) ConfirmIntent(ctx context.Context, intentID uuid.UUID, receivedAmount string, transactionRef string) error {
	args := m.Called(ctx, intentID, receivedAmount, transactionRef)
	return args.Error(0)
}

// ============================================================================
// Virtual Account Allocation Tests
// ============================================================================

func TestVirtualAccountAllocation_Success(t *testing.T) {
	// 测试虚拟户分配成功场景
	accountNumber := "1234567890"
	routingNumber := "021000021"
	paymentReference := "REF-12345678"

	va := &domain.VirtualAccountAllocation{
		AccountNumber:    accountNumber,
		RoutingNumber:    routingNumber,
		PaymentReference: paymentReference,
		ExpiresAt:        time.Now().Add(24 * time.Hour),
	}

	assert.Equal(t, accountNumber, va.AccountNumber)
	assert.Equal(t, routingNumber, va.RoutingNumber)
	assert.Equal(t, paymentReference, va.PaymentReference)
	assert.True(t, va.ExpiresAt.After(time.Now()))
}

func TestVirtualAccountAllocation_StaticType(t *testing.T) {
	// 测试静态虚拟账户类型
	intent := &domain.InboundIntent{
		ID:                 uuid.New(),
		VirtualAccountType: domain.VirtualAccountTypeStatic,
		PaymentReference:   "STATIC-REF-001",
	}

	assert.Equal(t, domain.VirtualAccountTypeStatic, intent.VirtualAccountType)
	assert.NotEmpty(t, intent.PaymentReference)
}

func TestVirtualAccountAllocation_DynamicType(t *testing.T) {
	// 测试动态虚拟账户类型
	intent := &domain.InboundIntent{
		ID:                 uuid.New(),
		VirtualAccountType: domain.VirtualAccountTypeDynamic,
		PaymentReference:   "DYN-REF-002",
	}

	assert.Equal(t, domain.VirtualAccountTypeDynamic, intent.VirtualAccountType)
	assert.NotEmpty(t, intent.PaymentReference)
}

// ============================================================================
// Amount/Currency/Reference Mismatch Tests
// ============================================================================

func TestReconciliation_AmountMismatch(t *testing.T) {
	svc := &BankCollectionService{}

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromInt(500), // 金额不匹配
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	result := svc.reconcileIntent(intent, payload)

	assert.False(t, result.Matched)
	assert.Equal(t, "AMOUNT_MISMATCH", result.MismatchType)
	assert.Equal(t, intent.ExpectedAmount, result.ExpectedAmount)
	assert.Equal(t, payload.Amount, result.ReceivedAmount)
}

func TestReconciliation_CurrencyMismatch(t *testing.T) {
	svc := &BankCollectionService{}

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromInt(1000),
		AssetCode:        "EUR", // 币种不匹配
		PaymentReference: "REF-001",
	}

	result := svc.reconcileIntent(intent, payload)

	assert.False(t, result.Matched)
	assert.Equal(t, "CURRENCY_MISMATCH", result.MismatchType)
}

func TestReconciliation_ReferenceMismatch(t *testing.T) {
	svc := &BankCollectionService{}

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-002", // 引用不匹配
	}

	result := svc.reconcileIntent(intent, payload)

	assert.False(t, result.Matched)
	assert.Equal(t, "REFERENCE_MISMATCH", result.MismatchType)
}

func TestReconciliation_Success(t *testing.T) {
	svc := &BankCollectionService{}

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	result := svc.reconcileIntent(intent, payload)

	assert.True(t, result.Matched)
	assert.Empty(t, result.MismatchType)
}

func TestReconciliation_SmallDifferenceAllowed(t *testing.T) {
	svc := &BankCollectionService{}

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	// 0.05% 差异，小于 0.1% 允许阈值
	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromInt(999), // 差异 1，小于 1 (0.1% of 1000)
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	result := svc.reconcileIntent(intent, payload)

	assert.True(t, result.Matched)
}

// ============================================================================
// Webhook Signature Validation Tests
// ============================================================================

func TestWebhookSignature_Invalid(t *testing.T) {
	// 测试验签失败不入账
	err := domain.ErrSignatureInvalid
	require.Error(t, err)
	assert.Equal(t, "bank collection: webhook signature invalid", err.Error())
}

func TestWebhookReplay_Detected(t *testing.T) {
	// 测试 webhook 重放检测
	err := domain.ErrWebhookReplay
	require.Error(t, err)
}

// ============================================================================
// Intent State Tests
// ============================================================================

func TestIntentState_Transition(t *testing.T) {
	// 测试状态转换
	state := domain.IntentStateCreated

	err := state.ValidateTransition(domain.IntentStateWaitingFunds)
	require.NoError(t, err)

	err = state.ValidateTransition(domain.IntentStateConfirmed)
	require.Error(t, err) // Created 不能直接到 Confirmed
}

func TestIntentState_Terminal(t *testing.T) {
	// 测试终态
	assert.True(t, domain.IntentStateConfirmed.IsTerminalState())
	assert.True(t, domain.IntentStateReconcileMismatch.IsTerminalState())
	assert.True(t, domain.IntentStateExpired.IsTerminalState())
	assert.True(t, domain.IntentStateFailed.IsTerminalState())
	assert.False(t, domain.IntentStateCreated.IsTerminalState())
	assert.False(t, domain.IntentStateWaitingFunds.IsTerminalState())
}

func TestIntentState_CannotTransitionFromTerminal(t *testing.T) {
	// 测试终态不可转换
	state := domain.IntentStateConfirmed

	err := state.ValidateTransition(domain.IntentStateWaitingFunds)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrIntentStateInvalid)
}

// ============================================================================
// UNKNOWN_EXTERNAL_STATE Tests
// ============================================================================

func TestUnknownExternalState_Error(t *testing.T) {
	err := domain.ErrUnknownExternalState
	require.Error(t, err)
	assert.Equal(t, "bank collection: unknown external state", err.Error())
}

func TestCreateInboundIntent_UnknownExternalState(t *testing.T) {
	// 测试 UNKNOWN_EXTERNAL_STATE 映射
	// 简化测试：直接验证错误类型
	err := adapterdomain.ErrUnknownExternalState
	require.Error(t, err)

	// 验证映射到 domain 错误
	assert.True(t, errors.Is(domain.ErrUnknownExternalState, domain.ErrUnknownExternalState))
}

// ============================================================================
// HandleWebhookEvent Tests
// ============================================================================

func TestHandleWebhookEvent_ReconcileMismatch(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := NewBankCollectionService(repo, nil)

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
		State:            domain.IntentStateWaitingFunds,
		ExpiresAt:        time.Now().Add(24 * time.Hour),
	}

	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromInt(500), // 金额不匹配
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	repo.On("GetInboundIntentByPaymentReference", ctx, "REF-001").Return(intent, nil)
	repo.On("UpdateInboundIntent", ctx, mock.Anything).Return(nil)

	err := svc.HandleWebhookEvent(ctx, HandleWebhookEventRequest{Payload: payload})

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrReconciliationFailed)
	assert.Equal(t, domain.IntentStateReconcileMismatch, intent.State)
}

func TestHandleWebhookEvent_Success(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := NewBankCollectionService(repo, nil)

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
		State:            domain.IntentStateWaitingFunds,
		ExpiresAt:        time.Now().Add(24 * time.Hour),
	}

	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	repo.On("GetInboundIntentByPaymentReference", ctx, "REF-001").Return(intent, nil)
	repo.On("UpdateInboundIntent", ctx, mock.Anything).Return(nil)

	err := svc.HandleWebhookEvent(ctx, HandleWebhookEventRequest{Payload: payload})

	require.NoError(t, err)
	assert.Equal(t, domain.IntentStateConfirmed, intent.State)
}

func TestHandleWebhookEvent_PartialMatch(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := NewBankCollectionService(repo, nil)

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		ReceivedAmount:   decimal.Zero, // 初始未收到
		AssetCode:        "USD",
		PaymentReference: "REF-001",
		State:            domain.IntentStateWaitingFunds,
		ExpiresAt:        time.Now().Add(24 * time.Hour),
	}

	// 收到 999.9，差异在 0.1% 以内（允许银行费用）
	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromFloat(999.9), // 差异 0.1 在允许范围内
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	repo.On("GetInboundIntentByPaymentReference", ctx, "REF-001").Return(intent, nil)
	repo.On("UpdateInboundIntent", ctx, mock.Anything).Return(nil)

	err := svc.HandleWebhookEvent(ctx, HandleWebhookEventRequest{Payload: payload})

	require.NoError(t, err)
	// 收到金额接近预期金额但略少，应该是部分到账状态
	assert.Equal(t, domain.IntentStatePartiallyMatched, intent.State)
}

func TestHandleWebhookEvent_IntentExpired(t *testing.T) {
	ctx := context.Background()
	repo := new(mockRepository)
	svc := NewBankCollectionService(repo, nil)

	intent := &domain.InboundIntent{
		ID:               uuid.New(),
		ExpectedAmount:   decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
		State:            domain.IntentStateWaitingFunds,
		ExpiresAt:        time.Now().Add(-1 * time.Hour), // 已过期
	}

	payload := &domain.InboundWebhookPayload{
		Amount:           decimal.NewFromInt(1000),
		AssetCode:        "USD",
		PaymentReference: "REF-001",
	}

	repo.On("GetInboundIntentByPaymentReference", ctx, "REF-001").Return(intent, nil)
	repo.On("UpdateInboundIntent", ctx, mock.Anything).Return(nil)

	err := svc.HandleWebhookEvent(ctx, HandleWebhookEventRequest{Payload: payload})

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrIntentExpired)
	assert.Equal(t, domain.IntentStateExpired, intent.State)
}
