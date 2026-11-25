package issuing

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type IssuingTier string

const (
	IssuingTierAggregator IssuingTier = "TIER_1_AGGREGATOR"
	IssuingTierDirect     IssuingTier = "TIER_2_DIRECT"
)

func (t IssuingTier) Validate() error {
	switch t {
	case IssuingTierAggregator, IssuingTierDirect:
		return nil
	default:
		return ErrIssuingTierUnknown
	}
}

type IssuingFundingMode string

const (
	FundingModeCryptoDesensitized IssuingFundingMode = "CRYPTO_DESENSITIZED"
	FundingModeFiatDirect         IssuingFundingMode = "FIAT_DIRECT"
)

func (m IssuingFundingMode) Validate() error {
	switch m {
	case FundingModeCryptoDesensitized, FundingModeFiatDirect:
		return nil
	default:
		return ErrFundingModeUnknown
	}
}

type VirtualCardStatus string

const (
	CardStatusPending              VirtualCardStatus = "PENDING"
	CardStatusActive               VirtualCardStatus = "ACTIVE"
	CardStatusFrozen               VirtualCardStatus = "FROZEN"
	CardStatusTerminated           VirtualCardStatus = "TERMINATED"
	CardStatusUnknownExternalState VirtualCardStatus = "UNKNOWN_EXTERNAL_STATE"
	CardStatusManualReview         VirtualCardStatus = "MANUAL_REVIEW"
)

func (s VirtualCardStatus) Validate() error {
	switch s {
	case CardStatusPending, CardStatusActive, CardStatusFrozen,
		CardStatusTerminated, CardStatusUnknownExternalState, CardStatusManualReview:
		return nil
	default:
		return ErrCardStatusUnknown
	}
}

// AdapterType 表示 issuing provider 的适配器类型（按能力注册 Factory）。
type AdapterType string

const (
	// AdapterTypeNexusIssuingHTTPV1 是 NexusGlobal Issuing HTTP v1 协议适配器。
	// production/real 模式下只有此类型注册 HTTP Adapter。
	AdapterTypeNexusIssuingHTTPV1 AdapterType = "NEXUS_ISSUING_HTTP_V1"

	// AdapterTypeMockLocal 是本地 mock 适配器（仅用于测试/local 模式）。
	// production 模式禁止使用。
	AdapterTypeMockLocal AdapterType = "MOCK_LOCAL"

	// AdapterTypeUnsupported 表示不支持的适配器类型。
	// production 模式拒绝启动。
	AdapterTypeUnsupported AdapterType = "UNSUPPORTED"
)

// Validate 校验 AdapterType 是否合法。
func (a AdapterType) Validate() error {
	switch a {
	case AdapterTypeNexusIssuingHTTPV1, AdapterTypeMockLocal, AdapterTypeUnsupported:
		return nil
	default:
		return ErrAdapterTypeUnknown
	}
}

// IsProductionAllowed 返回此 AdapterType 是否允许在 production 模式使用。
func (a AdapterType) IsProductionAllowed() bool {
	return a == AdapterTypeNexusIssuingHTTPV1
}

// CredentialType 表示凭证类型（与 adapter_credential_vault.credential_type 对应）。
type CredentialType string

const (
	CredentialTypeAPIKey         CredentialType = "API_KEY"
	CredentialTypeAPISecret      CredentialType = "API_SECRET"
	CredentialTypeWebhookSecret  CredentialType = "WEBHOOK_SECRET"
	CredentialTypeClientCert     CredentialType = "CLIENT_CERT"
	CredentialTypeSigningKeyRef  CredentialType = "SIGNING_KEY_REF"
	CredentialTypeRPCToken       CredentialType = "RPC_TOKEN"
	CredentialTypeWithdrawKeyRef CredentialType = "WITHDRAW_KEY_REF"
)

