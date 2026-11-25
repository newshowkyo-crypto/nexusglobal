// Package outbox — registry.go 定义全部 Outbox EventType 常量与处理器注册表（P0-4）。
//
// 设计目标：
//   - 为全部实际写入的 EventType 注册处理器，未知 EventType 返回 error
//   - 每个处理器必须幂等（重复消费不产生副作用）
//   - Redis 发布失败不得 ACK（返回 error 触发重试）
//   - 达到最大重试次数后进入 DeadLetter（由 worker.go 处理）
//
// 事件分类：
//  1. 系统控制事件：SYSTEM_LOCKDOWN_PUBLISH, SYSTEM_RECOVERY_PUBLISH
//  2. 账本事件：LEDGER_TRANSACTION_CREATED, LEDGER_RESERVATION_CREATED/CAPTURED/RELEASED
//  3. 支付分发事件：FX_LOCK_REQUESTED, LP_PAYOUT_REQUESTED, PAYMENT_DISPATCH_REQUESTED
//  4. 配置重载事件：CONFIG_RELOAD, ACQUIRER_CHANNEL_RELOAD, ISSUING_POOL_RELOAD
//  5. 配置变更事件（admin approval 触发）：
//     acquirer_channel.status_changed/config_updated,
//     issuing_pool.status_changed/config_updated,
//     merchant_reserve.updated, wallet_policy.disabled,
//     va_account.disabled/released, channel_risk.updated,
//     system.recovery_executed, adapter_channel.disabled/enabled,
//     bridge_profile.disabled, corporate_entity.risk_hold,
//     isolation_group.frozen/unfrozen
//  6. VCC 资金事件：VCC_FUNDING_REQUESTED, VCC_CRYPTO_BRIDGE_REQUESTED
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// EventType 常量定义全部已知的 Outbox 事件类型。
//
// 任何写入 transactional_outbox.event_type 的值必须在此定义。
// 未知 EventType 会被 Registry 拒绝（返回 ErrUnknownEventType），
// 防止事件被静默吞掉（P0-4 核心修复）。
const (
	// ── 系统控制事件 ──
	EventSystemLockdownPublish = "SYSTEM_LOCKDOWN_PUBLISH"
	EventSystemRecoveryPublish = "SYSTEM_RECOVERY_PUBLISH"

	// ── 账本事件 ──
	EventLedgerTransactionCreated  = "LEDGER_TRANSACTION_CREATED"
	EventLedgerReservationCreated  = "LEDGER_RESERVATION_CREATED"
	EventLedgerReservationCaptured = "LEDGER_RESERVATION_CAPTURED"
	EventLedgerReservationReleased = "LEDGER_RESERVATION_RELEASED"

	// ── 支付分发事件 ──
	EventFXLockRequested          = "FX_LOCK_REQUESTED"
	EventLPPayoutRequested        = "LP_PAYOUT_REQUESTED"
	EventPaymentDispatchRequested = "PAYMENT_DISPATCH_REQUESTED" // 预留：统一支付分发
	EventRefundRequested          = "REFUND_REQUESTED"           // A2: 商户退款异步执行

	// ── Sweep 对账事件（SEC-013：对账必须经异步命令查询银行，禁止同步假 Confirmed）──
	EventSweepReconcileRequested = "SWEEP_RECONCILE_REQUESTED"

	// ── Webhook 事件 ──
	EventWebhookReceived = "WEBHOOK_EVENT_RECEIVED" // B4: Webhook Outbox Consumer

	// ── 配置重载事件（任务要求覆盖）──
	EventConfigReload          = "CONFIG_RELOAD"
	EventAcquirerChannelReload = "ACQUIRER_CHANNEL_RELOAD"
	EventIssuingPoolReload     = "ISSUING_POOL_RELOAD"

	// ── 配置变更事件（admin approval 触发）──
	EventAcquirerChannelStatusChanged   = "acquirer_channel.status_changed"
	EventAcquirerChannelConfigUpdated   = "acquirer_channel.config_updated"
	EventIssuingPoolStatusChanged       = "issuing_pool.status_changed"
	EventIssuingPoolConfigUpdated       = "issuing_pool.config_updated"
	EventMerchantReserveUpdated         = "merchant_reserve.updated"
	EventWalletPolicyDisabled           = "wallet_policy.disabled"
	EventVAAccountDisabled              = "va_account.disabled"
	EventVAAccountReleased              = "va_account.released"
	EventChannelRiskUpdated             = "channel_risk.updated"
	EventSystemRecoveryExecuted         = "system.recovery_executed"
	EventAdapterChannelDisabled         = "adapter_channel.disabled"
	EventAdapterChannelEnabled          = "adapter_channel.enabled"
	EventBridgeProfileDisabled          = "bridge_profile.disabled"
	EventBridgeProfileEnabled           = "bridge_profile.enabled"
	EventCorporateEntityRiskHold        = "corporate_entity.risk_hold"
	EventCorporateEntityRiskHoldCleared = "corporate_entity.risk_hold_cleared"
	EventCorporateEntityEnabled         = "corporate_entity.enabled"
	EventIsolationGroupFrozen           = "isolation_group.frozen"
	EventIsolationGroupUnfrozen         = "isolation_group.unfrozen"

	// ── ADM-009: 审批服务产生的其余事件（producer/consumer 共用常量，
	//    缺失会导致 Worker 拒绝事件进入死信）──
	EventAdapterProviderUpdated       = "adapter_provider.updated"
	EventAdapterProviderDisabled      = "adapter_provider.disabled"
	EventAdapterProviderEnabled       = "adapter_provider.enabled"
	EventWalletPolicyEnabled          = "wallet_policy.enabled"
	EventMerchantRoutingPolicyCreated = "merchant_routing_policy.created"
	EventMerchantRoutingPolicyUpdated = "merchant_routing_policy.updated"
	EventPaymentLinkRevoked           = "payment_link.revoked"
	EventCorporateEntityUpdated       = "corporate_entity.updated"
	EventBridgeProfileUpdated         = "bridge_profile.updated"
	EventWalletPolicyUpdated          = "wallet_policy.updated"

	// ── VCC 资金事件 ──
	EventVCCFundingRequested        = "VCC_FUNDING_REQUESTED"
	EventVCCCryptoBridgeRequested   = "VCC_CRYPTO_BRIDGE_REQUESTED"
	EventVCCIssuingFundingRequested = "VCC_ISSUING_FUNDING_REQUESTED"
	EventVCCIssuingFundingCompleted = "VCC_ISSUING_FUNDING_COMPLETED"

	// ── 卡片冻结事件（SuspendActiveCards 触发）──
	EventCardFrozenSuspend = "CARD_FROZEN_SUSPEND"
)

