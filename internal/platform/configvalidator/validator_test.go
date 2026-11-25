package configvalidator

import (
	"testing"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// TestValidateAcquirerChannel_NestedFundingConflict 验证嵌套 funding 冲突被拒绝。
// 顶层 funding_mode 与 routing_config.vcc_funding_mode 不一致 → 422 CONFIG_INCONSISTENT。
func TestValidateAcquirerChannel_NestedFundingConflict(t *testing.T) {
	cfg := &AcquirerChannelConfig{
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
		ProviderTier:       "TIER_2_COMPLIANT",
		ProviderCode:       "stripe",
		ChannelCode:        "ch_test",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		StrategyMode:       "DEFAULT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		RoutingConfig: map[string]interface{}{
			"vcc_funding_mode": "FIAT_DIRECT", // 与顶层冲突
		},
	}

	err := ValidateAcquirerChannel(cfg)
	if err == nil {
		t.Fatal("expected CONFIG_INCONSISTENT error for nested funding conflict, got nil")
	}

	appErr, ok := apperr.AsAPIError(err)
	if !ok {
		t.Fatalf("expected apperr.Error, got %T: %v", err, err)
	}
	if appErr.Code != "CONFIG_INCONSISTENT" {
		t.Errorf("expected code CONFIG_INCONSISTENT, got %s", appErr.Code)
	}
	if appErr.HTTP != 422 {
		t.Errorf("expected HTTP 422, got %d", appErr.HTTP)
	}
}

// TestValidateAcquirerChannel_HighRiskFiatDirect 验证高风险 + FIAT_DIRECT 被拒绝。
func TestValidateAcquirerChannel_HighRiskFiatDirect(t *testing.T) {
	cfg := &AcquirerChannelConfig{
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
		ProviderTier:       "TIER_1_HIGH_RISK",
		ProviderCode:       "stripe",
		ChannelCode:        "ch_test",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		StrategyMode:       "DEFAULT",
		FundingMode:        "FIAT_DIRECT",
		RoutingConfig: map[string]interface{}{
			"vcc_funding_mode":        "FIAT_DIRECT",
			"risk_isolation_group_id": "550e8400-e29b-41d4-a716-446655440001",
		},
	}

	err := ValidateAcquirerChannel(cfg)
	if err == nil {
		t.Fatal("expected CONFIG_INCONSISTENT for high-risk + FIAT_DIRECT, got nil")
	}

	appErr, _ := apperr.AsAPIError(err)
	if appErr.Code != "CONFIG_INCONSISTENT" {
		t.Errorf("expected code CONFIG_INCONSISTENT, got %s", appErr.Code)
	}
}

// TestValidateAcquirerChannel_HighRiskWithoutIsolation 验证高风险必须绑定隔离组或人工复核。
func TestValidateAcquirerChannel_HighRiskWithoutIsolation(t *testing.T) {
	cfg := &AcquirerChannelConfig{
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
		ProviderTier:       "TIER_1_HIGH_RISK",
		ProviderCode:       "stripe",
		ChannelCode:        "ch_test",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		StrategyMode:       "DEFAULT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		RoutingConfig: map[string]interface{}{
			"vcc_funding_mode": "CRYPTO_DESENSITIZED",
			// 缺少 risk_isolation_group_id 和 manual_review_required
		},
	}

	err := ValidateAcquirerChannel(cfg)
	if err == nil {
		t.Fatal("expected CONFIG_INCONSISTENT for high-risk without isolation, got nil")
	}
}

// TestValidateAcquirerChannel_HighRiskWithIsolation 验证高风险 + 隔离组 + CRYPTO_DESENSITIZED 通过。
func TestValidateAcquirerChannel_HighRiskWithIsolation(t *testing.T) {
	cfg := &AcquirerChannelConfig{
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
		ProviderTier:       "TIER_1_HIGH_RISK",
		ProviderCode:       "stripe",
		ChannelCode:        "ch_test",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		StrategyMode:       "DEFAULT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		RoutingConfig: map[string]interface{}{
			"vcc_funding_mode":        "CRYPTO_DESENSITIZED",
			"risk_isolation_group_id": "550e8400-e29b-41d4-a716-446655440001",
		},
	}

	if err := ValidateAcquirerChannel(cfg); err != nil {
		t.Errorf("expected pass for high-risk with isolation, got %v", err)
	}
}

