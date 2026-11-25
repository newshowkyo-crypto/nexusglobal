package checkout

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type mockLedgerService struct {
	reservationCount int
}

func (m *mockLedgerService) Reserve(ctx context.Context, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error) {
	m.reservationCount++
	return "res_" + transactionID, nil
}

func (m *mockLedgerService) ReserveTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error) {
	m.reservationCount++
	return "res_" + transactionID, nil
}

func TestPaymentIntent_Idempotency_ReplaySamePayload(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ledger := &mockLedgerService{}
	merchantID := uuid.New()
	service := NewService(pool, nil, ledger, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	idempotencyKey := "test-idempotency-key"

	req := ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     idempotencyKey,
		PayloadHash:        "abc123",
		MerchantOrderID:    "order_001",
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

	if result1.PaymentIntentID != result2.PaymentIntentID {
		t.Errorf("expected same payment_intent_id, got %s and %s", result1.PaymentIntentID, result2.PaymentIntentID)
	}
}

func TestPaymentIntent_Idempotency_RejectPayloadMismatch(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))

	idempotencyKey := "test-idempotency-key-2"

	req1 := ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     idempotencyKey,
		PayloadHash:        "abc123",
		MerchantOrderID:    "order_001",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test",
	}

	req2 := ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     idempotencyKey,
		PayloadHash:        "def456",
		MerchantOrderID:    "order_002",
		AmountMinor:        2000,
		Currency:           "USD",
		PaymentMethodToken: "tok_test",
	}

	_, err := service.ProcessPaymentIntent(context.Background(), req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	_, err = service.ProcessPaymentIntent(context.Background(), req2)
	if err != ErrIdempotencyPayloadMismatch {
		t.Errorf("expected ErrIdempotencyPayloadMismatch, got %v", err)
	}
}

func TestPaymentIntent_DoesNotDoubleWriteLedgerOnReplay(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ledger := &mockLedgerService{}
	merchantID := uuid.New()
	service := NewService(pool, nil, ledger, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	idempotencyKey := "test-idempotency-key-3"

	req := ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     idempotencyKey,
		PayloadHash:        "same-payload",
		MerchantOrderID:    "order_001",
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

	// SEC-006: 收单不在 intent 创建时做账本预留（资金移动在捕获时收敛）。
	if ledger.reservationCount != 0 {
		t.Errorf("expected 0 ledger reservation at intent creation, got %d", ledger.reservationCount)
	}

	// 重放不重复入队 dispatch（幂等键 dispatch_<attemptID> 防双写）。
	var dispatchCount int
	err = pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM transactional_outbox
		WHERE event_type = 'PAYMENT_DISPATCH_REQUESTED' AND aggregate_id = $1
	`, first.PaymentAttemptID).Scan(&dispatchCount)
	if err != nil {
		t.Fatalf("query outbox failed: %v", err)
	}
	if dispatchCount != 1 {
		t.Errorf("expected 1 dispatch event, got %d", dispatchCount)
	}
}

func TestPaymentIntent_RejectsMissingPaymentMethodToken(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))

	idempotencyKey := "test-idempotency-key-4"

	req := ProcessPaymentIntentRequest{
		MerchantID:         merchantID,
		IdempotencyKey:     idempotencyKey,
		PayloadHash:        "abc123",
		MerchantOrderID:    "order_001",
		AmountMinor:        1000,
		Currency:           "USD",
		PaymentMethodToken: "",
	}

	_, err := service.ProcessPaymentIntent(context.Background(), req)
	if err == nil {
		t.Error("expected error for missing payment_method_token")
	}
}
