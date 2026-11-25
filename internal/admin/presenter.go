package admin

import "fmt"

// PresentOverrideAuditLogHumanText 将 override audit log 的机器字段翻译成 SOP 可读的一句话。
func PresentOverrideAuditLogHumanText(overrideType, operator, reason string) string {
	switch overrideType {
	case "RISK_OVERRIDE":
		return fmt.Sprintf("操作员 %s 对通道风控参数执行了人工覆盖，原因：%s", operator, reason)
	case "FREEZE":
		return fmt.Sprintf("操作员 %s 冻结了隔离组，原因：%s", operator, reason)
	case "UNFREEZE":
		return fmt.Sprintf("操作员 %s 解除了隔离组冻结，原因：%s", operator, reason)
	case "SYSTEM_LOCKDOWN":
		return fmt.Sprintf("操作员 %s 启动了系统全局 Fail-Closed 锁死大闸，原因：%s", operator, reason)
	case "RFI_APPROVE":
		return fmt.Sprintf("操作员 %s 通过了 RFI 资料包，原因：%s", operator, reason)
	case "RFI_REJECT":
		return fmt.Sprintf("操作员 %s 拒绝了 RFI 资料包，原因：%s", operator, reason)
	default:
		return fmt.Sprintf("操作员 %s 执行了 %s 操作，原因：%s", operator, overrideType, reason)
	}
}

// ── G08: Maker-Checker 审批展示函数 ──

// PresentApprovalInboxItem 将审批 inbox 项翻译成 SOP 可读的一句话。
//
// 用于审批列表展示，包含：动作、目标、申请人、状态、风险说明。
func PresentApprovalInboxItem(actionType, targetType, targetID, requester, status string) string {
	statusCN := presentApprovalStatusCN(status)
	actionCN := PresentActionTypeCN(actionType)
	if targetID == "" {
		return fmt.Sprintf("%s 由 %s 申请，状态：%s", actionCN, requester, statusCN)
	}
	return fmt.Sprintf("%s（%s: %s）由 %s 申请，状态：%s", actionCN, targetType, targetID, requester, statusCN)
}

// PresentApprovalDetail 将审批详情翻译成 SOP 可读的多行文本。
//
// 用于审批详情展示，包含：动作、目标、版本、before/after、maker/checker、风险说明。
// 不显示凭证或 token（before_snapshot 已通过 sanitize 脱敏）。
func PresentApprovalDetail(actionType, targetType, targetID string, targetVersion *int, requester, requesterRole, status, checker, reason, reviewReason, riskNote string) string {
	actionCN := PresentActionTypeCN(actionType)
	statusCN := presentApprovalStatusCN(status)

	detail := fmt.Sprintf("【审批详情】\n")
	detail += fmt.Sprintf("动作：%s（%s）\n", actionCN, actionType)
	detail += fmt.Sprintf("目标类型：%s\n", targetType)
	if targetID != "" {
		detail += fmt.Sprintf("目标 ID：%s\n", targetID)
	}
	if targetVersion != nil {
		detail += fmt.Sprintf("目标版本锁：%d\n", *targetVersion)
	}
	detail += fmt.Sprintf("申请人：%s（角色：%s）\n", requester, requesterRole)
	detail += fmt.Sprintf("申请理由：%s\n", reason)
	detail += fmt.Sprintf("状态：%s\n", statusCN)
	if checker != "" {
		detail += fmt.Sprintf("复核人：%s\n", checker)
	}
	if reviewReason != "" {
		detail += fmt.Sprintf("复核理由：%s\n", reviewReason)
	}
	if riskNote != "" {
		detail += fmt.Sprintf("风险说明：%s\n", riskNote)
	}
	return detail
}

// PresentApprovalBeforeAfter 将 before/after 快照翻译成 SOP 可读的对比文本。
//
// 用于审批详情展示 before/after 变更对比。
// 不显示凭证或 token（before/after 已通过 sanitize 脱敏）。
func PresentApprovalBeforeAfter(before, after map[string]interface{}) string {
	if len(before) == 0 && len(after) == 0 {
		return "无变更前快照"
	}

	result := "【变更对比】\n"
	result += "Before:\n"
	for k, v := range before {
		result += fmt.Sprintf("  %s: %v\n", k, v)
	}
	result += "After:\n"
	for k, v := range after {
		result += fmt.Sprintf("  %s: %v\n", k, v)
	}
	return result
}

// PresentApprovalSyncStatus 将审批同步状态翻译成 SOP 可读的四个阶段。
//
// G08: UI 分开显示"已申请、已批准、已应用、节点已同步"。
func PresentApprovalSyncStatus(status string, outboxStatus string) string {
	// 阶段 1: 已申请
	applied := status == "PENDING" || status == "APPLYING" || status == "APPLIED" || status == "REJECTED" || status == "FAILED_STALE"

	// 阶段 2: 已批准（APPLYING 或 APPLIED）
	approved := status == "APPLYING" || status == "APPLIED"

	// 阶段 3: 已应用（APPLIED）
	configApplied := status == "APPLIED"

	// 阶段 4: 节点已同步（outbox 已 dispatch）
	nodeSynced := configApplied && (outboxStatus == "Dispatched" || outboxStatus == "Completed")

	result := "【同步状态】\n"
	if applied {
		result += "✓ 已申请\n"
	} else {
		result += "○ 已申请\n"
	}
	if approved {
		result += "✓ 已批准\n"
	} else {
		result += "○ 已批准\n"
	}
	if configApplied {
		result += "✓ 已应用\n"
	} else {
		result += "○ 已应用\n"
	}
	if nodeSynced {
		result += "✓ 节点已同步\n"
	} else {
		result += "○ 节点已同步\n"
	}
	return result
}

// PresentActionTypeCN 将 action_type 翻译成中文。
func PresentActionTypeCN(actionType string) string {
	switch actionType {
	case ActionDisableAcquirerChannel:
		return "禁用收单通道"
	case ActionEnableAcquirerChannel:
		return "启用收单通道"
	case ActionUpdateAcquirerChannel:
		return "更新收单通道配置"
	case ActionDisableIssuingPool:
		return "禁用发卡卡池"
	case ActionEnableIssuingPool:
		return "启用发卡卡池"
	case ActionUpdateIssuingPool:
		return "更新发卡卡池配置"
	case ActionUpdateChannelRisk:
		return "更新通道风控参数"
	case ActionUpdateMerchantReserve:
		return "更新商户准备金比例"
	case ActionDisableWalletPolicy:
		return "禁用钱包策略"
	case ActionDisableVAAccount:
		return "禁用 VA 账号"
	case ActionSystemLockdownRecovery:
		return "解除系统封锁"
	// P0-4: 新增 action type 中文翻译
	case ActionEnableEntity:
		return "启用企业实体"
	case ActionClearEntityRiskHold:
		return "解除实体风控冻结"
	case ActionEnableBridgeProfile:
		return "启用桥接配置"
	case ActionEnableWalletPolicy:
		return "启用钱包策略"
	case ActionEnableAdapterProvider:
		return "启用适配器提供商"
	case ActionDisableAdapterProvider:
		return "禁用适配器提供商"
	case ActionUpdateAdapterProvider:
		return "更新适配器提供商配置"
	case ActionEnableBankProvider:
		return "启用银行提供商"
	case ActionDisableBankProvider:
		return "禁用银行提供商"
	case ActionRotateBankProviderCred:
		return "轮换银行提供商凭证"
	case ActionRevokePaymentLink:
		return "吊销支付链接"
	default:
		return actionType
	}
}
