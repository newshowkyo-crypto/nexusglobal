// Package admin — approval_service.go 实现 Maker-Checker 事务应用闭环 (G08)。
//
// 修复缺口：
//  1. 原先 requester 来自请求体，可伪造（RED-15）→ 改为从已验证 Context 获取
//  2. 原先创建审批直接 INSERT，无 typed payload 验证 → 改为按 action_type 验证 typed payload
//  3. 原先 Approve 只改审批状态，不执行 payload → 改为 Apply transaction 原子应用
//  4. 原先允许自审（checker=requester）→ 改为拒绝自审 403
//  5. 原先无版本锁 → 改为 target_version + SELECT FOR UPDATE + version+1
//
// Apply transaction 流程（核心）：
//  1. 从认证 Context 获取 checker，拒绝 body 中身份
//  2. SELECT approval FOR UPDATE，只允许 PENDING
//  3. 拒绝 checker=requester；验证 checker permission 和 tenant
//  4. SELECT target FOR UPDATE，目标 version 必须等于 target_version
//  5. 用 Task 3 的同一 Validator（configvalidator）重验完整配置
//  6. 应用配置并检查 affected rows=1，version+1
//  7. 写不可变审计和包含 target id/version 的 outbox；将审批改为 APPLIED
//  8. Commit 后由 worker publish config:acquirer_channel:reload 或 config:issuing_pool:reload
//
// 红线约束：
//   - 身份只从已验证 Context 获取（RED-15）
//   - DB 事务内禁止任何外部网络 I/O（RED-01）：config reload 由 worker 在事务外完成
//   - 幂等键 UNIQUE（RED-10）：重复事件不重复入账
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/issuing"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/configvalidator"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ── Approval Status 常量 ──
//
// P0-1: 统一审批状态机，禁止 APPROVED。
// 完整状态机：PENDING → APPLYING → APPLIED（成功）
//                              → REJECTED（拒绝）
//                              → FAILED_STALE（版本冲突/目标不存在/零 affected rows）
// 任何运行时代码写入 'APPROVED' 都是 bug，应使用 'APPLIED'。

const (
	ApprovalStatusPending     = "PENDING"
	ApprovalStatusApplying    = "APPLYING"
	ApprovalStatusApplied     = "APPLIED"
	ApprovalStatusRejected    = "REJECTED"
	ApprovalStatusFailedStale = "FAILED_STALE"
)

// validApprovalStatuses 是运行时审批状态白名单。
// 与迁移 000038 的 CHECK 约束完全一致，禁止 APPROVED。
var validApprovalStatuses = map[string]bool{
	ApprovalStatusPending:     true,
	ApprovalStatusApplying:    true,
	ApprovalStatusApplied:     true,
	ApprovalStatusRejected:    true,
	ApprovalStatusFailedStale: true,
}

// IsValidApprovalStatus 判断给定状态是否在统一状态机白名单内。
// 用于运行时校验（例如从 DB 读取后、写入前），防止 APPROVED 等非法状态泄漏。
func IsValidApprovalStatus(status string) bool {
	return validApprovalStatuses[status]
}

// ── Action Type 白名单（typed payload，禁止 map[string]any 直接执行任意 SQL）──

const (
	ActionDisableAcquirerChannel = "DISABLE_ACQUIRER_CHANNEL"
	ActionEnableAcquirerChannel  = "ENABLE_ACQUIRER_CHANNEL"
	ActionUpdateAcquirerChannel  = "UPDATE_ACQUIRER_CHANNEL"

	ActionDisableIssuingPool = "DISABLE_ISSUING_POOL"
	ActionEnableIssuingPool  = "ENABLE_ISSUING_POOL"
	ActionUpdateIssuingPool  = "UPDATE_ISSUING_POOL"

	ActionUpdateChannelRisk     = "UPDATE_CHANNEL_RISK"
	ActionUpdateMerchantReserve = "UPDATE_MERCHANT_RESERVE"
	ActionDisableWalletPolicy   = "DISABLE_WALLET_POLICY"
	ActionDisableVAAccount      = "DISABLE_VA_ACCOUNT"

	// P0-9: handler.go 中直接修改业务表的敏感操作也纳入 Maker-Checker
	ActionDisableAdapterChannel  = "DISABLE_ADAPTER_CHANNEL"
	ActionEnableAdapterChannel   = "ENABLE_ADAPTER_CHANNEL"
	ActionDisableBridgeProfile   = "DISABLE_BRIDGE_PROFILE"
	ActionSetEntityRiskHold      = "SET_ENTITY_RISK_HOLD"
	ActionFreezeIsolationGroup   = "FREEZE_ISOLATION_GROUP"
	ActionUnfreezeIsolationGroup = "UNFREEZE_ISOLATION_GROUP"
	ActionReleaseVAAccount       = "RELEASE_VA_ACCOUNT"

	ActionSystemLockdownRecovery = "SYSTEM_LOCKDOWN_RECOVERY"

	// P0-4: Merchant Routing Policy 创建/更新走 Maker-Checker
	ActionCreateMerchantPolicy = "CREATE_MERCHANT_POLICY"
	ActionUpdateMerchantPolicy = "UPDATE_MERCHANT_POLICY"

	// P0-4: 新增 Maker-Checker 保护动作
	ActionEnableEntity           = "ENABLE_ENTITY"
	ActionClearEntityRiskHold    = "CLEAR_ENTITY_RISK_HOLD"
	ActionEnableBridgeProfile    = "ENABLE_BRIDGE_PROFILE"
	ActionEnableWalletPolicy     = "ENABLE_WALLET_POLICY"
	ActionEnableAdapterProvider  = "ENABLE_ADAPTER_PROVIDER"
	ActionDisableAdapterProvider = "DISABLE_ADAPTER_PROVIDER"
	ActionUpdateAdapterProvider  = "UPDATE_ADAPTER_PROVIDER"
	ActionEnableBankProvider     = "ENABLE_BANK_PROVIDER"
	ActionDisableBankProvider    = "DISABLE_BANK_PROVIDER"
	ActionRotateBankProviderCred = "ROTATE_BANK_PROVIDER_CREDENTIAL"
	ActionRevokePaymentLink      = "REVOKE_PAYMENT_LINK"

	// ADM-002: 高风险配置更新统一收敛到 Maker-Checker（不再直接 UPDATE）
	ActionUpdateCorporateEntity = "UPDATE_CORPORATE_ENTITY"
	ActionUpdateBridgeProfile   = "UPDATE_BRIDGE_PROFILE"
	ActionUpdateWalletPolicy    = "UPDATE_WALLET_POLICY"
)

// TargetType 常量
const (
	TargetTypeAcquirerChannel = "acquirer_channel"
	TargetTypeIssuingPool     = "issuing_pool"
	TargetTypeSystemControl   = "system_control"
	TargetTypeMerchantReserve = "merchant_reserve"
	TargetTypeWalletPolicy    = "wallet_policy"
	TargetTypeVAAccount       = "va_account"
	TargetTypeChannelRisk     = "channel_risk"
	TargetTypeAdapterChannel  = "adapter_channel"
	TargetTypeBridgeProfile   = "bridge_profile"
	TargetTypeCorporateEntity = "corporate_entity"
	TargetTypeIsolationGroup  = "pipeline_isolation_group"
	TargetTypeMerchantPolicy  = "merchant_routing_policy"
	TargetTypeAdapterProvider = "adapter_provider"
	TargetTypeBankProvider    = "bank_provider"
	TargetTypePaymentLink     = "payment_link"
)

// allowedActions 是 action_type 白名单及其对应的 target_type。
// 每个保护动作有唯一 action type 和 typed payload，禁止 map[string]any 直接执行任意 SQL/字段。
var allowedActions = map[string]string{
	ActionDisableAcquirerChannel: TargetTypeAcquirerChannel,
	ActionEnableAcquirerChannel:  TargetTypeAcquirerChannel,
	ActionUpdateAcquirerChannel:  TargetTypeAcquirerChannel,

	ActionDisableIssuingPool: TargetTypeIssuingPool,
	ActionEnableIssuingPool:  TargetTypeIssuingPool,
	ActionUpdateIssuingPool:  TargetTypeIssuingPool,

	ActionUpdateChannelRisk:     TargetTypeChannelRisk,
	ActionUpdateMerchantReserve: TargetTypeMerchantReserve,
	ActionDisableWalletPolicy:   TargetTypeWalletPolicy,
	ActionDisableVAAccount:      TargetTypeVAAccount,

	ActionDisableAdapterChannel:  TargetTypeAdapterChannel,
	ActionEnableAdapterChannel:   TargetTypeAdapterChannel,
	ActionDisableBridgeProfile:   TargetTypeBridgeProfile,
	ActionSetEntityRiskHold:      TargetTypeCorporateEntity,
	ActionFreezeIsolationGroup:   TargetTypeIsolationGroup,
	ActionUnfreezeIsolationGroup: TargetTypeIsolationGroup,
	ActionReleaseVAAccount:       TargetTypeVAAccount,

	ActionSystemLockdownRecovery: TargetTypeSystemControl,

	ActionCreateMerchantPolicy: TargetTypeMerchantPolicy,
	ActionUpdateMerchantPolicy: TargetTypeMerchantPolicy,

	// P0-4: 新增 Maker-Checker 保护动作
	ActionEnableEntity:           TargetTypeCorporateEntity,
	ActionClearEntityRiskHold:    TargetTypeCorporateEntity,
	ActionEnableBridgeProfile:    TargetTypeBridgeProfile,
	ActionEnableWalletPolicy:     TargetTypeWalletPolicy,
	ActionEnableAdapterProvider:  TargetTypeAdapterProvider,
	ActionDisableAdapterProvider: TargetTypeAdapterProvider,
	ActionUpdateAdapterProvider:  TargetTypeAdapterProvider,
	ActionEnableBankProvider:     TargetTypeBankProvider,
	ActionDisableBankProvider:    TargetTypeBankProvider,
	ActionRotateBankProviderCred: TargetTypeBankProvider,
	ActionRevokePaymentLink:      TargetTypePaymentLink,

	// ADM-002: 高风险配置更新走 Maker-Checker
	ActionUpdateCorporateEntity: TargetTypeCorporateEntity,
	ActionUpdateBridgeProfile:   TargetTypeBridgeProfile,
	ActionUpdateWalletPolicy:    TargetTypeWalletPolicy,
}

// ── Typed Payload 定义 ──
// 每个保护动作有唯一的 typed payload 结构，禁止用 map[string]any 直接执行任意 SQL/字段。

// ChannelStatusPayload 用于 DISABLE_ACQUIRER_CHANNEL / ENABLE_ACQUIRER_CHANNEL。
type ChannelStatusPayload struct {
	ChannelID string `json:"channel_id"`
}

// ChannelUpdatePayload 用于 UPDATE_ACQUIRER_CHANNEL。
type ChannelUpdatePayload struct {
	ChannelID       string                 `json:"channel_id"`
	ProviderTier    string                 `json:"provider_tier,omitempty"`
	StrategyMode    string                 `json:"strategy_mode,omitempty"`
	FundingMode     string                 `json:"funding_mode,omitempty"`
	CredentialVault string                 `json:"credential_vault_key,omitempty"`
	ProviderConfig  map[string]interface{} `json:"provider_config,omitempty"`
	RoutingConfig   map[string]interface{} `json:"routing_config,omitempty"`
}

// PoolStatusPayload 用于 DISABLE_ISSUING_POOL / ENABLE_ISSUING_POOL。
type PoolStatusPayload struct {
	PoolID string `json:"pool_id"`
}

// PoolUpdatePayload 用于 UPDATE_ISSUING_POOL。
type PoolUpdatePayload struct {
	PoolID          string                 `json:"pool_id"`
	IssuingTier     string                 `json:"issuing_tier,omitempty"`
	FundingMode     string                 `json:"funding_mode,omitempty"`
	CredentialVault string                 `json:"credential_vault_key,omitempty"`
	ProviderConfig  map[string]interface{} `json:"provider_config,omitempty"`
	VelocityConfig  map[string]interface{} `json:"velocity_config,omitempty"`
	// Section 5: 新增 adapter 配置字段
	AdapterType       string `json:"adapter_type,omitempty"`
	AdapterProviderID string `json:"adapter_provider_id,omitempty"`
}

// MerchantReservePayload 用于 UPDATE_MERCHANT_RESERVE。
type MerchantReservePayload struct {
	MerchantID      string `json:"merchant_id"`
	ReserveRatioBps int    `json:"reserve_ratio_bps"`
}

// WalletPolicyPayload 用于 DISABLE_WALLET_POLICY。
type WalletPolicyPayload struct {
	PolicyID string `json:"policy_id"`
}

// VAAccountPayload 用于 DISABLE_VA_ACCOUNT。
type VAAccountPayload struct {
	VAID string `json:"va_id"`
}

// ChannelRiskPayload 用于 UPDATE_CHANNEL_RISK。
type ChannelRiskPayload struct {
	ChannelID     string `json:"channel_id"`
	BinDeclineBps int    `json:"bin_decline_bps,omitempty"`
}

// SystemRecoveryPayload 用于 SYSTEM_LOCKDOWN_RECOVERY。
type SystemRecoveryPayload struct{}

// AdapterChannelStatusPayload 用于 DISABLE_ADAPTER_CHANNEL / ENABLE_ADAPTER_CHANNEL。
type AdapterChannelStatusPayload struct {
	ChannelID string `json:"channel_id"`
}

// BridgeProfilePayload 用于 DISABLE_BRIDGE_PROFILE。
type BridgeProfilePayload struct {
	ProfileID string `json:"profile_id"`
}

// EntityRiskHoldPayload 用于 SET_ENTITY_RISK_HOLD。
type EntityRiskHoldPayload struct {
	EntityID string `json:"entity_id"`
}

// IsolationGroupPayload 用于 FREEZE_ISOLATION_GROUP / UNFREEZE_ISOLATION_GROUP。
type IsolationGroupPayload struct {
	GroupID string `json:"group_id"`
	Reason  string `json:"reason"`
}

// VAReleasePayload 用于 RELEASE_VA_ACCOUNT。
type VAReleasePayload struct {
	VAID string `json:"va_id"`
}

// MerchantPolicyCreatePayload 用于 CREATE_MERCHANT_POLICY。
type MerchantPolicyCreatePayload struct {
	MerchantID   string `json:"merchant_id"`
	ProviderTier string `json:"provider_tier"`
	Status       string `json:"status"`
}

// MerchantPolicyUpdatePayload 用于 UPDATE_MERCHANT_POLICY。
type MerchantPolicyUpdatePayload struct {
	MerchantID   string `json:"merchant_id"`
	ProviderTier string `json:"provider_tier"`
	Status       string `json:"status"`
}

// ADM-002: 高风险配置更新的 typed payload

// CorporateEntityUpdatePayload 用于 UPDATE_CORPORATE_ENTITY。
type CorporateEntityUpdatePayload struct {
	EntityID     string `json:"entity_id"`
	Name         string `json:"name"`
	LegalName    string `json:"legal_name"`
	Jurisdiction string `json:"jurisdiction"`
}

// BridgeProfileUpdatePayload 用于 UPDATE_BRIDGE_PROFILE。
type BridgeProfileUpdatePayload struct {
	ProfileID        string  `json:"profile_id"`
	Name             string  `json:"name"`
	BufferPercent    float64 `json:"buffer_percent"`
	MaxSlippageBps   int32   `json:"max_slippage_bps"`
	BatchingPolicy   string  `json:"batching_policy"`
	ExecutionPolicy  string  `json:"execution_policy"`
	WithdrawalPolicy string  `json:"withdrawal_policy"`
}

