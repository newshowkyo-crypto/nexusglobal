// Package configvalidator 提供统一的配置校验器（G11 修复产物）。
//
// 动机：原先 v3_channels_handler.validateChannelRequest 和
// v3_issuing_pools_handler.validateIssuingPoolRequest 各自内联校验，
// 导致以下漏洞：
//  1. 嵌套 funding 冲突未检查：顶层 funding_mode 与 routing_config.vcc_funding_mode
//     可以不一致，runtime 路由与 admin POST 走不同真相。
//  2. PATCH 只验证 patch 片段，不合并旧版本完整对象验证，可将合规配置
//     变成非法组合。
//  3. 高风险 + FIAT_DIRECT 仅在 POST 检查，PATCH/审批 apply/runtime routing 旁路。
//  4. 未知字段不拒绝。
//
// 本包提供 pure function Validator，POST/PATCH/审批创建/审批 apply/runtime routing
// 必须调用同一 Validator，确保单一持久化真相。
package configvalidator

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	acquirerdomain "github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/issuing"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// ── Acquirer Channel Validator ──

// AcquirerChannelConfig 是 acquirer channel 的完整配置视图。
// POST 传入完整对象；PATCH 先 FOR UPDATE 读取旧版本，合并 patch 后填入此结构。
type AcquirerChannelConfig struct {
	EntityID           string                 `json:"entity_id"`
	ProviderTier       string                 `json:"provider_tier"`
	ProviderCode       string                 `json:"provider_code"`
	ChannelCode        string                 `json:"channel_code"`
	CredentialVaultKey string                 `json:"credential_vault_key"`
	StrategyMode       string                 `json:"strategy_mode"`
	FundingMode        string                 `json:"funding_mode"`
	ProviderConfig     map[string]interface{} `json:"provider_config"`
	RoutingConfig      map[string]interface{} `json:"routing_config"`
}

// 已知 provider_config 字段白名单（acquirer channel）。
var knownAcquirerProviderConfigKeys = map[string]bool{
	"ACQUIRING_SETTLEMENT_BALANCE": true,
	"VCC_AVAILABLE_BALANCE":        true,
	"callback_url":                 true,
	"webhook_url":                  true,
	"max_amount":                   true,
	"min_amount":                   true,
	"rate_bps":                     true,
	"settlement_currency":          true,
	"currencies":                   true,
	"merchant_id":                  true,
}

