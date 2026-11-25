package checkout

import (
	"context"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
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

// ──────────────────────────────────────────────────────────────────────────────
// P0-3: Pipeline Isolation 阻断测试
// ──────────────────────────────────────────────────────────────────────────────

// mockAcquirerClient 是 domain.AcquirerClient 的测试 mock。
type mockAcquirerClient struct{}

func (m *mockAcquirerClient) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	return &domain.AuthorizeResult{Status: domain.PaymentStatusAuthorized}, nil
}
func (m *mockAcquirerClient) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	return &domain.CaptureResult{Status: "captured"}, nil
}
func (m *mockAcquirerClient) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	return &domain.RefundResult{Status: "refunded"}, nil
}
func (m *mockAcquirerClient) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	return &domain.VerifiedWebhookEvent{}, nil
}

// injectBundle 使用反射将一个 ClientBundle 注入 ChannelRegistry 的私有 bundles map。
// 这是测试专用辅助函数，生产代码不得使用。
func injectBundle(t *testing.T, reg *registry.ChannelRegistry, bundle *domain.ClientBundle) {
	t.Helper()
	v := reflect.ValueOf(reg).Elem()
	bundlesField := v.FieldByName("bundles")
	// 使用 unsafe 访问私有字段
	bundlesPtr := unsafe.Pointer(bundlesField.UnsafeAddr())
	bundles := *(*map[uuid.UUID]*domain.ClientBundle)(bundlesPtr)
	bundles[bundle.ChannelID] = bundle
}

// mockPipelineRepoForCheckout 是 pipeline.Repository 的测试 mock。
type mockPipelineRepoForCheckout struct {
	group *pipeline.IsolationGroup
}

func (m *mockPipelineRepoForCheckout) GetGroupByID(ctx context.Context, id uuid.UUID) (*pipeline.IsolationGroup, error) {
	if m.group != nil && m.group.ID == id {
		return m.group, nil
	}
	return nil, pipeline.ErrGroupNotFound
}
func (m *mockPipelineRepoForCheckout) GetGroupByName(ctx context.Context, name string) (*pipeline.IsolationGroup, error) {
	if m.group != nil && m.group.Name == name {
		return m.group, nil
	}
	return nil, pipeline.ErrGroupNotFound
}
func (m *mockPipelineRepoForCheckout) CreateGroup(ctx context.Context, group *pipeline.IsolationGroup) error {
	return nil
}
func (m *mockPipelineRepoForCheckout) UpdateGroup(ctx context.Context, group *pipeline.IsolationGroup) error {
	return nil
}
func (m *mockPipelineRepoForCheckout) FreezeGroup(ctx context.Context, id uuid.UUID, reason, operator string) error {
	return nil
}
func (m *mockPipelineRepoForCheckout) UnfreezeGroup(ctx context.Context, id uuid.UUID, operator string) error {
	return nil
}
func (m *mockPipelineRepoForCheckout) LogOverride(ctx context.Context, log *pipeline.AdminOverrideAuditLog) error {
	return nil
}

// TestAcquirerGateway_FrozenIsolationGroupRejected 验证 FROZEN 隔离组阻断 Charge。
func TestAcquirerGateway_FrozenIsolationGroupRejected(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeStaleServing,
	})

	// 创建一个 FROZEN 隔离组
	groupID := uuid.New()
	frozenGroup := &pipeline.IsolationGroup{
		ID:     groupID,
		Name:   "frozen-group",
		Status: pipeline.IsolationGroupStatusFrozen,
	}
	pipelineSvc := pipeline.NewService(&mockPipelineRepoForCheckout{group: frozenGroup}, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, false)

	// 注入一个 active channel，关联到 FROZEN 隔离组
	channelID := uuid.New()
	merchantID := uuid.New()
	bundle := &domain.ClientBundle{
		ChannelID:        channelID,
		ChannelCode:      "MOCK",
		ProviderCode:     "mock",
		MerchantID:       merchantID,
		Client:           &mockAcquirerClient{},
		IsolationGroupID: &groupID,
		IsActive:         true,
	}
	injectBundle(t, reg, bundle)

	_, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:           "MOCK-order-123",
		MerchantID:        merchantID.String(),
		AcquirerChannelID: channelID,
		Amount:            decimal.NewFromInt(100),
		Currency:          "USD",
		CardNumber:        "4242424242424242",
	})

	require.Error(t, err)
	assert.Equal(t, ErrGroupFrozen, err, "FROZEN isolation group should block Charge")
}