// WalletPolicyUpdatePayload 用于 UPDATE_WALLET_POLICY。
type WalletPolicyUpdatePayload struct {
	PolicyID            string  `json:"policy_id"`
	MaxSingleWithdrawal float64 `json:"max_single_withdrawal"`
	DailyLimit          float64 `json:"daily_limit"`
}

// P0-4: 新增 typed payload 结构

// EntityStatusPayload 用于 ENABLE_ENTITY / CLEAR_ENTITY_RISK_HOLD。
type EntityStatusPayload struct {
	EntityID string `json:"entity_id"`
}

// AdapterProviderPayload 用于 ENABLE_ADAPTER_PROVIDER / DISABLE_ADAPTER_PROVIDER。
type AdapterProviderPayload struct {
	ProviderID string `json:"provider_id"`
}

// AdapterProviderUpdatePayload 用于 UPDATE_ADAPTER_PROVIDER。
type AdapterProviderUpdatePayload struct {
	ProviderID     string          `json:"provider_id"`
	ProviderName   string          `json:"provider_name,omitempty"`
	ProviderConfig json.RawMessage `json:"provider_config,omitempty"`
}

// BankProviderPayload 用于 ENABLE_BANK_PROVIDER / DISABLE_BANK_PROVIDER。
type BankProviderPayload struct {
	ProviderID string `json:"provider_id"`
}

// BankProviderCredentialPayload 用于 ROTATE_BANK_PROVIDER_CREDENTIAL。
type BankProviderCredentialPayload struct {
	ProviderID         string `json:"provider_id"`
	CredentialVaultKey string `json:"credential_vault_key"`
}

// PaymentLinkPayload 用于 REVOKE_PAYMENT_LINK。
type PaymentLinkPayload struct {
	PaymentLinkID string `json:"payment_link_id"`
	Reason        string `json:"reason"`
}

// ── Approval Record ──

// ApprovalRecord 是 admin_approval_requests 表的 Go 视图。
type ApprovalRecord struct {
	ID               string
	TenantID         *string
	ActionType       string
	TargetType       string
	TargetID         *string
	TargetVersion    *int
	ValidatedPayload map[string]interface{}
	BeforeSnapshot   map[string]interface{}
	Requester        string
	RequesterRole    string
	Reason           string
	Status           string
	Checker          *string
	ReviewReason     *string
	CreatedAt        time.Time
	ReviewedAt       *time.Time
	AppliedAt        *time.Time
	RequestID        *string
}

// ── ApprovalService ──

// ApprovalService 实现 Maker-Checker 事务应用闭环。
//
// 与 ApprovalHandler 的关系：
//   - ApprovalHandler 是 HTTP 层，负责解析请求、注入身份、调用 Service
//   - ApprovalService 是业务层，负责创建审批、应用事务、写审计/outbox
//
// txBeginner 抽象事务起点，便于在纯单元测试中替换 ApplyTransaction 的事务来源。
// 生产路径由 *pgxpool.Pool 隐式满足（pool.Begin 签名一致：Begin(ctx) (pgx.Tx, error)）。
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type ApprovalService struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	// txBeginner 测试专用可替换事务起点；nil 时使用 pool。生产代码必须保持 nil。
	txBeginner txBeginner
	// runtimeMode 是运行时模式，R19 P0-2: 从 router/cmd/api 注入真实 RuntimeMode。
	// 默认 RuntimeModeProduction（fail-closed）。
	runtimeMode configvalidator.RuntimeMode
}

// setTxBeginnerForTest 仅供测试注入可替换事务起点；生产代码不得调用。
func (s *ApprovalService) setTxBeginnerForTest(b txBeginner) { s.txBeginner = b }

// SetRuntimeMode 注入运行时模式。R19 P0-2: 从 router/cmd/api 注入真实 RuntimeMode。
// 必须在服务启动时调用，禁止在审批处理过程中修改。
func (s *ApprovalService) SetRuntimeMode(mode configvalidator.RuntimeMode) {
	s.runtimeMode = mode
}

// NewApprovalService 创建审批服务。
func NewApprovalService(pool *pgxpool.Pool, rdb *redis.Client) *ApprovalService {
	return &ApprovalService{
		pool: pool,
		rdb:  rdb,
		// R19 P0-2: 默认 production（fail-closed），由注入方按需覆盖。
		runtimeMode: configvalidator.RuntimeModeProduction,
	}
}

// Pool 返回底层连接池（供 handler 复用）。
func (s *ApprovalService) Pool() *pgxpool.Pool { return s.pool }

// Rdb 返回 Redis 客户端（供 handler 复用）。
func (s *ApprovalService) Rdb() *redis.Client { return s.rdb }

// ── CreateApproval：创建 PENDING 审批请求 ──

// CreateApprovalRequest 创建 PENDING 审批请求。
//
// 身份从已验证 Context 获取（RED-15），拒绝 body 中伪造的 requester。
// 高危操作只能创建 PENDING，不直接执行（G08）。
//
// 参数：
//   - ctx: 必须包含 httpx.AdminContextKey{} 的已验证身份
//   - actionType: 白名单中的 action type
//   - targetID: 目标 UUID（可为空，如 SYSTEM_LOCKDOWN_RECOVERY）
//   - targetVersion: 目标版本锁（可为 0，表示不锁版本）
//   - validatedPayload: 已验证的 typed payload（按 action_type 验证）
//   - reason: 申请理由
//   - requestID: 幂等键（可为空，但建议传入以防止重复创建）
//
// 返回创建的审批 ID。
func (s *ApprovalService) CreateApprovalRequest(
	ctx context.Context,
	actionType string,
	targetType string,
	targetID *string,
	targetVersion *int,
	validatedPayload map[string]interface{},
	reason string,
	requestID *string,
) (string, error) {
	// 1. 身份只从已验证 Context 获取（RED-15）
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil {
		return "", apperr.ErrForbidden.WithMessage("admin identity not found in authenticated context")
	}
	requester := identity.Username
	if requester == "" {
		requester = identity.Subject
	}
	if requester == "" || requester == "unknown" {
		return "", apperr.ErrForbidden.WithMessage("requester identity not found in authenticated context")
	}

	// 2. 校验 action_type 白名单
	expectedTargetType, allowed := allowedActions[actionType]
	if !allowed {
		return "", apperr.ErrInvalidArgument.WithMessage("action_type not allowed: " + actionType)
	}
	if targetType != "" && targetType != expectedTargetType {
		return "", apperr.ErrInvalidArgument.WithMessage(
			fmt.Sprintf("target_type mismatch: expected %s for action %s, got %s", expectedTargetType, actionType, targetType))
	}
	targetType = expectedTargetType

	// 3. 校验 reason 非空
	if reason == "" {
		return "", apperr.ErrInvalidArgument.WithMessage("reason is required")
	}

	// 4. 校验 typed payload（按 action_type 验证）
	if err := validateTypedPayload(actionType, validatedPayload); err != nil {
		return "", err
	}

	// 5. 获取 before snapshot（不含凭证/token）
	beforeSnapshot, err := s.captureBeforeSnapshot(ctx, actionType, targetType, targetID)
	if err != nil {
		// before snapshot 失败不阻断创建（目标可能尚不存在），但记录空快照
		beforeSnapshot = map[string]interface{}{}
	}

	// 6. 序列化 payload（P0-5/ADM-003: view 与 execution 分离——
	//    view（requested_payload）完全脱敏用于展示；
	//    execution（validated_payload）保留 vault:// 引用以保证 apply 可执行，
	//    但真正的密钥值依然脱敏）。
	viewPayloadJSON, err := json.Marshal(SanitizeMap(validatedPayload))
	if err != nil {
		return "", apperr.ErrInternal.WithMessage("failed to marshal view payload")
	}
	execPayloadJSON, err := json.Marshal(SanitizeMapForExecution(validatedPayload))
	if err != nil {
		return "", apperr.ErrInternal.WithMessage("failed to marshal execution payload")
	}
	beforeJSON, _ := json.Marshal(SanitizeMap(beforeSnapshot))

	// 7. 解析 tenant_id
	var tenantID *uuid.UUID
	if identity.TenantID != "" {
		if tid, err := uuid.Parse(identity.TenantID); err == nil {
			tenantID = &tid
		}
	}

	// 8. 解析 target_id
	var targetUUID *uuid.UUID
	if targetID != nil && *targetID != "" {
		if tid, err := uuid.Parse(*targetID); err == nil {
			targetUUID = &tid
		}
	}

	// 9. INSERT 审批请求（PENDING）
	// ADM-007/ADM-003: requested_payload=脱敏 view；validated_payload=可执行 payload
	//（保留 vault:// 引用，密钥值仍脱敏）；before_snapshot=脱敏 view。
	var id uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO admin_approval_requests (
			tenant_id, action_type, target_type, target_id, target_version,
			requested_payload, validated_payload, before_snapshot,
			requester, requester_role, reason, status, request_id
		) VALUES (
			$1, $2, $3, $4, $5,
			$6::jsonb, $7::jsonb, $8::jsonb,
			$9, $10, $11, 'PENDING', $12
		)
		RETURNING id`,
		tenantID, actionType, targetType, targetUUID, targetVersion,
		string(viewPayloadJSON), string(execPayloadJSON), string(beforeJSON),
		requester, identity.Role, reason, requestID,
	).Scan(&id)
	if err != nil {
		// 幂等键冲突（RED-10）
		if isDuplicateKeyError(err) {
			return "", apperr.ErrIdempotencyConflict.WithMessage("approval request with this request_id already exists")
		}
		return "", apperr.ErrInternal.WithMessage("failed to create approval request: " + err.Error())
	}

	return id.String(), nil
}

// ── ApplyTransaction：核心 — 审批通过后原子应用 ──

// ApplyTransaction 是 Maker-Checker 事务应用闭环的核心。
//
// 流程：
//  1. 从认证 Context 获取 checker，拒绝 body 中身份
//  2. SELECT approval FOR UPDATE，只允许 PENDING
//  3. 拒绝 checker=requester；验证 checker permission 和 tenant
//  4. SELECT target FOR UPDATE，目标 version 必须等于 target_version
//  5. 用 Task 3 的同一 Validator（configvalidator）重验完整配置
//  6. 应用配置并检查 affected rows=1，version+1
//  7. 写不可变审计和包含 target id/version 的 outbox；将审批改为 APPLIED
//  8. Commit 后由 worker publish config:acquirer_channel:reload 或 config:issuing_pool:reload
//
// 红线约束：
//   - DB 事务内禁止任何外部网络 I/O（RED-01）：config reload 由调用方在事务外完成
//   - 幂等键 UNIQUE（RED-10）：重复事件不重复入账
func (s *ApprovalService) ApplyTransaction(ctx context.Context, approvalID string, reviewReason string) (*ApplyResult, error) {
	// 1. 身份只从已验证 Context 获取（RED-15）
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil {
		return nil, apperr.ErrForbidden.WithMessage("checker identity not found in authenticated context")
	}
	checker := identity.Username
	if checker == "" {
		checker = identity.Subject
	}
	if checker == "" || checker == "unknown" {
		return nil, apperr.ErrForbidden.WithMessage("checker identity not found in authenticated context")
	}

	// 2. 验证 checker 拥有审批权限
	if !identity.HasPermission(httpx.PermAdminApprove) {
		return nil, apperr.ErrForbidden.WithMessage("checker lacks admin:approve permission")
	}

	// 3. 解析 approval ID
	approvalUUID, err := uuid.Parse(approvalID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid approval id")
	}

	// 4. 开启 DB 事务
	// P0-1 测试支持: 优先使用可替换事务起点（仅测试注入），否则使用真实连接池
	var tx pgx.Tx
	if s.txBeginner != nil {
		tx, err = s.txBeginner.Begin(ctx)
	} else {
		tx, err = s.pool.Begin(ctx)
	}
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to begin transaction: " + err.Error())
	}
	defer tx.Rollback(ctx)

	// 5. SELECT approval FOR UPDATE，只允许 PENDING
	rec, err := selectApprovalForUpdate(ctx, tx, approvalUUID)
	if err != nil {
		return nil, err
	}
	if rec.Status != ApprovalStatusPending {
		return nil, apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("approval is not PENDING (current=%s), cannot apply", rec.Status))
	}

	// 6. 拒绝自审（checker=requester）
	if checker == rec.Requester {
		return nil, apperr.ErrForbidden.WithMessage(
			fmt.Sprintf("self-review rejected: checker=%s cannot approve own request (requester=%s)", checker, rec.Requester))
	}

	// 7. 验证 tenant 隔离（若审批有 tenant_id，checker 必须属于同一 tenant）
	if rec.TenantID != nil && identity.TenantID != "" {
		if *rec.TenantID != identity.TenantID {
			return nil, apperr.ErrForbidden.WithMessage("cross-tenant approval rejected")
		}
	}

	// 8. 将审批状态改为 APPLYING（瞬态，防止并发 apply）
	//    P0-1: 必须检查 RowsAffected()==1，防止并发审批或状态已变更时静默成功
	applyingTag, err := tx.Exec(ctx, `
		UPDATE admin_approval_requests
		SET status = 'APPLYING', checker = $1, review_reason = $2, reviewed_at = now()
		WHERE id = $3 AND status = 'PENDING'`,
		checker, reviewReason, approvalUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to mark approval as APPLYING: " + err.Error())
	}
	if applyingTag.RowsAffected() != 1 {
		return nil, apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("approval %s is not PENDING (RowsAffected=%d), cannot apply — concurrent approval or stale state",
				approvalID, applyingTag.RowsAffected()))
	}

	// P0-1: 创建 SAVEPOINT，applyByActionType 失败时可撤销全部业务表写入，
	// 同时保留 FAILED_STALE 状态标记（不回滚整个事务）
	if _, err := tx.Exec(ctx, `SAVEPOINT approval_apply`); err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to create savepoint: " + err.Error())
	}

	// 9. 按 action_type 应用配置
	applyOut, err := s.applyByActionType(ctx, tx, rec)
	if err != nil {
		// P0-1: 失败路径统一处理（SAVEPOINT 回滚 → FAILED_STALE → Commit）
		return nil, s.handleApplyFailure(ctx, tx, approvalUUID, approvalID, err)
	}

	// 10. 写不可变审计日志
	auditDetails, _ := json.Marshal(map[string]interface{}{
		"approval_id":    approvalID,
		"action_type":    rec.ActionType,
		"target_type":    rec.TargetType,
		"target_id":      rec.TargetID,
		"target_version": rec.TargetVersion,
		"new_version":    applyOut.NewVersion,
		"requester":      rec.Requester,
		"checker":        checker,
		"review_reason":  reviewReason,
		"reason":         rec.Reason,
	})
	var actorID *uuid.UUID
	// checker 不一定是 UUID，存为 actor_id=NULL，actor 字段在 details 中
	_, err = tx.Exec(ctx, `
		INSERT INTO admin_audit_log (actor_type, action, entity_type, entity_id, details)
		VALUES ('ADMIN', $1, $2, $3, $4::jsonb)`,
		"APPROVAL_APPLIED:"+rec.ActionType, rec.TargetType, applyOut.TargetUUID, string(auditDetails))
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to write audit log: " + err.Error())
	}
	_ = actorID

	// 11. 写 outbox（包含 target id/version，由 worker 在事务外 publish）
	outboxPayload, _ := json.Marshal(map[string]interface{}{
		"approval_id": approvalID,
		"action_type": rec.ActionType,
		"target_type": rec.TargetType,
		"target_id":   applyOut.TargetUUID.String(),
		"old_version": rec.TargetVersion,
		"new_version": applyOut.NewVersion,
		"checker":     checker,
		"ts":          time.Now().UTC().Format(time.RFC3339Nano),
	})
	outboxIDempotencyKey := fmt.Sprintf("approval:%s:applied", approvalID)
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox (aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ($1, $2, $3, $4::jsonb, $5, 'Pending')`,
		rec.TargetType, applyOut.TargetUUID, applyOut.EventType, string(outboxPayload), outboxIDempotencyKey)
	if err != nil {
		if isDuplicateKeyError(err) {
			// outbox 已存在（重放场景），跳过
		} else {
			return nil, apperr.ErrInternal.WithMessage("failed to write outbox: " + err.Error())
		}
	}

	// 12. 将审批改为 APPLIED
	//     P0-1: 必须检查 RowsAffected()==1，防止并发或状态机不一致时静默成功
	appliedTag, err := tx.Exec(ctx, `
		UPDATE admin_approval_requests
		SET status = 'APPLIED', applied_at = now()
		WHERE id = $1 AND status = 'APPLYING'`, approvalUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to mark approval as APPLIED: " + err.Error())
	}
	if appliedTag.RowsAffected() != 1 {
		return nil, apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("approval %s is not APPLYING (RowsAffected=%d), cannot mark APPLIED — concurrent modification",
				approvalID, appliedTag.RowsAffected()))
	}

	// 13. Commit
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to commit transaction: " + err.Error())
	}

	// 14. 返回结果（config reload 由调用方在事务外完成，RED-01）
	return &ApplyResult{
		ApprovalID:  approvalID,
		ActionType:  rec.ActionType,
		TargetType:  rec.TargetType,
		TargetID:    applyOut.TargetUUID.String(),
		OldVersion:  rec.TargetVersion,
		NewVersion:  applyOut.NewVersion,
		Checker:     checker,
		EventType:   applyOut.EventType,
		ReloadTopic: applyOut.ReloadTopic,
	}, nil
}