// TestMergeAcquirerPatch_PatchMakesIllegal 验证 PATCH 将合规配置变成非法组合被拒绝。
// 旧配置合规（COMPLIANT + CRYPTO_DESENSITIZED），patch 只改 funding_mode=FIAT_DIRECT，
// 合并后变成 COMPLIANT + FIAT_DIRECT（合法）但缺少隔离组 → 但 COMPLIANT 不需要隔离组。
// 改为：旧配置 HIGH_RISK + CRYPTO + 隔离组，patch 改 funding_mode=FIAT_DIRECT → 非法。
func TestMergeAcquirerPatch_PatchMakesIllegal(t *testing.T) {
	old := &AcquirerChannelConfig{
		ProviderTier:       "TIER_1_HIGH_RISK",
		ProviderCode:       "stripe",
		ChannelCode:        "ch_test",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		StrategyMode:       "DEFAULT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		RoutingConfig: map[string]interface{}{
			"vcc_funding_mode":        "CRYPTO_DESENSITIZED",
			"risk_isolation_group_id": "550e8400-e29b-41d4-a716-446655440001",
		},
	}

	// patch 只改 funding_mode（和 routing_config 中的 vcc_funding_mode）
	patch := &AcquirerChannelConfig{
		FundingMode: "FIAT_DIRECT",
		RoutingConfig: map[string]interface{}{
			"vcc_funding_mode":        "FIAT_DIRECT",
			"risk_isolation_group_id": "550e8400-e29b-41d4-a716-446655440001",
		},
	}

	merged := MergeAcquirerPatch(old, patch)

	// 旧配置单独校验应该通过
	if err := ValidateAcquirerChannel(old); err != nil {
		t.Fatalf("old config should be valid, got %v", err)
	}

	// patch 片段单独看 funding_mode=FIAT_DIRECT 是合法 enum 值
	// 但合并后 HIGH_RISK + FIAT_DIRECT → 非法
	err := ValidateAcquirerChannel(merged)
	if err == nil {
		t.Fatal("expected CONFIG_INCONSISTENT after merge, got nil")
	}

	appErr, _ := apperr.AsAPIError(err)
	if appErr.Code != "CONFIG_INCONSISTENT" {
		t.Errorf("expected code CONFIG_INCONSISTENT, got %s", appErr.Code)
	}
}

// TestValidateAcquirerChannel_UnknownField 验证未知字段被拒绝。
func TestValidateAcquirerChannel_UnknownField(t *testing.T) {
	cfg := &AcquirerChannelConfig{
		ProviderTier:       "TIER_2_COMPLIANT",
		ProviderCode:       "stripe",
		ChannelCode:        "ch_test",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		StrategyMode:       "DEFAULT",
		ProviderConfig: map[string]interface{}{
			"unknown_field": "value",
		},
	}

	err := ValidateAcquirerChannel(cfg)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

//GlossaryError := apperr.ErrInvalidArgument // keep import

// TestValidateAcquirerChannel_HTTPSCallback 验证非 HTTPS callback 被拒绝。
func TestValidateAcquirerChannel_HTTPSCallback(t *testing.T) {
	cfg := &AcquirerChannelConfig{
		ProviderTier:       "TIER_2_COMPLIANT",
		ProviderCode:       "stripe",
		ChannelCode:        "ch_test",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		StrategyMode:       "DEFAULT",
		ProviderConfig: map[string]interface{}{
			"callback_url": "http://insecure.example.com/webhook",
		},
	}

	err := ValidateAcquirerChannel(cfg)
	if err == nil {
		t.Fatal("expected error for non-HTTPS callback, got nil")
	}
}

// TestValidateIssuingPool_Valid 验证合法 issuing pool 配置通过。
func TestValidateIssuingPool_Valid(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_1",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		VelocityConfig: map[string]interface{}{
			"declined_count_sliding_window": 5,
		},
	}

	if err := ValidateIssuingPool(cfg); err != nil {
		t.Errorf("expected pass, got %v", err)
	}
}