// AcquirerCurrencies validates the optional ISO currency code list. Nil
// preserves a legacy channel's existing configuration when the field is absent.
func AcquirerCurrencies(config map[string]any) ([]string, error) {
	raw, present := config["currencies"]
	if !present {
		return nil, nil
	}
	var codes []string
	switch list := raw.(type) {
	case []string:
		codes = list
	case []any:
		for _, item := range list {
			code, _ := item.(string)
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 || len(codes) > 40 {
		return nil, apperr.ErrInvalidArgument.WithMessage("请填写 1 到 40 种获批收款币种")
	}
	for _, code := range codes {
		if len(code) != 3 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return nil, apperr.ErrInvalidArgument.WithMessage("收款币种必须为三位大写字母")
		}
	}
	return codes, nil
}

// 已知 routing_config 字段白名单（acquirer channel）。
var knownAcquirerRoutingConfigKeys = map[string]bool{
	"vcc_funding_mode":            true,
	"preferred_issuing_pool_id":   true,
	"fallback_issuing_pool_id":    true,
	"velocity_window_seconds":     true,
	"max_transactions_per_window": true,
	"risk_isolation_group_id":     true,
	"manual_review_required":      true,
	"burn_mode_enabled":           true,
	"callback_url":                true,
}

// ValidateAcquirerChannel 校验 acquirer channel 完整配置。
// 这是 pure function，POST/PATCH/审批/runtime 共用同一校验路径。
//
// 返回 *apperr.Error（HTTP 422 CONFIG_INCONSISTENT 或 400 INVALID_ARGUMENT）。
func ValidateAcquirerChannel(cfg *AcquirerChannelConfig) error {
	// 1. 必填字段
	if cfg.ProviderCode == "" {
		return apperr.ErrInvalidArgument.WithMessage("provider_code is required")
	}
	if cfg.CredentialVaultKey == "" {
		return apperr.ErrInvalidArgument.WithMessage("credential_vault_key is required")
	}
	if !strings.HasPrefix(cfg.CredentialVaultKey, "vault://") {
		return apperr.ErrInvalidArgument.WithMessage("credential_vault_key must start with vault://")
	}
	if cfg.EntityID != "" {
		if _, err := uuid.Parse(cfg.EntityID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("entity_id must be a valid UUID")
		}
	}

	// 2. enum 校验：provider_tier
	if err := acquirerdomain.ProviderTier(cfg.ProviderTier).Validate(); err != nil {
		return apperr.ErrInvalidArgument.WithMessage("invalid provider_tier")
	}

	// 3. enum 校验：strategy_mode
	if cfg.StrategyMode != "" &&
		cfg.StrategyMode != string(acquirerdomain.ChannelStrategyModeDefault) &&
		cfg.StrategyMode != string(acquirerdomain.ChannelStrategyModeBurnMode) {
		return apperr.ErrInvalidArgument.WithMessage("invalid strategy_mode")
	}

	// 4. 拒绝未知字段：provider_config
	for k := range cfg.ProviderConfig {
		if !knownAcquirerProviderConfigKeys[k] {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("unknown provider_config field: %s", k))
		}
	}
	if _, err := AcquirerCurrencies(cfg.ProviderConfig); err != nil {
		return err
	}
	if raw, present := cfg.ProviderConfig["merchant_id"]; present {
		merchantID, ok := raw.(string)
		if !ok || strings.TrimSpace(merchantID) == "" || len(merchantID) > 128 {
			return apperr.ErrInvalidArgument.WithMessage("请填写有效的机构商户号")
		}
	}

	// 5. 拒绝未知字段：routing_config
	for k := range cfg.RoutingConfig {
		if !knownAcquirerRoutingConfigKeys[k] {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("unknown routing_config field: %s", k))
		}
	}

	// 6. HTTPS callback 校验
	if err := validateHTTPSURL(cfg.ProviderConfig, "callback_url"); err != nil {
		return err
	}
	if err := validateHTTPSURL(cfg.ProviderConfig, "webhook_url"); err != nil {
		return err
	}
	if err := validateHTTPSURL(cfg.RoutingConfig, "callback_url"); err != nil {
		return err
	}

	// 7. UUID 字段校验（routing_config 中的 ID 字段）
	if err := validateOptionalUUID(cfg.RoutingConfig, "preferred_issuing_pool_id"); err != nil {
		return err
	}
	if err := validateOptionalUUID(cfg.RoutingConfig, "fallback_issuing_pool_id"); err != nil {
		return err
	}
	if err := validateOptionalUUID(cfg.RoutingConfig, "risk_isolation_group_id"); err != nil {
		return err
	}

	// 8. amount/rate 范围校验
	if err := validatePositiveNumber(cfg.ProviderConfig, "max_amount"); err != nil {
		return err
	}
	if err := validatePositiveNumber(cfg.ProviderConfig, "min_amount"); err != nil {
		return err
	}
	if err := validateBPSRange(cfg.ProviderConfig, "rate_bps"); err != nil {
		return err
	}

	// 9. velocity window 校验
	if err := validatePositiveInt(cfg.RoutingConfig, "velocity_window_seconds"); err != nil {
		return err
	}
	if err := validatePositiveInt(cfg.RoutingConfig, "max_transactions_per_window"); err != nil {
		return err
	}

	// 10. 嵌套 funding 冲突检查：
	// 顶层 funding_mode 与 routing_config.vcc_funding_mode 必须一致（单一真相）。
	topFunding := cfg.FundingMode
	nestedFunding, hasNested := cfg.RoutingConfig["vcc_funding_mode"]
	if hasNested {
		nestedStr, ok := nestedFunding.(string)
		if !ok {
			return apperr.ErrConfigInconsistent.WithMessage("routing_config.vcc_funding_mode must be a string")
		}
		if topFunding != "" && topFunding != nestedStr {
			return apperr.ErrConfigInconsistent.WithMessage(
				fmt.Sprintf("funding_mode conflict: top-level=%q vs routing_config.vcc_funding_mode=%q (single source of truth required)",
					topFunding, nestedStr))
		}
		// 如果顶层为空，用嵌套值填充（规范化）
		if topFunding == "" {
			cfg.FundingMode = nestedStr
		}
	}

	// 11. funding_mode enum 校验（如果存在）
	if cfg.FundingMode != "" {
		if err := issuing.IssuingFundingMode(cfg.FundingMode).Validate(); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("invalid funding_mode")
		}
	}

	// 12. 高风险 + FIAT_DIRECT → 422 CONFIG_INCONSISTENT
	//     高风险通道必须绑定风险隔离/人工复核政策，禁止直连发卡仓。
	if acquirerdomain.ProviderTier(cfg.ProviderTier) == acquirerdomain.ProviderTierHighRisk {
		if cfg.FundingMode == string(issuing.FundingModeFiatDirect) {
			return apperr.ErrConfigInconsistent.WithMessage(
				"高危收单通道禁止 FIAT_DIRECT：必须绑定 CRYPTO_DESENSITIZED + 风险隔离组/人工复核")
		}
		// 高风险必须绑定风险隔离组或人工复核
		hasIsolation := cfg.RoutingConfig["risk_isolation_group_id"] != nil
		hasManualReview, _ := cfg.RoutingConfig["manual_review_required"].(bool)
		if !hasIsolation && !hasManualReview {
			return apperr.ErrConfigInconsistent.WithMessage(
				"高危收单通道必须绑定 risk_isolation_group_id 或 manual_review_required=true")
		}
	}

	return nil
}

