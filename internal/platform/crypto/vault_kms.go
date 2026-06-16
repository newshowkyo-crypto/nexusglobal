package crypto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

type VaultKMS struct {
	baseURL    string
	httpClient *http.Client
	token      string
	mountPath  string
	keyName    string
}

type VaultKMSConfig struct {
	BaseURL   string
	Token     string
	MountPath string
	KeyName   string
	Timeout   time.Duration
}

func NewVaultKMS(cfg VaultKMSConfig) *VaultKMS {
	if cfg.MountPath == "" {
		cfg.MountPath = "transit"
	}
	if cfg.KeyName == "" {
		cfg.KeyName = "mamr-master"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &VaultKMS{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   cfg.Timeout,
		},
		token:     cfg.Token,
		mountPath: cfg.MountPath,
		keyName:   cfg.KeyName,
	}
}

func (v *VaultKMS) DEK(ctx context.Context, tenantID string) ([]byte, error) {
	plaintext := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("mamr-dek-%s-%d", tenantID, time.Now().UnixNano())))

	encryptURL := fmt.Sprintf("%s/v1/%s/encrypt/%s", v.baseURL, v.mountPath, v.keyName)
	body := fmt.Sprintf(`{"plaintext":"%s"}`, plaintext)

	req, err := http.NewRequestWithContext(ctx, "POST", encryptURL, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("vault: create encrypt request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", v.token)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault: encrypt request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault: encrypt returned status %d", resp.StatusCode)
	}

	var encResp struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&encResp); err != nil {
		return nil, fmt.Errorf("vault: decode encrypt response: %w", err)
	}

	decryptURL := fmt.Sprintf("%s/v1/%s/decrypt/%s", v.baseURL, v.mountPath, v.keyName)
	decBody := fmt.Sprintf(`{"ciphertext":"%s"}`, encResp.Data.Ciphertext)

	decReq, err := http.NewRequestWithContext(ctx, "POST", decryptURL, strings.NewReader(decBody))
	if err != nil {
		return nil, fmt.Errorf("vault: create decrypt request: %w", err)
	}
	decReq.Header.Set("Content-Type", "application/json")
	decReq.Header.Set("X-Vault-Token", v.token)

	decResp, err := v.httpClient.Do(decReq)
	if err != nil {
		return nil, fmt.Errorf("vault: decrypt request: %w", err)
	}
	defer decResp.Body.Close()

	if decResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault: decrypt returned status %d", decResp.StatusCode)
	}

	var decResult struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.NewDecoder(decResp.Body).Decode(&decResult); err != nil {
		return nil, fmt.Errorf("vault: decode decrypt response: %w", err)
	}

	rawDEK, err := base64.StdEncoding.DecodeString(decResult.Data.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("vault: decode DEK: %w", err)
	}

	dek := make([]byte, 32)
	copy(dek, rawDEK)
	for i, b := range []byte(tenantID) {
		dek[i%32] ^= b
	}
	return dek, nil
}