// TestAcquirerGateway_RiskHoldRejected 验证 RISK_HOLD 隔离组阻断 Charge。
func TestAcquirerGateway_RiskHoldRejected(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeStaleServing,
	})

	// 创建一个 RISK_HOLD 隔离组
	groupID := uuid.New()
	riskHoldGroup := &pipeline.IsolationGroup{
		ID:     groupID,
		Name:   "risk-hold-group",
		Status: pipeline.IsolationGroupStatusRiskHold,
	}
	pipelineSvc := pipeline.NewService(&mockPipelineRepoForCheckout{group: riskHoldGroup}, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, false)

	// 注入一个 active channel，关联到 RISK_HOLD 隔离组
	channelID := uuid.New()
	merchantID := uuid.New()
	bundle := &domain.ClientBundle{
		ChannelID:        channelID,
		ChannelCode:      "MOCK",
		ProviderCode:     "mock",
		MerchantID:       merchantID,
		Client:           &mockAcquirerClient{},
		IsolationGroupID: &groupID,
		IsActive:         true,
	}
	injectBundle(t, reg, bundle)

	_, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:           "MOCK-order-123",
		MerchantID:        merchantID.String(),
		AcquirerChannelID: channelID,
		Amount:            decimal.NewFromInt(100),
		Currency:          "USD",
		CardNumber:        "4242424242424242",
	})

	require.Error(t, err)
	assert.Equal(t, ErrGroupFrozen, err, "RISK_HOLD isolation group should block Charge with ErrGroupFrozen")
}

// TestAcquirerGateway_ActiveGroupAllowed 验证 ACTIVE 隔离组允许 Charge。
func TestAcquirerGateway_ActiveGroupAllowed(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		FailMode: registry.FailModeStaleServing,
	})

	// 创建一个 ACTIVE 隔离组
	groupID := uuid.New()
	activeGroup := &pipeline.IsolationGroup{
		ID:     groupID,
		Name:   "active-group",
		Status: pipeline.IsolationGroupStatusActive,
	}
	pipelineSvc := pipeline.NewService(&mockPipelineRepoForCheckout{group: activeGroup}, time.Now)

	gw := NewAcquirerGateway(reg, pipelineSvc, false)

	// 注入一个 active channel，关联到 ACTIVE 隔离组
	channelID := uuid.New()
	merchantID := uuid.New()
	bundle := &domain.ClientBundle{
		ChannelID:        channelID,
		ChannelCode:      "MOCK",
		ProviderCode:     "mock",
		MerchantID:       merchantID,
		Client:           &mockAcquirerClient{},
		IsolationGroupID: &groupID,
		IsActive:         true,
	}
	injectBundle(t, reg, bundle)

	result, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID:           "MOCK-order-123",
		MerchantID:        merchantID.String(),
		AcquirerChannelID: channelID,
		Amount:            decimal.NewFromInt(100),
		Currency:          "USD",
		CardNumber:        "4242424242424242",
	})

	require.NoError(t, err, "ACTIVE isolation group should allow Charge")
	assert.NotNil(t, result)
}

// TestAcquirerGateway_SetPipelineService 验证 SetPipelineService 正确注入。
func TestAcquirerGateway_SetPipelineService(t *testing.T) {
	reg := registry.NewChannelRegistry(registry.RegistryConfig{})
	gw := NewAcquirerGateway(reg, nil, false)

	// 初始状态：pipelineSvc 为 nil
	assert.Nil(t, gw.PipelineService())

	// 注入后：pipelineSvc 不为 nil
	svc := pipeline.NewService(&mockPipelineRepoForCheckout{}, time.Now)
	gw.SetPipelineService(svc)
	assert.Equal(t, svc, gw.PipelineService())
}