// TestValidateIssuingPool_InvalidFundingMode 验证非法 funding_mode 被拒绝。
func TestValidateIssuingPool_InvalidFundingMode(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "INVALID_MODE",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_1",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
	}

	err := ValidateIssuingPool(cfg)
	if err == nil {
		t.Fatal("expected error for invalid funding_mode, got nil")
	}
}

// TestNormalizeAcquirerFundingMode_WritesNestedValue 顶层 funding_mode 非空且嵌套不存在时，
// Normalize 必须把值写入 routing_config["vcc_funding_mode"]，且保留既有 routing 字段。
func TestNormalizeAcquirerFundingMode_WritesNestedValue(t *testing.T) {
	cfg := &AcquirerChannelConfig{
		FundingMode: "CRYPTO_DESENSITIZED",
		RoutingConfig: map[string]interface{}{
			"risk_isolation_group_id": "grp-1",
		},
	}
	if err := NormalizeAcquirerFundingMode(cfg); err != nil {
		t.Fatalf("NormalizeAcquirerFundingMode failed: %v", err)
	}
	got, ok := cfg.RoutingConfig["vcc_funding_mode"]
	if !ok {
		t.Fatal("expected vcc_funding_mode to be written, but key not found")
	}
	if got != "CRYPTO_DESENSITIZED" {
		t.Fatalf("expected vcc_funding_mode=CRYPTO_DESENSITIZED, got %v", got)
	}
	if cfg.RoutingConfig["risk_isolation_group_id"] != "grp-1" {
		t.Fatal("expected risk_isolation_group_id to be preserved")
	}
}

// TestNormalizeAcquirerFundingMode_RejectsRequestConflict 请求同时带顶层 funding_mode 与
// routing_config.vcc_funding_mode 且两者不同 → CONFIG_INCONSISTENT。
func TestNormalizeAcquirerFundingMode_RejectsRequestConflict(t *testing.T) {
	cfg := &AcquirerChannelConfig{
		FundingMode: "CRYPTO_DESENSITIZED",
		RoutingConfig: map[string]interface{}{
			"vcc_funding_mode": "FIAT_DIRECT",
		},
	}
	err := NormalizeAcquirerFundingMode(cfg)
	if err == nil {
		t.Fatal("expected CONFIG_INCONSISTENT error for top/nested conflict, got nil")
	}
	appErr, ok := apperr.AsAPIError(err)
	if !ok {
		t.Fatalf("expected apperr, got %T: %v", err, err)
	}
	if appErr.Code != "CONFIG_INCONSISTENT" {
		t.Fatalf("expected CONFIG_INCONSISTENT, got %s", appErr.Code)
	}
}

// ── Section 5: adapter_type / adapter_provider_id 校验测试 ──

// validAdapterProviderID 是测试用的合法 UUID（adapter_provider 引用）。
const validAdapterProviderID = "550e8400-e29b-41d4-a716-446655440099"

// TestValidateIssuingPool_AdapterTypeRequired 空 adapter_type 默认 UNSUPPORTED，通过校验。
func TestValidateIssuingPool_AdapterTypeRequired(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_default_adapter",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		// AdapterType 故意留空，期望被规范化为 UNSUPPORTED 并通过
	}

	if err := ValidateIssuingPool(cfg); err != nil {
		t.Fatalf("expected pass with default UNSUPPORTED adapter_type, got %v", err)
	}
	if cfg.AdapterType != "UNSUPPORTED" {
		t.Fatalf("expected AdapterType normalized to UNSUPPORTED, got %s", cfg.AdapterType)
	}
}