// ── Issuing Pool Validator ──

// IssuingPoolConfig 是 issuing pool 的完整配置视图。
type IssuingPoolConfig struct {
	EntityID           string                 `json:"entity_id"`
	IssuingTier        string                 `json:"issuing_tier"`
	FundingMode        string                 `json:"funding_mode"`
	ProviderCode       string                 `json:"provider_code"`
	PoolCode           string                 `json:"pool_code"`
	CredentialVaultKey string                 `json:"credential_vault_key"`
	ProviderConfig     map[string]interface{} `json:"provider_config"`
	VelocityConfig     map[string]interface{} `json:"velocity_config"`
	// Section 5: Admin/Dashboard 配置链路新增字段。
	// adapter_type 标识卡池绑定的适配器类型，决定后续校验规则。
	AdapterType string `json:"adapter_type"`
	// adapter_provider_id 引用 adapter_provider(id)，NEXUS_ISSUING_HTTP_V1 必填。
	AdapterProviderID string `json:"adapter_provider_id"`
}

// 已知 issuing pool adapter_type 白名单。
// 与迁移 000050 中 issuing_providers.adapter_type 列的语义一致：
//   - NEXUS_ISSUING_HTTP_V1: 走 Nexus HTTP v1 协议的真实发卡适配器，需绑定 adapter_provider_id
//   - MOCK_LOCAL: 本地 mock 适配器，仅用于测试/演示，不要求 adapter_provider_id
//   - UNSUPPORTED: 未配置适配器（迁移默认值），禁止启用卡池
var validIssuingAdapterTypes = map[string]bool{
	"NEXUS_ISSUING_HTTP_V1": true,
	"MOCK_LOCAL":            true,
	"UNSUPPORTED":           true,
}

// 已知 provider_config 字段白名单（issuing pool）。
var knownIssuingProviderConfigKeys = map[string]bool{
	"ACQUIRING_SETTLEMENT_BALANCE": true,
	"VCC_AVAILABLE_BALANCE":        true,
	"callback_url":                 true,
	"webhook_url":                  true,
	"max_card_amount":              true,
	"min_card_amount":              true,
	"spending_limit_minor":         true,
	// Section 5: NEXUS_ISSUING_HTTP_V1 适配器的上游 base_url。
	"base_url": true,
}

// 已知 velocity_config 字段白名单（issuing pool）。
var knownIssuingVelocityConfigKeys = map[string]bool{
	"declined_count_sliding_window": true,
	"declined_window_seconds":       true,
	"fraud_rate_bps_threshold":      true,
	"max_transactions_per_second":   true,
	"velocity_window_seconds":       true,
}

