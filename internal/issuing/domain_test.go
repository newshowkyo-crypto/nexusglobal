package issuing

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// R20 P1-3: ResolveRuntimeMode 模式矩阵纯单测
// ============================================================================

func TestResolveRuntimeMode_ProductionMatrix(t *testing.T) {
	tests := []struct {
		name         string
		appEnv       string
		externalMode string
		wantMode     RuntimeMode
		wantErr      bool
	}{
		// production + real → Production
		{"production+real", "production", "real", RuntimeModeProduction, false},
		{"production+mock", "production", "mock", RuntimeModeProduction, false},

		// real + real → Production
		{"real+real", "real", "real", RuntimeModeProduction, false},
		{"real+mock", "real", "mock", RuntimeModeProduction, false},

		// local + real → Production (fail-closed)
		{"local+real", "local", "real", RuntimeModeProduction, false},

		// local + mock → LocalMock
		{"local+mock", "local", "mock", RuntimeModeLocalMock, false},

		// mock + mock → LocalMock
		{"mock+mock", "mock", "mock", RuntimeModeLocalMock, false},

		// staging/development/dev/test + mock → LocalMock
		{"staging+mock", "staging", "mock", RuntimeModeLocalMock, false},
		{"development+mock", "development", "mock", RuntimeModeLocalMock, false},
		{"dev+mock", "dev", "mock", RuntimeModeLocalMock, false},
		{"test+mock", "test", "mock", RuntimeModeLocalMock, false},

		// staging/development/dev/test + real → Production (fail-closed)
		{"staging+real", "staging", "real", RuntimeModeProduction, false},
		{"development+real", "development", "real", RuntimeModeProduction, false},

		// R21 P0-1: EXTERNAL_MODE 空值 → error（不再允许空值）
		{"production+empty", "production", "", 0, true},
		{"real+empty", "real", "", 0, true},
		{"local+empty", "local", "", 0, true},
		{"mock+empty", "mock", "", 0, true},
		{"staging+empty", "staging", "", 0, true},
		{"dev+empty", "dev", "", 0, true},

		// 未知 APP_ENV → error
		{"unknown_env", "foobar", "mock", 0, true},
		{"empty_env", "", "mock", 0, true},

		// 未知 EXTERNAL_MODE → error
		{"production+unknown_mode", "production", "foobar", 0, true},
		{"local+unknown_mode", "local", "production", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, err := ResolveRuntimeMode(tt.appEnv, tt.externalMode)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ResolveRuntimeMode(%q, %q): expected error, got nil", tt.appEnv, tt.externalMode)
				}
				return
			}
			if err != nil {
				t.Errorf("ResolveRuntimeMode(%q, %q): unexpected error: %v", tt.appEnv, tt.externalMode, err)
				return
			}
			if mode != tt.wantMode {
				t.Errorf("ResolveRuntimeMode(%q, %q): got %v, want %v", tt.appEnv, tt.externalMode, mode, tt.wantMode)
			}
		})
	}
}

// ============================================================================
// R20 P1-3: ParseVaultPath 严格解析测试
// ============================================================================

func TestParseVaultPath_ValidPaths(t *testing.T) {
	tests := []struct {
		name      string
		vaultKey  string
		wantField CredentialType
		wantVer   int
	}{
		{
			"standard api_key",
			"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
			CredentialTypeAPIKey,
			1,
		},
		{
			"uppercase field normalized",
			"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/API_KEY/2",
			CredentialTypeAPIKey,
			2,
		},
		{
			"high version number",
			"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/999",
			CredentialTypeAPIKey,
			999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vp, err := ParseVaultPath(tt.vaultKey)
			if err != nil {
				t.Fatalf("ParseVaultPath(%q): unexpected error: %v", tt.vaultKey, err)
			}
			if vp.FieldName != tt.wantField {
				t.Errorf("FieldName: got %v, want %v", vp.FieldName, tt.wantField)
			}
			if vp.KeyVersion != tt.wantVer {
				t.Errorf("KeyVersion: got %d, want %d", vp.KeyVersion, tt.wantVer)
			}
			if vp.TenantID.String() != "550e8400-e29b-41d4-a716-446655440000" {
				t.Errorf("TenantID: got %s", vp.TenantID)
			}
			if vp.RecordID.String() != "660e8400-e29b-41d4-a716-446655440001" {
				t.Errorf("RecordID: got %s", vp.RecordID)
			}
		})
	}
}

