package config

import (
	"os"
	"testing"
)

func TestLoad_ProductionRejectsMockProviders(t *testing.T) {
	tests := []struct {
		name           string
		envVars        map[string]string
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "production rejects KMS_MODE=mock",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "mock",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "KMS_MODE=mock is not allowed in production",
		},
		{
			name: "production rejects EXTERNAL_MODE=mock",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "mock",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "EXTERNAL_MODE=mock is not allowed in production",
		},
		{
			name: "production rejects ACQUIRER_MODE=mock",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "mock",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "ACQUIRER_MODE=mock is not allowed in production",
		},
		{
			name: "production rejects ACQUIRER_PROVIDER=mock",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "mock",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "ACQUIRER_PROVIDER=mock is not allowed in production",
		},
		{
			name: "production rejects ISSUING_PROVIDER=mock",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "mock",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "ISSUING_PROVIDER=mock is not allowed in production",
		},
		{
			name: "production rejects VCC_FUNDING_PROVIDER=mock",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "mock",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "VCC_FUNDING_PROVIDER=mock is not allowed in production",
		},
		{
			name: "production rejects CHECKOUT_ORDER_PROVIDER=mock",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "mock",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "CHECKOUT_ORDER_PROVIDER=mock is not allowed in production",
		},
		{
			name: "production rejects empty JWT_SECRET",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "JWT_SECRET is required in production",
		},
		{
			name: "production rejects short JWT_SECRET",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "short",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "JWT_SECRET must be at least 32 bytes in production",
		},
		{
			name: "production rejects empty ADMIN_SESSION_SECRET",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "ADMIN_SESSION_SECRET is required in production",
		},
		{
			name: "production rejects missing VAULT_BASE_URL when REQUIRE_VAULT=true",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "",
				"VAULT_TOKEN":             "vault_token",
				"REQUIRE_VAULT":           "true",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "VAULT_BASE_URL is required when REQUIRE_VAULT=true in production",
		},
		{
			name: "production rejects missing VAULT_TOKEN when REQUIRE_VAULT=true",
			envVars: map[string]string{
				"APP_ENV":                 "production",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"JWT_SECRET":              "a_very_long_secret_key_that_is_at_least_32_bytes",
				"INTERNAL_TOKEN":          "a_very_long_internal_token_that_is_at_least_32_bytes",
				"ADMIN_SESSION_SECRET":    "a_very_long_admin_secret_that_is_at_least_32_bytes",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "",
				"REQUIRE_VAULT":           "true",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expectError:    true,
			expectedErrMsg: "VAULT_TOKEN is required when REQUIRE_VAULT=true in production",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldEnv := make(map[string]string)
			for k, v := range tc.envVars {
				oldEnv[k] = os.Getenv(k)
				os.Setenv(k, v)
			}
			defer func() {
				for k, v := range oldEnv {
					os.Setenv(k, v)
				}
			}()

			_, err := Load()
			if tc.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				} else if tc.expectedErrMsg != "" && !contains(err.Error(), tc.expectedErrMsg) {
					t.Errorf("Expected error containing %q, got %q", tc.expectedErrMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestLoad_LocalAllowsMockProviders(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		expected bool
	}{
		{
			name: "local allows all mock providers",
			envVars: map[string]string{
				"APP_ENV":                 "local",
				"KMS_MODE":                "mock",
				"EXTERNAL_MODE":           "mock",
				"ACQUIRER_MODE":           "mock",
				"ACQUIRER_PROVIDER":       "mock",
				"ISSUING_PROVIDER":        "mock",
				"VCC_FUNDING_PROVIDER":    "mock",
				"CHECKOUT_ORDER_PROVIDER": "mock",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expected: true,
		},
		{
			name: "local allows mixed mock and real",
			envVars: map[string]string{
				"APP_ENV":                 "local",
				"KMS_MODE":                "real",
				"EXTERNAL_MODE":           "mock",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "mock",
				"VCC_FUNDING_PROVIDER":    "mock",
				"CHECKOUT_ORDER_PROVIDER": "http",
				"VAULT_BASE_URL":          "https://vault.example.com",
				"VAULT_TOKEN":             "vault_token",
				"POSTGRES_DSN":            "postgres://test:test@localhost/test",
				"MASTER_KEY_BASE64":       "dGVzdG1hc3RlcmtleTEyMzQ1Njc4OTA=",
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldEnv := make(map[string]string)
			for k, v := range tc.envVars {
				oldEnv[k] = os.Getenv(k)
				os.Setenv(k, v)
			}
			defer func() {
				for k, v := range oldEnv {
					os.Setenv(k, v)
				}
			}()

			cfg, err := Load()
			if tc.expected {
				if err != nil {
					t.Errorf("Expected success, got error: %v", err)
				}
				if cfg == nil {
					t.Error("Expected config, got nil")
				}
			} else {
				if err == nil {
					t.Error("Expected error, got nil")
				}
			}
		})
	}
}

func TestIsProduction(t *testing.T) {
	oldAppEnv := os.Getenv("APP_ENV")
	defer os.Setenv("APP_ENV", oldAppEnv)

	tests := []struct {
		name     string
		appEnv   string
		expected bool
	}{
		{"production returns true", "production", true},
		{"local returns false", "local", false},
		{"staging returns false", "staging", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			os.Setenv("APP_ENV", tc.appEnv)
			result := IsProduction()
			if result != tc.expected {
				t.Errorf("Expected %v for APP_ENV=%s, got %v", tc.expected, tc.appEnv, result)
			}
		})
	}
}

func TestIsMockMode(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		expected bool
	}{
		{
			name: "all real providers returns false",
			envVars: map[string]string{
				"EXTERNAL_MODE":           "real",
				"KMS_MODE":                "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "stripe",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
			},
			expected: false,
		},
		{
			name: "any mock provider returns true",
			envVars: map[string]string{
				"EXTERNAL_MODE":           "real",
				"KMS_MODE":                "real",
				"ACQUIRER_MODE":           "real",
				"ACQUIRER_PROVIDER":       "mock",
				"ISSUING_PROVIDER":        "real_provider",
				"VCC_FUNDING_PROVIDER":    "crypto_bridge",
				"CHECKOUT_ORDER_PROVIDER": "http",
			},
			expected: true,
		},
		{
			name: "all mock providers returns true",
			envVars: map[string]string{
				"EXTERNAL_MODE":           "mock",
				"KMS_MODE":                "mock",
				"ACQUIRER_MODE":           "mock",
				"ACQUIRER_PROVIDER":       "mock",
				"ISSUING_PROVIDER":        "mock",
				"VCC_FUNDING_PROVIDER":    "mock",
				"CHECKOUT_ORDER_PROVIDER": "mock",
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldEnv := make(map[string]string)
			for k, v := range tc.envVars {
				oldEnv[k] = os.Getenv(k)
				os.Setenv(k, v)
			}
			defer func() {
				for k, v := range oldEnv {
					os.Setenv(k, v)
				}
			}()

			result := IsMockMode()
			if result != tc.expected {
				t.Errorf("Expected %v, got %v", tc.expected, result)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