// ApplyResult 是 ApplyTransaction 的返回值。
type ApplyResult struct {
	ApprovalID  string
	ActionType  string
	TargetType  string
	TargetID    string
	OldVersion  *int
	NewVersion  int
	Checker     string
	EventType   string // outbox event type
	ReloadTopic string // config reload topic（由 worker publish）
}

// handleApplyFailure 处理 applyByActionType 失败：回滚到 SAVEPOINT 撤销业务写入，
// 再严格标记 FAILED_STALE 并提交（只保留失败状态，不留半成品写入）。
// 返回原始 apply 错误（或任何失败步骤的内部错误）。
//
// 调用序列（P0-1 失败路径，可用 mock pgx.Tx 无 DB 验证）：
//  1. ROLLBACK TO SAVEPOINT approval_apply — 失败则立即返回内部错误且不提交
//  2. UPDATE ... SET status='FAILED_STALE' WHERE id AND status='APPLYING'
//     — 严格成功语义：错误不得忽略，且必须恰好影响 1 行；任一失败即返回错误、
//     不 Commit（由调用方 ApplyTransaction 的 defer Rollback 回滚整个事务，
//     审批回到 PENDING 而不是残留 APPLYING）
//  3. Commit — 仅在步骤 1、2 都严格成功后执行；失败则返回内部错误
//  4. 成功时保持返回原始 applyErr
func (s *ApprovalService) handleApplyFailure(ctx context.Context, tx pgx.Tx, approvalUUID uuid.UUID, approvalID string, applyErr error) error {
	// 1. 回滚到 SAVEPOINT，撤销 applyByActionType 的全部业务写入
	if _, rbErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT approval_apply`); rbErr != nil {
		return apperr.ErrInternal.WithMessage("failed to rollback to savepoint: " + rbErr.Error())
	}

	// 2. FAILED_STALE 标记必须严格成功：不得忽略 error，必须影响恰好 1 行。
	//    失败时不得 Commit — 由调用方 ApplyTransaction 的 defer Rollback 回滚整个事务，
	//    审批回到 PENDING（而不是残留 APPLYING）。
	staleTag, staleErr := tx.Exec(ctx, `
		UPDATE admin_approval_requests
		SET status = 'FAILED_STALE'
		WHERE id = $1 AND status = 'APPLYING'`, approvalUUID)
	if staleErr != nil {
		return apperr.ErrInternal.WithMessage("failed to mark approval as FAILED_STALE: " + staleErr.Error())
	}
	if staleTag.RowsAffected() != 1 {
		return apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("approval %s is not APPLYING (RowsAffected=%d), cannot mark FAILED_STALE — will rollback to PENDING",
				approvalID, staleTag.RowsAffected()))
	}

	// 3. 只有 SAVEPOINT 回滚成功且 FAILED_STALE 严格成功时才 Commit
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return apperr.ErrInternal.WithMessage("failed to commit FAILED_STALE: " + commitErr.Error())
	}

	// 4. 保持返回原始 applyErr
	return applyErr
}

// ── RejectApproval：拒绝审批 ──

// RejectApproval 拒绝审批请求。
//
// 身份从已验证 Context 获取（RED-15），拒绝自审。
func (s *ApprovalService) RejectApproval(ctx context.Context, approvalID string, reviewReason string) error {
	// 1. 身份只从已验证 Context 获取
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil {
		return apperr.ErrForbidden.WithMessage("checker identity not found in authenticated context")
	}
	checker := identity.Username
	if checker == "" {
		checker = identity.Subject
	}
	if checker == "" || checker == "unknown" {
		return apperr.ErrForbidden.WithMessage("checker identity not found in authenticated context")
	}

	// 2. 验证审批权限
	if !identity.HasPermission(httpx.PermAdminApprove) {
		return apperr.ErrForbidden.WithMessage("checker lacks admin:approve permission")
	}

	// 3. 解析 approval ID
	approvalUUID, err := uuid.Parse(approvalID)
	if err != nil {
		return apperr.ErrInvalidArgument.WithMessage("invalid approval id")
	}

	// 4. SELECT approval FOR UPDATE
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return apperr.ErrInternal.WithMessage("failed to begin transaction: " + err.Error())
	}
	defer tx.Rollback(ctx)

	rec, err := selectApprovalForUpdate(ctx, tx, approvalUUID)
	if err != nil {
		return err
	}
	if rec.Status != ApprovalStatusPending {
		return apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("approval is not PENDING (current=%s), cannot reject", rec.Status))
	}

	// 5. 拒绝自审
	if checker == rec.Requester {
		return apperr.ErrForbidden.WithMessage(
			fmt.Sprintf("self-review rejected: checker=%s cannot reject own request (requester=%s)", checker, rec.Requester))
	}

	// 6. 更新状态为 REJECTED
	//    P0-1: 必须检查 RowsAffected()==1，防止并发审批或状态已变更时静默成功
	rejectTag, err := tx.Exec(ctx, `
		UPDATE admin_approval_requests
		SET status = 'REJECTED', checker = $1, review_reason = $2, reviewed_at = now()
		WHERE id = $3 AND status = 'PENDING'`,
		checker, reviewReason, approvalUUID)
	if err != nil {
		return apperr.ErrInternal.WithMessage("failed to reject approval: " + err.Error())
	}
	if rejectTag.RowsAffected() != 1 {
		return apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("approval %s is not PENDING (RowsAffected=%d), cannot reject — concurrent modification",
				approvalID, rejectTag.RowsAffected()))
	}

	if err := tx.Commit(ctx); err != nil {
		return apperr.ErrInternal.WithMessage("failed to commit transaction: " + err.Error())
	}
	return nil
}

// ── 辅助函数 ──

// selectApprovalForUpdate 读取审批记录并加行锁。
func selectApprovalForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*ApprovalRecord, error) {
	var rec ApprovalRecord
	var tenantID, targetID, checker, reviewReason, requestID *string
	var targetVersion *int
	var validatedPayload, beforeSnapshot []byte
	var reviewedAt, appliedAt *time.Time

	err := tx.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, action_type,
		       COALESCE(target_type, ''), target_id::text, target_version,
		       validated_payload::text, before_snapshot::text,
		       requester, COALESCE(requester_role, ''), COALESCE(reason, ''), status,
		       checker, review_reason, created_at, reviewed_at, applied_at, request_id
		FROM admin_approval_requests
		WHERE id = $1 FOR UPDATE`, id).Scan(
		&rec.ID, &tenantID, &rec.ActionType,
		&rec.TargetType, &targetID, &targetVersion,
		&validatedPayload, &beforeSnapshot,
		&rec.Requester, &rec.RequesterRole, &rec.Reason, &rec.Status,
		&checker, &reviewReason, &rec.CreatedAt, &reviewedAt, &appliedAt, &requestID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("approval request not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to select approval: " + err.Error())
	}

	rec.TenantID = tenantID
	rec.TargetID = targetID
	rec.TargetVersion = targetVersion
	rec.Checker = checker
	rec.ReviewReason = reviewReason
	rec.ReviewedAt = reviewedAt
	rec.AppliedAt = appliedAt
	rec.RequestID = requestID

	if len(validatedPayload) > 0 {
		json.Unmarshal(validatedPayload, &rec.ValidatedPayload)
	}
	if len(beforeSnapshot) > 0 {
		json.Unmarshal(beforeSnapshot, &rec.BeforeSnapshot)
	}
	return &rec, nil
}

// applyByActionType 按 action_type 应用配置。
// 返回 applyOut（含 new version 和 outbox event type）。
func (s *ApprovalService) applyByActionType(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	switch rec.ActionType {
	case ActionDisableAcquirerChannel, ActionEnableAcquirerChannel:
		return s.applyChannelStatus(ctx, tx, rec)
	case ActionUpdateAcquirerChannel:
		return s.applyChannelUpdate(ctx, tx, rec)
	case ActionDisableIssuingPool, ActionEnableIssuingPool:
		return s.applyPoolStatus(ctx, tx, rec)
	case ActionUpdateIssuingPool:
		return s.applyPoolUpdate(ctx, tx, rec)
	case ActionUpdateMerchantReserve:
		return s.applyMerchantReserve(ctx, tx, rec)
	case ActionDisableWalletPolicy:
		return s.applyWalletPolicyDisable(ctx, tx, rec)
	case ActionUpdateCorporateEntity:
		return s.applyCorporateEntityUpdate(ctx, tx, rec)
	case ActionUpdateBridgeProfile:
		return s.applyBridgeProfileUpdate(ctx, tx, rec)
	case ActionUpdateWalletPolicy:
		return s.applyWalletPolicyUpdate(ctx, tx, rec)
	case ActionDisableVAAccount:
		return s.applyVADisable(ctx, tx, rec)
	case ActionUpdateChannelRisk:
		return s.applyChannelRisk(ctx, tx, rec)
	case ActionSystemLockdownRecovery:
		return s.applySystemRecovery(ctx, tx, rec)
	case ActionDisableAdapterChannel, ActionEnableAdapterChannel:
		return s.applyAdapterChannelStatus(ctx, tx, rec)
	case ActionDisableBridgeProfile:
		return s.applyBridgeProfileDisable(ctx, tx, rec)
	case ActionSetEntityRiskHold:
		return s.applyEntityRiskHold(ctx, tx, rec)
	case ActionFreezeIsolationGroup, ActionUnfreezeIsolationGroup:
		return s.applyIsolationGroupFreeze(ctx, tx, rec)
	case ActionReleaseVAAccount:
		return s.applyVARelease(ctx, tx, rec)
	case ActionCreateMerchantPolicy:
		return s.applyMerchantPolicyCreate(ctx, tx, rec)
	case ActionUpdateMerchantPolicy:
		return s.applyMerchantPolicyUpdate(ctx, tx, rec)
	// P0-4: 新增 apply 分支
	case ActionEnableEntity:
		return s.applyEntityEnable(ctx, tx, rec)
	case ActionClearEntityRiskHold:
		return s.applyEntityRiskHoldClear(ctx, tx, rec)
	case ActionEnableBridgeProfile:
		return s.applyBridgeProfileEnable(ctx, tx, rec)
	case ActionEnableWalletPolicy:
		return s.applyWalletPolicyEnable(ctx, tx, rec)
	case ActionEnableAdapterProvider, ActionDisableAdapterProvider:
		return s.applyAdapterProviderStatus(ctx, tx, rec)
	case ActionUpdateAdapterProvider:
		return s.applyAdapterProviderUpdate(ctx, tx, rec)
	case ActionEnableBankProvider, ActionDisableBankProvider:
		return s.applyBankProviderStatus(ctx, tx, rec)
	case ActionRotateBankProviderCred:
		return s.applyBankProviderCredentialRotate(ctx, tx, rec)
	case ActionRevokePaymentLink:
		return s.applyPaymentLinkRevoke(ctx, tx, rec)
	default:
		return nil, apperr.ErrInvalidArgument.WithMessage("unsupported action_type: " + rec.ActionType)
	}
}

// applyByActionOut 是 applyByActionType 的内部返回值。
type applyByActionOut struct {
	TargetUUID  uuid.UUID
	NewVersion  int
	EventType   string
	ReloadTopic string
}