// TestValidateIssuingPool_NexusHTTPV1RequiresAdapterProviderID
// NEXUS_ISSUING_HTTP_V1 缺少 adapter_provider_id 时返回错误。
func TestValidateIssuingPool_NexusHTTPV1RequiresAdapterProviderID(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_http_v1",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		// AdapterProviderID 故意留空
		ProviderConfig: map[string]interface{}{
			"base_url": "https://api.example.com",
		},
	}

	err := ValidateIssuingPool(cfg)
	if err == nil {
		t.Fatal("expected error for NEXUS_ISSUING_HTTP_V1 without adapter_provider_id, got nil")
	}
	appErr, ok := apperr.AsAPIError(err)
	if !ok {
		t.Fatalf("expected apperr, got %T: %v", err, err)
	}
	if appErr.Code != "INVALID_ARGUMENT" {
		t.Errorf("expected code INVALID_ARGUMENT, got %s", appErr.Code)
	}
}

// TestValidateIssuingPool_NexusHTTPV1RequiresBaseURL
// NEXUS_ISSUING_HTTP_V1 缺少 base_url 时返回错误。
func TestValidateIssuingPool_NexusHTTPV1RequiresBaseURL(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_http_v1_no_base",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  validAdapterProviderID,
		// ProviderConfig 故意不带 base_url
	}

	err := ValidateIssuingPool(cfg)
	if err == nil {
		t.Fatal("expected error for NEXUS_ISSUING_HTTP_V1 without base_url, got nil")
	}
	appErr, ok := apperr.AsAPIError(err)
	if !ok {
		t.Fatalf("expected apperr, got %T: %v", err, err)
	}
	if appErr.Code != "INVALID_ARGUMENT" {
		t.Errorf("expected code INVALID_ARGUMENT, got %s", appErr.Code)
	}
}

// TestValidateIssuingPool_NexusHTTPV1RequiresHTTPSBaseURL
// NEXUS_ISSUING_HTTP_V1 的 base_url 必须为 HTTPS。
func TestValidateIssuingPool_NexusHTTPV1RequiresHTTPSBaseURL(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_http_v1_insecure",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  validAdapterProviderID,
		ProviderConfig: map[string]interface{}{
			"base_url": "http://insecure.example.com",
		},
	}

	err := ValidateIssuingPool(cfg)
	if err == nil {
		t.Fatal("expected error for non-HTTPS base_url, got nil")
	}
}

// TestValidateIssuingPool_NexusHTTPV1Valid 完整 NEXUS_ISSUING_HTTP_V1 配置通过校验。
func TestValidateIssuingPool_NexusHTTPV1Valid(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_http_v1_valid",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  validAdapterProviderID,
		ProviderConfig: map[string]interface{}{
			"base_url": "https://api.example.com",
		},
	}

	if err := ValidateIssuingPool(cfg); err != nil {
		t.Fatalf("expected pass for valid NEXUS_ISSUING_HTTP_V1 config, got %v", err)
	}
}

// TestValidateIssuingPool_UnsupportedAllowsEmptyProviderID
// UNSUPPORTED 允许空 adapter_provider_id。
func TestValidateIssuingPool_UnsupportedAllowsEmptyProviderID(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_unsupported",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "UNSUPPORTED",
		// AdapterProviderID 故意留空
	}

	if err := ValidateIssuingPool(cfg); err != nil {
		t.Fatalf("expected pass for UNSUPPORTED with empty adapter_provider_id, got %v", err)
	}
}

// TestValidateIssuingPool_MockLocalAllowsEmptyProviderID
// MOCK_LOCAL 允许空 adapter_provider_id。
func TestValidateIssuingPool_MockLocalAllowsEmptyProviderID(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_mock",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "MOCK_LOCAL",
		// AdapterProviderID 故意留空
	}

	if err := ValidateIssuingPool(cfg); err != nil {
		t.Fatalf("expected pass for MOCK_LOCAL with empty adapter_provider_id, got %v", err)
	}
}

