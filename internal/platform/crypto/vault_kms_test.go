// vault_kms_test.go — SEC-001 P0 证据测试。
//
// 用 httptest 伪造 Vault transit 端点，验证：
//  1. GenerateDEK → Encrypt → Decrypt 跨 Encryptor 实例（模拟跨进程）可解密。
//  2. wrapped DEK 被篡改 / Vault 解包失败时 fail closed。
//  3. Vault datakey 不可用时 Encrypt 失败（不产生不可解密的密文）。
//  4. VaultKMS 不支持 StableDEK（legacy v1 密文解密显式报错，不静默兜底）。
package crypto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVault 模拟 Vault transit 引擎。
// 内部用 MockKMS 的 GenerateDEK 生成 (DEK, wrapped)，并登记 wrapped→DEK
// 映射（等价于 Vault 内部 KEK 加密存储）；decrypt 端点按映射还原。
type fakeVault struct {
	mu           sync.Mutex
	datakeyOK    bool
	decryptOK    bool
	dekByWrapped map[string][]byte
}

func newFakeVault() *fakeVault {
	return &fakeVault{
		datakeyOK:    true,
		decryptOK:    true,
		dekByWrapped: make(map[string][]byte),
	}
}

func (f *fakeVault) setDecryptOK(ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decryptOK = ok
}

func (f *fakeVault) setDatakeyOK(ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.datakeyOK = ok
}

func (f *fakeVault) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/transit/datakey/plaintext/mamr-master", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.datakeyOK
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		kek := make([]byte, 32) // 测试 KEK：全零 32 字节
		dek, wrapped := sealWithKEK(kek)
		f.mu.Lock()
		f.dekByWrapped[string(wrapped)] = dek
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]string{
				"plaintext":  base64.StdEncoding.EncodeToString(dek),
				"ciphertext": "vault:v1:" + base64.RawStdEncoding.EncodeToString(wrapped),
			},
		})
	})

	mux.HandleFunc("POST /v1/transit/decrypt/mamr-master", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.decryptOK
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req struct {
			Ciphertext string `json:"ciphertext"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		const prefix = "vault:v1:"
		if len(req.Ciphertext) <= len(prefix) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		wrapped, err := base64.RawStdEncoding.DecodeString(req.Ciphertext[len(prefix):])
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		dek, known := f.dekByWrapped[string(wrapped)]
		f.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]string{
				"plaintext": base64.StdEncoding.EncodeToString(dek),
			},
		})
	})

	return mux
}

// sealWithKEK 用固定 KEK 对随机 DEK 做 AES-GCM 包装（与 MockKMS.GenerateDEK 同构）。
func sealWithKEK(kek []byte) (dek, wrapped []byte) {
	mk := &MockKMS{masterKey: kek}
	dek, wrapped, err := mk.GenerateDEK(context.Background(), "")
	if err != nil {
		panic(err) // 测试 KEK 固定合法，不会发生
	}
	return dek, wrapped
}

func newVaultTestServer(t *testing.T) (*httptest.Server, *fakeVault) {
	t.Helper()
	fv := newFakeVault()
	srv := httptest.NewServer(fv.handler())
	t.Cleanup(srv.Close)
	return srv, fv
}

func TestVaultKMS_EnvelopeRoundTrip_CrossInstance(t *testing.T) {
	srv, _ := newVaultTestServer(t)
	kms := NewVaultKMS(VaultKMSConfig{BaseURL: srv.URL, Token: "test"})

	enc1 := NewEncryptor(kms)
	enc2 := NewEncryptor(kms)
	ctx := context.Background()
	meta := testMeta()

	ct, err := enc1.Encrypt(ctx, []byte("vault-secret"), meta)
	require.NoError(t, err)
	require.True(t, hasEnvelopeMagic(ct), "new ciphertext must use envelope v2 format")

	got, err := enc2.Decrypt(ctx, ct, meta)
	require.NoError(t, err, "decrypt must succeed with a different Encryptor instance (cross-process)")
	assert.Equal(t, []byte("vault-secret"), got)
}

func TestVaultKMS_UnwrapFailClosed(t *testing.T) {
	srv, fv := newVaultTestServer(t)
	kms := NewVaultKMS(VaultKMSConfig{BaseURL: srv.URL, Token: "test"})
	enc := NewEncryptor(kms)
	ctx := context.Background()
	meta := testMeta()

	ct, err := enc.Encrypt(ctx, []byte("secret"), meta)
	require.NoError(t, err)

	// 破坏 wrapped DEK 区段 → Vault decrypt 失败 → 解密报错。
	bad := append([]byte{}, ct...)
	bad[6] ^= 0xFF
	_, err = enc.Decrypt(ctx, bad, meta)
	assert.Error(t, err)

	// Vault decrypt 端点 5xx → fail closed。
	fv.setDecryptOK(false)
	_, err = enc.Decrypt(ctx, ct, meta)
	assert.Error(t, err)
}

func TestVaultKMS_DatakeyFailClosed(t *testing.T) {
	srv, fv := newVaultTestServer(t)
	fv.setDatakeyOK(false)
	kms := NewVaultKMS(VaultKMSConfig{BaseURL: srv.URL, Token: "test"})
	enc := NewEncryptor(kms)

	_, err := enc.Encrypt(context.Background(), []byte("secret"), testMeta())
	assert.Error(t, err, "encrypt must fail closed when Vault cannot issue a datakey")
}

func TestVaultKMS_NoStableDEK(t *testing.T) {
	srv, _ := newVaultTestServer(t)
	kms := NewVaultKMS(VaultKMSConfig{BaseURL: srv.URL, Token: "test"})

	var k KMS = kms
	_, ok := k.(StableDEKSource)
	assert.False(t, ok, "VaultKMS must not claim StableDEK support")

	enc := NewEncryptor(kms)
	_, err := enc.Decrypt(context.Background(), []byte{0x00, 0x01, 0x02, 0x03, 0x04}, testMeta())
	assert.Error(t, err, "legacy v1 ciphertext with VaultKMS must fail closed with explicit error")
}