// ValidateIssuingPool 校验 issuing pool 完整配置。
func ValidateIssuingPool(cfg *IssuingPoolConfig) error {
	// 1. 必填字段
	if cfg.ProviderCode == "" {
		return apperr.ErrInvalidArgument.WithMessage("provider_code is required")
	}
	if cfg.CredentialVaultKey == "" {
		return apperr.ErrInvalidArgument.WithMessage("credential_vault_key is required")
	}
	if !strings.HasPrefix(cfg.CredentialVaultKey, "vault://") {
		return apperr.ErrInvalidArgument.WithMessage("credential_vault_key must start with vault://")
	}
	if cfg.PoolCode == "" {
		return apperr.ErrInvalidArgument.WithMessage("pool_code is required")
	}
	if cfg.EntityID != "" {
		if _, err := uuid.Parse(cfg.EntityID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("entity_id must be a valid UUID")
		}
	}

	// 2. enum 校验
	if err := issuing.IssuingTier(cfg.IssuingTier).Validate(); err != nil {
		return apperr.ErrInvalidArgument.WithMessage("invalid issuing_tier")
	}
	if err := issuing.IssuingFundingMode(cfg.FundingMode).Validate(); err != nil {
		return apperr.ErrInvalidArgument.WithMessage("invalid funding_mode")
	}

	// 3. 拒绝未知字段
	for k := range cfg.ProviderConfig {
		if !knownIssuingProviderConfigKeys[k] {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("unknown provider_config field: %s", k))
		}
	}
	for k := range cfg.VelocityConfig {
		if !knownIssuingVelocityConfigKeys[k] {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("unknown velocity_config field: %s", k))
		}
	}

	// 4. HTTPS callback 校验
	if err := validateHTTPSURL(cfg.ProviderConfig, "callback_url"); err != nil {
		return err
	}
	if err := validateHTTPSURL(cfg.ProviderConfig, "webhook_url"); err != nil {
		return err
	}

	// 5. amount 范围校验
	if err := validatePositiveNumber(cfg.ProviderConfig, "max_card_amount"); err != nil {
		return err
	}
	if err := validatePositiveNumber(cfg.ProviderConfig, "min_card_amount"); err != nil {
		return err
	}
	if err := validatePositiveInt(cfg.ProviderConfig, "spending_limit_minor"); err != nil {
		return err
	}

	// 6. velocity window 校验
	if err := validatePositiveInt(cfg.VelocityConfig, "declined_count_sliding_window"); err != nil {
		return err
	}
	if err := validatePositiveInt(cfg.VelocityConfig, "declined_window_seconds"); err != nil {
		return err
	}
	if err := validateBPSRange(cfg.VelocityConfig, "fraud_rate_bps_threshold"); err != nil {
		return err
	}
	if err := validatePositiveInt(cfg.VelocityConfig, "max_transactions_per_second"); err != nil {
		return err
	}

	// 7. Section 5: adapter_type / adapter_provider_id 校验
	// 空 adapter_type 默认为 UNSUPPORTED（与迁移 000050 DEFAULT 'UNSUPPORTED' 对齐）
	if cfg.AdapterType == "" {
		cfg.AdapterType = "UNSUPPORTED"
	}
	if !validIssuingAdapterTypes[cfg.AdapterType] {
		return apperr.ErrInvalidArgument.WithMessage(
			fmt.Sprintf("invalid adapter_type: %s (allowed: NEXUS_ISSUING_HTTP_V1, MOCK_LOCAL, UNSUPPORTED)", cfg.AdapterType))
	}
	switch cfg.AdapterType {
	case "NEXUS_ISSUING_HTTP_V1":
		// 真实发卡适配器必须绑定 adapter_provider_id 且为合法 UUID
		if cfg.AdapterProviderID == "" {
			return apperr.ErrInvalidArgument.WithMessage(
				"adapter_provider_id is required when adapter_type=NEXUS_ISSUING_HTTP_V1")
		}
		if _, err := uuid.Parse(cfg.AdapterProviderID); err != nil {
			return apperr.ErrInvalidArgument.WithMessage(
				"adapter_provider_id must be a valid UUID when adapter_type=NEXUS_ISSUING_HTTP_V1")
		}
		// NEXUS_ISSUING_HTTP_V1 必须配置 provider_config.base_url 且为合法 HTTPS URL
		baseURL, hasBase := cfg.ProviderConfig["base_url"]
		if !hasBase || baseURL == nil {
			return apperr.ErrInvalidArgument.WithMessage(
				"provider_config.base_url is required when adapter_type=NEXUS_ISSUING_HTTP_V1")
		}
		baseStr, ok := baseURL.(string)
		if !ok || baseStr == "" {
			return apperr.ErrInvalidArgument.WithMessage(
				"provider_config.base_url must be a non-empty string when adapter_type=NEXUS_ISSUING_HTTP_V1")
		}
		if err := validateHTTPSURL(cfg.ProviderConfig, "base_url"); err != nil {
			return err
		}
	case "MOCK_LOCAL", "UNSUPPORTED":
		// 允许 adapter_provider_id 为空；若提供则必须为合法 UUID
		if cfg.AdapterProviderID != "" {
			if _, err := uuid.Parse(cfg.AdapterProviderID); err != nil {
				return apperr.ErrInvalidArgument.WithMessage(
					"adapter_provider_id must be a valid UUID when provided")
			}
		}
	}

	return nil
}