func TestParseVaultPath_InvalidPaths(t *testing.T) {
	tests := []struct {
		name     string
		vaultKey string
	}{
		{"missing prefix", "http://tenant/record/api_key/1"},
		{"empty", ""},
		{"only prefix", "vault://"},
		{"one segment", "vault://550e8400-e29b-41d4-a716-446655440000"},
		{"two segments", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001"},
		{"three segments", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key"},
		{"five segments", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1/extra"},
		{"invalid tenant uuid", "vault://not-a-uuid/660e8400-e29b-41d4-a716-446655440001/api_key/1"},
		{"invalid record uuid", "vault://550e8400-e29b-41d4-a716-446655440000/not-a-uuid/api_key/1"},
		{"zero version", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/0"},
		{"negative version", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/-1"},
		{"non-numeric version", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/abc"},
		// R21 P0-2: 非 API_KEY field 必须被拒绝
		{"api_secret field rejected", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_secret/1"},
		{"webhook_secret field rejected", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/webhook_secret/1"},
		{"client_cert field rejected", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/client_cert/1"},
		{"unknown field rejected", "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/unknown_field/1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseVaultPath(tt.vaultKey)
			if err == nil {
				t.Errorf("ParseVaultPath(%q): expected error, got nil", tt.vaultKey)
			}
		})
	}
}

// ============================================================================
// R21 P0-2: UNSUPPORTED 在所有模式都不能激活测试
// ============================================================================

func TestValidateProviderActivation_UnsupportedRejectedInAllModes(t *testing.T) {
	validVaultKey := "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1"

	tests := []struct {
		name string
		mode RuntimeMode
	}{
		{"production rejects UNSUPPORTED", RuntimeModeProduction},
		{"local-mock rejects UNSUPPORTED", RuntimeModeLocalMock},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ActivationConfig{
				ProviderCode:       "test-provider",
				PoolCode:           "test-pool",
				CredentialVaultKey: validVaultKey,
				IssuingTier:        "TIER_2_DIRECT",
				FundingMode:        "CRYPTO_DESENSITIZED",
				AdapterType:        "UNSUPPORTED",
			}

			err := ValidateProviderActivation(cfg, tt.mode)

			if err == nil {
				t.Error("UNSUPPORTED adapter_type should be rejected in all modes")
			}
			if !errors.Is(err, ErrAdapterTypeUnsupported) {
				t.Errorf("expected ErrAdapterTypeUnsupported, got: %v", err)
			}
		})
	}
}

// R21 P0-2: 草稿 validator 允许 UNSUPPORTED
func TestValidateDraftProvider_AllowsUnsupported(t *testing.T) {
	cfg := &ActivationConfig{
		ProviderCode: "test-provider",
		PoolCode:     "test-pool",
		IssuingTier:  "TIER_2_DIRECT",
		FundingMode:  "CRYPTO_DESENSITIZED",
		AdapterType:  "UNSUPPORTED",
	}

	err := ValidateDraftProvider(cfg)
	if err != nil {
		t.Errorf("draft validator should allow UNSUPPORTED, got error: %v", err)
	}
}

func TestValidateDraftProvider_RejectsInvalidFields(t *testing.T) {
	// missing provider_code
	err := ValidateDraftProvider(&ActivationConfig{
		PoolCode:    "test-pool",
		IssuingTier: "TIER_2_DIRECT",
		FundingMode: "CRYPTO_DESENSITIZED",
	})
	if err == nil {
		t.Error("draft validator should reject missing provider_code")
	}

	// invalid vault key format
	err = ValidateDraftProvider(&ActivationConfig{
		ProviderCode:       "test-provider",
		PoolCode:           "test-pool",
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		CredentialVaultKey: "not-a-vault-key",
	})
	if err == nil {
		t.Error("draft validator should reject invalid vault key")
	}
}

func TestValidateProviderActivation_ProductionRejectsMockLocal(t *testing.T) {
	validVaultKey := "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1"

	cfg := &ActivationConfig{
		ProviderCode:       "test-provider",
		PoolCode:           "test-pool",
		CredentialVaultKey: validVaultKey,
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		AdapterType:        "MOCK_LOCAL",
		EntityID:           "550e8400-e29b-41d4-a716-446655440000",
	}

	err := ValidateProviderActivation(cfg, RuntimeModeProduction)
	if err == nil {
		t.Fatal("production mode should reject MOCK_LOCAL adapter_type")
	}
	if !errors.Is(err, ErrAdapterTypeUnsupported) {
		t.Errorf("expected ErrAdapterTypeUnsupported, got: %v", err)
	}
}

func TestValidateProviderActivation_LocalMockAllowsMockLocal(t *testing.T) {
	validVaultKey := "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1"

	cfg := &ActivationConfig{
		ProviderCode:       "test-provider",
		PoolCode:           "test-pool",
		CredentialVaultKey: validVaultKey,
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		AdapterType:        "MOCK_LOCAL",
	}

	err := ValidateProviderActivation(cfg, RuntimeModeLocalMock)
	if err != nil {
		t.Errorf("local-mock mode should allow MOCK_LOCAL, got error: %v", err)
	}
}

func TestValidateProviderActivation_InvalidVaultKeyRejected(t *testing.T) {
	cfg := &ActivationConfig{
		ProviderCode:       "test-provider",
		PoolCode:           "test-pool",
		CredentialVaultKey: "not-a-vault-key",
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		AdapterType:        "MOCK_LOCAL",
	}

	err := ValidateProviderActivation(cfg, RuntimeModeLocalMock)
	if err == nil {
		t.Error("invalid vault key should be rejected")
	}
}

func TestValidateProviderActivation_ProductionRequiresEntityID(t *testing.T) {
	validVaultKey := "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1"

	cfg := &ActivationConfig{
		ProviderCode:       "test-provider",
		PoolCode:           "test-pool",
		CredentialVaultKey: validVaultKey,
		IssuingTier:        "TIER_2_DIRECT",
		FundingMode:        "CRYPTO_DESENSITIZED",
		AdapterType:        "NEXUS_ISSUING_HTTP_V1",
		AdapterProviderID:  "770e8400-e29b-41d4-a716-446655440002",
		ProviderConfig:     map[string]interface{}{"base_url": "https://api.example.com"},
		// EntityID empty in production
	}

	err := ValidateProviderActivation(cfg, RuntimeModeProduction)
	if err == nil {
		t.Error("production mode should require non-empty entity_id")
	}
}

// ============================================================================
// R20 P1-3: classifyHTTPStatus 测试
// ============================================================================

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    error
		wantNil    bool
	}{
		{"200 OK", http.StatusOK, nil, true},
		{"201 Created", http.StatusCreated, nil, true},
		{"202 Accepted", http.StatusAccepted, nil, true},
		{"204 No Content", http.StatusNoContent, nil, true},
		{"408 Request Timeout", http.StatusRequestTimeout, ErrProviderTimeout, false},
		{"504 Gateway Timeout", http.StatusGatewayTimeout, ErrProviderTimeout, false},
		{"429 Too Many Requests", http.StatusTooManyRequests, ErrProviderRetryable, false},
		{"500 Internal Server Error", 500, ErrProviderRetryable, false},
		{"502 Bad Gateway", 502, ErrProviderRetryable, false},
		{"503 Service Unavailable", 503, ErrProviderRetryable, false},
		{"400 Bad Request", 400, ErrProviderPermanent, false},
		{"401 Unauthorized", 401, ErrProviderPermanent, false},
		{"403 Forbidden", 403, ErrProviderPermanent, false},
		{"404 Not Found", 404, ErrProviderPermanent, false},
		{"422 Unprocessable Entity", 422, ErrProviderPermanent, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyHTTPStatus(tt.statusCode)
			if tt.wantNil {
				if err != nil {
					t.Errorf("classifyHTTPStatus(%d): expected nil, got %v", tt.statusCode, err)
				}
				return
			}
			if err == nil {
				t.Errorf("classifyHTTPStatus(%d): expected error wrapping %v, got nil", tt.statusCode, tt.wantErr)
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("classifyHTTPStatus(%d): expected error wrapping %v, got %v", tt.statusCode, tt.wantErr, err)
			}
		})
	}
}

func TestClassifyHTTPError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr error
		wantNil bool
	}{
		{"nil error", nil, nil, true},
		{"context.DeadlineExceeded", context.DeadlineExceeded, ErrProviderTimeout, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyHTTPError(tt.err)
			if tt.wantNil {
				if result != nil {
					t.Errorf("classifyHTTPError(%v): expected nil, got %v", tt.err, result)
				}
				return
			}
			if result == nil {
				t.Errorf("classifyHTTPError(%v): expected %v, got nil", tt.err, tt.wantErr)
				return
			}
			if !errors.Is(result, tt.wantErr) {
				t.Errorf("classifyHTTPError(%v): expected %v, got %v", tt.err, tt.wantErr, result)
			}
		})
	}
}