// TestValidateIssuingPool_InvalidAdapterType 无效 adapter_type 返回错误。
func TestValidateIssuingPool_InvalidAdapterType(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_invalid_adapter",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "BOGUS_ADAPTER",
	}

	err := ValidateIssuingPool(cfg)
	if err == nil {
		t.Fatal("expected error for invalid adapter_type, got nil")
	}
	appErr, ok := apperr.AsAPIError(err)
	if !ok {
		t.Fatalf("expected apperr, got %T: %v", err, err)
	}
	if appErr.Code != "INVALID_ARGUMENT" {
		t.Errorf("expected code INVALID_ARGUMENT, got %s", appErr.Code)
	}
}

// TestValidateIssuingPool_NexusHTTPV1InvalidProviderID
// NEXUS_ISSUING_HTTP_V1 的 adapter_provider_id 必须为合法 UUID。
func TestValidateIssuingPool_NexusHTTPV1InvalidProviderID(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_http_v1_bad_uuid",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  "not-a-uuid",
		ProviderConfig: map[string]interface{}{
			"base_url": "https://api.example.com",
		},
	}

	err := ValidateIssuingPool(cfg)
	if err == nil {
		t.Fatal("expected error for invalid adapter_provider_id UUID, got nil")
	}
}

// TestMergeIssuingPatch_AdapterFields 验证 patch 的 adapter_type / adapter_provider_id 被合并。
func TestMergeIssuingPatch_AdapterFields(t *testing.T) {
	old := &IssuingPoolConfig{
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_merge",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "UNSUPPORTED",
	}
	patch := &IssuingPoolConfig{
		AdapterType:       "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID: validAdapterProviderID,
	}

	merged := MergeIssuingPatch(old, patch)
	if merged.AdapterType != "NEXUS_ISSUING_HTTP_V1" {
		t.Fatalf("expected merged AdapterType=NEXUS_ISSUING_HTTP_V1, got %s", merged.AdapterType)
	}
	if merged.AdapterProviderID != validAdapterProviderID {
		t.Fatalf("expected merged AdapterProviderID=%s, got %s", validAdapterProviderID, merged.AdapterProviderID)
	}
}

// TestMergeIssuingPatch_AdapterFieldsPreservedWhenPatchEmpty
// patch 不带 adapter 字段时，merged 保留 old 的值。
func TestMergeIssuingPatch_AdapterFieldsPreservedWhenPatchEmpty(t *testing.T) {
	old := &IssuingPoolConfig{
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_preserve",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "MOCK_LOCAL",
		AdapterProviderID:  validAdapterProviderID,
	}
	patch := &IssuingPoolConfig{
		// 只改 funding_mode，不动 adapter 字段
		FundingMode: "FIAT_DIRECT",
	}

	merged := MergeIssuingPatch(old, patch)
	if merged.AdapterType != "MOCK_LOCAL" {
		t.Fatalf("expected merged AdapterType preserved as MOCK_LOCAL, got %s", merged.AdapterType)
	}
	if merged.AdapterProviderID != validAdapterProviderID {
		t.Fatalf("expected merged AdapterProviderID preserved, got %s", merged.AdapterProviderID)
	}
}

// ── R18 P0-4: ValidateIssuingRuntimeConfig 测试 ──

// TestValidateIssuingRuntimeConfig_ProductionRejectsMockLocal 验证 production 拒绝 MOCK_LOCAL。
func TestValidateIssuingRuntimeConfig_ProductionRejectsMockLocal(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_mock",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "MOCK_LOCAL",
	}

	err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeProduction)
	if err == nil {
		t.Fatal("expected error for MOCK_LOCAL in production, got nil")
	}
}

// TestValidateIssuingRuntimeConfig_ProductionRejectsUnsupported 验证 production 拒绝 UNSUPPORTED。
func TestValidateIssuingRuntimeConfig_ProductionRejectsUnsupported(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_unsupported",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "UNSUPPORTED",
	}

	err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeProduction)
	if err == nil {
		t.Fatal("expected error for UNSUPPORTED in production, got nil")
	}
}