// ErrUnknownEventType 表示收到未注册的 EventType。
// P0-4: 未知 EventType 必须返回 error，进入重试/死信，绝对不能返回 nil 后 ACK。
var ErrUnknownEventType = errors.New("UNKNOWN_EVENT_TYPE: no handler registered for this event type")

// ErrHandlerNotRegistered 表示 EventType 已知但未注册处理器。
var ErrHandlerNotRegistered = errors.New("HANDLER_NOT_REGISTERED: event type is known but no handler was registered")

// KnownEventTypes 返回全部已知的 EventType 常量列表。
// 用于启动时校验、测试覆盖检查。
func KnownEventTypes() []string {
	return []string{
		EventSystemLockdownPublish,
		EventSystemRecoveryPublish,
		EventLedgerTransactionCreated,
		EventLedgerReservationCreated,
		EventLedgerReservationCaptured,
		EventLedgerReservationReleased,
		EventFXLockRequested,
		EventLPPayoutRequested,
		EventPaymentDispatchRequested,
		EventRefundRequested,
		EventSweepReconcileRequested,
		EventConfigReload,
		EventAcquirerChannelReload,
		EventIssuingPoolReload,
		EventAcquirerChannelStatusChanged,
		EventAcquirerChannelConfigUpdated,
		EventIssuingPoolStatusChanged,
		EventIssuingPoolConfigUpdated,
		EventMerchantReserveUpdated,
		EventWalletPolicyDisabled,
		EventVAAccountDisabled,
		EventVAAccountReleased,
		EventChannelRiskUpdated,
		EventSystemRecoveryExecuted,
		EventAdapterChannelDisabled,
		EventAdapterChannelEnabled,
		EventBridgeProfileDisabled,
		EventBridgeProfileEnabled,
		EventCorporateEntityRiskHold,
		EventCorporateEntityRiskHoldCleared,
		EventCorporateEntityEnabled,
		EventIsolationGroupFrozen,
		EventIsolationGroupUnfrozen,
		EventAdapterProviderUpdated,
		EventAdapterProviderDisabled,
		EventAdapterProviderEnabled,
		EventWalletPolicyEnabled,
		EventMerchantRoutingPolicyCreated,
		EventMerchantRoutingPolicyUpdated,
		EventPaymentLinkRevoked,
		EventCorporateEntityUpdated,
		EventBridgeProfileUpdated,
		EventWalletPolicyUpdated,
		EventVCCFundingRequested,
		EventVCCCryptoBridgeRequested,
		EventVCCIssuingFundingRequested,
		EventVCCIssuingFundingCompleted,
		EventWebhookReceived,
		EventCardFrozenSuspend,
	}
}

// IsKnownEventType 返回 EventType 是否在已知列表中。
func IsKnownEventType(eventType string) bool {
	for _, t := range KnownEventTypes() {
		if t == eventType {
			return true
		}
	}
	return false
}