// ============================================================================
// R20 P1-3: RuntimeMode.String() 和 IsProduction() 测试
// ============================================================================

func TestRuntimeModeString(t *testing.T) {
	if RuntimeModeProduction.String() != "production" {
		t.Errorf("RuntimeModeProduction.String() = %q, want %q", RuntimeModeProduction.String(), "production")
	}
	if RuntimeModeLocalMock.String() != "local-mock" {
		t.Errorf("RuntimeModeLocalMock.String() = %q, want %q", RuntimeModeLocalMock.String(), "local-mock")
	}
}

func TestRuntimeModeIsProduction(t *testing.T) {
	if !RuntimeModeProduction.IsProduction() {
		t.Error("RuntimeModeProduction.IsProduction() should be true")
	}
	if RuntimeModeLocalMock.IsProduction() {
		t.Error("RuntimeModeLocalMock.IsProduction() should be false")
	}
}

// ============================================================================
// R24 P0-2: RuntimeMode.Validate() 和 IsLocalMock() 测试
// ============================================================================

func TestRuntimeModeValidate(t *testing.T) {
	if err := RuntimeModeProduction.Validate(); err != nil {
		t.Errorf("RuntimeModeProduction.Validate(): unexpected error: %v", err)
	}
	if err := RuntimeModeLocalMock.Validate(); err != nil {
		t.Errorf("RuntimeModeLocalMock.Validate(): unexpected error: %v", err)
	}
	if err := RuntimeMode(99).Validate(); err == nil {
		t.Error("RuntimeMode(99).Validate(): expected error for unknown mode")
	}
}

