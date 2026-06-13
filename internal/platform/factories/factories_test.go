package factories

import (
	"testing"

	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewExternalDeps_MockMode(t *testing.T) {
	cfg := &config.Config{
		ExternalMode:    "mock",
		MasterKeyBase64: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}

	deps, err := NewExternalDeps(cfg)
	require.NoError(t, err)
	assert.NotNil(t, deps)
	assert.NotNil(t, deps.LP)
	assert.NotNil(t, deps.Bank)
	assert.NotNil(t, deps.MAE)
	assert.NotNil(t, deps.KMS)
}

func TestNewExternalDeps_RealMode_MissingURLs(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr string
	}{
		{
			name: "missing LP_BASE_URL",
			cfg: &config.Config{
				ExternalMode: "real",
				LPBaseURL:    "",
			},
			wantErr: "factories: EXTERNAL_MODE=real requires LP_BASE_URL",
		},
		{
			name: "missing BANK_BASE_URL",
			cfg: &config.Config{
				ExternalMode: "real",
				LPBaseURL:    "http://lp.test",
				BankBaseURL:  "",
			},
			wantErr: "factories: EXTERNAL_MODE=real requires BANK_BASE_URL",
		},
		{
			name: "missing MAE_BASE_URL",
			cfg: &config.Config{
				ExternalMode: "real",
				LPBaseURL:    "http://lp.test",
				BankBaseURL:  "http://bank.test",
				MAEBaseURL:   "",
			},
			wantErr: "factories: EXTERNAL_MODE=real requires MAE_BASE_URL",
		},
		{
			name: "missing VAULT_BASE_URL",
			cfg: &config.Config{
				ExternalMode: "real",
				LPBaseURL:    "http://lp.test",
				BankBaseURL:  "http://bank.test",
				MAEBaseURL:   "http://mae.test",
				KMSMode:      "real",
				VaultBaseURL: "",
			},
			wantErr: "factories: KMS_MODE=real requires VAULT_BASE_URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewExternalDeps(tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNewExternalDeps_RealMode_Success(t *testing.T) {
	cfg := &config.Config{
		ExternalMode:    "real",
		LPBaseURL:       "http://lp.test",
		LPAPIKey:        "lp-key",
		BankBaseURL:     "http://bank.test",
		BankAPIKey:      "bank-key",
		MAEBaseURL:      "http://mae.test",
		MAEAPIKey:       "mae-key",
		KMSMode:         "mock",
		MasterKeyBase64: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}

	deps, err := NewExternalDeps(cfg)
	require.NoError(t, err)
	assert.NotNil(t, deps)
	assert.NotNil(t, deps.LP)
	assert.NotNil(t, deps.Bank)
	assert.NotNil(t, deps.MAE)
	assert.NotNil(t, deps.KMS)
}
