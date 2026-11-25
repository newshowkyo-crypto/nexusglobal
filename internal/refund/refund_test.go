package refund

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/checkout"
	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	merchantwebhook "github.com/nexusglobal/mamr/internal/merchantportal/webhook"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/testenv"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
)

type fakeGateway struct {
	err   error
	calls int
}

func (f *fakeGateway) Refund(_ context.Context, req checkout.RefundRequest) (*checkout.RefundResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &checkout.RefundResult{ProviderRef: "re_mock_1", Status: "succeeded", RefundedAmount: req.Amount}, nil
}

func setupRefundDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping database integration test in short mode")
	}
	ctx := context.Background()
	pool, cleanup := testenv.Postgres(ctx, t)

	dir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	cfg := pool.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	m, err := migrate.New("file://"+filepath.ToSlash(dir), dsn)
	require.NoError(t, err)
	require.NoError(t, m.Up())
	_, _ = m.Close()
	return pool, cleanup
}

// seedRefundablePayment 构造一笔已捕获、已结算、可退款的支付。
func seedRefundablePayment(t *testing.T, pool *pgxpool.Pool, merchantID, attemptID uuid.UUID, amount int64) {
	t.Helper()
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		INSERT INTO payment_attempts
		    (id, order_id, merchant_id, tenant_id, acquirer_channel_id, entry_channel, entry_ref,
		     merchant_order_id, amount_minor, currency, status, provider_ref, provider_idempotency_key)
		VALUES ($1, $2, $3, $3, $4, 'S2S', 'refund-test', 'order-refund', $5, 'USD', 'CAPTURED', 'pi_refund_1', 'idem-1')
	`, attemptID, uuid.New(), merchantID, uuid.New(), amount)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description, normal_side)
		VALUES ($1, $3, 'SETTLED', 'USD', 'settled', 'CREDIT'),
		       ($2, $3, 'PROVIDER_RECEIVABLE', 'USD', 'receivable', 'DEBIT')
	`, uuid.New(), uuid.New(), merchantID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO merchant_treasury_ledger (merchant_id, currency, settled_balance)
		VALUES ($1, 'USD', $2)
	`, merchantID, amount)
	require.NoError(t, err)
}

func seedOpeningBalance(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, amount int64) {
	t.Helper()
	ctx := context.Background()
	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)

	var settledID, receivableID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT id, (SELECT id FROM ledger_account WHERE merchant_id = $1 AND account_type = 'PROVIDER_RECEIVABLE' LIMIT 1)
		FROM ledger_account WHERE merchant_id = $1 AND account_type = 'SETTLED' LIMIT 1
	`, merchantID).Scan(&settledID, &receivableID))

	dec := decimal.NewFromInt(amount)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, ledgerSvc.CreateTransaction(ctx, tx, ledgerdomain.Transaction{
		IdempotencyKey:  "opening-" + merchantID.String(),
		TransactionType: ledgerdomain.TransactionType("DEPOSIT"),
		AggregateType:   "test",
		AggregateID:     merchantID.String(),
		TenantID:        merchantID,
		AssetCode:       "USD",
		Amount:          dec,
		Status:          ledgerdomain.TransactionStatusConfirmed,
		Entries: []ledgerdomain.Entry{
			{AccountID: receivableID, Direction: ledgerdomain.DirectionDebit, Amount: dec, AssetCode: "USD"},
			{AccountID: settledID, Direction: ledgerdomain.DirectionCredit, Amount: dec, AssetCode: "USD"},
		},
	}))
	require.NoError(t, tx.Commit(ctx))
}

func TestCreate_PartialAndCumulativeAndIdempotency(t *testing.T) {
	pool, cleanup := setupRefundDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	repo := NewRepository(pool)
	svc := NewService(pool, txm, repo)

	merchantID := uuid.New()
	attemptID := uuid.New()
	seedRefundablePayment(t, pool, merchantID, attemptID, 10000)

	r1, err := svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-key-1", AmountMinor: 4000,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, r1.Status)
	assert.Equal(t, "USD", r1.Currency)

	// 幂等重放：同 key 同 payload → 返回同一单
	r1b, err := svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-key-1", AmountMinor: 4000,
	})
	require.NoError(t, err)
	assert.Equal(t, r1.ID, r1b.ID)

	// 同 key 不同金额 → 冲突
	_, err = svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-key-1", AmountMinor: 5000,
	})
	assert.ErrorIs(t, err, ErrIdempotencyConflict)

	// 第二笔部分退款
	_, err = svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-key-2", AmountMinor: 4000,
	})
	require.NoError(t, err)

	// 超退：累计 12000 > 10000
	_, err = svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-key-3", AmountMinor: 3000,
	})
	assert.ErrorIs(t, err, ErrAmountExceeds)

	// 不可退款状态
	_, err = pool.Exec(ctx, `UPDATE payment_attempts SET status = 'FAILED' WHERE id = $1`, attemptID)
	require.NoError(t, err)
	_, err = svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-key-4", AmountMinor: 100,
	})
	assert.ErrorIs(t, err, ErrNotRefundable)
}