func TestRuntimeModeIsLocalMock(t *testing.T) {
	if !RuntimeModeLocalMock.IsLocalMock() {
		t.Error("RuntimeModeLocalMock.IsLocalMock() should be true")
	}
	if RuntimeModeProduction.IsLocalMock() {
		t.Error("RuntimeModeProduction.IsLocalMock() should be false")
	}
	if RuntimeMode(99).IsLocalMock() {
		t.Error("RuntimeMode(99).IsLocalMock() should be false")
	}
}

// TestValidateProviderActivation_UnknownMode_Rejects 验证未知 RuntimeMode 被 ValidateProviderActivation 拒绝。
func TestValidateProviderActivation_UnknownMode_Rejects(t *testing.T) {
	validVaultKey := "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1"

	tests := []struct {
		name        string
		adapterType string
	}{
		{"unknown mode + MOCK_LOCAL", "MOCK_LOCAL"},
		{"unknown mode + HTTP", "NEXUS_ISSUING_HTTP_V1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ActivationConfig{
				ProviderCode:       "test-provider",
				PoolCode:           "test-pool",
				CredentialVaultKey: validVaultKey,
				IssuingTier:        "TIER_2_DIRECT",
				FundingMode:        "CRYPTO_DESENSITIZED",
				AdapterType:        tt.adapterType,
			}
			if tt.adapterType == "NEXUS_ISSUING_HTTP_V1" {
				cfg.AdapterProviderID = "770e8400-e29b-41d4-a716-446655440002"
				cfg.EntityID = "550e8400-e29b-41d4-a716-446655440000"
				cfg.ProviderConfig = map[string]interface{}{"base_url": "https://api.example.com"}
			}

			err := ValidateProviderActivation(cfg, RuntimeMode(99))
			if err == nil {
				t.Fatal("ValidateProviderActivation with unknown RuntimeMode should return error")
			}
			if !containsSubstring(err.Error(), "unknown RuntimeMode") {
				t.Errorf("expected error containing 'unknown RuntimeMode', got: %v", err)
			}
		})
	}
}

