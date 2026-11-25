package vcc_funding

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestVCCFundingIdempotencyKey_Stable 验证幂等键由稳定业务字段派生（P0-A3）。
func TestVCCFundingIdempotencyKey_Stable(t *testing.T) {
	providerID := uuid.New()

	k1 := vccFundingIdempotencyKey("batch-abc", providerID)
	k2 := vccFundingIdempotencyKey("batch-abc", providerID)
	require.Equal(t, k1, k2)
	require.Equal(t, "vcc_funding:batch-abc:"+providerID.String(), k1)

	// 不同 batch 或 provider 必须产生不同键
	require.NotEqual(t, k1, vccFundingIdempotencyKey("batch-def", providerID))
	require.NotEqual(t, k1, vccFundingIdempotencyKey("batch-abc", uuid.New()))
}

// TestVCCFundingAggregateID_Stable 验证 aggregate_id 由 bridge_batch_id
// 确定性派生，不随重试随机变化（P0-A3）。
func TestVCCFundingAggregateID_Stable(t *testing.T) {
	a1 := vccFundingAggregateID("batch-abc")
	a2 := vccFundingAggregateID("batch-abc")
	require.Equal(t, a1, a2)
	require.NotEqual(t, a1, uuid.Nil)
	require.NotEqual(t, a1, vccFundingAggregateID("batch-def"))
}

// TestMarshalFundingPayload_NoManualConcat 验证 payload 由 json.Marshal
// 生成且包含全部下游字段（P0-A4）。
func TestMarshalFundingPayload_NoManualConcat(t *testing.T) {
	merchantID := uuid.New()
	providerID := uuid.New()
	payload, err := marshalFundingPayload(VCCFundingEvent{
		BridgeBatchID:    "batch-xyz",
		MerchantID:       merchantID,
		TargetProviderID: providerID,
		AmountMinor:      1234567890123, // 超过 rune 截断范围（2^21），旧手工拼接会丢失精度
		Currency:         "USD",
		Provenance:       map[string]string{"source_tier": "TIER_2_COMPLIANT"},
	})
	require.NoError(t, err)
	require.Contains(t, string(payload), `"amount_minor":1234567890123`)
	require.Contains(t, string(payload), `"bridge_batch_id":"batch-xyz"`)
	require.Contains(t, string(payload), `"target_provider_id":"`+providerID.String()+`"`)
}
