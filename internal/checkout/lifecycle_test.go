package checkout

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// ============================================================================
// G10: 三入口统一生命周期测试
// ============================================================================

// TestG10_AttemptStatusStateMachine 验证状态机转换。
func TestG10_AttemptStatusStateMachine(t *testing.T) {
	tests := []struct {
		from  AttemptStatus
		to    AttemptStatus
		valid bool
	}{
		{AttemptRequiresAction, AttemptProcessing, true},
		{AttemptRequiresAction, AttemptFailed, true},
		{AttemptRequiresAction, AttemptCanceled, true},
		{AttemptProcessing, AttemptAuthorized, true},
		{AttemptProcessing, AttemptCaptured, true},
		{AttemptProcessing, AttemptFailed, true},
		{AttemptProcessing, AttemptUnknownExternalState, true},
		{AttemptAuthorized, AttemptCaptured, true},
		{AttemptAuthorized, AttemptFailed, true},
		{AttemptCaptured, AttemptSettled, true},
		{AttemptCaptured, AttemptRefunded, true},
		{AttemptSettled, AttemptRefunded, true},
		{AttemptSettled, AttemptChargedBack, true},
		// 非法转换
		{AttemptSettled, AttemptProcessing, false},
		{AttemptFailed, AttemptProcessing, false},
		{AttemptCanceled, AttemptProcessing, false},
		{AttemptRefunded, AttemptProcessing, false},
		{AttemptChargedBack, AttemptProcessing, false},
	}

	for _, tt := range tests {
		got := tt.from.IsValidTransition(tt.to)
		if got != tt.valid {
			t.Errorf("transition %s -> %s: expected %v, got %v", tt.from, tt.to, tt.valid, got)
		}
	}
}

// TestG10_TerminalStates 验证终态。
func TestG10_TerminalStates(t *testing.T) {
	terminalStates := []AttemptStatus{
		AttemptSettled, AttemptFailed, AttemptCanceled,
		AttemptRefunded, AttemptChargedBack,
	}
	for _, s := range terminalStates {
		if !s.IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
	}

	nonTerminalStates := []AttemptStatus{
		AttemptRequiresAction, AttemptProcessing, AttemptAuthorized,
		AttemptCaptured, AttemptUnknownExternalState,
	}
	for _, s := range nonTerminalStates {
		if s.IsTerminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

// TestG10_PendingIsNotCompleted 验证 PENDING 不是完成态。
func TestG10_PendingIsNotCompleted(t *testing.T) {
	// G10: PENDING 不是完成态
	if AttemptProcessing.IsCompleted() {
		t.Error("PROCESSING should not be completed")
	}
	if AttemptRequiresAction.IsCompleted() {
		t.Error("REQUIRES_ACTION should not be completed")
	}
	// SETTLED 是完成态
	if !AttemptSettled.IsCompleted() {
		t.Error("SETTLED should be completed")
	}
}

// TestG10_ThreeEntriesShareAttempt 验证三入口共享 payment_attempt。
//
// Hosted / S2S / Payment Link 都生成 payment_attempt_id。
func TestG10_ThreeEntriesShareAttempt(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	tenantID := merchantID

	// 1. Hosted Checkout
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	session, err := service.CreateSession(context.Background(), CreateSessionRequest{
		MerchantOrderID: "order_hosted",
		MerchantID:      merchantID,
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		BaseURL:         "https://checkout.example.com",
	})
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}

	// 验证 Hosted 创建了 payment_attempt
	var hostedAttemptID uuid.UUID
	err = pool.QueryRow(context.Background(), `
		SELECT payment_attempt_id FROM checkout_sessions WHERE session_id = $1
	`, session.SessionID).Scan(&hostedAttemptID)
	if err != nil {
		t.Fatalf("query session attempt failed: %v", err)
	}
	if hostedAttemptID == uuid.Nil {
		t.Error("Hosted checkout should create payment_attempt_id")
	}

	// 验证 payment_attempt 存在
	var attemptChannel string
	err = pool.QueryRow(context.Background(), `
		SELECT entry_channel FROM payment_attempts WHERE id = $1
	`, hostedAttemptID).Scan(&attemptChannel)
	if err != nil {
		t.Fatalf("query attempt failed: %v", err)
	}
	if attemptChannel != "HOSTED" {
		t.Errorf("expected HOSTED, got %s", attemptChannel)
	}

	// 2. S2S Payment Intent
	piResult, err := service.ProcessPaymentIntent(context.Background(), ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     "idem_s2s_001",
		PayloadHash:        "hash_s2s_001",
		MerchantOrderID:    "order_s2s",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test",
	})
	if err != nil {
		t.Fatalf("process payment intent failed: %v", err)
	}
	if piResult.PaymentAttemptID == "" {
		t.Error("S2S should create payment_attempt_id")
	}

	// 验证 S2S payment_attempt
	err = pool.QueryRow(context.Background(), `
		SELECT entry_channel FROM payment_attempts WHERE id::text = $1
	`, piResult.PaymentAttemptID).Scan(&attemptChannel)
	if err != nil {
		t.Fatalf("query S2S attempt failed: %v", err)
	}
	if attemptChannel != "S2S" {
		t.Errorf("expected S2S, got %s", attemptChannel)
	}

	// 3. Payment Link
	linkSvc := NewPaymentLinkService(pool)
	linkSvc.SetChannelRegistry(service.channelRegistry)
	linkResult, err := linkSvc.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "order_link",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		SingleUse:       false,
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}
	if linkResult.PaymentLinkID == "" {
		t.Error("Payment Link should create payment_link_id")
	}

	_ = tenantID // used for clarity
}

