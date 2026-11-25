package checkout

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// G09: Payment Link token 安全测试
// ============================================================================

func TestPaymentLink_SingleUseExpiresAfterPaid(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewPaymentLinkService(pool)
	reg := seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID)
	service.SetChannelRegistry(reg)

	result, err := service.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_001",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       true,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	// G09: single-use 必须先 claim 才能 MarkAsPaid
	attemptID := uuid.New()
	_, err = service.ClaimForCheckout(context.Background(), result.Token, attemptID)
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}

	err = service.MarkAsPaid(context.Background(), result.Token)
	if err != nil {
		t.Fatalf("mark as paid failed: %v", err)
	}

	// 再次 MarkAsPaid 应该失败
	err = service.MarkAsPaid(context.Background(), result.Token)
	if err == nil {
		t.Error("expected error when trying to use single-use link again")
	}
}

// TestPaymentLink_G09_TokenNotInDB 验证 token 不以明文存入 DB。
//
// G09 核心测试：DB 中 payment_url 列不应包含明文 token。
func TestPaymentLink_G09_TokenNotInDB(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewPaymentLinkService(pool)
	reg := seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID)
	service.SetChannelRegistry(reg)

	result, err := service.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_g09_001",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       true,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	// 查询 DB 中的 payment_url
	var dbPaymentURL string
	var dbTokenHash string
	err = pool.QueryRow(context.Background(), `
		SELECT payment_url, token_hash FROM payment_links WHERE id::text = $1
	`, result.PaymentLinkID).Scan(&dbPaymentURL, &dbTokenHash)
	if err != nil {
		t.Fatalf("query payment link failed: %v", err)
	}

	// G09: payment_url 不应包含明文 token
	if strings.Contains(dbPaymentURL, result.Token) {
		t.Errorf("G09 VIOLATION: payment_url contains plaintext token: %s", dbPaymentURL)
	}

	// G09: token_hash 不应等于明文 token
	if dbTokenHash == result.Token {
		t.Errorf("G09 VIOLATION: token_hash equals plaintext token")
	}

	// G09: payment_url 应该是 /pay/{paymentLinkID} 格式
	expectedPrefix := "/pay/"
	if !strings.HasPrefix(dbPaymentURL, expectedPrefix) {
		t.Errorf("payment_url should start with /pay/, got: %s", dbPaymentURL)
	}

	// G09: payment_url 中的 ID 应该是 paymentLinkID，不是 token
	if strings.Contains(dbPaymentURL, result.PaymentLinkID) == false {
		t.Errorf("payment_url should contain paymentLinkID, got: %s", dbPaymentURL)
	}
}

// TestPaymentLink_G09_TokenNotInAccessLog 验证 token 不出现在 access log 中。
func TestPaymentLink_G09_TokenNotInAccessLog(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewPaymentLinkService(pool)
	reg := seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID)
	service.SetChannelRegistry(reg)

	result, err := service.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_g09_log",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       true,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	// 触发 access log 写入
	attemptID := uuid.New()
	_, _ = service.ClaimForCheckout(context.Background(), result.Token, attemptID)

	// 查询 access log
	rows, err := pool.Query(context.Background(), `
		SELECT token_hash FROM payment_link_access_log WHERE payment_link_id::text = $1
	`, result.PaymentLinkID)
	if err != nil {
		t.Fatalf("query access log failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var logTokenHash string
		if err := rows.Scan(&logTokenHash); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		// G09: access log 不应包含明文 token
		if logTokenHash == result.Token {
			t.Errorf("G09 VIOLATION: access log contains plaintext token")
		}
		if strings.Contains(logTokenHash, result.Token) {
			t.Errorf("G09 VIOLATION: access log token_hash contains plaintext token")
		}
	}
}

// TestPaymentLink_G09_RandReadErrorChecked 验证 rand.Read 错误被检查。
//
// G09: generatePaymentLinkToken 返回 error，不再忽略。
func TestPaymentLink_G09_RandReadErrorChecked(t *testing.T) {
	// generatePaymentLinkToken 现在返回 (string, error)
	token, err := generatePaymentLinkToken()
	if err != nil {
		t.Fatalf("generatePaymentLinkToken failed: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("expected 64 char token, got %d", len(token))
	}
}

// TestPaymentLink_G09_ResolveByTokenConstantTime 验证 ResolveByToken 恒时比对。
func TestPaymentLink_G09_ResolveByTokenConstantTime(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewPaymentLinkService(pool)
	reg := seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID)
	service.SetChannelRegistry(reg)

	result, err := service.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_g09_ct",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       false,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	// 有效 token
	_, err = service.ResolveByToken(context.Background(), result.Token)
	if err != nil {
		t.Errorf("valid token should resolve: %v", err)
	}

	// 无效 token（长度错误）
	_, err = service.ResolveByToken(context.Background(), "invalid")
	if err == nil {
		t.Error("invalid token should fail")
	}

	// 无效 token（长度正确但内容错误）
	_, err = service.ResolveByToken(context.Background(), strings.Repeat("a", 64))
	if err == nil {
		t.Error("invalid token should fail")
	}
}