// applyChannelStatus 应用通道启用/禁用。
func (s *ApprovalService) applyChannelStatus(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload ChannelStatusPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	channelUUID, err := uuid.Parse(payload.ChannelID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid channel_id in payload")
	}

	// 确定目标状态
	var targetStatus string
	switch rec.ActionType {
	case ActionDisableAcquirerChannel:
		targetStatus = "INACTIVE"
	case ActionEnableAcquirerChannel:
		targetStatus = "ACTIVE"
	}

	// SELECT target FOR UPDATE，验证 version
	var currentVersion int
	var controlStrategyMode string
	err = tx.QueryRow(ctx, `SELECT version, strategy_mode FROM acquirer_channels WHERE id = $1 FOR UPDATE`, channelUUID).Scan(&currentVersion, &controlStrategyMode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target channel not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock channel: " + err.Error())
	}
	if rec.TargetVersion != nil && currentVersion != *rec.TargetVersion {
		return nil, apperr.ErrVersionConflict.WithMessage(
			fmt.Sprintf("version conflict: approval target_version=%d, current=%d (target modified by another operator)", *rec.TargetVersion, currentVersion))
	}

	// P0-3: 先锁 runtime binding FOR UPDATE；缺 binding 返回错误，由 P0-1 SAVEPOINT 回滚
	var runtimeChannelID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM tenant_acquirer_channel WHERE acquirer_channel_id = $1 FOR UPDATE`, channelUUID).Scan(&runtimeChannelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("no runtime channel bound to acquirer_channel_id: " + channelUUID.String())
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock runtime channel: " + err.Error())
	}

	// P0-3: Enable + BURN_MODE 是非法转换，不得出现"控制面启用、运行时 BURN"假状态
	if rec.ActionType == ActionEnableAcquirerChannel && controlStrategyMode == "BURN_MODE" {
		return nil, apperr.ErrIllegalTransition.WithMessage(
			"cannot enable channel in BURN_MODE: resolve burn mode via config update first")
	}

	// 应用配置，检查 affected rows=1，version+1
	var newVersion int
	err = tx.QueryRow(ctx, `
		UPDATE acquirer_channels
		SET status = $1, version = version + 1, updated_at = now()
		WHERE id = $2 AND version = $3
		RETURNING version`, targetStatus, channelUUID, currentVersion).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: channel version changed concurrently")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to update channel: " + err.Error())
	}

	// P0-3: 同步更新绑定的 runtime channel（is_active 与控制面一致）
	isActive := targetStatus == "ACTIVE"
	if _, err := tx.Exec(ctx, `
		UPDATE tenant_acquirer_channel
		SET is_active = $1, version = version + 1, updated_at = now()
		WHERE id = $2`,
		isActive, runtimeChannelID); err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to sync runtime channel: " + err.Error())
	}

	// outbox/audit 的 aggregate 必须是审批目标（控制面通道 ID）——
	// runtime 同步只是副作用，不得替换 aggregate 身份（否则审计无法按 target_id 追溯）
	return &applyByActionOut{
		TargetUUID:  channelUUID,
		NewVersion:  newVersion,
		EventType:   "acquirer_channel.status_changed",
		ReloadTopic: "config:acquirer_channel:reload",
	}, nil
}

// applyChannelUpdate 应用通道配置更新。
func (s *ApprovalService) applyChannelUpdate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload ChannelUpdatePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	channelUUID, err := uuid.Parse(payload.ChannelID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid channel_id in payload")
	}

	// SELECT FOR UPDATE 读取旧配置
	var oldTier, oldStrategy, oldProviderCode, oldChannelCode, oldCredKey, oldFunding, controlStatus string
	var oldProviderConfig, oldRoutingConfig []byte
	var currentVersion int
	err = tx.QueryRow(ctx, `
		SELECT provider_tier, strategy_mode, provider_code, channel_code,
		       credential_vault_key, COALESCE(routing_config->>'vcc_funding_mode', ''),
		       provider_config, routing_config, version, status
		FROM acquirer_channels WHERE id = $1 FOR UPDATE`, channelUUID).Scan(
		&oldTier, &oldStrategy, &oldProviderCode, &oldChannelCode,
		&oldCredKey, &oldFunding, &oldProviderConfig, &oldRoutingConfig, &currentVersion, &controlStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target channel not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock channel: " + err.Error())
	}
	if rec.TargetVersion != nil && currentVersion != *rec.TargetVersion {
		return nil, apperr.ErrVersionConflict.WithMessage(
			fmt.Sprintf("version conflict: approval target_version=%d, current=%d", *rec.TargetVersion, currentVersion))
	}

	// P0-2: 在任何 UPDATE acquirer_channels 之前锁定 runtime binding FOR UPDATE；
	// 缺 binding 返回明确错误，由 P0-1 SAVEPOINT 回滚，绝不允许 control ID fallback
	var runtimeChannelID uuid.UUID
	var runtimeCurrentVersion int
	var runtimeCurrentActive bool
	err = tx.QueryRow(ctx, `
		SELECT id, version, is_active FROM tenant_acquirer_channel
		WHERE acquirer_channel_id = $1 FOR UPDATE`, channelUUID).Scan(
		&runtimeChannelID, &runtimeCurrentVersion, &runtimeCurrentActive)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("no runtime channel bound to acquirer_channel_id: " + channelUUID.String())
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock runtime channel: " + err.Error())
	}
	// runtimeCurrentVersion/runtimeCurrentActive 按规格读取，供将来条件更新使用
	_ = runtimeCurrentVersion
	_ = runtimeCurrentActive

	// 合并 patch 到旧配置
	oldPC := map[string]interface{}{}
	if len(oldProviderConfig) > 0 {
		json.Unmarshal(oldProviderConfig, &oldPC)
	}
	oldRC := map[string]interface{}{}
	if len(oldRoutingConfig) > 0 {
		json.Unmarshal(oldRoutingConfig, &oldRC)
	}
	oldCfg := &configvalidator.AcquirerChannelConfig{
		ProviderTier:       oldTier,
		ProviderCode:       oldProviderCode,
		ChannelCode:        oldChannelCode,
		CredentialVaultKey: oldCredKey,
		StrategyMode:       oldStrategy,
		FundingMode:        oldFunding,
		ProviderConfig:     oldPC,
		RoutingConfig:      oldRC,
	}
	// P0-2: 顶层 funding_mode 唯一持久化事实源是 routing_config["vcc_funding_mode"]。
	// patch 只带顶层 funding_mode（RoutingConfig==nil）时：深拷贝 oldRC，写入新 vcc_funding_mode，
	// 保留旧 routing 字段（risk_isolation_group_id 等），赋给 payload.RoutingConfig。
	// patch 显式带 routing_config 时：Normalize 拒绝请求内 top/nested 冲突。
	if payload.FundingMode != "" && payload.RoutingConfig == nil {
		copiedRouting := make(map[string]interface{}, len(oldRC)+1)
		for k, v := range oldRC {
			copiedRouting[k] = v
		}
		copiedRouting["vcc_funding_mode"] = payload.FundingMode
		payload.RoutingConfig = copiedRouting
	} else if payload.RoutingConfig != nil {
		patchNorm := &configvalidator.AcquirerChannelConfig{
			FundingMode:   payload.FundingMode,
			RoutingConfig: payload.RoutingConfig,
		}
		if err := configvalidator.NormalizeAcquirerFundingMode(patchNorm); err != nil {
			return nil, err
		}
		payload.RoutingConfig = patchNorm.RoutingConfig
	}
	patchCfg := &configvalidator.AcquirerChannelConfig{
		ProviderTier:       payload.ProviderTier,
		StrategyMode:       payload.StrategyMode,
		FundingMode:        payload.FundingMode,
		CredentialVaultKey: payload.CredentialVault,
		ProviderConfig:     payload.ProviderConfig,
		RoutingConfig:      payload.RoutingConfig,
	}
	merged := configvalidator.MergeAcquirerPatch(oldCfg, patchCfg)

	// 用 Task 3 的同一 Validator 重验完整配置
	if err := configvalidator.ValidateAcquirerChannel(merged); err != nil {
		return nil, err
	}

	// 构造 UPDATE
	var updates []string
	var args []interface{}
	argIdx := 1
	if payload.ProviderTier != "" {
		updates = append(updates, fmt.Sprintf("provider_tier = $%d", argIdx))
		args = append(args, payload.ProviderTier)
		argIdx++
	}
	if payload.StrategyMode != "" {
		updates = append(updates, fmt.Sprintf("strategy_mode = $%d", argIdx))
		args = append(args, payload.StrategyMode)
		argIdx++
	}
	if payload.CredentialVault != "" {
		updates = append(updates, fmt.Sprintf("credential_vault_key = $%d", argIdx))
		args = append(args, payload.CredentialVault)
		argIdx++
	}
	if payload.ProviderConfig != nil {
		configJSON, _ := json.Marshal(payload.ProviderConfig)
		updates = append(updates, fmt.Sprintf("provider_config = $%d", argIdx))
		args = append(args, configJSON)
		argIdx++
	}
	if payload.RoutingConfig != nil {
		configJSON, _ := json.Marshal(payload.RoutingConfig)
		updates = append(updates, fmt.Sprintf("routing_config = $%d", argIdx))
		args = append(args, configJSON)
		argIdx++
	}
	updates = append(updates, "updated_at = now()")
	updates = append(updates, "version = version + 1")
	args = append(args, channelUUID)
	args = append(args, currentVersion)

	query := fmt.Sprintf(`UPDATE acquirer_channels SET %s WHERE id = $%d AND version = $%d RETURNING version`,
		joinWithComma(updates), argIdx, argIdx+1)
	var newVersion int
	err = tx.QueryRow(ctx, query, args...).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: channel version changed concurrently")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to update channel: " + err.Error())
	}

	// P0-1: 构造 runtime channel_config — 只写非敏感执行配置
	runtimeConfig, runtimeIsActive := buildRuntimeChannelConfig(merged, controlStatus)

	// P0-1: UPDATE tenant_acquirer_channel 同步 runtime channel_config（非敏感执行配置）
	configJSON, _ := json.Marshal(runtimeConfig)
	currencies, err := configvalidator.AcquirerCurrencies(merged.ProviderConfig)
	if err != nil {
		return nil, err
	}
	var currenciesJSON []byte
	if currencies != nil {
		currenciesJSON, _ = json.Marshal(currencies)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE tenant_acquirer_channel
		 SET channel_config = $1, is_active = $2, currencies = COALESCE($4::jsonb, currencies),
		     version = version + 1, updated_at = now()
		 WHERE id = $3`,
		configJSON, runtimeIsActive, runtimeChannelID, currenciesJSON); err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to sync runtime channel config: " + err.Error())
	}

	// outbox/audit 的 aggregate 必须是审批目标（控制面通道 ID）
	return &applyByActionOut{
		TargetUUID:  channelUUID,
		NewVersion:  newVersion,
		EventType:   "acquirer_channel.config_updated",
		ReloadTopic: "config:acquirer_channel:reload",
	}, nil
}