// TestG10_ResolveSessionReturnsAttempt 验证 GET /checkout/v3/{session_id} 返回 attempt。
func TestG10_ResolveSessionReturnsAttempt(t *testing.T) {
	merchantID := uuid.New()
	pool := getTestPool(t)
	defer pool.Close()

	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	session, err := service.CreateSession(context.Background(), CreateSessionRequest{
		MerchantOrderID: "order_resolve",
		MerchantID:      merchantID,
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		BaseURL:         "https://checkout.example.com",
	})
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}

	_, attempt, err := service.ResolveSession(context.Background(), session.SessionID)
	if err != nil {
		t.Fatalf("resolve session failed: %v", err)
	}

	if attempt == nil {
		t.Error("resolve session should return attempt")
	} else {
		if attempt.Status != AttemptRequiresAction {
			t.Errorf("expected REQUIRES_ACTION, got %s", attempt.Status)
		}
		if attempt.EntryChannel != EntryHosted {
			t.Errorf("expected HOSTED, got %s", attempt.EntryChannel)
		}
	}
}

// TestG10_ResolveSessionNotFound 验证不泄露记录存在性。
func TestG10_ResolveSessionNotFound(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	service := NewService(pool, nil, nil, nil)

	// 不存在的 session
	_, _, err := service.ResolveSession(context.Background(), "nonexistent_session")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}

	// 空 session
	_, _, err = service.ResolveSession(context.Background(), "")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for empty session, got %v", err)
	}
}

// ============================================================================
// G12: 并发幂等测试
// ============================================================================

// TestG12_IdempotencyKeyRequired 验证强制 Idempotency-Key。
func TestG12_IdempotencyKeyRequired(t *testing.T) {
	if err := ValidateIdempotencyKey(""); err == nil {
		t.Error("empty key should fail")
	}
	if err := ValidateIdempotencyKey("valid_key"); err != nil {
		t.Errorf("valid key should pass: %v", err)
	}
	longKey := strings.Repeat("a", 257)
	if err := ValidateIdempotencyKey(longKey); err == nil {
		t.Error("too long key should fail")
	}
}

// TestG12_ValidateAmount 验证金额校验。
func TestG12_ValidateAmount(t *testing.T) {
	if err := ValidateAmount(0); err == nil {
		t.Error("zero amount should fail")
	}
	if err := ValidateAmount(-1); err == nil {
		t.Error("negative amount should fail")
	}
	if err := ValidateAmount(1000); err != nil {
		t.Errorf("valid amount should pass: %v", err)
	}
	// int64 overflow protection (max is 10^15)
	if err := ValidateAmount(10_000_000_000_000_001); err == nil {
		t.Error("too large amount should fail")
	}
}

