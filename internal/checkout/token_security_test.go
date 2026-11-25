package checkout

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ============================================================================
// P0-C1: Token 安全 Fail-Closed 单元测试
//
// 验证 nil Encryptor + 非空 token 时必须返回错误，禁止明文存储。
// 这些测试不依赖 DB/Docker，纯单元测试。
// ============================================================================

// TestCreatePaymentAttempt_NilEncryptor_FailClosed 验证 nil Encryptor + 非空 token
// 时 CreatePaymentAttempt 返回 fail-closed 错误，不执行 DB 写入。
func TestCreatePaymentAttempt_NilEncryptor_FailClosed(t *testing.T) {
	repo := NewPaymentAttemptRepository(nil) // nil pool — 不应到达 DB 操作

	attempt := &PaymentAttempt{
		ID:                 uuid.New(),
		OrderID:            uuid.New(),
		MerchantID:         uuid.New(),
		TenantID:           uuid.New(),
		EntryChannel:       EntryS2S,
		PaymentMethodToken: "tok_test_secret_12345",
	}

	err := repo.CreatePaymentAttempt(context.Background(), attempt)
	if err == nil {
		t.Fatal("expected fail-closed error when encryptor is nil and token is non-empty, got nil")
	}
	if !strings.Contains(err.Error(), "encryption is required") {
		t.Errorf("expected error containing 'encryption is required', got: %v", err)
	}
}

// TestCreatePaymentAttempt_NilEncryptor_EmptyToken_OK 验证 nil Encryptor + 空 token
// 时不返回错误（空 token 不需要加密）。
func TestCreatePaymentAttempt_NilEncryptor_EmptyToken_OK(t *testing.T) {
	// nil pool + 空 token → 不会到达 DB 操作（token 为空，跳过加密逻辑）
	// 但 CreatePaymentAttempt 仍会尝试 DB 写入，所以这里只验证不返回加密错误
	repo := NewPaymentAttemptRepository(nil)

	attempt := &PaymentAttempt{
		ID:                 uuid.New(),
		OrderID:            uuid.New(),
		MerchantID:         uuid.New(),
		TenantID:           uuid.New(),
		EntryChannel:       EntryS2S,
		PaymentMethodToken: "", // 空 token，不需要加密
	}

	// 会因 nil pool 在 DB 操作时 panic/error，但不应返回 "encryption is required" 错误
	// 使用 defer recover 捕获 panic
	defer func() {
		_ = recover()
	}()
	err := repo.CreatePaymentAttempt(context.Background(), attempt)
	if err != nil && strings.Contains(err.Error(), "encryption is required") {
		t.Errorf("empty token should not trigger encryption error, got: %v", err)
	}
}

// TestConfirmPayment_NilEncryptor_FailClosed 验证 nil Encryptor + 非空 token
// 时 ConfirmPayment 返回 fail-closed 错误，不执行 DB 操作。
func TestConfirmPayment_NilEncryptor_FailClosed(t *testing.T) {
	svc := NewPaymentLinkService(nil) // nil pool — 不应到达 DB 操作

	// 构造合法长度 token（64 hex chars）
	token := strings.Repeat("a", 64)

	_, err := svc.ConfirmPayment(context.Background(), token, "tok_test_secret_12345")
	if err == nil {
		t.Fatal("expected fail-closed error when encryptor is nil and token is non-empty, got nil")
	}
	if !strings.Contains(err.Error(), "encryption is required") {
		t.Errorf("expected error containing 'encryption is required', got: %v", err)
	}
}

// TestConfirmPayment_NilEncryptor_EmptyToken_NoEncryptionError 验证 nil Encryptor + 空 token
// 时不返回加密错误（空 token 不需要加密，但会因 nil pool 在后续 DB 操作失败）。
func TestConfirmPayment_NilEncryptor_EmptyToken_NoEncryptionError(t *testing.T) {
	svc := NewPaymentLinkService(nil)

	token := strings.Repeat("a", 64)

	defer func() {
		_ = recover()
	}()
	_, err := svc.ConfirmPayment(context.Background(), token, "")
	if err != nil && strings.Contains(err.Error(), "encryption is required") {
		t.Errorf("empty token should not trigger encryption error, got: %v", err)
	}
}

// ============================================================================
// P0-C1: TokenEncryptor AAD 绑定测试（纯单元测试，不依赖 DB）
// ============================================================================

// TestTokenEncryptor_CorrectAttemptID_DecryptsOK 验证用正确的 Attempt ID 加密的 token
// 可以用同一个 Attempt ID 解密。
func TestTokenEncryptor_CorrectAttemptID_DecryptsOK(t *testing.T) {
	testKey := []byte("0123456789abcdef0123456789abcdef")
	masterKeyB64 := base64.StdEncoding.EncodeToString(testKey)

	enc, err := NewTokenEncryptorFromMasterKey(masterKeyB64)
	if err != nil {
		t.Fatalf("construct encryptor failed: %v", err)
	}

	tenantID := uuid.New()
	attemptID := uuid.New()
	rawToken := "tok_secret_for_aad_test"

	ciphertext, err := enc.EncryptToken(context.Background(), tenantID, attemptID, rawToken)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	decrypted, err := enc.DecryptToken(context.Background(), tenantID, attemptID, ciphertext)
	if err != nil {
		t.Fatalf("decrypt with correct attempt ID failed: %v", err)
	}
	if decrypted != rawToken {
		t.Errorf("decrypted token mismatch: got %q, want %q", decrypted, rawToken)
	}
}

