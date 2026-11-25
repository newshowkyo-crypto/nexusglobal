package treasury

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	vccfunding "github.com/nexusglobal/mamr/internal/worker/vcc_funding"
)

// ============================================================================
// P0-A2: Worker FundingRouter 与 Treasury VCCFundingRouter 的统一适配
//
// 两个接口语义不同：
//   - vcc_funding.FundingRouter.TriggerVCCFunding：worker 事件消费侧，
//     输入只有 merchant/provider/amount/currency/provenance
//   - treasury.VCCFundingRouter.RouteFunding：路由决策侧，
//     还需要 source_tier / funding_mode / source_ledger_tx_id
//
// 本适配器负责补全缺失的权威字段：
//   - SourceTier ← provenance["source_tier"]（crypto bridge 事件携带）
//   - TargetFundingMode ← issuing_providers 表（provider 权威配置）
//   - SourceLedgerTxID ← provenance["crypto_bridge_batch_id"]
//
// 禁止 Noop / nil / 空实现：装配时 router 与 repo 均不可为 nil。
// ============================================================================

// TreasuryFundingRouterAdapter 把 vcc_funding worker 的 FundingRouter 接口
// 适配到 Treasury VCCFundingRouter。
type TreasuryFundingRouterAdapter struct {
	router VCCFundingRouter
	repo   VCCFundingRepository
}

// NewTreasuryFundingRouterAdapter 创建适配器。router 与 repo 必须非 nil（fail-closed）。
func NewTreasuryFundingRouterAdapter(router VCCFundingRouter, repo VCCFundingRepository) (*TreasuryFundingRouterAdapter, error) {
	if router == nil {
		return nil, fmt.Errorf("treasury: funding router adapter requires non-nil VCCFundingRouter")
	}
	if repo == nil {
		return nil, fmt.Errorf("treasury: funding router adapter requires non-nil VCCFundingRepository")
	}
	return &TreasuryFundingRouterAdapter{router: router, repo: repo}, nil
}

// TriggerVCCFunding 实现 vcc_funding.FundingRouter。
//
// P0-A1 修复：不再调用 RouteFunding（会导致无限递归），
// 而是调用 FinalizeVCCFunding 执行桥接完成后的最终入金：
//   - debit pending_balance（资金从在途转出）
//   - credit issuing provider funding balance（资金最终入金到发卡池）
//   - 写 VCC_ISSUING_FUNDING_REQUESTED outbox 事件
//   - 幂等键：vcc_funding_finalize:<bridge_batch_id>:<target_provider_id>
//
// 成功（Approved=true）返回 nil，worker 才会标记事件 Processed；
// 拒绝或异常返回 error，worker 标记 Failed 进入重试，不得 ACK。
func (a *TreasuryFundingRouterAdapter) TriggerVCCFunding(ctx context.Context, req vccfunding.FundingTriggerRequest) error {
	if req.TargetProviderID == uuid.Nil {
		return fmt.Errorf("treasury: VCC funding trigger missing target_provider_id")
	}
	if req.AmountMinor <= 0 {
		return fmt.Errorf("treasury: VCC funding trigger invalid amount %d", req.AmountMinor)
	}

	// 从 provenance 获取 bridge_batch_id
	bridgeBatchID := req.Provenance["crypto_bridge_batch_id"]
	if bridgeBatchID == "" {
		bridgeBatchID = req.Provenance["lp_conversion_ref"]
	}
	if bridgeBatchID == "" {
		return fmt.Errorf("treasury: VCC funding trigger missing bridge_batch_id in provenance")
	}

	finalizeReq := FinalizeVCCFundingRequest{
		BridgeBatchID:    bridgeBatchID,
		MerchantID:       req.MerchantID,
		TargetProviderID: req.TargetProviderID,
		AmountMinor:      req.AmountMinor,
		Currency:         req.Currency,
		Provenance:       req.Provenance,
	}

	result, err := a.router.FinalizeVCCFunding(ctx, finalizeReq)
	if err != nil {
		return fmt.Errorf("treasury: finalize VCC funding failed: %w", err)
	}
	if result == nil || !result.Approved {
		reason := "unknown"
		if result != nil {
			reason = result.RejectReason
		}
		return fmt.Errorf("treasury: VCC funding finalize rejected: %s", reason)
	}

	return nil
}