// validateIssuingActivationTx 在当前 pgx.Tx 内验证 issuing provider 的完整 ownership。
// R21 Step C: 所有 ownership 查询使用当前 pgx.Tx，使用 FOR SHARE/FOR KEY SHARE。
// 调用纯函数 issuing.ValidateProviderOwnershipFacts，禁止复制判断逻辑。
//
// 参数：
//   - ctx: 请求上下文
//   - tx: 当前事务
//   - poolUUID: issuing_providers.id
//   - entityID: corporate_entity.id（从 issuing_providers.entity_id 读取）
//   - adapterProviderIDStr: adapter_provider.id 字符串形式
//   - credentialVaultKey: vault://... 路径
//
// 返回 error 表示验证失败（调用方应返回错误，不更新配置、不写审计/outbox）。
func (s *ApprovalService) validateIssuingActivationTx(
	ctx context.Context,
	tx pgx.Tx,
	poolUUID uuid.UUID,
	entityID *uuid.UUID,
	adapterProviderIDStr string,
	credentialVaultKey string,
) error {
	// 1. 查询 entity tenant_id（FOR SHARE 锁）
	if entityID == nil {
		return apperr.ErrConfigInconsistent.WithMessage("entity_id is null at apply time")
	}
	var entityTenantID *uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT ce.tenant_id FROM corporate_entity ce
		WHERE ce.id = $1 FOR SHARE`, *entityID).Scan(&entityTenantID)
	if err != nil {
		return apperr.ErrConfigInconsistent.WithMessage("entity not found or tenant_id missing at apply time")
	}

	// 2. 查询 adapter_provider status/adapter_type（FOR SHARE 锁）
	if adapterProviderIDStr == "" {
		return apperr.ErrConfigInconsistent.WithMessage("adapter_provider_id is empty at apply time")
	}
	apID, parseErr := uuid.Parse(adapterProviderIDStr)
	if parseErr != nil {
		return apperr.ErrConfigInconsistent.WithMessage("invalid adapter_provider_id format at apply time")
	}
	var apStatus, apAdapterType string
	apErr := tx.QueryRow(ctx,
		`SELECT status, adapter_type::text FROM adapter_provider WHERE id = $1 FOR SHARE`,
		apID).Scan(&apStatus, &apAdapterType)
	if apErr != nil {
		return apperr.ErrConfigInconsistent.WithMessage("adapter_provider not found at apply time")
	}

	// 3. 解析 vault path
	if credentialVaultKey == "" {
		return apperr.ErrConfigInconsistent.WithMessage("credential_vault_key is empty at apply time")
	}
	vaultPath, vpErr := issuing.ParseVaultPath(credentialVaultKey)
	if vpErr != nil {
		return apperr.ErrConfigInconsistent.WithMessage(
			"invalid credential_vault_key format at apply time: " + vpErr.Error())
	}

	// 4. 查询 vault 记录（FOR KEY SHARE 锁）
	var vStatus, vCredType string
	var vTenantID, vIssuingProviderID, vProviderID, vRecordID *uuid.UUID
	var vExpiresAt *time.Time
	var vKeyVersion int
	vaultErr := tx.QueryRow(ctx, `
		SELECT id, status, credential_type, tenant_id, issuing_provider_id, provider_id, expires_at, key_version
		FROM adapter_credential_vault
		WHERE id = $1 AND key_version = $2
		FOR KEY SHARE`,
		vaultPath.RecordID, vaultPath.KeyVersion).Scan(&vRecordID, &vStatus, &vCredType, &vTenantID, &vIssuingProviderID, &vProviderID, &vExpiresAt, &vKeyVersion)
	if vaultErr != nil {
		return apperr.ErrConfigInconsistent.WithMessage("vault credential not found at apply time")
	}

	// 5. 调用纯函数校验全部 ownership 事实
	apIDPtr := &apID
	if err := issuing.ValidateProviderOwnershipFacts(&issuing.OwnershipFacts{
		PathTenantID:           vaultPath.TenantID,
		PathRecordID:           vaultPath.RecordID,
		PathField:              vaultPath.FieldName,
		PathKeyVersion:         vaultPath.KeyVersion,
		EntityTenantID:         entityTenantID,
		AdapterStatus:          apStatus,
		AdapterType:            apAdapterType,
		VaultRecordID:          vRecordID,
		VaultStatus:            vStatus,
		VaultCredentialType:    vCredType,
		VaultTenantID:          vTenantID,
		VaultIssuingProviderID: vIssuingProviderID,
		VaultProviderID:        vProviderID,
		VaultExpiresAt:         vExpiresAt,
		VaultKeyVersion:        vKeyVersion,
		ProviderID:             poolUUID,
		AdapterProviderID:      apIDPtr,
	}); err != nil {
		return apperr.ErrConfigInconsistent.WithMessage(err.Error())
	}

	return nil
}

// applyPoolStatus 应用卡池启用/禁用。
func (s *ApprovalService) applyPoolStatus(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload PoolStatusPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	poolUUID, err := uuid.Parse(payload.PoolID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid pool_id in payload")
	}

	var targetStatus string
	switch rec.ActionType {
	case ActionDisableIssuingPool:
		targetStatus = "INACTIVE"
	case ActionEnableIssuingPool:
		targetStatus = "ACTIVE"
	}

	// R18 P0-4: 批准生效时在同一 DB 事务内重新执行 Fail-Closed 校验。
	// 查询完整配置行（FOR UPDATE 锁行），构造 IssuingPoolConfig，调用 ValidateIssuingRuntimeConfig。
	var oldTier, oldFunding, oldProviderCode, oldPoolCode, oldCredKey string
	var oldProviderConfig, oldVelocityConfig []byte
	var oldAdapterType string
	var oldAdapterProviderID *string
	var oldEntityID *uuid.UUID
	var currentVersion int
	err = tx.QueryRow(ctx, `
		SELECT issuing_tier, funding_mode, provider_code, pool_code,
		       credential_vault_key, provider_config, velocity_config, version,
		       adapter_type::text, adapter_provider_id::text, entity_id
		FROM issuing_providers WHERE id = $1 FOR UPDATE`, poolUUID).Scan(
		&oldTier, &oldFunding, &oldProviderCode, &oldPoolCode,
		&oldCredKey, &oldProviderConfig, &oldVelocityConfig, &currentVersion,
		&oldAdapterType, &oldAdapterProviderID, &oldEntityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target pool not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock pool: " + err.Error())
	}
	if rec.TargetVersion != nil && currentVersion != *rec.TargetVersion {
		return nil, apperr.ErrVersionConflict.WithMessage(
			fmt.Sprintf("version conflict: approval target_version=%d, current=%d", *rec.TargetVersion, currentVersion))
	}

	// R18 P0-4: 启用卡池时在同一事务内重新执行 Fail-Closed 运行时校验。
	// 审批 apply 视为 production 路径，禁止 MOCK_LOCAL/UNSUPPORTED，要求 entity_id 非空。
	if rec.ActionType == ActionEnableIssuingPool {
		oldPC := map[string]interface{}{}
		if len(oldProviderConfig) > 0 {
			json.Unmarshal(oldProviderConfig, &oldPC)
		}
		oldVC := map[string]interface{}{}
		if len(oldVelocityConfig) > 0 {
			json.Unmarshal(oldVelocityConfig, &oldVC)
		}
		oldAdapterProviderIDStr := ""
		if oldAdapterProviderID != nil {
			oldAdapterProviderIDStr = *oldAdapterProviderID
		}
		oldEntityIDStr := ""
		if oldEntityID != nil {
			oldEntityIDStr = oldEntityID.String()
		}
		runtimeCfg := &configvalidator.IssuingPoolConfig{
			EntityID:           oldEntityIDStr,
			IssuingTier:        oldTier,
			FundingMode:        oldFunding,
			ProviderCode:       oldProviderCode,
			PoolCode:           oldPoolCode,
			CredentialVaultKey: oldCredKey,
			ProviderConfig:     oldPC,
			VelocityConfig:     oldVC,
			AdapterType:        oldAdapterType,
			AdapterProviderID:  oldAdapterProviderIDStr,
		}
		if err := configvalidator.ValidateIssuingRuntimeConfig(runtimeCfg, s.runtimeMode); err != nil {
			return nil, apperr.ErrConfigInconsistent.WithMessage(
				"fail-closed validation failed on pool enable: " + err.Error())
		}

		// R23 Item 1: 按 AdapterType 分支处理 ownership 校验
		switch issuing.AdapterType(oldAdapterType) {
		case issuing.AdapterTypeNexusIssuingHTTPV1:
			// NEXUS_ISSUING_HTTP_V1: 必须调用 validateIssuingActivationTx 完整 DB ownership 校验
			if err := s.validateIssuingActivationTx(ctx, tx, poolUUID, oldEntityID, oldAdapterProviderIDStr, oldCredKey); err != nil {
				return nil, err
			}
		case issuing.AdapterTypeMockLocal:
			// R24 P0-2: MOCK_LOCAL 激活必须显式满足 mode == RuntimeModeLocalMock
			// 不得使用 !mode.IsProduction() 作为放行条件
			if !s.runtimeMode.IsLocalMock() {
				return nil, apperr.ErrConfigInconsistent.WithMessage(
					"MOCK_LOCAL adapter_type can only be activated in RuntimeModeLocalMock")
			}
			// 不调用 validateIssuingActivationTx，MOCK_LOCAL 无需 DB ownership 校验
		case issuing.AdapterTypeUnsupported:
			// UNSUPPORTED: 任何模式不得激活
			return nil, apperr.ErrConfigInconsistent.WithMessage(
				"UNSUPPORTED adapter_type cannot be activated in any mode")
		default:
			return nil, apperr.ErrConfigInconsistent.WithMessage(
				fmt.Sprintf("unknown adapter_type %q cannot be activated", oldAdapterType))
		}
	}

	var newVersion int
	err = tx.QueryRow(ctx, `
		UPDATE issuing_providers
		SET status = $1, version = version + 1, updated_at = now()
		WHERE id = $2 AND version = $3
		RETURNING version`, targetStatus, poolUUID, currentVersion).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: pool version changed concurrently")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to update pool: " + err.Error())
	}

	return &applyByActionOut{
		TargetUUID:  poolUUID,
		NewVersion:  newVersion,
		EventType:   "issuing_pool.status_changed",
		ReloadTopic: "config:issuing_pool:reload",
	}, nil
}

// applyPoolUpdate 应用卡池配置更新。
// R21 Step C: oldStatus=ACTIVE 时调用完整 activation+ownership helper；
// oldStatus=DISABLED 时只调用 ValidateDraftProvider；保留 EntityID。
func (s *ApprovalService) applyPoolUpdate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload PoolUpdatePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	poolUUID, err := uuid.Parse(payload.PoolID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid pool_id in payload")
	}

	var oldTier, oldFunding, oldProviderCode, oldPoolCode, oldCredKey, oldStatus string
	var oldProviderConfig, oldVelocityConfig []byte
	var oldAdapterType string
	var oldAdapterProviderID *string
	var oldEntityID *uuid.UUID
	var currentVersion int
	err = tx.QueryRow(ctx, `
		SELECT issuing_tier, funding_mode, provider_code, pool_code,
		       credential_vault_key, provider_config, velocity_config, version,
		       adapter_type::text, adapter_provider_id::text, entity_id, status
		FROM issuing_providers WHERE id = $1 FOR UPDATE`, poolUUID).Scan(
		&oldTier, &oldFunding, &oldProviderCode, &oldPoolCode,
		&oldCredKey, &oldProviderConfig, &oldVelocityConfig, &currentVersion,
		&oldAdapterType, &oldAdapterProviderID, &oldEntityID, &oldStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target pool not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock pool: " + err.Error())
	}
	if rec.TargetVersion != nil && currentVersion != *rec.TargetVersion {
		return nil, apperr.ErrVersionConflict.WithMessage(
			fmt.Sprintf("version conflict: approval target_version=%d, current=%d", *rec.TargetVersion, currentVersion))
	}

	oldPC := map[string]interface{}{}
	if len(oldProviderConfig) > 0 {
		json.Unmarshal(oldProviderConfig, &oldPC)
	}
	oldVC := map[string]interface{}{}
	if len(oldVelocityConfig) > 0 {
		json.Unmarshal(oldVelocityConfig, &oldVC)
	}
	oldAdapterProviderIDStr := ""
	if oldAdapterProviderID != nil {
		oldAdapterProviderIDStr = *oldAdapterProviderID
	}
	// R20 P0-4 / R21 Step C: 合并配置时保留 EntityID
	oldEntityIDStr := ""
	if oldEntityID != nil {
		oldEntityIDStr = oldEntityID.String()
	}
	oldCfg := &configvalidator.IssuingPoolConfig{
		EntityID:           oldEntityIDStr,
		IssuingTier:        oldTier,
		FundingMode:        oldFunding,
		ProviderCode:       oldProviderCode,
		PoolCode:           oldPoolCode,
		CredentialVaultKey: oldCredKey,
		ProviderConfig:     oldPC,
		VelocityConfig:     oldVC,
		AdapterType:        oldAdapterType,
		AdapterProviderID:  oldAdapterProviderIDStr,
	}
	patchCfg := &configvalidator.IssuingPoolConfig{
		IssuingTier:        payload.IssuingTier,
		FundingMode:        payload.FundingMode,
		CredentialVaultKey: payload.CredentialVault,
		ProviderConfig:     payload.ProviderConfig,
		VelocityConfig:     payload.VelocityConfig,
		AdapterType:        payload.AdapterType,
		AdapterProviderID:  payload.AdapterProviderID,
	}
	merged := configvalidator.MergeIssuingPatch(oldCfg, patchCfg)
	// R21 Step C: 合并后保留 EntityID（patch 不携带 EntityID，merged 应使用 old EntityID）
	merged.EntityID = oldEntityIDStr

	// R22 Item 4: 按 oldStatus 明确 switch 分支校验（Fail-Closed）
	switch oldStatus {
	case "ACTIVE":
		// oldStatus=ACTIVE: 调用完整 activation + ownership helper
		// 1. ValidateProviderActivation（激活级校验，拒绝 UNSUPPORTED，要求完整配置）
		actCfg := &issuing.ActivationConfig{
			EntityID:           merged.EntityID,
			IssuingTier:        merged.IssuingTier,
			FundingMode:        merged.FundingMode,
			ProviderCode:       merged.ProviderCode,
			PoolCode:           merged.PoolCode,
			CredentialVaultKey: merged.CredentialVaultKey,
			ProviderConfig:     merged.ProviderConfig,
			VelocityConfig:     merged.VelocityConfig,
			AdapterType:        merged.AdapterType,
			AdapterProviderID:  merged.AdapterProviderID,
		}
		if err := issuing.ValidateProviderActivation(actCfg, issuing.RuntimeMode(s.runtimeMode)); err != nil {
			return nil, apperr.ErrConfigInconsistent.WithMessage(
				"activation validation failed on pool update (oldStatus=ACTIVE): " + err.Error())
		}

		// R23 Item 1: 按 AdapterType 分支处理 ownership 校验
		switch issuing.AdapterType(merged.AdapterType) {
		case issuing.AdapterTypeNexusIssuingHTTPV1:
			// NEXUS_ISSUING_HTTP_V1: 必须调用 validateIssuingActivationTx 完整 DB ownership 校验
			if err := s.validateIssuingActivationTx(ctx, tx, poolUUID, oldEntityID, merged.AdapterProviderID, merged.CredentialVaultKey); err != nil {
				return nil, err
			}
		case issuing.AdapterTypeMockLocal:
			// R24 P0-2: MOCK_LOCAL 激活必须显式满足 mode == RuntimeModeLocalMock
			// 不得使用 !mode.IsProduction() 作为放行条件
			if !s.runtimeMode.IsLocalMock() {
				return nil, apperr.ErrConfigInconsistent.WithMessage(
					"MOCK_LOCAL adapter_type can only be activated in RuntimeModeLocalMock")
			}
			// 不调用 validateIssuingActivationTx，MOCK_LOCAL 无需 DB ownership 校验
		case issuing.AdapterTypeUnsupported:
			// UNSUPPORTED: 任何模式不得激活
			return nil, apperr.ErrConfigInconsistent.WithMessage(
				"UNSUPPORTED adapter_type cannot be activated in any mode")
		default:
			return nil, apperr.ErrConfigInconsistent.WithMessage(
				fmt.Sprintf("unknown adapter_type %q cannot be activated", merged.AdapterType))
		}
	case "DISABLED", "INACTIVE":
		// oldStatus=DISABLED/INACTIVE: 只调用 ValidateDraftProvider（草稿级校验，允许 UNSUPPORTED）
		actCfg := &issuing.ActivationConfig{
			EntityID:           merged.EntityID,
			IssuingTier:        merged.IssuingTier,
			FundingMode:        merged.FundingMode,
			ProviderCode:       merged.ProviderCode,
			PoolCode:           merged.PoolCode,
			CredentialVaultKey: merged.CredentialVaultKey,
			ProviderConfig:     merged.ProviderConfig,
			VelocityConfig:     merged.VelocityConfig,
			AdapterType:        merged.AdapterType,
			AdapterProviderID:  merged.AdapterProviderID,
		}
		if err := issuing.ValidateDraftProvider(actCfg); err != nil {
			return nil, apperr.ErrConfigInconsistent.WithMessage(
				"draft validation failed on pool update (oldStatus=" + oldStatus + "): " + err.Error())
		}
	default:
		// R22 Item 4: 空值或未知状态 → CONFIG_INCONSISTENT（Fail-Closed）
		return nil, apperr.ErrConfigInconsistent.WithMessage(
			fmt.Sprintf("unknown issuing provider status %q at apply time (expected ACTIVE/DISABLED/INACTIVE)", oldStatus))
	}

	var updates []string
	var args []interface{}
	argIdx := 1
	if payload.IssuingTier != "" {
		updates = append(updates, fmt.Sprintf("issuing_tier = $%d", argIdx))
		args = append(args, payload.IssuingTier)
		argIdx++
	}
	if payload.FundingMode != "" {
		updates = append(updates, fmt.Sprintf("funding_mode = $%d", argIdx))
		args = append(args, payload.FundingMode)
		argIdx++
	}
	if payload.CredentialVault != "" {
		updates = append(updates, fmt.Sprintf("credential_vault_key = $%d", argIdx))
		args = append(args, payload.CredentialVault)
		argIdx++
	}
	if payload.ProviderConfig != nil {
		configJSON, _ := json.Marshal(payload.ProviderConfig)
		updates = append(updates, fmt.Sprintf("provider_config = $%d", argIdx))
		args = append(args, configJSON)
		argIdx++
	}
	if payload.VelocityConfig != nil {
		configJSON, _ := json.Marshal(payload.VelocityConfig)
		updates = append(updates, fmt.Sprintf("velocity_config = $%d", argIdx))
		args = append(args, configJSON)
		argIdx++
	}
	// Section 5: 应用 adapter_type / adapter_provider_id 更新
	if payload.AdapterType != "" {
		updates = append(updates, fmt.Sprintf("adapter_type = $%d", argIdx))
		args = append(args, payload.AdapterType)
		argIdx++
	}
	if payload.AdapterProviderID != "" {
		adapterProviderUUID, err := uuid.Parse(payload.AdapterProviderID)
		if err != nil {
			return nil, apperr.ErrInvalidArgument.WithMessage("adapter_provider_id must be a valid UUID")
		}
		updates = append(updates, fmt.Sprintf("adapter_provider_id = $%d", argIdx))
		args = append(args, adapterProviderUUID)
		argIdx++
	}
	updates = append(updates, "updated_at = now()")
	updates = append(updates, "version = version + 1")
	args = append(args, poolUUID)
	args = append(args, currentVersion)

	query := fmt.Sprintf(`UPDATE issuing_providers SET %s WHERE id = $%d AND version = $%d RETURNING version`,
		joinWithComma(updates), argIdx, argIdx+1)
	var newVersion int
	err = tx.QueryRow(ctx, query, args...).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: pool version changed concurrently")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to update pool: " + err.Error())
	}

	return &applyByActionOut{
		TargetUUID:  poolUUID,
		NewVersion:  newVersion,
		EventType:   "issuing_pool.config_updated",
		ReloadTopic: "config:issuing_pool:reload",
	}, nil
}

// applyMerchantReserve 应用商户准备金更新。
// 键控 merchant_treasury_ledger.merchant_id（id 是代理主键），一次更新该商户全部币种行；
// 乐观锁基准取各币种行的最小 version（投影 apply() 会在资金变动时独立 bump 各行 version）。
func (s *ApprovalService) applyMerchantReserve(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload MerchantReservePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	merchantUUID, err := uuid.Parse(payload.MerchantID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id in payload")
	}
	if payload.ReserveRatioBps < 0 || payload.ReserveRatioBps > 10000 {
		return nil, apperr.ErrInvalidArgument.WithMessage("reserve_ratio_bps must be in [0, 10000]")
	}

	var rowCount int64
	var currentVersion int
	err = tx.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(MIN(version), 0)
		FROM merchant_treasury_ledger
		WHERE merchant_id = $1`, merchantUUID).Scan(&rowCount, &currentVersion)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to read merchant treasury: " + err.Error())
	}
	if rowCount == 0 {
		return nil, apperr.ErrNotFound.WithMessage("target merchant treasury row not found")
	}
	if rec.TargetVersion != nil && currentVersion != *rec.TargetVersion {
		return nil, apperr.ErrVersionConflict.WithMessage(
			fmt.Sprintf("version conflict: approval target_version=%d, current=%d", *rec.TargetVersion, currentVersion))
	}

	tag, err := tx.Exec(ctx, `
		UPDATE merchant_treasury_ledger
		SET reserve_ratio_bps = $1, version = version + 1, updated_at = now()
		WHERE merchant_id = $2 AND version = $3`, payload.ReserveRatioBps, merchantUUID, currentVersion)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to update merchant reserve: " + err.Error())
	}
	if tag.RowsAffected() != rowCount {
		return nil, apperr.ErrVersionConflict.WithMessage("version changed concurrently")
	}
	newVersion := currentVersion + 1

	return &applyByActionOut{
		TargetUUID:  merchantUUID,
		NewVersion:  newVersion,
		EventType:   "merchant_reserve.updated",
		ReloadTopic: "config:merchant:reload",
	}, nil
}