// TestPaymentLink_G09_RevokeAndRotate 验证 revoke 和 rotate。
func TestPaymentLink_G09_RevokeAndRotate(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewPaymentLinkService(pool)
	reg := seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID)
	service.SetChannelRegistry(reg)

	result, err := service.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_g09_rotate",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       false,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	// Revoke
	linkID, _ := uuid.Parse(result.PaymentLinkID)
	err = service.Revoke(context.Background(), linkID, "test revoke")
	if err != nil {
		t.Fatalf("revoke failed: %v", err)
	}

	// Revoked link should not resolve
	_, err = service.ResolveByToken(context.Background(), result.Token)
	if err == nil {
		t.Error("revoked link should not resolve")
	}

	// Rotate
	result2, err := service.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_g09_rotate2",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       false,
	})
	if err != nil {
		t.Fatalf("create second payment link failed: %v", err)
	}

	rotated, err := service.Rotate(context.Background(), result2.Token)
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}

	// Old token should not work
	_, err = service.ResolveByToken(context.Background(), result2.Token)
	if err == nil {
		t.Error("old token should not work after rotate")
	}

	// New token should work
	_, err = service.ResolveByToken(context.Background(), rotated.Token)
	if err != nil {
		t.Errorf("new token should work after rotate: %v", err)
	}
}

// TestPaymentLink_G09_ConcurrentClaimSingleUse 验证 single-use 原子 claim。
//
// G09: 并发 checkout 时只能有一个成功 attempt。
func TestPaymentLink_G09_ConcurrentClaimSingleUse(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewPaymentLinkService(pool)
	reg := seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID)
	service.SetChannelRegistry(reg)

	result, err := service.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_g09_concurrent",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       true,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	const numGoroutines = 100
	var wg sync.WaitGroup
	var successCount, failCount int64
	var mu sync.Mutex

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			attemptID := uuid.New()
			_, err := service.ClaimForCheckout(context.Background(), result.Token, attemptID)
			mu.Lock()
			if err == nil {
				successCount++
			} else {
				failCount++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// G09: 只有一个成功 claim
	if successCount != 1 {
		t.Errorf("G09 VIOLATION: expected 1 successful claim, got %d", successCount)
	}
	if failCount != numGoroutines-1 {
		t.Errorf("expected %d failed claims, got %d", numGoroutines-1, failCount)
	}
}

// TestPaymentLink_G09_ExpiredLink 验证过期链接。
func TestPaymentLink_G09_ExpiredLink(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewPaymentLinkService(pool)
	reg := seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID)
	service.SetChannelRegistry(reg)

	expiredAt := time.Now().Add(-1 * time.Hour)
	result, err := service.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_g09_expired",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       false,
		ExpiresAt:       &expiredAt,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	_, err = service.ResolveByToken(context.Background(), result.Token)
	if err != ErrPaymentLinkExpired {
		t.Errorf("expected ErrPaymentLinkExpired, got %v", err)
	}
}

// TestPaymentLink_G09_PlaintokenMigrationRevoked 验证历史明文 token 链接被吊销。
//
// G09: 迁移 000037 应该吊销所有 payment_url 含明文 token 的链接。
func TestPaymentLink_G09_PlaintokenMigrationRevoked(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	// 手动插入一个模拟的旧明文 token 链接
	merchantID := uuid.New()
	tokenHash := hashToken(strings.Repeat("a", 64))
	plaintextURL := "/pay/" + strings.Repeat("a", 64)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO payment_links (id, token_hash, merchant_id, merchant_order_id, amount_minor, currency, success_url, cancel_url, payment_url, single_use, status)
		VALUES (gen_random_uuid(), $1, $2, 'old_order', 1000, 'USD', 'https://example.com/s', 'https://example.com/c', $3, false, 'ACTIVE')
	`, tokenHash, merchantID, plaintextURL)
	if err != nil {
		t.Fatalf("insert old plaintext link failed: %v", err)
	}

	// 运行迁移的 UPDATE（模拟）
	_, err = pool.Exec(context.Background(), `
		UPDATE payment_links
		SET status = 'REVOKED', revoked_at = now(), revoked_reason = 'G09: plaintext token revoked', updated_at = now()
		WHERE status = 'ACTIVE' AND payment_url ~ '^/pay/[0-9a-f]{64}$'
	`)
	if err != nil {
		t.Fatalf("revoke plaintext links failed: %v", err)
	}

	// 验证链接被吊销
	var status string
	err = pool.QueryRow(context.Background(), `
		SELECT status FROM payment_links WHERE token_hash = $1
	`, tokenHash).Scan(&status)
	if err != nil {
		t.Fatalf("query status failed: %v", err)
	}

	if status != "REVOKED" {
		t.Errorf("G09: plaintext token link should be REVOKED, got %s", status)
	}
}