// normalizeCredentialType 将路径字段名规范化为标准 CredentialType。
// F-08: 统一 api_key <-> API_KEY 映射，不做 raw string 比较。
func normalizeCredentialType(field string) CredentialType {
	switch strings.ToUpper(field) {
	case "API_KEY":
		return CredentialTypeAPIKey
	case "API_SECRET":
		return CredentialTypeAPISecret
	case "WEBHOOK_SECRET":
		return CredentialTypeWebhookSecret
	case "CLIENT_CERT":
		return CredentialTypeClientCert
	case "SIGNING_KEY_REF":
		return CredentialTypeSigningKeyRef
	case "RPC_TOKEN":
		return CredentialTypeRPCToken
	case "WITHDRAW_KEY_REF":
		return CredentialTypeWithdrawKeyRef
	default:
		return CredentialType(field)
	}
}

// String 返回 CredentialType 的字符串表示。
func (c CredentialType) String() string { return string(c) }

type IssuingProvider struct {
	ID                 uuid.UUID
	EntityID           *uuid.UUID
	TenantID           *uuid.UUID // F-07: 权威租户（通过 entity_id -> corporate_entity.tenant_id 解析）
	IssuingTier        IssuingTier
	FundingMode        IssuingFundingMode
	ProviderCode       string
	PoolCode           string
	CredentialVaultKey string
	AdapterProviderID  *uuid.UUID  // F-01: UUID 外键引用 adapter_provider(id)
	AdapterType        AdapterType // 000050: 按能力注册 Factory
	Status             string
	Version            int
	ProviderConfig     map[string]any
	VelocityConfig     map[string]any
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// VaultCredentialRequest 是 Vault 凭证解密请求。
// F-04/F-05/F-06/F-07: 包含权威归属信息，Decrypt 必须严格校验全部字段一致。
type VaultCredentialRequest struct {
	VaultKey                  string         // vault://tenant_id/record_id/field_name/key_version
	ExpectedIssuingProviderID uuid.UUID      // F-05: 发卡 Provider ID（issuing_providers.id），必须强制校验
	ExpectedAdapterProviderID uuid.UUID      // F-01: 适配器 Provider ID（adapter_provider.id），UUID 强类型
	ExpectedTenantID          uuid.UUID      // F-04: 权威租户 ID，UUID 强类型
	ExpectedCredentialType    CredentialType // F-06/F-08: 期望凭证类型（强类型枚举）
}

type VirtualCard struct {
	ID                         uuid.UUID
	MerchantID                 uuid.UUID
	ProviderID                 uuid.UUID
	ProviderCardRef            string
	Last4                      string
	Status                     VirtualCardStatus
	Currency                   string
	SpendingLimitMinor         int64
	DeclinedCountSlidingWindow int
	DeclinedWindowStartedAt    *time.Time
	FraudRateBps               int
	LastDeclineReason          string
	IdempotencyKey             string
	PayloadHash                string
	CreatedBy                  string
	FrozenReason               string
	TerminatedReason           string
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type CardAuthEvent struct {
	EventID           string
	CardID            uuid.UUID
	ProviderRef       string
	MerchantID        uuid.UUID
	Amount            decimal.Decimal
	Currency          string
	Status            string
	IsDeclined        bool
	DeclineReason     string
	AuthorizationCode string
	Timestamp         time.Time
}

type ProviderStatus struct {
	ProviderID    uuid.UUID
	IsActive      bool
	FundingMode   IssuingFundingMode
	Version       int
	LastUpdatedAt time.Time
}

var (
	ErrProviderNotFound                  = errors.New("issuing: provider not found")
	ErrProviderNotActive                 = errors.New("issuing: provider not active")
	ErrProviderCodeUnregistered          = errors.New("issuing: provider_code not registered in factory")
	ErrCredentialVaultKeyEmpty           = errors.New("issuing: credential_vault_key is empty")
	ErrCredentialDecryptFailed           = errors.New("issuing: vault decrypt failed")
	ErrCredentialCrossTenant             = errors.New("issuing: credential cross-tenant access denied")
	ErrCredentialAdapterProviderMismatch = errors.New("issuing: credential adapter_provider_id mismatch")
	ErrCredentialIssuingProviderMismatch = errors.New("issuing: credential issuing_provider_id mismatch")
	ErrCredentialFieldTypeMismatch       = errors.New("issuing: credential field_type mismatch")
	ErrCredentialAADMismatch             = errors.New("issuing: credential AAD context mismatch")
	ErrCredentialMissingTenant           = errors.New("issuing: credential missing tenant ownership")
	ErrCredentialRevokedOrExpired        = errors.New("issuing: credential revoked or expired")
	ErrAdapterTypeUnknown                = errors.New("issuing: adapter_type unknown")
	ErrAdapterTypeUnsupported            = errors.New("issuing: adapter_type unsupported in production")
	ErrSyncPayloadMismatch               = errors.New("issuing: sync payload mismatch (event_id reused with different payload)")
	ErrSyncLeaseLost                     = errors.New("issuing: sync lease lost (fencing token mismatch)")
	ErrIssuingTierUnknown                = errors.New("issuing: issuing_tier unknown")
	ErrFundingModeUnknown                = errors.New("issuing: funding_mode unknown")
	ErrFundingModeMismatch               = errors.New("issuing: funding_mode mismatch with source")
	ErrMerchantFraudRateExceeded         = errors.New("issuing: merchant fraud rate exceeded threshold")
	ErrCardDeclinedSlidingWindowExceeded = errors.New("issuing: card declined sliding window exceeded")
	ErrWebhookSignatureInvalid           = errors.New("issuing: webhook signature invalid")
	ErrProviderTimeout                   = errors.New("issuing: provider timeout")
	ErrUnknownExternalState              = errors.New("issuing: unknown external state")
	ErrCardNotFound                      = errors.New("issuing: card not found")
	ErrCardStatusUnknown                 = errors.New("issuing: card status unknown")
	ErrMerchantIssuingNotAllowed         = errors.New("issuing: merchant not allowed to issue cards")
	ErrDuplicateCard                     = errors.New("issuing: duplicate card (idempotency)")
	// R20 P1-2: HTTP Provider 错误类型
	ErrProviderRetryable = errors.New("issuing: provider retryable error")
	ErrProviderPermanent = errors.New("issuing: provider permanent error")
)

func IsTimeout(err error) bool {
	return errors.Is(err, ErrProviderTimeout)
}

func IsUnknownState(err error) bool {
	return errors.Is(err, ErrUnknownExternalState)
}

// RuntimeMode 表示运行时模式，用于区分 production/real 与 local/mock。
// R19 P0-2: 替代含义模糊的 bool isProduction 参数。
// 定义在 issuing 包中以避免循环依赖（configvalidator 包导入 issuing 包）。
type RuntimeMode int

const (
	// RuntimeModeProduction 表示 production/real 模式。
	// 禁止 MOCK_LOCAL、UNSUPPORTED，要求完整配置。
	RuntimeModeProduction RuntimeMode = iota
	// RuntimeModeLocalMock 表示 local/mock 模式。
	// 允许 MOCK_LOCAL，不要求真实 adapter provider 绑定。
	RuntimeModeLocalMock
)

// String 返回 RuntimeMode 的字符串表示。
func (m RuntimeMode) String() string {
	switch m {
	case RuntimeModeProduction:
		return "production"
	case RuntimeModeLocalMock:
		return "local-mock"
	default:
		return "unknown"
	}
}

// IsProduction 返回是否为 production 模式。
func (m RuntimeMode) IsProduction() bool {
	return m == RuntimeModeProduction
}

// IsLocalMock 返回是否为 local-mock 模式。
// R24 P0-2: 显式判断，不得用 !IsProduction() 作为放行条件。
func (m RuntimeMode) IsLocalMock() bool {
	return m == RuntimeModeLocalMock
}

// Validate 校验 RuntimeMode 是否为已知合法值。
// R24 P0-2: 未知值必须 fail-closed，拒绝除 Production/LocalMock 之外的任何值。
func (m RuntimeMode) Validate() error {
	switch m {
	case RuntimeModeProduction, RuntimeModeLocalMock:
		return nil
	default:
		return fmt.Errorf("issuing: unknown RuntimeMode value %d (allowed: Production=%d, LocalMock=%d)",
			m, RuntimeModeProduction, RuntimeModeLocalMock)
	}
}

// ResolveRuntimeMode 是 pure resolver，根据 APP_ENV 和 EXTERNAL_MODE 解析 RuntimeMode。
// R21 P0-1: 统一所有运行模式判断，禁止默认落入 LocalMock。
//
// 规则：
//   - EXTERNAL_MODE 只允许显式 "real" 或 "mock"；空值和未知值返回 error
//   - APP_ENV=production 或 EXTERNAL_MODE=real → RuntimeModeProduction
//   - APP_ENV!=production 且 EXTERNAL_MODE=mock → RuntimeModeLocalMock
//   - local+real 必须 Production（fail-closed）
func ResolveRuntimeMode(appEnv, externalMode string) (RuntimeMode, error) {
	// 校验 APP_ENV
	switch appEnv {
	case "production", "real", "local", "mock", "staging", "development", "dev", "test":
		// 合法值
	default:
		return 0, fmt.Errorf("issuing: unknown APP_ENV value %q (allowed: production, real, local, mock, staging, development, dev, test)", appEnv)
	}

	// R21 P0-1: EXTERNAL_MODE 只允许显式 "real" 或 "mock"；空值和未知值返回 error
	switch externalMode {
	case "real", "mock":
		// 合法值
	default:
		return 0, fmt.Errorf("issuing: EXTERNAL_MODE must be explicitly \"real\" or \"mock\", got %q", externalMode)
	}

	// production 或 real → RuntimeModeProduction
	if appEnv == "production" || appEnv == "real" || externalMode == "real" {
		return RuntimeModeProduction, nil
	}

	// R21 P0-1: 只有 APP_ENV!=production 且 EXTERNAL_MODE=mock 才返回 LocalMock
	if externalMode == "mock" {
		return RuntimeModeLocalMock, nil
	}

	// 不应到达此处（所有合法值已覆盖），但 fail-closed
	return 0, fmt.Errorf("issuing: cannot resolve RuntimeMode from APP_ENV=%q EXTERNAL_MODE=%q", appEnv, externalMode)
}

// ActivationConfig 是激活校验的配置视图。
// R19 P0-2: BuildWorkerIssuingRuntime 和 configvalidator.ValidateIssuingRuntimeConfig
// 共用此结构体，确保单一校验路径。
type ActivationConfig struct {
	EntityID           string
	IssuingTier        string
	FundingMode        string
	ProviderCode       string
	PoolCode           string
	CredentialVaultKey string
	ProviderConfig     map[string]interface{}
	VelocityConfig     map[string]interface{}
	AdapterType        string
	AdapterProviderID  string
}

// VaultPath 是解析后的 Vault 引用路径。
// R20 P0-3: 严格解析 vault://tenant_uuid/record_uuid/field_name/key_version
type VaultPath struct {
	TenantID   uuid.UUID
	RecordID   uuid.UUID
	FieldName  CredentialType
	KeyVersion int
}

// ParseVaultPath 严格解析 vault:// 引用路径。
// R21 P0-2: 必须恰好四段；tenant/record 必须 UUID；version 必须为正整数；field 只允许 API_KEY。
// 格式错误不得跳过查询，必须返回错误。
func ParseVaultPath(vaultKey string) (*VaultPath, error) {
	if !strings.HasPrefix(vaultKey, "vault://") {
		return nil, fmt.Errorf("issuing: vault key must start with vault://")
	}
	body := strings.TrimPrefix(vaultKey, "vault://")
	parts := strings.SplitN(body, "/", 4)
	if len(parts) != 4 {
		return nil, fmt.Errorf("issuing: vault path must have exactly 4 segments (tenant_uuid/record_uuid/field_name/key_version), got %d", len(parts))
	}

	tenantID, err := uuid.Parse(parts[0])
	if err != nil {
		return nil, fmt.Errorf("issuing: vault path tenant segment must be valid UUID: %w", err)
	}

	recordID, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, fmt.Errorf("issuing: vault path record segment must be valid UUID: %w", err)
	}

	// R21 P0-2: field 只允许 API_KEY
	fieldName := normalizeCredentialType(parts[2])
	if fieldName != CredentialTypeAPIKey {
		return nil, fmt.Errorf("issuing: vault path field must be API_KEY, got %q", parts[2])
	}

	keyVersion, err := strconv.Atoi(parts[3])
	if err != nil {
		return nil, fmt.Errorf("issuing: vault path key_version segment must be positive integer: %w", err)
	}
	if keyVersion < 1 {
		return nil, fmt.Errorf("issuing: vault path key_version must be >= 1, got %d", keyVersion)
	}

	return &VaultPath{
		TenantID:   tenantID,
		RecordID:   recordID,
		FieldName:  fieldName,
		KeyVersion: keyVersion,
	}, nil
}

// ValidateProviderActivation 是共享激活校验器。
// R21 P0-2: Admin 审批启用和 Worker ACTIVE Provider 启动预检必须调用同一函数。
// 定义在 issuing 包中，configvalidator 包委托到此函数。
//
// 校验内容：
// - adapter type 合法
// - 所有模式禁止 UNSUPPORTED（草稿用 ValidateDraftProvider）
// - production/real 禁止 MOCK_LOCAL
// - NEXUS HTTP 必须有 adapter provider UUID
// - vault:// 格式
// - entity/tenant 非空（production）
// - HTTPS URL
// - URL host 非空
// - 禁止 userinfo
// - 禁止 fragment
func ValidateProviderActivation(cfg *ActivationConfig, mode RuntimeMode) error {
	// R24 P0-2: RuntimeMode 未知值必须 fail-closed，拒绝除 Production/LocalMock 之外的任何值
	if err := mode.Validate(); err != nil {
		return err
	}

	// 1. 必填字段
	if cfg.ProviderCode == "" {
		return fmt.Errorf("issuing: provider_code is required")
	}
	if cfg.CredentialVaultKey == "" {
		return ErrCredentialVaultKeyEmpty
	}
	// R20 P0-3: 使用严格 Vault 路径解析器替代简单前缀检查
	if _, err := ParseVaultPath(cfg.CredentialVaultKey); err != nil {
		return fmt.Errorf("issuing: invalid credential_vault_key: %w", err)
	}
	if cfg.PoolCode == "" {
		return fmt.Errorf("issuing: pool_code is required")
	}

	// 2. enum 校验
	if err := IssuingTier(cfg.IssuingTier).Validate(); err != nil {
		return err
	}
	if err := IssuingFundingMode(cfg.FundingMode).Validate(); err != nil {
		return err
	}

	// 3. adapter_type 校验
	adapterType := AdapterType(cfg.AdapterType)
	if adapterType == "" {
		adapterType = AdapterTypeUnsupported
	}
	if err := adapterType.Validate(); err != nil {
		return err
	}

	isProd := mode.IsProduction()

	// R21 P0-2: 所有模式拒绝 UNSUPPORTED（激活校验）
	if adapterType == AdapterTypeUnsupported {
		return fmt.Errorf("issuing: UNSUPPORTED adapter_type cannot be activated in any mode: %w", ErrAdapterTypeUnsupported)
	}

	// 4. production/real 禁止 MOCK_LOCAL
	if isProd && adapterType == AdapterTypeMockLocal {
		return fmt.Errorf("issuing: production mode rejects MOCK_LOCAL adapter_type: %w", ErrAdapterTypeUnsupported)
	}

	// 6. production/real 要求 entity 非空
	if isProd && cfg.EntityID == "" {
		return fmt.Errorf("issuing: production mode requires non-empty entity_id")
	}

	// 7. NEXUS_ISSUING_HTTP_V1 校验
	if adapterType == AdapterTypeNexusIssuingHTTPV1 {
		// 必须绑定 adapter_provider_id 且为合法 UUID
		if cfg.AdapterProviderID == "" {
			return fmt.Errorf("issuing: adapter_provider_id is required when adapter_type=NEXUS_ISSUING_HTTP_V1")
		}
		if _, err := uuid.Parse(cfg.AdapterProviderID); err != nil {
			return fmt.Errorf("issuing: adapter_provider_id must be a valid UUID when adapter_type=NEXUS_ISSUING_HTTP_V1")
		}
		// base_url 校验
		baseURL, _ := cfg.ProviderConfig["base_url"].(string)
		if baseURL == "" {
			return fmt.Errorf("issuing: provider_config.base_url is required when adapter_type=NEXUS_ISSUING_HTTP_V1")
		}
		parsedURL, err := url.Parse(baseURL)
		if err != nil {
			return fmt.Errorf("issuing: invalid base_url format")
		}
		if isProd && parsedURL.Scheme != "https" {
			return fmt.Errorf("issuing: production mode requires HTTPS base_url: %w", ErrAdapterTypeUnsupported)
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("issuing: base_url must have non-empty host: %w", ErrAdapterTypeUnsupported)
		}
		if parsedURL.User != nil {
			return fmt.Errorf("issuing: base_url must not contain userinfo: %w", ErrAdapterTypeUnsupported)
		}
		if parsedURL.Fragment != "" {
			return fmt.Errorf("issuing: base_url must not contain fragment: %w", ErrAdapterTypeUnsupported)
		}
	}

	return nil
}

// ValidateDraftProvider 是草稿（DISABLED Provider）校验器。
// R21 P0-2: 草稿允许 UNSUPPORTED（未配置适配器的初始状态），但不执行激活级校验。
// 草稿只校验基本字段格式：provider_code、pool_code、issuing_tier、funding_mode。
func ValidateDraftProvider(cfg *ActivationConfig) error {
	if cfg.ProviderCode == "" {
		return fmt.Errorf("issuing: provider_code is required")
	}
	if cfg.PoolCode == "" {
		return fmt.Errorf("issuing: pool_code is required")
	}
	if err := IssuingTier(cfg.IssuingTier).Validate(); err != nil {
		return err
	}
	if err := IssuingFundingMode(cfg.FundingMode).Validate(); err != nil {
		return err
	}
	// adapter_type 如果指定，必须是合法值
	adapterType := AdapterType(cfg.AdapterType)
	if adapterType != "" {
		if err := adapterType.Validate(); err != nil {
			return err
		}
	}
	// vault key 如果指定，必须格式正确
	if cfg.CredentialVaultKey != "" {
		if _, err := ParseVaultPath(cfg.CredentialVaultKey); err != nil {
			return fmt.Errorf("issuing: invalid credential_vault_key: %w", err)
		}
	}
	return nil
}

// OwnershipFacts 包含 Provider ownership 校验所需的全部事实。
// R21 Step B: Worker DB checker 和 Admin 事务校验必须调用同一个纯函数，
// 禁止复制两套判断逻辑。
type OwnershipFacts struct {
	// 从 Vault path 解析得到
	PathTenantID   uuid.UUID
	PathRecordID   uuid.UUID
	PathField      CredentialType
	PathKeyVersion int

	// 从 corporate_entity 查询得到
	EntityTenantID *uuid.UUID

	// 从 adapter_provider 查询得到
	AdapterStatus string
	AdapterType   string

	// 从 adapter_credential_vault 查询得到
	VaultRecordID          *uuid.UUID // R22 Item 3: DB 返回的 vault record ID
	VaultStatus            string
	VaultCredentialType    string
	VaultTenantID          *uuid.UUID
	VaultIssuingProviderID *uuid.UUID
	VaultProviderID        *uuid.UUID
	VaultExpiresAt         *time.Time
	VaultKeyVersion        int

	// Provider 身份
	ProviderID        uuid.UUID
	AdapterProviderID *uuid.UUID
}

// ValidateProviderOwnershipFacts 是纯函数，校验 Provider ownership 事实的一致性。
// R21 Step B: Worker DB checker 和 Admin 事务校验必须调用此函数。
//
// 校验规则：
//  1. path tenant == entity tenant == vault tenant
//  2. path record/version == DB record/version (vault key version)
//  3. path field == API_KEY == DB credential_type
//  4. adapter ACTIVE 且 CARD_ISSUER
//  5. vault ACTIVE 且未过期
//  6. issuing_provider_id/provider_id 完整匹配
func ValidateProviderOwnershipFacts(facts *OwnershipFacts) error {
	// R22 Item 3: nil facts 必须返回错误，不能 panic
	if facts == nil {
		return fmt.Errorf("issuing: ownership facts is nil")
	}

	// 1. tenant 三方一致：path tenant == entity tenant == vault tenant
	if facts.EntityTenantID == nil || *facts.EntityTenantID == uuid.Nil {
		return fmt.Errorf("issuing: entity tenant_id is null")
	}
	if *facts.EntityTenantID != facts.PathTenantID {
		return fmt.Errorf("issuing: path tenant %s does not match entity tenant %s",
			facts.PathTenantID, facts.EntityTenantID)
	}
	if facts.VaultTenantID == nil || *facts.VaultTenantID == uuid.Nil {
		return fmt.Errorf("issuing: vault tenant_id is null")
	}
	if *facts.VaultTenantID != facts.PathTenantID {
		return fmt.Errorf("issuing: path tenant %s does not match vault tenant %s",
			facts.PathTenantID, facts.VaultTenantID)
	}

	// 2. R22 Item 3: path record == DB record (VaultRecordID)
	if facts.VaultRecordID == nil || *facts.VaultRecordID == uuid.Nil {
		return fmt.Errorf("issuing: vault record_id is null")
	}
	if *facts.VaultRecordID != facts.PathRecordID {
		return fmt.Errorf("issuing: path record_id %s does not match vault record_id %s",
			facts.PathRecordID, facts.VaultRecordID)
	}
	// path key_version == DB key_version
	if facts.VaultKeyVersion != facts.PathKeyVersion {
		return fmt.Errorf("issuing: path key_version %d does not match vault key_version %d",
			facts.PathKeyVersion, facts.VaultKeyVersion)
	}

	// 3. path field == API_KEY == DB credential_type
	if facts.PathField != CredentialTypeAPIKey {
		return fmt.Errorf("issuing: path field must be API_KEY, got %s", facts.PathField)
	}
	if facts.VaultCredentialType != "API_KEY" {
		return fmt.Errorf("issuing: vault credential_type must be API_KEY, got %s", facts.VaultCredentialType)
	}

	// 4. adapter ACTIVE 且 CARD_ISSUER
	if facts.AdapterStatus != "ACTIVE" {
		return fmt.Errorf("issuing: adapter_provider is not ACTIVE (current: %s)", facts.AdapterStatus)
	}
	if facts.AdapterType != "CARD_ISSUER" {
		return fmt.Errorf("issuing: adapter_provider adapter_type must be CARD_ISSUER (current: %s)", facts.AdapterType)
	}

	// 5. vault ACTIVE 且未过期
	if facts.VaultStatus != "ACTIVE" {
		return fmt.Errorf("issuing: vault credential is not ACTIVE (current: %s)", facts.VaultStatus)
	}
	if facts.VaultExpiresAt != nil && time.Now().After(*facts.VaultExpiresAt) {
		return fmt.Errorf("issuing: vault credential is expired")
	}

	// 6. issuing_provider_id/provider_id 完整匹配
	if facts.VaultIssuingProviderID == nil || *facts.VaultIssuingProviderID != facts.ProviderID {
		return fmt.Errorf("issuing: vault issuing_provider_id mismatch")
	}
	if facts.AdapterProviderID == nil || *facts.AdapterProviderID == uuid.Nil {
		return fmt.Errorf("issuing: adapter_provider_id is null")
	}
	if facts.VaultProviderID == nil || *facts.VaultProviderID != *facts.AdapterProviderID {
		return fmt.Errorf("issuing: vault provider_id mismatch")
	}

	return nil
}