// applyWalletPolicyDisable 应用钱包策略禁用。
// ADM-004: 实表为 transit_wallet_policy（与 enable 路径一致）；该表无 version 列，
// 状态取值受 CHECK 约束（ACTIVE/DISABLED/RISK_HOLD），禁用写 'DISABLED'。
func (s *ApprovalService) applyWalletPolicyDisable(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload WalletPolicyPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	policyUUID, err := uuid.Parse(payload.PolicyID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid policy_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM transit_wallet_policy WHERE id = $1 FOR UPDATE`, policyUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target wallet policy not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock wallet policy: " + err.Error())
	}
	if currentStatus == "DISABLED" {
		return nil, apperr.ErrIllegalTransition.WithMessage("wallet policy already disabled")
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE transit_wallet_policy
		SET status = 'DISABLED', updated_at = now()
		WHERE id = $1 AND status <> 'DISABLED'`, policyUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to disable wallet policy: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: wallet policy status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  policyUUID,
		NewVersion:  0,
		EventType:   "wallet_policy.disabled",
		ReloadTopic: "config:wallet_policy:reload",
	}, nil
}

// applyVADisable 应用 VA 账号禁用。
// ADM-005: 实表为 bank_collection_virtual_account_pool（不存在 virtual_account 表）；
// 状态取值受 CHECK 约束（AVAILABLE/ALLOCATED/DISABLED），禁用写 'DISABLED'。
// 该表无 version 列，使用 status 作为乐观锁。
func (s *ApprovalService) applyVADisable(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload VAAccountPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	vaUUID, err := uuid.Parse(payload.VAID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid va_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM bank_collection_virtual_account_pool WHERE id = $1 FOR UPDATE`, vaUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target VA account not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock VA account: " + err.Error())
	}
	if currentStatus == "DISABLED" {
		return nil, apperr.ErrIllegalTransition.WithMessage("VA account already disabled")
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE bank_collection_virtual_account_pool
		SET status = 'DISABLED', updated_at = now()
		WHERE id = $1 AND status <> 'DISABLED'`, vaUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to disable VA account: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: VA status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  vaUUID,
		NewVersion:  0,
		EventType:   "va_account.disabled",
		ReloadTopic: "config:va_account:reload",
	}, nil
}

// applyChannelRisk 应用通道风控更新。
// ADM-008: 不存在 channel_risk_config 表——风险字段统一存入 adapter_channel
// 的 channel_config JSONB（key: bin_decline_bps），乐观锁使用 adapter_channel.version。
func (s *ApprovalService) applyChannelRisk(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload ChannelRiskPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	channelUUID, err := uuid.Parse(payload.ChannelID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid channel_id in payload")
	}
	if payload.BinDeclineBps < 0 || payload.BinDeclineBps > 10000 {
		return nil, apperr.ErrInvalidArgument.WithMessage("bin_decline_bps must be in [0, 10000]")
	}

	var currentVersion int
	err = tx.QueryRow(ctx, `SELECT version FROM adapter_channel WHERE id = $1 FOR UPDATE`, channelUUID).Scan(&currentVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target adapter channel not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock adapter channel: " + err.Error())
	}
	if rec.TargetVersion != nil && currentVersion != *rec.TargetVersion {
		return nil, apperr.ErrVersionConflict.WithMessage(
			fmt.Sprintf("version conflict: approval target_version=%d, current=%d", *rec.TargetVersion, currentVersion))
	}

	var newVersion int
	err = tx.QueryRow(ctx, `
		UPDATE adapter_channel
		SET channel_config = jsonb_set(COALESCE(channel_config, '{}'::jsonb), '{bin_decline_bps}', to_jsonb($1::int)),
		    version = version + 1, updated_at = now()
		WHERE id = $2 AND version = $3
		RETURNING version`, payload.BinDeclineBps, channelUUID, currentVersion).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: channel version changed concurrently")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to update channel risk: " + err.Error())
	}

	return &applyByActionOut{
		TargetUUID:  channelUUID,
		NewVersion:  newVersion,
		EventType:   "channel_risk.updated",
		ReloadTopic: "config:acquirer_channel:reload",
	}, nil
}

// applySystemRecovery 应用系统封锁解除。
//
// P0-1: 严格状态机 — 仅允许从 RECOVERY_PENDING → OPEN，禁止从 LOCKED 直接恢复。
// 调用方（ApprovalService.ApplyTransaction）已确保审批记录为 PENDING 且 checker != requester。
// 此函数在事务内完成：
//  1. 锁定 system_control_state (SELECT FOR UPDATE)
//  2. 校验 state == RECOVERY_PENDING（禁止 LOCKED 直接恢复）
//  3. RECOVERY_PENDING → OPEN，epoch+1
//  4. 检查 RowsAffected()==1
func (s *ApprovalService) applySystemRecovery(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var currentState string
	var currentEpoch int64
	err := tx.QueryRow(ctx, `
		SELECT state::text, epoch FROM system_control_state WHERE id = 1 FOR UPDATE
	`).Scan(&currentState, &currentEpoch)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to lock system_control_state: " + err.Error())
	}

	// P0-1: 禁止从 LOCKED 直接恢复，必须先 RequestRecovery 走 RECOVERY_PENDING
	if currentState != "RECOVERY_PENDING" {
		return nil, apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("system recovery requires state RECOVERY_PENDING, got %s — direct recovery from LOCKED is prohibited",
				currentState))
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE system_control_state
		SET state = 'OPEN', epoch = epoch + 1, released_at = now(),
		    version = version + 1, updated_at = now()
		WHERE id = 1 AND state = 'RECOVERY_PENDING'`)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to release system lockdown: " + err.Error())
	}
	if cmd.RowsAffected() != 1 {
		return nil, apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("system is not in RECOVERY_PENDING state (RowsAffected=%d) — concurrent recovery or stale state",
				cmd.RowsAffected()))
	}

	return &applyByActionOut{
		TargetUUID:  uuid.New(), // system_control 无具体 target UUID
		NewVersion:  int(currentEpoch + 1),
		EventType:   "system.recovery_executed",
		ReloadTopic: "config:system:reload",
	}, nil
}

// applyAdapterChannelStatus 应用 adapter_channel 禁用/启用。
func (s *ApprovalService) applyAdapterChannelStatus(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload AdapterChannelStatusPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	channelUUID, err := uuid.Parse(payload.ChannelID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid channel_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM adapter_channel WHERE id = $1 FOR UPDATE`, channelUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target adapter channel not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock adapter channel: " + err.Error())
	}

	newStatus := "DISABLED"
	eventType := "adapter_channel.disabled"
	if rec.ActionType == ActionEnableAdapterChannel {
		newStatus = "ACTIVE"
		eventType = "adapter_channel.enabled"
	}
	if currentStatus == newStatus {
		return nil, apperr.ErrIllegalTransition.WithMessage("adapter channel already in status: " + newStatus)
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE adapter_channel SET status = $1, updated_at = now() WHERE id = $2 AND status <> $1`,
		newStatus, channelUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to update adapter channel status: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: adapter channel status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  channelUUID,
		NewVersion:  0,
		EventType:   eventType,
		ReloadTopic: "config:adapter_channel:reload",
	}, nil
}

// applyBridgeProfileDisable 应用 bridge_profile 禁用。
func (s *ApprovalService) applyBridgeProfileDisable(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload BridgeProfilePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	profileUUID, err := uuid.Parse(payload.ProfileID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid profile_id in payload")
	}

	var currentEnabled bool
	err = tx.QueryRow(ctx, `SELECT enabled FROM bridge_profile WHERE id = $1 FOR UPDATE`, profileUUID).Scan(&currentEnabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target bridge profile not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock bridge profile: " + err.Error())
	}
	if !currentEnabled {
		return nil, apperr.ErrIllegalTransition.WithMessage("bridge profile already disabled")
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE bridge_profile SET enabled = false, updated_at = now() WHERE id = $1 AND enabled = true`,
		profileUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to disable bridge profile: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: bridge profile state changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  profileUUID,
		NewVersion:  0,
		EventType:   "bridge_profile.disabled",
		ReloadTopic: "config:bridge_profile:reload",
	}, nil
}

// applyEntityRiskHold 应用 corporate_entity 风险冻结。
func (s *ApprovalService) applyEntityRiskHold(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload EntityRiskHoldPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	entityUUID, err := uuid.Parse(payload.EntityID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid entity_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM corporate_entity WHERE id = $1 FOR UPDATE`, entityUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target corporate entity not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock corporate entity: " + err.Error())
	}
	if currentStatus == "RISK_HOLD" {
		return nil, apperr.ErrIllegalTransition.WithMessage("entity already in RISK_HOLD")
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE corporate_entity SET status = 'RISK_HOLD', updated_at = now() WHERE id = $1 AND status <> 'RISK_HOLD'`,
		entityUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to set entity risk hold: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: entity status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  entityUUID,
		NewVersion:  0,
		EventType:   "corporate_entity.risk_hold",
		ReloadTopic: "config:corporate_entity:reload",
	}, nil
}

// applyIsolationGroupFreeze 应用管道隔离组冻结/解冻。
func (s *ApprovalService) applyIsolationGroupFreeze(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload IsolationGroupPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	groupUUID, err := uuid.Parse(payload.GroupID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid group_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM pipeline_isolation_groups WHERE id = $1 FOR UPDATE`, groupUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target isolation group not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock isolation group: " + err.Error())
	}

	var eventType string
	switch rec.ActionType {
	case ActionFreezeIsolationGroup:
		eventType = "isolation_group.frozen"
		if currentStatus == "FROZEN" {
			return nil, apperr.ErrIllegalTransition.WithMessage("isolation group already frozen")
		}
	case ActionUnfreezeIsolationGroup:
		eventType = "isolation_group.unfrozen"
		if currentStatus == "ACTIVE" {
			return nil, apperr.ErrIllegalTransition.WithMessage("isolation group already active")
		}
	}

	var rowsAffected int64
	if rec.ActionType == ActionFreezeIsolationGroup {
		cmd, execErr := tx.Exec(ctx, `
			UPDATE pipeline_isolation_groups
			SET status = 'FROZEN', frozen_reason = $1, frozen_by = $2, frozen_at = now(), updated_at = now()
			WHERE id = $3 AND status <> 'FROZEN'`,
			payload.Reason, rec.Requester, groupUUID)
		if execErr != nil {
			return nil, apperr.ErrInternal.WithMessage("failed to update isolation group: " + execErr.Error())
		}
		rowsAffected = cmd.RowsAffected()
	} else {
		cmd, execErr := tx.Exec(ctx, `
			UPDATE pipeline_isolation_groups
			SET status = 'ACTIVE', frozen_reason = NULL, frozen_by = NULL, frozen_at = NULL, updated_at = now()
			WHERE id = $1 AND status <> 'ACTIVE'`,
			groupUUID)
		if execErr != nil {
			return nil, apperr.ErrInternal.WithMessage("failed to update isolation group: " + execErr.Error())
		}
		rowsAffected = cmd.RowsAffected()
	}
	if rowsAffected == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: isolation group state changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  groupUUID,
		NewVersion:  0,
		EventType:   eventType,
		ReloadTopic: "config:isolation_group:reload",
	}, nil
}

// applyVARelease 应用 VA 账号释放。
// ADM-006: 池表状态机为 AVAILABLE/ALLOCATED/DISABLED（DB CHECK），不存在 'RELEASED'。
// 释放语义 = ALLOCATED → AVAILABLE（归还池），清空 allocated_intent_id 并记录 released_at。
func (s *ApprovalService) applyVARelease(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload VAReleasePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	vaUUID, err := uuid.Parse(payload.VAID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid va_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM bank_collection_virtual_account_pool WHERE id = $1 FOR UPDATE`, vaUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target VA account not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock VA account: " + err.Error())
	}
	if currentStatus != "ALLOCATED" {
		return nil, apperr.ErrIllegalTransition.WithMessage(
			fmt.Sprintf("VA account is not allocated (status=%s), cannot release", currentStatus))
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE bank_collection_virtual_account_pool
		SET status = 'AVAILABLE', allocated_intent_id = NULL, released_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'ALLOCATED'`, vaUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to release VA account: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: VA status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  vaUUID,
		NewVersion:  0,
		EventType:   "va_account.released",
		ReloadTopic: "config:va_account:reload",
	}, nil
}

// applyMerchantPolicyCreate 应用商户路由策略创建。
func (s *ApprovalService) applyMerchantPolicyCreate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload MerchantPolicyCreatePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	merchantUUID, err := uuid.Parse(payload.MerchantID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id in payload")
	}

	// 检查是否已存在 policy（幂等：已存在则返回冲突）
	var existingVersion int
	err = tx.QueryRow(ctx, `SELECT version FROM merchant_routing_policy WHERE merchant_id = $1 FOR UPDATE`, merchantUUID).Scan(&existingVersion)
	if err == nil {
		return nil, apperr.ErrIdempotencyConflict.WithMessage("merchant routing policy already exists for this merchant")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrInternal.WithMessage("failed to check existing policy: " + err.Error())
	}

	// INSERT 新 policy
	_, err = tx.Exec(ctx,
		`INSERT INTO merchant_routing_policy (merchant_id, provider_tier, status, version)
         VALUES ($1, $2, $3, 1)`,
		merchantUUID, payload.ProviderTier, payload.Status)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to create merchant routing policy: " + err.Error())
	}

	return &applyByActionOut{
		TargetUUID:  merchantUUID,
		NewVersion:  1,
		EventType:   "merchant_routing_policy.created",
		ReloadTopic: "config:merchant_routing_policy:reload",
	}, nil
}

// applyMerchantPolicyUpdate 应用商户路由策略更新。
func (s *ApprovalService) applyMerchantPolicyUpdate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload MerchantPolicyUpdatePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	merchantUUID, err := uuid.Parse(payload.MerchantID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id in payload")
	}

	// SELECT FOR UPDATE 读取当前版本
	var currentVersion int
	err = tx.QueryRow(ctx, `SELECT version FROM merchant_routing_policy WHERE merchant_id = $1 FOR UPDATE`, merchantUUID).Scan(&currentVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("merchant routing policy not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock merchant routing policy: " + err.Error())
	}
	if rec.TargetVersion != nil && currentVersion != *rec.TargetVersion {
		return nil, apperr.ErrVersionConflict.WithMessage(
			fmt.Sprintf("version conflict: approval target_version=%d, current=%d", *rec.TargetVersion, currentVersion))
	}

	// UPDATE policy
	var newVersion int
	err = tx.QueryRow(ctx,
		`UPDATE merchant_routing_policy
         SET provider_tier = $1, status = $2, version = version + 1, updated_at = now()
         WHERE merchant_id = $3 AND version = $4
         RETURNING version`,
		payload.ProviderTier, payload.Status, merchantUUID, currentVersion).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: policy version changed concurrently")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to update merchant routing policy: " + err.Error())
	}

	return &applyByActionOut{
		TargetUUID:  merchantUUID,
		NewVersion:  newVersion,
		EventType:   "merchant_routing_policy.updated",
		ReloadTopic: "config:merchant_routing_policy:reload",
	}, nil
}

// ── P0-4: 新增 apply 函数 ──

// applyEntityEnable 应用 corporate_entity 启用。
func (s *ApprovalService) applyEntityEnable(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload EntityStatusPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	entityUUID, err := uuid.Parse(payload.EntityID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid entity_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM corporate_entity WHERE id = $1 FOR UPDATE`, entityUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target corporate entity not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock corporate entity: " + err.Error())
	}
	if currentStatus == "ACTIVE" {
		return nil, apperr.ErrIllegalTransition.WithMessage("entity already ACTIVE")
	}

	cmd, err := tx.Exec(ctx,
		`UPDATE corporate_entity SET status = 'ACTIVE', updated_at = now() WHERE id = $1 AND status <> 'ACTIVE'`,
		entityUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to enable entity: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: entity status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  entityUUID,
		NewVersion:  0,
		EventType:   "corporate_entity.enabled",
		ReloadTopic: "config:corporate_entity:reload",
	}, nil
}