// Handler 处理单个 OutboxEvent。
// 必须幂等：重复消费同一事件不产生副作用。
// 返回 error 时，worker 会重试或进入死信；返回 nil 时 ACK。
type Handler func(ctx context.Context, event OutboxEvent) error

// Registry 是 EventType → Handler 的注册表。
//
// 设计：
//   - 启动时注册全部已知 EventType 的处理器
//   - Process 时查找 handler，未知 EventType 返回 ErrUnknownEventType
//   - 已知但未注册 handler 返回 ErrHandlerNotRegistered
//   - 已注册 handler 调用并返回结果
//
// 这修复了 P0-4 的核心问题：旧代码对未知 EventType 返回 nil（ACK），
// 导致事件被静默吞掉。新代码返回 error，进入重试/死信。
type Registry struct {
	handlers map[string]Handler
	logger   *slog.Logger
}

// NewRegistry 创建空注册表。
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		handlers: make(map[string]Handler),
		logger:   logger,
	}
}

// Register 注册 EventType 的处理器。
// 若 EventType 未知（不在 KnownEventTypes 列表），返回 ErrUnknownEventType。
// 若已注册，覆盖旧 handler（允许测试重置）。
func (r *Registry) Register(eventType string, handler Handler) error {
	if !IsKnownEventType(eventType) {
		return fmt.Errorf("%w: %s", ErrUnknownEventType, eventType)
	}
	if handler == nil {
		return errors.New("handler cannot be nil")
	}
	r.handlers[eventType] = handler
	r.logger.Debug("outbox handler registered", "event_type", eventType)
	return nil
}

// MustRegister 注册处理器，失败时 panic（用于启动时静态注册）。
func (r *Registry) MustRegister(eventType string, handler Handler) {
	if err := r.Register(eventType, handler); err != nil {
		panic(fmt.Sprintf("outbox: register handler for %s: %v", eventType, err))
	}
}

// Process 处理单个事件。
//   - 未知 EventType → ErrUnknownEventType（不 ACK，进入重试/死信）
//   - 已知但未注册 handler → ErrHandlerNotRegistered（不 ACK）
//   - 已注册 handler → 调用并返回结果
//
// 此方法满足 outbox.EventProcessor 签名，可直接传给 outbox.New。
func (r *Registry) Process(ctx context.Context, event OutboxEvent) error {
	// P0-4: 未知 EventType 必须返回 error，绝不返回 nil 后 ACK。
	if !IsKnownEventType(event.EventType) {
		r.logger.Error("outbox: unknown event type, rejecting (not ACKing)",
			"event_id", event.ID,
			"event_type", event.EventType,
		)
		return fmt.Errorf("%w: %s", ErrUnknownEventType, event.EventType)
	}

	handler, ok := r.handlers[event.EventType]
	if !ok {
		// 已知 EventType 但未注册 handler：返回 error，不 ACK。
		// 这通常表示启动配置不完整，应告警。
		r.logger.Error("outbox: known event type but no handler registered",
			"event_id", event.ID,
			"event_type", event.EventType,
		)
		return fmt.Errorf("%w: %s", ErrHandlerNotRegistered, event.EventType)
	}

	return handler(ctx, event)
}

// RegisteredEventTypes 返回已注册 handler 的 EventType 列表。
// 用于启动时校验全部已知 EventType 都有 handler。
func (r *Registry) RegisteredEventTypes() []string {
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	return types
}

// AllKnownHandlersRegistered 返回是否全部已知 EventType 都已注册 handler。
// 启动时应调用此方法校验，未全部注册时告警（但不阻止启动，
// 因为某些 EventType 可能由其他 worker 处理，如 VCC_FUNDING_REQUESTED 由 vcc_funding worker 处理）。
func (r *Registry) AllKnownHandlersRegistered() (bool, []string) {
	var missing []string
	for _, t := range KnownEventTypes() {
		if _, ok := r.handlers[t]; !ok {
			missing = append(missing, t)
		}
	}
	return len(missing) == 0, missing
}

// ValidateOutboxRegistry 校验 Registry 是否满足启动门禁要求。
// strict=true（production/real 模式）：缺失任何已知 Handler 返回 error，拒绝启动。
// strict=false（local/mock 模式）：缺失 Handler 只记录 warning，不返回 error。
func ValidateOutboxRegistry(reg *Registry, strict bool) error {
	allRegistered, missing := reg.AllKnownHandlersRegistered()
	if !allRegistered {
		if strict {
			return fmt.Errorf("outbox: strict mode (production/real): missing handlers for %d event types: %v",
				len(missing), missing)
		}
		reg.logger.Warn("outbox: non-strict mode (local/mock): some known event types have no handler registered",
			"missing_count", len(missing),
			"missing", missing,
		)
	}
	return nil
}