// RuntimeMode 表示运行时模式。R19 P0-2: 委托给 issuing.RuntimeMode 避免循环依赖。
type RuntimeMode = issuing.RuntimeMode

// RuntimeModeProduction 和 RuntimeModeLocalMock 是 issuing 包中对应常量的别名。
const (
	RuntimeModeProduction = issuing.RuntimeModeProduction
	RuntimeModeLocalMock  = issuing.RuntimeModeLocalMock
)

// ValidateIssuingDraft 校验草稿配置（Draft validator）。
// R19 P0-2: 允许创建 DISABLED+UNSUPPORTED 草稿，不执行激活级校验。
// 用于 Admin 创建卡池时的初始校验。
func ValidateIssuingDraft(cfg *IssuingPoolConfig) error {
	return ValidateIssuingPool(cfg)
}

// ValidateIssuingRuntimeConfig 是共享激活校验器（Activation validator）。
// R19 P0-2: Admin 审批启用和 Worker ACTIVE Provider 启动预检必须调用同一函数。
// R19 P0-2: 委托给 issuing.ValidateProviderActivation，确保单一校验路径。
func ValidateIssuingRuntimeConfig(cfg *IssuingPoolConfig, mode RuntimeMode) error {
	// 先执行基础配置校验（字段白名单、HTTPS URL 等）
	if err := ValidateIssuingPool(cfg); err != nil {
		return err
	}

	// 委托给 issuing.ValidateProviderActivation（共享激活校验逻辑）
	return issuing.ValidateProviderActivation(&issuing.ActivationConfig{
		EntityID:           cfg.EntityID,
		IssuingTier:        cfg.IssuingTier,
		FundingMode:        cfg.FundingMode,
		ProviderCode:       cfg.ProviderCode,
		PoolCode:           cfg.PoolCode,
		CredentialVaultKey: cfg.CredentialVaultKey,
		ProviderConfig:     cfg.ProviderConfig,
		VelocityConfig:     cfg.VelocityConfig,
		AdapterType:        cfg.AdapterType,
		AdapterProviderID:  cfg.AdapterProviderID,
	}, mode)
}

// ── 辅助校验函数 ──

// validateHTTPSURL 校验字段（如果存在）是 https:// URL。
func validateHTTPSURL(m map[string]interface{}, key string) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a string", key))
	}
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a valid URL", key))
	}
	if u.Scheme != "https" {
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must use https scheme", key))
	}
	return nil
}

// validateOptionalUUID 校验字段（如果存在）是合法 UUID 字符串。
func validateOptionalUUID(m map[string]interface{}, key string) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a string", key))
	}
	if s == "" {
		return nil
	}
	if _, err := uuid.Parse(s); err != nil {
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a valid UUID", key))
	}
	return nil
}

// validatePositiveNumber 校验字段（如果存在）是正数（int 或 float64）。
func validatePositiveNumber(m map[string]interface{}, key string) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		if n <= 0 {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be positive", key))
		}
	case int:
		if n <= 0 {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be positive", key))
		}
	case int64:
		if n <= 0 {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be positive", key))
		}
	default:
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a number", key))
	}
	return nil
}

// validatePositiveInt 校验字段（如果存在）是正整数。
func validatePositiveInt(m map[string]interface{}, key string) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		if n <= 0 {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a positive integer", key))
		}
	case int:
		if n <= 0 {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a positive integer", key))
		}
	case int64:
		if n <= 0 {
			return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a positive integer", key))
		}
	default:
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be an integer", key))
	}
	return nil
}

// validateBPSRange 校验字段（如果存在）是 [0, 10000] 的 bps 值。
func validateBPSRange(m map[string]interface{}, key string) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	var bps float64
	switch n := v.(type) {
	case float64:
		bps = n
	case int:
		bps = float64(n)
	case int64:
		bps = float64(n)
	default:
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be a number", key))
	}
	if bps < 0 || bps > 10000 {
		return apperr.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s must be in range [0, 10000] bps", key))
	}
	return nil
}