// applyEntityRiskHoldClear 清除 corporate_entity 风险冻结（恢复为 ACTIVE）。
func (s *ApprovalService) applyEntityRiskHoldClear(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload EntityStatusPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	entityUUID, err := uuid.Parse(payload.EntityID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid entity_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM corporate_entity WHERE id = $1 FOR UPDATE`, entityUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target corporate entity not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock corporate entity: " + err.Error())
	}
	if currentStatus != "RISK_HOLD" {
		return nil, apperr.ErrIllegalTransition.WithMessage("entity not in RISK_HOLD, cannot clear")
	}

	cmd, err := tx.Exec(ctx,
		`UPDATE corporate_entity SET status = 'ACTIVE', updated_at = now() WHERE id = $1 AND status = 'RISK_HOLD'`,
		entityUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to clear entity risk hold: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: entity status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  entityUUID,
		NewVersion:  0,
		EventType:   "corporate_entity.risk_hold_cleared",
		ReloadTopic: "config:corporate_entity:reload",
	}, nil
}

// applyBridgeProfileEnable 应用 bridge_profile 启用。
func (s *ApprovalService) applyBridgeProfileEnable(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload BridgeProfilePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	profileUUID, err := uuid.Parse(payload.ProfileID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid profile_id in payload")
	}

	var currentEnabled bool
	err = tx.QueryRow(ctx, `SELECT enabled FROM bridge_profile WHERE id = $1 FOR UPDATE`, profileUUID).Scan(&currentEnabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target bridge profile not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock bridge profile: " + err.Error())
	}
	if currentEnabled {
		return nil, apperr.ErrIllegalTransition.WithMessage("bridge profile already enabled")
	}

	cmd, err := tx.Exec(ctx,
		`UPDATE bridge_profile SET enabled = true, updated_at = now() WHERE id = $1 AND enabled = false`,
		profileUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to enable bridge profile: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: bridge profile changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  profileUUID,
		NewVersion:  0,
		EventType:   "bridge_profile.enabled",
		ReloadTopic: "config:bridge_profile:reload",
	}, nil
}

// applyWalletPolicyEnable 应用 transit_wallet_policy 启用。
func (s *ApprovalService) applyWalletPolicyEnable(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload WalletPolicyPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	policyUUID, err := uuid.Parse(payload.PolicyID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid policy_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM transit_wallet_policy WHERE id = $1 FOR UPDATE`, policyUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target wallet policy not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock wallet policy: " + err.Error())
	}
	if currentStatus == "ACTIVE" {
		return nil, apperr.ErrIllegalTransition.WithMessage("wallet policy already ACTIVE")
	}

	cmd, err := tx.Exec(ctx,
		`UPDATE transit_wallet_policy SET status = 'ACTIVE', updated_at = now() WHERE id = $1 AND status <> 'ACTIVE'`,
		policyUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to enable wallet policy: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: wallet policy status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  policyUUID,
		NewVersion:  0,
		EventType:   "wallet_policy.enabled",
		ReloadTopic: "config:wallet_policy:reload",
	}, nil
}

// applyAdapterProviderStatus 应用 adapter_provider 启用/禁用。
func (s *ApprovalService) applyAdapterProviderStatus(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload AdapterProviderPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	providerUUID, err := uuid.Parse(payload.ProviderID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid provider_id in payload")
	}

	targetStatus := "DISABLED"
	if rec.ActionType == ActionEnableAdapterProvider {
		targetStatus = "ACTIVE"
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM adapter_provider WHERE id = $1 FOR UPDATE`, providerUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target adapter provider not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock adapter provider: " + err.Error())
	}
	if currentStatus == targetStatus {
		return nil, apperr.ErrIllegalTransition.WithMessage("adapter provider already " + targetStatus)
	}

	cmd, err := tx.Exec(ctx,
		`UPDATE adapter_provider SET status = $1, updated_at = now() WHERE id = $2 AND status <> $1`,
		targetStatus, providerUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to update adapter provider status: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: adapter provider status changed concurrently")
	}

	eventType := "adapter_provider.disabled"
	if targetStatus == "ACTIVE" {
		eventType = "adapter_provider.enabled"
	}
	return &applyByActionOut{
		TargetUUID:  providerUUID,
		NewVersion:  0,
		EventType:   eventType,
		ReloadTopic: "config:adapter_provider:reload",
	}, nil
}

// applyAdapterProviderUpdate 应用 adapter_provider 配置更新。
func (s *ApprovalService) applyAdapterProviderUpdate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload AdapterProviderUpdatePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	providerUUID, err := uuid.Parse(payload.ProviderID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid provider_id in payload")
	}

	// SELECT FOR UPDATE 锁定行
	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM adapter_provider WHERE id = $1 FOR UPDATE`, providerUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target adapter provider not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock adapter provider: " + err.Error())
	}

	// 执行更新
	cmd, err := tx.Exec(ctx,
		`UPDATE adapter_provider SET provider_name = $1, provider_config = $2, updated_at = now() WHERE id = $3`,
		payload.ProviderName, payload.ProviderConfig, providerUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to update adapter provider: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: adapter provider update failed")
	}

	return &applyByActionOut{
		TargetUUID:  providerUUID,
		NewVersion:  0,
		EventType:   "adapter_provider.updated",
		ReloadTopic: "config:adapter_provider:reload",
	}, nil
}

// applyBankProviderStatus 应用 bank_provider 启用/禁用（P1-2 预留）。
func (s *ApprovalService) applyBankProviderStatus(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload BankProviderPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	providerUUID, err := uuid.Parse(payload.ProviderID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid provider_id in payload")
	}

	// P1-2: bank_provider 表尚未创建，预留 apply 逻辑
	return nil, apperr.ErrFailedPrecondition.WithMessage("bank_provider status change not yet implemented, provider_id=" + providerUUID.String())
}

// applyBankProviderCredentialRotate 应用 bank_provider 凭证轮换（P1-2 预留）。
func (s *ApprovalService) applyBankProviderCredentialRotate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload BankProviderCredentialPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	providerUUID, err := uuid.Parse(payload.ProviderID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid provider_id in payload")
	}

	// P1-2: bank_provider 表尚未创建，预留 apply 逻辑
	return nil, apperr.ErrFailedPrecondition.WithMessage("bank_provider credential rotation not yet implemented, provider_id=" + providerUUID.String())
}

// applyPaymentLinkRevoke 应用 payment_link 吊销。
func (s *ApprovalService) applyPaymentLinkRevoke(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload PaymentLinkPayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	linkUUID, err := uuid.Parse(payload.PaymentLinkID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid payment_link_id in payload")
	}

	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM payment_links WHERE id = $1 FOR UPDATE`, linkUUID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.ErrNotFound.WithMessage("target payment link not found")
		}
		return nil, apperr.ErrInternal.WithMessage("failed to lock payment link: " + err.Error())
	}
	if currentStatus == "REVOKED" {
		return nil, apperr.ErrIllegalTransition.WithMessage("payment link already REVOKED")
	}

	cmd, err := tx.Exec(ctx,
		`UPDATE payment_links SET status = 'REVOKED', updated_at = now() WHERE id = $1 AND status <> 'REVOKED'`,
		linkUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to revoke payment link: " + err.Error())
	}
	if cmd.RowsAffected() == 0 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: payment link status changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  linkUUID,
		NewVersion:  0,
		EventType:   "payment_link.revoked",
		ReloadTopic: "config:payment_link:reload",
	}, nil
}

// applyCorporateEntityUpdate 应用法人实体信息更新（ADM-002：原直接 UPDATE 收敛为审批）。
// corporate_entity 无 version 列，使用 FOR UPDATE 行锁 + RowsAffected 防并发。
func (s *ApprovalService) applyCorporateEntityUpdate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload CorporateEntityUpdatePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	entityUUID, err := uuid.Parse(payload.EntityID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid entity_id in payload")
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM corporate_entity WHERE id = $1 FOR UPDATE`, entityUUID); err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to lock corporate entity: " + err.Error())
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE corporate_entity
		SET name = $1, legal_name = $2, jurisdiction = $3, updated_at = now()
		WHERE id = $4`,
		payload.Name, payload.LegalName, payload.Jurisdiction, entityUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to update corporate entity: " + err.Error())
	}
	if cmd.RowsAffected() != 1 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: corporate entity changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  entityUUID,
		NewVersion:  0,
		EventType:   "corporate_entity.updated",
		ReloadTopic: "config:corporate_entity:reload",
	}, nil
}

// applyBridgeProfileUpdate 应用桥接配置更新（ADM-002）。bridge_profile 无 version 列。
func (s *ApprovalService) applyBridgeProfileUpdate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload BridgeProfileUpdatePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	profileUUID, err := uuid.Parse(payload.ProfileID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid profile_id in payload")
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM bridge_profile WHERE id = $1 FOR UPDATE`, profileUUID); err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to lock bridge profile: " + err.Error())
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE bridge_profile
		SET name = $1, buffer_percent = $2, max_slippage_bps = $3,
		    batching_policy = $4, execution_policy = $5, withdrawal_policy = $6, updated_at = now()
		WHERE id = $7`,
		payload.Name, payload.BufferPercent, payload.MaxSlippageBps,
		payload.BatchingPolicy, payload.ExecutionPolicy, payload.WithdrawalPolicy, profileUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to update bridge profile: " + err.Error())
	}
	if cmd.RowsAffected() != 1 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: bridge profile changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  profileUUID,
		NewVersion:  0,
		EventType:   "bridge_profile.updated",
		ReloadTopic: "config:bridge_profile:reload",
	}, nil
}

// applyWalletPolicyUpdate 应用钱包策略限额更新（ADM-002）。transit_wallet_policy 无 version 列。
func (s *ApprovalService) applyWalletPolicyUpdate(ctx context.Context, tx pgx.Tx, rec *ApprovalRecord) (*applyByActionOut, error) {
	var payload WalletPolicyUpdatePayload
	if err := decodePayload(rec.ValidatedPayload, &payload); err != nil {
		return nil, err
	}
	policyUUID, err := uuid.Parse(payload.PolicyID)
	if err != nil {
		return nil, apperr.ErrInvalidArgument.WithMessage("invalid policy_id in payload")
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM transit_wallet_policy WHERE id = $1 FOR UPDATE`, policyUUID); err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to lock wallet policy: " + err.Error())
	}

	cmd, err := tx.Exec(ctx, `
		UPDATE transit_wallet_policy
		SET max_single_withdrawal = $1, daily_limit = $2, updated_at = now()
		WHERE id = $3`,
		payload.MaxSingleWithdrawal, payload.DailyLimit, policyUUID)
	if err != nil {
		return nil, apperr.ErrInternal.WithMessage("failed to update wallet policy: " + err.Error())
	}
	if cmd.RowsAffected() != 1 {
		return nil, apperr.ErrVersionConflict.WithMessage("zero affected rows: wallet policy changed concurrently")
	}

	return &applyByActionOut{
		TargetUUID:  policyUUID,
		NewVersion:  0,
		EventType:   "wallet_policy.updated",
		ReloadTopic: "config:wallet_policy:reload",
	}, nil
}

// ── 辅助函数 ──