// R21 P0-2: ValidateIssuingRuntimeConfig 在 local-mock 模式也拒绝 UNSUPPORTED。
func TestValidateIssuingRuntimeConfig_LocalMockRejectsUnsupported(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_unsupported",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "UNSUPPORTED",
	}

	err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeLocalMock)
	if err == nil {
		t.Fatal("expected error for UNSUPPORTED in local-mock, got nil")
	}
}

// TestValidateIssuingRuntimeConfig_ProductionRequiresEntityID 验证 production 要求 entity_id 非空。
func TestValidateIssuingRuntimeConfig_ProductionRequiresEntityID(t *testing.T) {
	cfg := &IssuingPoolConfig{
		EntityID:           "", // 空 entity_id
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_no_entity",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  validAdapterProviderID,
		ProviderConfig: map[string]interface{}{
			"base_url": "https://api.example.com",
		},
	}

	err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeProduction)
	if err == nil {
		t.Fatal("expected error for empty entity_id in production, got nil")
	}
}

// TestValidateIssuingRuntimeConfig_ProductionValidConfig 验证 production 合法配置通过。
func TestValidateIssuingRuntimeConfig_ProductionValidConfig(t *testing.T) {
	cfg := &IssuingPoolConfig{
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_valid",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  validAdapterProviderID,
		ProviderConfig: map[string]interface{}{
			"base_url": "https://api.example.com",
		},
	}

	if err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeProduction); err != nil {
		t.Fatalf("expected pass for valid production config, got %v", err)
	}
}

// TestValidateIssuingRuntimeConfig_NexusHTTPV1RejectsUserinfoInURL 验证 base_url 含 userinfo 被拒绝。
func TestValidateIssuingRuntimeConfig_NexusHTTPV1RejectsUserinfoInURL(t *testing.T) {
	cfg := &IssuingPoolConfig{
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_userinfo",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  validAdapterProviderID,
		ProviderConfig: map[string]interface{}{
			"base_url": "https://user:pass@api.example.com",
		},
	}

	err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeLocalMock)
	if err == nil {
		t.Fatal("expected error for base_url with userinfo, got nil")
	}
}

// TestValidateIssuingRuntimeConfig_NexusHTTPV1RejectsFragmentInURL 验证 base_url 含 fragment 被拒绝。
func TestValidateIssuingRuntimeConfig_NexusHTTPV1RejectsFragmentInURL(t *testing.T) {
	cfg := &IssuingPoolConfig{
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_fragment",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  validAdapterProviderID,
		ProviderConfig: map[string]interface{}{
			"base_url": "https://api.example.com#frag",
		},
	}

	err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeLocalMock)
	if err == nil {
		t.Fatal("expected error for base_url with fragment, got nil")
	}
}

// TestValidateIssuingRuntimeConfig_NexusHTTPV1RejectsEmptyHost 验证 base_url 空 host 被拒绝。
func TestValidateIssuingRuntimeConfig_NexusHTTPV1RejectsEmptyHost(t *testing.T) {
	cfg := &IssuingPoolConfig{
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_empty_host",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  validAdapterProviderID,
		ProviderConfig: map[string]interface{}{
			"base_url": "https://",
		},
	}

	err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeLocalMock)
	if err == nil {
		t.Fatal("expected error for base_url with empty host, got nil")
	}
}

// TestValidateIssuingRuntimeConfig_NonProductionAllowsMockLocal 验证非 production 允许 MOCK_LOCAL。
func TestValidateIssuingRuntimeConfig_NonProductionAllowsMockLocal(t *testing.T) {
	cfg := &IssuingPoolConfig{
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		ProviderCode:       "nxtapay",
		PoolCode:           "pool_mock_dev",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		AdapterType:        "MOCK_LOCAL",
	}

	if err := ValidateIssuingRuntimeConfig(cfg, RuntimeModeLocalMock); err != nil {
		t.Fatalf("expected pass for MOCK_LOCAL in non-production, got %v", err)
	}
}