// MergeAcquirerPatch 将 patch 合并到旧配置，生成完整对象供 ValidateAcquirerChannel 校验。
// PATCH 不能只验证 patch 片段，必须合并旧版本完整对象后验证（G11 要求）。
func MergeAcquirerPatch(old, patch *AcquirerChannelConfig) *AcquirerChannelConfig {
	merged := &AcquirerChannelConfig{}
	*merged = *old

	if patch.ProviderTier != "" {
		merged.ProviderTier = patch.ProviderTier
	}
	if patch.StrategyMode != "" {
		merged.StrategyMode = patch.StrategyMode
	}
	if patch.FundingMode != "" {
		merged.FundingMode = patch.FundingMode
	}
	if patch.CredentialVaultKey != "" {
		merged.CredentialVaultKey = patch.CredentialVaultKey
	}
	if patch.ProviderCode != "" {
		merged.ProviderCode = patch.ProviderCode
	}
	if patch.ChannelCode != "" {
		merged.ChannelCode = patch.ChannelCode
	}
	// map 字段：patch 非 nil 则整体替换（与现有 SQL 行为一致）
	if patch.ProviderConfig != nil {
		merged.ProviderConfig = patch.ProviderConfig
	}
	if patch.RoutingConfig != nil {
		merged.RoutingConfig = patch.RoutingConfig
	}
	return merged
}

// NormalizeAcquirerFundingMode 将顶层 FundingMode 规范化写入 routing_config["vcc_funding_mode"]。
// acquirer_channels 无独立 funding_mode 列；唯一持久化事实源是 routing_config.vcc_funding_mode。
// 规则：
//   - cfg.FundingMode 为空：不改动（返回 nil）。
//   - FundingMode 非空且 RoutingConfig 为 nil：初始化 map。
//   - 若 routing_config.vcc_funding_mode 已存在且与 FundingMode 不同：返回 CONFIG_INCONSISTENT。
//   - 若 routing_config.vcc_funding_mode 已存在且相同：幂等，不改动。
//   - 否则写入 routing_config["vcc_funding_mode"] = FundingMode。
//
// 不校验 FundingMode 的 enum 合法性（由 ValidateAcquirerChannel 统一校验）。
func NormalizeAcquirerFundingMode(cfg *AcquirerChannelConfig) error {
	if cfg.FundingMode == "" {
		return nil
	}
	if cfg.RoutingConfig == nil {
		cfg.RoutingConfig = map[string]interface{}{}
	}
	existing, ok := cfg.RoutingConfig["vcc_funding_mode"]
	if ok {
		existingStr, isStr := existing.(string)
		if !isStr {
			return apperr.ErrConfigInconsistent.WithMessage("routing_config.vcc_funding_mode must be a string")
		}
		if existingStr != cfg.FundingMode {
			return apperr.ErrConfigInconsistent.WithMessage(fmt.Sprintf(
				"funding_mode conflict: top-level=%q vs routing_config.vcc_funding_mode=%q (single source of truth required)",
				cfg.FundingMode, existingStr))
		}
		return nil
	}
	cfg.RoutingConfig["vcc_funding_mode"] = cfg.FundingMode
	return nil
}

// MergeIssuingPatch 将 patch 合并到旧配置，生成完整对象供 ValidateIssuingPool 校验。
func MergeIssuingPatch(old, patch *IssuingPoolConfig) *IssuingPoolConfig {
	merged := &IssuingPoolConfig{}
	*merged = *old

	if patch.IssuingTier != "" {
		merged.IssuingTier = patch.IssuingTier
	}
	if patch.FundingMode != "" {
		merged.FundingMode = patch.FundingMode
	}
	if patch.CredentialVaultKey != "" {
		merged.CredentialVaultKey = patch.CredentialVaultKey
	}
	if patch.ProviderCode != "" {
		merged.ProviderCode = patch.ProviderCode
	}
	if patch.PoolCode != "" {
		merged.PoolCode = patch.PoolCode
	}
	if patch.ProviderConfig != nil {
		merged.ProviderConfig = patch.ProviderConfig
	}
	if patch.VelocityConfig != nil {
		merged.VelocityConfig = patch.VelocityConfig
	}
	// Section 5: 合并 adapter_type / adapter_provider_id
	if patch.AdapterType != "" {
		merged.AdapterType = patch.AdapterType
	}
	if patch.AdapterProviderID != "" {
		merged.AdapterProviderID = patch.AdapterProviderID
	}
	return merged
}

// IsConfigInconsistentError 判断是否为 CONFIG_INCONSISTENT 错误（HTTP 422）。
// 供测试断言使用。
func IsConfigInconsistentError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "CONFIG_INCONSISTENT")
}