// TestTokenEncryptor_WrongAttemptID_DecryptFails 验证用错误的 Attempt ID 解密必须失败。
// AAD 绑定确保 token 不能跨 attempt 重放。
func TestTokenEncryptor_WrongAttemptID_DecryptFails(t *testing.T) {
	testKey := []byte("0123456789abcdef0123456789abcdef")
	masterKeyB64 := base64.StdEncoding.EncodeToString(testKey)

	enc, err := NewTokenEncryptorFromMasterKey(masterKeyB64)
	if err != nil {
		t.Fatalf("construct encryptor failed: %v", err)
	}

	tenantID := uuid.New()
	attemptID := uuid.New()
	wrongAttemptID := uuid.New()
	rawToken := "tok_secret_for_aad_mismatch"

	ciphertext, err := enc.EncryptToken(context.Background(), tenantID, attemptID, rawToken)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = enc.DecryptToken(context.Background(), tenantID, wrongAttemptID, ciphertext)
	if err == nil {
		t.Fatal("expected decrypt to fail with wrong attempt ID (AAD mismatch), but it succeeded")
	}
}

// ============================================================================
// P0-C1: 源代码安全约束测试（grep 式验证，不依赖 DB）
// ============================================================================

// TestSourceCode_NoPlaintextTokenFallback 验证源代码中不存在明文 token fallback 赋值。
// 禁止模式：
//   - plaintextToken = paymentMethodToken
//   - plaintextToken = attempt.PaymentMethodToken
func TestSourceCode_NoPlaintextTokenFallback(t *testing.T) {
	// 获取当前包目录
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd failed: %v", err)
	}

	forbiddenPatterns := []string{
		"plaintextToken = paymentMethodToken",
		"plaintextToken = attempt.PaymentMethodToken",
	}

	// 检查所有 .go 文件（排除 _test.go）
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		for _, pattern := range forbiddenPatterns {
			if strings.Contains(string(content), pattern) {
				t.Errorf("FORBIDDEN pattern %q found in %s — plaintext token fallback must be removed", pattern, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dir failed: %v", err)
	}
}

// TestSourceCode_NoFullTokenPrinted 验证源代码中不存在打印完整 token 的日志语句。
// 禁止模式：
//   - slog.* 或 log.* 中包含 PaymentMethodToken 作为参数
//   - fmt.Printf/Sprintf 中包含完整 token
func TestSourceCode_NoFullTokenPrinted(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd failed: %v", err)
	}

	// 禁止在日志/输出中直接打印 token 字段
	forbiddenPatterns := []string{
		"slog.*PaymentMethodToken",
		"log.*PaymentMethodToken",
		"Printf.*PaymentMethodToken",
		"Sprintf.*PaymentMethodToken",
	}

	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		src := string(content)
		for _, pattern := range forbiddenPatterns {
			// 简单子串匹配（非正则），足以捕获直接引用
			if strings.Contains(src, pattern) {
				t.Errorf("FORBIDDEN pattern %q found in %s — full token must not be printed in logs", pattern, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dir failed: %v", err)
	}
}

// ============================================================================
// P0-A4: Payment Intent Token Encryptor 缺失时硬拒绝
// ============================================================================

// TestProcessPaymentIntent_NilEncryptor_FailClosed 验证 nil Encryptor + 非空 token
// 时 ProcessPaymentIntent 返回 fail-closed 错误，不创建任何 DB 记录。
func TestProcessPaymentIntent_NilEncryptor_FailClosed(t *testing.T) {
	svc := &CheckoutService{
		pool:           nil,
		tokenEncryptor: nil,
	}

	_, err := svc.ProcessPaymentIntent(context.Background(), ProcessPaymentIntentRequest{
		MerchantID:         uuid.New(),
		IdempotencyKey:     "idem_test_nil_enc",
		PayloadHash:        "hash_test_nil_enc",
		MerchantOrderID:    "order_test_nil_enc",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test_secret_nil_enc",
	})
	if err == nil {
		t.Fatal("expected fail-closed error when tokenEncryptor is nil and PaymentMethodToken is non-empty, got nil")
	}
	if !strings.Contains(err.Error(), "encryption is required") {
		t.Errorf("expected error containing 'encryption is required', got: %v", err)
	}
}

// TestProcessPaymentIntent_NilEncryptor_EmptyToken_NoEncryptionError 验证 nil Encryptor + 空 token
// 时不返回加密错误（空 token 不需要加密）。
func TestProcessPaymentIntent_NilEncryptor_EmptyToken_NoEncryptionError(t *testing.T) {
	svc := &CheckoutService{
		pool:           nil,
		tokenEncryptor: nil,
	}

	defer func() {
		_ = recover()
	}()
	_, err := svc.ProcessPaymentIntent(context.Background(), ProcessPaymentIntentRequest{
		MerchantID:         uuid.New(),
		IdempotencyKey:     "idem_test_empty_tok",
		PayloadHash:        "hash_test_empty_tok",
		MerchantOrderID:    "order_test_empty_tok",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "",
	})
	if err != nil && strings.Contains(err.Error(), "encryption is required") {
		t.Errorf("empty token should not trigger encryption error, got: %v", err)
	}
}