// TestG12_ValidateCurrency 验证 ISO currency。
func TestG12_ValidateCurrency(t *testing.T) {
	if err := ValidateCurrency("USD"); err != nil {
		t.Errorf("USD should pass: %v", err)
	}
	if err := ValidateCurrency("usd"); err == nil {
		t.Error("lowercase should fail")
	}
	if err := ValidateCurrency("US"); err == nil {
		t.Error("2 char should fail")
	}
	if err := ValidateCurrency("USDD"); err == nil {
		t.Error("4 char should fail")
	}
}

// TestG12_ValidateMetadata 验证 metadata 校验。
func TestG12_ValidateMetadata(t *testing.T) {
	// valid
	valid := map[string]string{"key": "value"}
	if err := ValidateMetadata(valid); err != nil {
		t.Errorf("valid metadata should pass: %v", err)
	}

	// too many keys
	tooMany := make(map[string]string)
	for i := 0; i < 51; i++ {
		tooMany[fmt.Sprintf("key%d", i)] = "value"
	}
	if err := ValidateMetadata(tooMany); err == nil {
		t.Error("too many keys should fail")
	}

	// value too long
	longValue := map[string]string{"key": strings.Repeat("a", 501)}
	if err := ValidateMetadata(longValue); err == nil {
		t.Error("too long value should fail")
	}
}