// ============================================================================
// R22 Item 7: ValidateProviderOwnershipFacts 全字段表驱动测试
// ============================================================================

func TestValidateProviderOwnershipFacts_TableDriven(t *testing.T) {
	// 构造一组基准合法值，每个 case 只修改需要测试的字段
	tenantID := uuid.New()
	recordID := uuid.New()
	providerID := uuid.New()
	adapterProviderID := uuid.New()
	futureTime := time.Now().Add(24 * time.Hour)

	baseFacts := func() *OwnershipFacts {
		return &OwnershipFacts{
			PathTenantID:           tenantID,
			PathRecordID:           recordID,
			PathField:              CredentialTypeAPIKey,
			PathKeyVersion:         1,
			EntityTenantID:         &tenantID,
			AdapterStatus:          "ACTIVE",
			AdapterType:            "CARD_ISSUER",
			VaultRecordID:          &recordID,
			VaultStatus:            "ACTIVE",
			VaultCredentialType:    "API_KEY",
			VaultTenantID:          &tenantID,
			VaultIssuingProviderID: &providerID,
			VaultProviderID:        &adapterProviderID,
			VaultExpiresAt:         &futureTime,
			VaultKeyVersion:        1,
			ProviderID:             providerID,
			AdapterProviderID:      &adapterProviderID,
		}
	}

	tests := []struct {
		name      string
		mutate    func(facts *OwnershipFacts)
		wantError bool
		errMsg    string
	}{
		{
			name:      "nil facts returns error",
			mutate:    func(f *OwnershipFacts) { *f = OwnershipFacts{} },
			wantError: true,
			errMsg:    "ownership facts is nil",
		},
		{
			name:      "entity tenant null returns error",
			mutate:    func(f *OwnershipFacts) { f.EntityTenantID = nil },
			wantError: true,
			errMsg:    "entity tenant_id is null",
		},
		{
			name:      "path tenant != entity tenant returns error",
			mutate:    func(f *OwnershipFacts) { other := uuid.New(); f.EntityTenantID = &other },
			wantError: true,
			errMsg:    "does not match entity tenant",
		},
		{
			name:      "vault tenant null returns error",
			mutate:    func(f *OwnershipFacts) { f.VaultTenantID = nil },
			wantError: true,
			errMsg:    "vault tenant_id is null",
		},
		{
			name:      "path tenant != vault tenant returns error",
			mutate:    func(f *OwnershipFacts) { other := uuid.New(); f.VaultTenantID = &other },
			wantError: true,
			errMsg:    "does not match vault tenant",
		},
		{
			name:      "vault record_id null returns error",
			mutate:    func(f *OwnershipFacts) { f.VaultRecordID = nil },
			wantError: true,
			errMsg:    "vault record_id is null",
		},
		{
			name:      "path record_id != vault record_id returns error",
			mutate:    func(f *OwnershipFacts) { other := uuid.New(); f.VaultRecordID = &other },
			wantError: true,
			errMsg:    "does not match vault record_id",
		},
		{
			name:      "path key_version != vault key_version returns error",
			mutate:    func(f *OwnershipFacts) { f.VaultKeyVersion = 2 },
			wantError: true,
			errMsg:    "does not match vault key_version",
		},
		{
			name:      "path field != API_KEY returns error",
			mutate:    func(f *OwnershipFacts) { f.PathField = CredentialTypeAPISecret },
			wantError: true,
			errMsg:    "path field must be API_KEY",
		},
		{
			name:      "vault credential_type != API_KEY returns error",
			mutate:    func(f *OwnershipFacts) { f.VaultCredentialType = "API_SECRET" },
			wantError: true,
			errMsg:    "vault credential_type must be API_KEY",
		},
		{
			name:      "adapter not ACTIVE returns error",
			mutate:    func(f *OwnershipFacts) { f.AdapterStatus = "DISABLED" },
			wantError: true,
			errMsg:    "adapter_provider is not ACTIVE",
		},
		{
			name:      "adapter type != CARD_ISSUER returns error",
			mutate:    func(f *OwnershipFacts) { f.AdapterType = "BANK_COLLECTION" },
			wantError: true,
			errMsg:    "adapter_type must be CARD_ISSUER",
		},
		{
			name:      "vault not ACTIVE returns error",
			mutate:    func(f *OwnershipFacts) { f.VaultStatus = "REVOKED" },
			wantError: true,
			errMsg:    "vault credential is not ACTIVE",
		},
		{
			name:      "vault expired returns error",
			mutate:    func(f *OwnershipFacts) { past := time.Now().Add(-1 * time.Hour); f.VaultExpiresAt = &past },
			wantError: true,
			errMsg:    "vault credential is expired",
		},
		{
			name:      "vault issuing_provider_id mismatch returns error",
			mutate:    func(f *OwnershipFacts) { other := uuid.New(); f.VaultIssuingProviderID = &other },
			wantError: true,
			errMsg:    "vault issuing_provider_id mismatch",
		},
		{
			name:      "adapter_provider_id null returns error",
			mutate:    func(f *OwnershipFacts) { f.AdapterProviderID = nil },
			wantError: true,
			errMsg:    "adapter_provider_id is null",
		},
		{
			name:      "vault provider_id mismatch returns error",
			mutate:    func(f *OwnershipFacts) { other := uuid.New(); f.VaultProviderID = &other },
			wantError: true,
			errMsg:    "vault provider_id mismatch",
		},
		{
			name:      "all correct returns nil (success)",
			mutate:    func(f *OwnershipFacts) {},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var facts *OwnershipFacts
			if tt.name == "nil facts returns error" {
				facts = nil
			} else {
				facts = baseFacts()
				tt.mutate(facts)
			}

			err := ValidateProviderOwnershipFacts(facts)
			if tt.wantError {
				if err == nil {
					t.Fatalf("ValidateProviderOwnershipFacts: expected error containing %q, got nil", tt.errMsg)
				}
				if !containsSubstring(err.Error(), tt.errMsg) {
					t.Errorf("ValidateProviderOwnershipFacts: expected error containing %q, got %q", tt.errMsg, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateProviderOwnershipFacts: expected nil, got error: %v", err)
			}
		})
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
