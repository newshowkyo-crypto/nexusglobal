package factories

import (
	"fmt"
	"time"

	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	bankPkg "github.com/nexusglobal/mamr/pkg/external/bank"
	lpPkg "github.com/nexusglobal/mamr/pkg/external/lp"
	maePkg "github.com/nexusglobal/mamr/pkg/external/mae"
)

type ExternalDeps struct {
	LP   lpPkg.Client
	Bank bankPkg.Client
	MAE  maePkg.Client
	KMS  crypto.KMS
}

func NewExternalDeps(cfg *config.Config) (*ExternalDeps, error) {
	lpClient, err := newLPClient(cfg)
	if err != nil {
		return nil, err
	}

	bankClient, err := newBankClient(cfg)
	if err != nil {
		return nil, err
	}

	maeClient, err := newMAEClient(cfg)
	if err != nil {
		return nil, err
	}

	kms, err := newKMS(cfg)
	if err != nil {
		return nil, err
	}

	return &ExternalDeps{
		LP:   lpClient,
		Bank: bankClient,
		MAE:  maeClient,
		KMS:  kms,
	}, nil
}

func newLPClient(cfg *config.Config) (lpPkg.Client, error) {
	if cfg.ExternalMode == "real" {
		if cfg.LPBaseURL == "" {
			return nil, fmt.Errorf("factories: EXTERNAL_MODE=real requires LP_BASE_URL")
		}
		return lpPkg.NewHTTPClient(cfg.LPBaseURL, cfg.LPAPIKey, 30*time.Second), nil
	}
	return lpPkg.NewMockLP(), nil
}

func newBankClient(cfg *config.Config) (bankPkg.Client, error) {
	if cfg.ExternalMode == "real" {
		if cfg.BankBaseURL == "" {
			return nil, fmt.Errorf("factories: EXTERNAL_MODE=real requires BANK_BASE_URL")
		}
		return bankPkg.NewHTTPClient(cfg.BankBaseURL, cfg.BankAPIKey, 30*time.Second), nil
	}
	return bankPkg.NewMockBank(), nil
}

func newMAEClient(cfg *config.Config) (maePkg.Client, error) {
	if cfg.ExternalMode == "real" {
		if cfg.MAEBaseURL == "" {
			return nil, fmt.Errorf("factories: EXTERNAL_MODE=real requires MAE_BASE_URL")
		}
		return maePkg.NewHTTPClient(cfg.MAEBaseURL, cfg.MAEAPIKey, 30*time.Second), nil
	}
	return maePkg.NewMockMAE(), nil
}

func newKMS(cfg *config.Config) (crypto.KMS, error) {
	if cfg.KMSMode == "real" {
		if cfg.VaultBaseURL == "" {
			return nil, fmt.Errorf("factories: KMS_MODE=real requires VAULT_BASE_URL")
		}
		return crypto.NewVaultKMS(crypto.VaultKMSConfig{
			BaseURL:   cfg.VaultBaseURL,
			Token:     cfg.VaultToken,
			MountPath: cfg.VaultMountPath,
			KeyName:   cfg.VaultKeyName,
		}), nil
	}
	return crypto.NewMockKMS(cfg.MasterKeyBase64)
}
