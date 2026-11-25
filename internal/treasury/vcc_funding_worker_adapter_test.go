package treasury

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	vccfunding "github.com/nexusglobal/mamr/internal/worker/vcc_funding"
)

// mockRepoForAdapter 复用 vcc_funding_router_test.go 的 mockVCCFundingRepository。

// TestVCCAdapter_TriggerSuccess 验证适配器正确调用 FinalizeVCCFunding（P0-A1 修复无限递归）。
func TestVCCAdapter_TriggerSuccess(t *testing.T) {
	mockRepo := new(mockVCCFundingRepository)
	mockRouter := new(mockVCCFundingRouteCaller)
	adapter, err := NewTreasuryFundingRouterAdapter(mockRouter, mockRepo)
	require.NoError(t, err)

	targetProviderID := uuid.New()
	merchantID := uuid.New()

	mockRouter.On("FinalizeVCCFunding", mock.Anything, mock.MatchedBy(func(req FinalizeVCCFundingRequest) bool {
		return req.BridgeBatchID == "bb-123" &&
			req.AmountMinor == 5000 &&
			req.MerchantID == merchantID &&
			req.TargetProviderID == targetProviderID
	})).Return(&FundingRouteResult{Approved: true, RouteType: RouteTypeCryptoDesensitized}, nil)

	err = adapter.TriggerVCCFunding(context.Background(), vccfunding.FundingTriggerRequest{
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      5000,
		Currency:         "USD",
		Provenance: map[string]string{
			"source_tier":            "TIER_1_HIGH_RISK",
			"crypto_bridge_batch_id": "bb-123",
		},
	})
	require.NoError(t, err)
	mockRouter.AssertExpectations(t)

	// 关键约束：不应调用 RouteFunding（避免无限递归）
	mockRouter.AssertNotCalled(t, "RouteFunding")
}

// TestVCCAdapter_RejectedFinalize 验证 FinalizeVCCFunding 拒绝时返回 error（worker 会重试，不 ACK）。
func TestVCCAdapter_RejectedFinalize(t *testing.T) {
	mockRepo := new(mockVCCFundingRepository)
	mockRouter := new(mockVCCFundingRouteCaller)
	adapter, err := NewTreasuryFundingRouterAdapter(mockRouter, mockRepo)
	require.NoError(t, err)

	targetProviderID := uuid.New()

	mockRouter.On("FinalizeVCCFunding", mock.Anything, mock.Anything).
		Return(&FundingRouteResult{Approved: false, RejectReason: "insufficient pending balance"}, nil)

	err = adapter.TriggerVCCFunding(context.Background(), vccfunding.FundingTriggerRequest{
		MerchantID:       uuid.New(),
		TargetProviderID: targetProviderID,
		AmountMinor:      100,
		Currency:         "USD",
		Provenance: map[string]string{
			"source_tier":            "TIER_2_COMPLIANT",
			"crypto_bridge_batch_id": "bb-456",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "rejected")
}

// TestVCCAdapter_MissingBridgeBatchID 验证 provenance 缺失 bridge_batch_id 时 fail-closed。
func TestVCCAdapter_MissingBridgeBatchID(t *testing.T) {
	mockRepo := new(mockVCCFundingRepository)
	mockRouter := new(mockVCCFundingRouteCaller)
	adapter, err := NewTreasuryFundingRouterAdapter(mockRouter, mockRepo)
	require.NoError(t, err)

	err = adapter.TriggerVCCFunding(context.Background(), vccfunding.FundingTriggerRequest{
		MerchantID:       uuid.New(),
		TargetProviderID: uuid.New(),
		AmountMinor:      100,
		Currency:         "USD",
		Provenance:       map[string]string{"source_tier": "TIER_2_COMPLIANT"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "bridge_batch_id")

	// 不应调用任何 router 方法
	mockRouter.AssertNotCalled(t, "FinalizeVCCFunding")
	mockRouter.AssertNotCalled(t, "RouteFunding")
}

// TestVCCAdapter_NilDeps 验证 nil 依赖构造失败（禁止空实现）。
func TestVCCAdapter_NilDeps(t *testing.T) {
	_, err := NewTreasuryFundingRouterAdapter(nil, new(mockVCCFundingRepository))
	require.Error(t, err)

	_, err = NewTreasuryFundingRouterAdapter(new(mockVCCFundingRouteCaller), nil)
	require.Error(t, err)
}

// mockVCCFundingRouteCaller 是 VCCFundingRouter 的 testify mock。
type mockVCCFundingRouteCaller struct {
	mock.Mock
}

func (m *mockVCCFundingRouteCaller) RouteFunding(ctx context.Context, req FundingRouteRequest) (*FundingRouteResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*FundingRouteResult), args.Error(1)
}

func (m *mockVCCFundingRouteCaller) FinalizeVCCFunding(ctx context.Context, req FinalizeVCCFundingRequest) (*FundingRouteResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*FundingRouteResult), args.Error(1)
}