func TestWorker_SuccessPostsLedgerAndNotifies(t *testing.T) {
	pool, cleanup := setupRefundDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	attemptID := uuid.New()
	seedRefundablePayment(t, pool, merchantID, attemptID, 10000)
	seedOpeningBalance(t, pool, merchantID, 10000)

	kms, err := crypto.NewMockKMS("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	require.NoError(t, err)
	enc := crypto.NewEncryptor(kms)
	whRepo := merchantwebhook.NewRepository(pool, enc)
	_, err = whRepo.CreateEndpoint(ctx, merchantID, merchantID, "https://merchant.example.com/cb", "whsec_x", "", nil)
	require.NoError(t, err)

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	repo := NewRepository(pool)
	svc := NewService(pool, txm, repo)

	r, err := svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-worker-1", AmountMinor: 10000,
	})
	require.NoError(t, err)

	gw := &fakeGateway{}
	w := NewWorker(pool, txm, repo, gw, ledgerSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload := []byte(fmt.Sprintf(`{"refund_request_id":%q}`, r.ID.String()))
	require.NoError(t, w.Handle(ctx, outbox.OutboxEvent{EventType: outbox.EventRefundRequested, Payload: payload}))
	assert.Equal(t, 1, gw.calls)

	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM refund_request WHERE id = $1`, r.ID).Scan(&status))
	assert.Equal(t, StatusSucceeded, status)

	var settled int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT settled_balance::bigint FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = 'USD'
	`, merchantID).Scan(&settled))
	assert.Equal(t, int64(0), settled, "refund must reduce withdrawable balance")

	var attemptStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM payment_attempts WHERE id = $1`, attemptID).Scan(&attemptStatus))
	assert.Equal(t, "REFUNDED", attemptStatus)

	var deliveryCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM merchant_webhook_delivery WHERE merchant_id = $1 AND event_type = 'refund.succeeded'
	`, merchantID).Scan(&deliveryCount))
	assert.Equal(t, 1, deliveryCount)

	// 重复消费：已终态，不得重复调用上游或重复记账
	require.NoError(t, w.Handle(ctx, outbox.OutboxEvent{EventType: outbox.EventRefundRequested, Payload: payload}))
	assert.Equal(t, 1, gw.calls, "already succeeded refund must not call provider again")

	var ledgerCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM ledger_transaction WHERE idempotency_key = $1
	`, "refund_"+r.ID.String()).Scan(&ledgerCount))
	assert.Equal(t, 1, ledgerCount)
}

func TestWorker_ProviderDeclineMarksFailed(t *testing.T) {
	pool, cleanup := setupRefundDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	attemptID := uuid.New()
	seedRefundablePayment(t, pool, merchantID, attemptID, 5000)
	seedOpeningBalance(t, pool, merchantID, 5000)

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	repo := NewRepository(pool)
	svc := NewService(pool, txm, repo)

	r, err := svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-decline-1", AmountMinor: 5000,
	})
	require.NoError(t, err)

	w := NewWorker(pool, txm, repo, &fakeGateway{err: checkout.ErrPaymentFailed}, ledgerSvc,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload := []byte(fmt.Sprintf(`{"refund_request_id":%q}`, r.ID.String()))
	require.NoError(t, w.Handle(ctx, outbox.OutboxEvent{EventType: outbox.EventRefundRequested, Payload: payload}))

	var status, code string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT status, failure_code FROM refund_request WHERE id = $1
	`, r.ID).Scan(&status, &code))
	assert.Equal(t, StatusFailed, status)
	assert.Equal(t, "PROVIDER_DECLINED", code)

	var settled int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT settled_balance::bigint FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = 'USD'
	`, merchantID).Scan(&settled))
	assert.Equal(t, int64(5000), settled, "declined refund must not touch balance")

	// 失败后可再次发起（额度已释放）
	_, err = svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-decline-2", AmountMinor: 5000,
	})
	require.NoError(t, err)
}