// TestG12_ConcurrentSameKeyOneAttempt 验证 100 并发相同 key 只创建一个 attempt。
//
// G12 核心测试：100 并发相同 idempotency key → 一个 attempt。
func TestG12_ConcurrentSameKeyOneAttempt(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	service := NewService(pool, nil, nil, nil)
	merchantID := uuid.New()
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	// 规范化 payload 并计算 hash
	canonicalPayload, _ := CanonicalizePayload(map[string]string{
		"merchant_order_id": "order_concurrent",
		"amount_minor":      "1000",
		"currency":          "USD",
	})
	payloadHash := HashPayload(canonicalPayload)

	const numGoroutines = 100
	var wg sync.WaitGroup
	var successCount, failCount int64
	var mu sync.Mutex
	var attemptIDs []string

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			result, err := service.ProcessPaymentIntent(context.Background(), ProcessPaymentIntentRequest{
				MerchantID:         merchantID,
				IdempotencyKey:     "concurrent_key_001",
				PayloadHash:        payloadHash,
				MerchantOrderID:    "order_concurrent",
				AmountMinor:        1000,
				Currency:           "USD",
				PaymentMethodToken: "tok_test",
			})
			mu.Lock()
			if err == nil {
				successCount++
				attemptIDs = append(attemptIDs, result.PaymentAttemptID)
			} else {
				failCount++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// G12: 所有请求都应成功（幂等返回）
	if successCount != numGoroutines {
		t.Errorf("expected %d successes, got %d", numGoroutines, successCount)
	}

	// G12: 所有返回的 attempt_id 应该相同
	uniqueAttempts := make(map[string]bool)
	for _, id := range attemptIDs {
		uniqueAttempts[id] = true
	}
	if len(uniqueAttempts) != 1 {
		t.Errorf("G12 VIOLATION: expected 1 unique attempt_id, got %d", len(uniqueAttempts))
	}

	// G12: DB 中应该只有 1 个 payment_intent
	var piCount int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM payment_intents WHERE merchant_id = $1 AND idempotency_key = 'concurrent_key_001'
	`, merchantID).Scan(&piCount)
	if err != nil {
		t.Fatalf("query count failed: %v", err)
	}
	if piCount != 1 {
		t.Errorf("G12 VIOLATION: expected 1 payment_intent, got %d", piCount)
	}

	// G12: DB 中应该只有 1 个 payment_attempt
	var attemptCount int
	err = pool.QueryRow(context.Background(), `
		SELECT count(*) FROM payment_attempts WHERE merchant_id = $1 AND entry_ref = 'concurrent_key_001'
	`, merchantID).Scan(&attemptCount)
	if err != nil {
		t.Fatalf("query attempt count failed: %v", err)
	}
	if attemptCount != 1 {
		t.Errorf("G12 VIOLATION: expected 1 payment_attempt, got %d", attemptCount)
	}

	// G12: DB 中应该只有 1 个 idempotency_record
	var idemCount int
	err = pool.QueryRow(context.Background(), `
		SELECT count(*) FROM idempotency_records WHERE merchant_id = $1 AND idempotency_key = 'concurrent_key_001'
	`, merchantID).Scan(&idemCount)
	if err != nil {
		t.Fatalf("query idem count failed: %v", err)
	}
	if idemCount != 1 {
		t.Errorf("G12 VIOLATION: expected 1 idempotency_record, got %d", idemCount)
	}
}

// TestG12_PayloadMismatchReturns409 验证不同 payload 返回 409。
func TestG12_PayloadMismatchReturns409(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	service := NewService(pool, nil, nil, nil)
	merchantID := uuid.New()
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	// 第一次请求
	_, err := service.ProcessPaymentIntent(context.Background(), ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     "mismatch_key",
		PayloadHash:        "hash_a",
		MerchantOrderID:    "order_a",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test",
	})
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	// 第二次请求，相同 key 但不同 payload
	_, err = service.ProcessPaymentIntent(context.Background(), ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     "mismatch_key",
		PayloadHash:        "hash_b",
		MerchantOrderID:    "order_b",
		AmountMinor:        2000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test",
	})
	if err != ErrIdempotencyPayloadMismatch {
		t.Errorf("expected ErrIdempotencyPayloadMismatch, got %v", err)
	}
}

// TestG12_SamePayloadReturnsSameResponse 验证相同 payload 返回首次响应。
func TestG12_SamePayloadReturnsSameResponse(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	service := NewService(pool, nil, nil, nil)
	merchantID := uuid.New()
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	req := ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     "same_payload_key",
		PayloadHash:        "same_hash",
		MerchantOrderID:    "order_same",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test",
	}

	result1, err := service.ProcessPaymentIntent(context.Background(), req)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	result2, err := service.ProcessPaymentIntent(context.Background(), req)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}

	// G12: 应返回相同的 payment_attempt_id
	if result1.PaymentAttemptID != result2.PaymentAttemptID {
		t.Errorf("expected same attempt_id, got %s and %s", result1.PaymentAttemptID, result2.PaymentAttemptID)
	}
}

// TestG12_ProviderIdempotencyKeyUnique 验证一个 attempt → 一个 provider key。
func TestG12_ProviderIdempotencyKeyUnique(t *testing.T) {
	attemptID := uuid.New()
	key1 := ProviderIdempotencyKey(attemptID)
	key2 := ProviderIdempotencyKey(attemptID)

	if key1 != key2 {
		t.Error("same attempt should produce same provider key")
	}

	attemptID2 := uuid.New()
	key3 := ProviderIdempotencyKey(attemptID2)
	if key1 == key3 {
		t.Error("different attempts should produce different provider keys")
	}
}

// TestG12_LedgerNotDoubleWritten 验证幂等不重复写账本。
func TestG12_LedgerNotDoubleWritten(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ledger := &mockLedgerService{}
	service := NewService(pool, nil, ledger, nil)
	merchantID := uuid.New()
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))

	req := ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     "ledger_key",
		PayloadHash:        "ledger_hash",
		MerchantOrderID:    "order_ledger",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test",
	}

	first, err := service.ProcessPaymentIntent(context.Background(), req)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	_, err = service.ProcessPaymentIntent(context.Background(), req)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}

	// SEC-006: 收单不在 intent 创建时冻结商户余额，期望 0 笔预留；
	// 捕获时的账本收敛由 webhook consumer 的 AcquireCaptureTx（幂等键
	// acquire_capture_<transactionID>）保证不重复入账。
	if ledger.reservationCount != 0 {
		t.Errorf("G12 VIOLATION: expected 0 ledger reservation at intent creation, got %d", ledger.reservationCount)
	}

	// G12: 幂等重放不重复入队 dispatch——双写防护在 Outbox 幂等键 dispatch_<attemptID>
	var dispatchCount int
	err = pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM transactional_outbox
		WHERE event_type = 'PAYMENT_DISPATCH_REQUESTED' AND aggregate_id = $1
	`, first.PaymentAttemptID).Scan(&dispatchCount)
	if err != nil {
		t.Fatalf("query outbox failed: %v", err)
	}
	if dispatchCount != 1 {
		t.Errorf("G12 VIOLATION: expected 1 dispatch event, got %d", dispatchCount)
	}
}

// Ensure atomic is used
var _ = atomic.AddInt64