// validateTypedPayload 按 action_type 验证 typed payload。
// 禁止 map[string]any 直接执行任意 SQL/字段：每个 action 有唯一的 typed payload 结构。
func validateTypedPayload(actionType string, payload map[string]interface{}) error {
	if payload == nil {
		return apperr.ErrInvalidArgument.WithMessage("validated_payload is required")
	}
	switch actionType {
	case ActionDisableAcquirerChannel, ActionEnableAcquirerChannel:
		var p ChannelStatusPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ChannelID == "" {
			return apperr.ErrInvalidArgument.WithMessage("channel_id is required in payload")
		}
		if _, err := uuid.Parse(p.ChannelID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("channel_id must be a valid UUID")
		}
	case ActionUpdateAcquirerChannel:
		var p ChannelUpdatePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ChannelID == "" {
			return apperr.ErrInvalidArgument.WithMessage("channel_id is required in payload")
		}
		if _, err := uuid.Parse(p.ChannelID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("channel_id must be a valid UUID")
		}
	case ActionDisableIssuingPool, ActionEnableIssuingPool:
		var p PoolStatusPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.PoolID == "" {
			return apperr.ErrInvalidArgument.WithMessage("pool_id is required in payload")
		}
		if _, err := uuid.Parse(p.PoolID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("pool_id must be a valid UUID")
		}
	case ActionUpdateIssuingPool:
		var p PoolUpdatePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.PoolID == "" {
			return apperr.ErrInvalidArgument.WithMessage("pool_id is required in payload")
		}
		if _, err := uuid.Parse(p.PoolID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("pool_id must be a valid UUID")
		}
	case ActionUpdateMerchantReserve:
		var p MerchantReservePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.MerchantID == "" {
			return apperr.ErrInvalidArgument.WithMessage("merchant_id is required in payload")
		}
		if _, err := uuid.Parse(p.MerchantID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("merchant_id must be a valid UUID")
		}
		if p.ReserveRatioBps < 0 || p.ReserveRatioBps > 10000 {
			return apperr.ErrInvalidArgument.WithMessage("reserve_ratio_bps must be in [0, 10000]")
		}
	case ActionUpdateCorporateEntity:
		var p CorporateEntityUpdatePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.EntityID == "" {
			return apperr.ErrInvalidArgument.WithMessage("entity_id is required in payload")
		}
		if _, err := uuid.Parse(p.EntityID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("entity_id must be a valid UUID")
		}
		if p.Name == "" || p.LegalName == "" || p.Jurisdiction == "" {
			return apperr.ErrInvalidArgument.WithMessage("name, legal_name and jurisdiction are required in payload")
		}
	case ActionUpdateBridgeProfile:
		var p BridgeProfileUpdatePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ProfileID == "" {
			return apperr.ErrInvalidArgument.WithMessage("profile_id is required in payload")
		}
		if _, err := uuid.Parse(p.ProfileID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("profile_id must be a valid UUID")
		}
		if p.Name == "" {
			return apperr.ErrInvalidArgument.WithMessage("name is required in payload")
		}
		if p.BufferPercent < 0 || p.MaxSlippageBps < 0 {
			return apperr.ErrInvalidArgument.WithMessage("buffer_percent and max_slippage_bps must be non-negative")
		}
	case ActionUpdateWalletPolicy:
		var p WalletPolicyUpdatePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.PolicyID == "" {
			return apperr.ErrInvalidArgument.WithMessage("policy_id is required in payload")
		}
		if _, err := uuid.Parse(p.PolicyID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("policy_id must be a valid UUID")
		}
		if p.MaxSingleWithdrawal < 0 || p.DailyLimit < 0 {
			return apperr.ErrInvalidArgument.WithMessage("limits must be non-negative")
		}
	case ActionDisableWalletPolicy:
		var p WalletPolicyPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.PolicyID == "" {
			return apperr.ErrInvalidArgument.WithMessage("policy_id is required in payload")
		}
		if _, err := uuid.Parse(p.PolicyID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("policy_id must be a valid UUID")
		}
	case ActionDisableVAAccount:
		var p VAAccountPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.VAID == "" {
			return apperr.ErrInvalidArgument.WithMessage("va_id is required in payload")
		}
		if _, err := uuid.Parse(p.VAID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("va_id must be a valid UUID")
		}
	case ActionUpdateChannelRisk:
		var p ChannelRiskPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ChannelID == "" {
			return apperr.ErrInvalidArgument.WithMessage("channel_id is required in payload")
		}
		if _, err := uuid.Parse(p.ChannelID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("channel_id must be a valid UUID")
		}
		if p.BinDeclineBps < 0 || p.BinDeclineBps > 10000 {
			return apperr.ErrInvalidArgument.WithMessage("bin_decline_bps must be in [0, 10000]")
		}
	case ActionSystemLockdownRecovery:
		// 无需额外字段
	case ActionDisableAdapterChannel, ActionEnableAdapterChannel:
		var p AdapterChannelStatusPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ChannelID == "" {
			return apperr.ErrInvalidArgument.WithMessage("channel_id is required in payload")
		}
		if _, err := uuid.Parse(p.ChannelID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("channel_id must be a valid UUID")
		}
	case ActionDisableBridgeProfile:
		var p BridgeProfilePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ProfileID == "" {
			return apperr.ErrInvalidArgument.WithMessage("profile_id is required in payload")
		}
		if _, err := uuid.Parse(p.ProfileID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("profile_id must be a valid UUID")
		}
	case ActionSetEntityRiskHold:
		var p EntityRiskHoldPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.EntityID == "" {
			return apperr.ErrInvalidArgument.WithMessage("entity_id is required in payload")
		}
		if _, err := uuid.Parse(p.EntityID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("entity_id must be a valid UUID")
		}
	case ActionFreezeIsolationGroup, ActionUnfreezeIsolationGroup:
		var p IsolationGroupPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.GroupID == "" {
			return apperr.ErrInvalidArgument.WithMessage("group_id is required in payload")
		}
		if _, err := uuid.Parse(p.GroupID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("group_id must be a valid UUID")
		}
	case ActionReleaseVAAccount:
		var p VAReleasePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.VAID == "" {
			return apperr.ErrInvalidArgument.WithMessage("va_id is required in payload")
		}
		if _, err := uuid.Parse(p.VAID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("va_id must be a valid UUID")
		}
	case ActionCreateMerchantPolicy:
		var p MerchantPolicyCreatePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.MerchantID == "" {
			return apperr.ErrInvalidArgument.WithMessage("merchant_id is required in payload")
		}
		if _, err := uuid.Parse(p.MerchantID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("merchant_id must be a valid UUID")
		}
		if p.ProviderTier != "TIER_1_HIGH_RISK" && p.ProviderTier != "TIER_2_COMPLIANT" {
			return apperr.ErrInvalidArgument.WithMessage("provider_tier must be TIER_1_HIGH_RISK or TIER_2_COMPLIANT")
		}
		if p.Status != "ACTIVE" && p.Status != "INACTIVE" {
			return apperr.ErrInvalidArgument.WithMessage("status must be ACTIVE or INACTIVE")
		}
	case ActionUpdateMerchantPolicy:
		var p MerchantPolicyUpdatePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.MerchantID == "" {
			return apperr.ErrInvalidArgument.WithMessage("merchant_id is required in payload")
		}
		if _, err := uuid.Parse(p.MerchantID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("merchant_id must be a valid UUID")
		}
		if p.ProviderTier != "TIER_1_HIGH_RISK" && p.ProviderTier != "TIER_2_COMPLIANT" {
			return apperr.ErrInvalidArgument.WithMessage("provider_tier must be TIER_1_HIGH_RISK or TIER_2_COMPLIANT")
		}
		if p.Status != "ACTIVE" && p.Status != "INACTIVE" {
			return apperr.ErrInvalidArgument.WithMessage("status must be ACTIVE or INACTIVE")
		}
	// P0-4: 新增 payload 验证
	case ActionEnableEntity, ActionClearEntityRiskHold:
		var p EntityStatusPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.EntityID == "" {
			return apperr.ErrInvalidArgument.WithMessage("entity_id is required in payload")
		}
		if _, err := uuid.Parse(p.EntityID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("entity_id must be a valid UUID")
		}
	case ActionEnableBridgeProfile:
		var p BridgeProfilePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ProfileID == "" {
			return apperr.ErrInvalidArgument.WithMessage("profile_id is required in payload")
		}
		if _, err := uuid.Parse(p.ProfileID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("profile_id must be a valid UUID")
		}
	case ActionEnableWalletPolicy:
		var p WalletPolicyPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.PolicyID == "" {
			return apperr.ErrInvalidArgument.WithMessage("policy_id is required in payload")
		}
		if _, err := uuid.Parse(p.PolicyID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("policy_id must be a valid UUID")
		}
	case ActionEnableAdapterProvider, ActionDisableAdapterProvider:
		var p AdapterProviderPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ProviderID == "" {
			return apperr.ErrInvalidArgument.WithMessage("provider_id is required in payload")
		}
		if _, err := uuid.Parse(p.ProviderID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("provider_id must be a valid UUID")
		}
	case ActionUpdateAdapterProvider:
		var p AdapterProviderUpdatePayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ProviderID == "" {
			return apperr.ErrInvalidArgument.WithMessage("provider_id is required in payload")
		}
		if _, err := uuid.Parse(p.ProviderID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("provider_id must be a valid UUID")
		}
		if p.ProviderName == "" && len(p.ProviderConfig) == 0 {
			return apperr.ErrInvalidArgument.WithMessage("at least one of provider_name or provider_config is required")
		}
	case ActionEnableBankProvider, ActionDisableBankProvider:
		var p BankProviderPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ProviderID == "" {
			return apperr.ErrInvalidArgument.WithMessage("provider_id is required in payload")
		}
		if _, err := uuid.Parse(p.ProviderID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("provider_id must be a valid UUID")
		}
	case ActionRotateBankProviderCred:
		var p BankProviderCredentialPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.ProviderID == "" {
			return apperr.ErrInvalidArgument.WithMessage("provider_id is required in payload")
		}
		if _, err := uuid.Parse(p.ProviderID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("provider_id must be a valid UUID")
		}
		if p.CredentialVaultKey == "" {
			return apperr.ErrInvalidArgument.WithMessage("credential_vault_key is required in payload")
		}
		if !strings.HasPrefix(p.CredentialVaultKey, "vault://") {
			return apperr.ErrInvalidArgument.WithMessage("credential_vault_key must start with vault://")
		}
	case ActionRevokePaymentLink:
		var p PaymentLinkPayload
		if err := decodePayload(payload, &p); err != nil {
			return err
		}
		if p.PaymentLinkID == "" {
			return apperr.ErrInvalidArgument.WithMessage("payment_link_id is required in payload")
		}
		if _, err := uuid.Parse(p.PaymentLinkID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("payment_link_id must be a valid UUID")
		}
		if p.Reason == "" {
			return apperr.ErrInvalidArgument.WithMessage("reason is required for payment link revocation")
		}
	default:
		return apperr.ErrInvalidArgument.WithMessage("unsupported action_type: " + actionType)
	}
	return nil
}

// captureBeforeSnapshot 获取变更前快照（不含凭证/token）。
func (s *ApprovalService) captureBeforeSnapshot(ctx context.Context, actionType, targetType string, targetID *string) (map[string]interface{}, error) {
	if targetID == nil || *targetID == "" {
		return map[string]interface{}{}, nil
	}
	targetUUID, err := uuid.Parse(*targetID)
	if err != nil {
		return map[string]interface{}{}, nil
	}

	switch targetType {
	case TargetTypeAcquirerChannel:
		var status, tier, strategy, providerCode, channelCode string
		var version int
		var providerConfig, routingConfig string
		err = s.pool.QueryRow(ctx, `
			SELECT status, provider_tier, strategy_mode, provider_code, channel_code,
			       version, provider_config::text, routing_config::text
			FROM acquirer_channels WHERE id = $1`, targetUUID).Scan(
			&status, &tier, &strategy, &providerCode, &channelCode,
			&version, &providerConfig, &routingConfig)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{
			"status":          status,
			"provider_tier":   tier,
			"strategy_mode":   strategy,
			"provider_code":   providerCode,
			"channel_code":    channelCode,
			"version":         version,
			"provider_config": sanitizeJSONText(providerConfig),
			"routing_config":  sanitizeJSONText(routingConfig),
		}, nil
	case TargetTypeIssuingPool:
		var status, tier, funding, providerCode, poolCode string
		var version int
		var providerConfig, velocityConfig string
		err = s.pool.QueryRow(ctx, `
			SELECT status, issuing_tier, funding_mode, provider_code, pool_code,
			       version, provider_config::text, velocity_config::text
			FROM issuing_providers WHERE id = $1`, targetUUID).Scan(
			&status, &tier, &funding, &providerCode, &poolCode,
			&version, &providerConfig, &velocityConfig)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{
			"status":          status,
			"issuing_tier":    tier,
			"funding_mode":    funding,
			"provider_code":   providerCode,
			"pool_code":       poolCode,
			"version":         version,
			"provider_config": sanitizeJSONText(providerConfig),
			"velocity_config": sanitizeJSONText(velocityConfig),
		}, nil
	case TargetTypeAdapterChannel:
		var status, channelCode, channelName string
		err = s.pool.QueryRow(ctx, `SELECT status, channel_code, channel_name FROM adapter_channel WHERE id = $1`, targetUUID).Scan(&status, &channelCode, &channelName)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{"status": status, "channel_code": channelCode, "channel_name": channelName}, nil
	case TargetTypeBridgeProfile:
		var name string
		var enabled bool
		err = s.pool.QueryRow(ctx, `SELECT name, enabled FROM bridge_profile WHERE id = $1`, targetUUID).Scan(&name, &enabled)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{"name": name, "enabled": enabled}, nil
	case TargetTypeCorporateEntity:
		var name, status string
		err = s.pool.QueryRow(ctx, `SELECT name, status FROM corporate_entity WHERE id = $1`, targetUUID).Scan(&name, &status)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{"name": name, "status": status}, nil
	case TargetTypeIsolationGroup:
		var name, status string
		err = s.pool.QueryRow(ctx, `SELECT name, status FROM pipeline_isolation_groups WHERE id = $1`, targetUUID).Scan(&name, &status)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{"name": name, "status": status}, nil
	case TargetTypeMerchantPolicy:
		var providerTier, status string
		var version int
		err = s.pool.QueryRow(ctx, `SELECT provider_tier, status, version FROM merchant_routing_policy WHERE merchant_id = $1`, targetUUID).Scan(&providerTier, &status, &version)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{"provider_tier": providerTier, "status": status, "version": version}, nil
	// P0-4: 新增 before snapshot 捕获
	case TargetTypeAdapterProvider:
		var providerCode, providerName, adapterType, status string
		var providerConfig string
		err = s.pool.QueryRow(ctx, `SELECT provider_code, provider_name, adapter_type, status, provider_config::text FROM adapter_provider WHERE id = $1`, targetUUID).Scan(&providerCode, &providerName, &adapterType, &status, &providerConfig)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{
			"provider_code":   providerCode,
			"provider_name":   providerName,
			"adapter_type":    adapterType,
			"status":          status,
			"provider_config": sanitizeJSONText(providerConfig),
		}, nil
	case TargetTypeBankProvider:
		// P1-2: bank_provider 表尚未创建，预留 snapshot
		return map[string]interface{}{}, nil
	case TargetTypePaymentLink:
		var status string
		var merchantID string
		err = s.pool.QueryRow(ctx, `SELECT status, merchant_id::text FROM payment_links WHERE id = $1`, targetUUID).Scan(&status, &merchantID)
		if err != nil {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{"status": status, "merchant_id": merchantID}, nil
	default:
		return map[string]interface{}{}, nil
	}
}

// decodePayload 将 map[string]interface{} 解码到 typed struct。
func decodePayload(payload map[string]interface{}, target interface{}) error {
	if payload == nil {
		return apperr.ErrInvalidArgument.WithMessage("payload is nil")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return apperr.ErrInternal.WithMessage("failed to marshal payload: " + err.Error())
	}
	if err := json.Unmarshal(data, target); err != nil {
		return apperr.ErrInvalidArgument.WithMessage("failed to decode payload: " + err.Error())
	}
	return nil
}

// isDuplicateKeyError 判断是否为唯一键冲突错误（RED-10）。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	// pgx 唯一键冲突错误码是 23505
	msg := err.Error()
	return contains(msg, "23505") || contains(msg, "duplicate key value violates unique constraint")
}

// buildRuntimeChannelConfig 从 merged 配置构造 runtime channel_config。
// 只写非敏感执行配置；严禁复制 credential、token、secret、api_key、private_key。
// server-side 覆盖 burn_mode：
//
//	BURN_MODE → burn_mode=true，runtime is_active=false（无论 control status）
//	DEFAULT   → burn_mode=false，runtime is_active 只能等于 controlStatus == 'ACTIVE'
func buildRuntimeChannelConfig(merged *configvalidator.AcquirerChannelConfig, controlStatus string) (config map[string]any, isActive bool) {
	config = map[string]any{}
	if merged.ProviderConfig != nil {
		for k, v := range merged.ProviderConfig {
			lowerKey := strings.ToLower(k)
			if strings.Contains(lowerKey, "credential") || strings.Contains(lowerKey, "token") ||
				strings.Contains(lowerKey, "secret") || strings.Contains(lowerKey, "api_key") ||
				strings.Contains(lowerKey, "private_key") {
				continue
			}
			config[k] = v
		}
	}
	if merged.StrategyMode == "BURN_MODE" {
		config["burn_mode"] = true
		return config, false
	}
	config["burn_mode"] = false
	return config, controlStatus == "ACTIVE"
}

// ApprovalOutboxEventTypes 列出审批服务可能写入 transactional_outbox 的全部
// event_type（ADM-009 source of truth）。Worker 侧 registry 必须认识其中每一个，
// 双向集合测试见 internal/worker/outbox/registry_adm009_test.go。
func ApprovalOutboxEventTypes() []string {
	return []string{
		"acquirer_channel.config_updated",
		"acquirer_channel.status_changed",
		"adapter_channel.disabled",
		"adapter_channel.enabled",
		"adapter_provider.disabled",
		"adapter_provider.enabled",
		"adapter_provider.updated",
		"bridge_profile.disabled",
		"bridge_profile.enabled",
		"channel_risk.updated",
		"corporate_entity.enabled",
		"corporate_entity.risk_hold",
		"corporate_entity.risk_hold_cleared",
		"isolation_group.frozen",
		"isolation_group.unfrozen",
		"issuing_pool.config_updated",
		"issuing_pool.status_changed",
		"merchant_reserve.updated",
		"merchant_routing_policy.created",
		"merchant_routing_policy.updated",
		"payment_link.revoked",
		"corporate_entity.updated",
		"bridge_profile.updated",
		"wallet_policy.updated",
		"system.recovery_executed",
		"va_account.disabled",
		"va_account.released",
		"wallet_policy.disabled",
		"wallet_policy.enabled",
	}
}
