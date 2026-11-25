// Package crypto — VaultKMS：HashiCorp Vault transit 引擎实现的信封加密 KMS。
//
// 协议（标准 envelope encryption，SEC-001 P0 修复）：
//   - GenerateDEK: POST /v1/{mount}/datakey/plaintext/{key}
//     Vault 生成随机 256-bit DEK，返回 {plaintext: b64, ciphertext: "vault:v1:..."}。
//     ciphertext 即 wrapped DEK，随业务密文持久化。
//   - UnwrapDEK: POST /v1/{mount}/decrypt/{key} {ciphertext}
//     Vault 解包还原 DEK 明文（b64）。
//
// 旧实现（每次调用生成含纳秒时间戳的新 DEK 再加密/解密往返）已被替换：
// 旧逻辑下加密与解密必然拿到不同 DEK，密文写后不可读。新实现中 wrapped DEK
// 存于密文信封内，跨进程、跨调用、跨 key version 均可稳定解密（见 crypto_test.go
// 与 vault_kms_test.go 的证据测试）。
package crypto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// GenerateDEK 调用 Vault transit datakey API 生成随机 DEK。
// 返回 plaintextDEK（32 字节原始密钥）与 wrappedDEK（Vault ciphertext 字符串字节）。
func (v *VaultKMS) GenerateDEK(ctx context.Context, _ string) ([]byte, []byte, error) {
	url := fmt.Sprintf("%s/v1/%s/datakey/plaintext/%s", v.baseURL, v.mountPath, v.keyName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"bits":256}`))
	if err != nil {
		return nil, nil, fmt.Errorf("vault: create datakey request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", v.token)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("vault: datakey request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("vault: read datakey response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("vault: datakey returned status %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Plaintext  string `json:"plaintext"`  // b64 编码的 DEK 明文
			Ciphertext string `json:"ciphertext"` // "vault:v1:..." wrapped 形式
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, nil, fmt.Errorf("vault: decode datakey response: %w", err)
	}
	if result.Data.Plaintext == "" || result.Data.Ciphertext == "" {
		return nil, nil, fmt.Errorf("vault: datakey response missing plaintext/ciphertext")
	}

	dek, err := base64.StdEncoding.DecodeString(result.Data.Plaintext)
	if err != nil {
		return nil, nil, fmt.Errorf("vault: decode DEK plaintext: %w", err)
	}
	if len(dek) != 32 {
		return nil, nil, fmt.Errorf("vault: datakey DEK must be 32 bytes, got %d", len(dek))
	}
	return dek, []byte(result.Data.Ciphertext), nil
}

// UnwrapDEK 调用 Vault transit decrypt API 解包 wrapped DEK。
func (v *VaultKMS) UnwrapDEK(ctx context.Context, _ string, wrappedDEK []byte) ([]byte, error) {
	if len(wrappedDEK) == 0 {
		return nil, fmt.Errorf("vault: wrapped DEK is empty")
	}
	url := fmt.Sprintf("%s/v1/%s/decrypt/%s", v.baseURL, v.mountPath, v.keyName)
	payload, err := json.Marshal(map[string]string{"ciphertext": string(wrappedDEK)})
	if err != nil {
		return nil, fmt.Errorf("vault: marshal unwrap payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("vault: create decrypt request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", v.token)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault: decrypt request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("vault: read decrypt response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault: decrypt returned status %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Plaintext string `json:"plaintext"` // b64 编码的 DEK 明文
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("vault: decode decrypt response: %w", err)
	}
	dek, err := base64.StdEncoding.DecodeString(result.Data.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("vault: decode DEK plaintext: %w", err)
	}
	if len(dek) != 32 {
		return nil, fmt.Errorf("vault: unwrapped DEK must be 32 bytes, got %d", len(dek))
	}
	return dek, nil
}
