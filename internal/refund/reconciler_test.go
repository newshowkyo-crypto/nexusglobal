package refund

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/checkout"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	merchantwebhook "github.com/nexusglobal/mamr/internal/merchantportal/webhook"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
)

// scriptGateway 按脚本依次返回：先消耗 errs（制造 UNKNOWN），耗尽后默认成功
// （模拟幂等重放得到原始成功结果）。
type scriptGateway struct {
	errs  []error
	calls int
}

func (g *scriptGateway) Refund(_ context.Context, req checkout.RefundRequest) (*checkout.RefundResult, error) {
	i := g.calls
	g.calls++
	if i < len(g.errs) {
		return nil, g.errs[i]
	}
	return &checkout.RefundResult{
		ProviderRef:    "re_rec_" + req.IdempotencyKey,
		Status:         "succeeded",
		RefundedAmount: req.Amount,
	}, nil
}

// execAge 手工拨动退款单时间列，模拟静默期/对账窗口的流逝。
func execAge(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, setExpr string) error {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`UPDATE refund_request SET `+setExpr+` WHERE id = $1`, id)
	return err
}

func newRefundStack(t *testing.T, pool *pgxpool.Pool) (*Repository, *postgres.TxManager, *ledgerservice.Service) {
	t.Helper()
	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	return NewRepository(pool), txm, ledgerSvc
}

func refundCount(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func settledBalance(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID) int64 {
	t.Helper()
	var settled int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT settled_balance::bigint FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = 'USD'
	`, merchantID).Scan(&settled))
	return settled
}

func newTestReconciler(w *Worker) *Reconciler {
	return NewReconciler(w, 24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// seedWebhookEndpoint 创建商户回调端点（EnqueueTx 无端点时跳过投递）。
func seedWebhookEndpoint(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID) {
	t.Helper()
	kms, err := crypto.NewMockKMS("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	require.NoError(t, err)
	enc := crypto.NewEncryptor(kms)
	whRepo := merchantwebhook.NewRepository(pool, enc)
	_, err = whRepo.CreateEndpoint(context.Background(), merchantID, merchantID, "https://merchant.example.com/cb", "whsec_x", "", nil)
	require.NoError(t, err)
}

func TestReconciler_RecoversUnknownToSucceeded(t *testing.T) {
	pool, cleanup := setupRefundDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	attemptID := uuid.New()
	seedRefundablePayment(t, pool, merchantID, attemptID, 10000)
	seedOpeningBalance(t, pool, merchantID, 10000)
	seedWebhookEndpoint(t, pool, merchantID)

	repo, txm, ledgerSvc := newRefundStack(t, pool)
	svc := NewService(pool, txm, repo)
	f, err := svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-rec-1", AmountMinor: 10000,
	})
	require.NoError(t, err)

	// 首次处理：上游超时 → UNKNOWN（脚本第一次调用返回超时）
	gw := &scriptGateway{errs: []error{checkout.ErrPaymentTimeout}}
	w := NewWorker(pool, txm, repo, gw, ledgerSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload := []byte(fmt.Sprintf(`{"refund_request_id":%q}`, f.ID.String()))
	require.NoError(t, w.Handle(ctx, outbox.OutboxEvent{EventType: outbox.EventRefundRequested, Payload: payload}))

	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM refund_request WHERE id = $1`, f.ID).Scan(&status))
	require.Equal(t, StatusUnknown, status)
	require.Equal(t, int64(10000), settledBalance(t, pool, merchantID), "unknown refund must not touch balance")

	// 静默期过后（模拟 3 分钟已过），对账重放 → 成功
	require.NoError(t, execAge(t, pool, f.ID, `updated_at = now() - interval '3 minutes'`))
	rec := newTestReconciler(w)
	n, err := rec.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 2, gw.calls, "reconcile must replay exactly once")

	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM refund_request WHERE id = $1`, f.ID).Scan(&status))
	assert.Equal(t, StatusSucceeded, status)
	assert.Equal(t, int64(0), settledBalance(t, pool, merchantID), "recovered refund must reduce withdrawable balance")

	var attemptStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM payment_attempts WHERE id = $1`, attemptID).Scan(&attemptStatus))
	assert.Equal(t, "REFUNDED", attemptStatus)

	// 账本只应用一次（幂等）
	assert.Equal(t, 1, refundCount(t, pool,
		`SELECT COUNT(*) FROM ledger_transaction WHERE idempotency_key = $1`, "refund_"+f.ID.String()))

	// 商户回调：首次 UNKNOWN 一条 + 对账成功一条
	assert.Equal(t, 1, refundCount(t, pool, `
		SELECT COUNT(*) FROM merchant_webhook_delivery WHERE merchant_id = $1 AND event_type = 'refund.unknown'
	`, merchantID))
	assert.Equal(t, 1, refundCount(t, pool, `
		SELECT COUNT(*) FROM merchant_webhook_delivery WHERE merchant_id = $1 AND event_type = 'refund.succeeded'
	`, merchantID))

	// 再次扫描：无 UNKNOWN 可处理，不重复调用上游
	n, err = rec.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, 2, gw.calls)
}

func TestReconciler_SilenceAndWindowGuards(t *testing.T) {
	pool, cleanup := setupRefundDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	attemptID := uuid.New()
	seedRefundablePayment(t, pool, merchantID, attemptID, 5000)
	seedOpeningBalance(t, pool, merchantID, 5000)

	repo, txm, ledgerSvc := newRefundStack(t, pool)
	svc := NewService(pool, txm, repo)
	f, err := svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-rec-2", AmountMinor: 5000,
	})
	require.NoError(t, err)

	gw := &scriptGateway{errs: []error{checkout.ErrPaymentTimeout}}
	w := NewWorker(pool, txm, repo, gw, ledgerSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload := []byte(fmt.Sprintf(`{"refund_request_id":%q}`, f.ID.String()))
	require.NoError(t, w.Handle(ctx, outbox.OutboxEvent{EventType: outbox.EventRefundRequested, Payload: payload}))

	rec := newTestReconciler(w)

	// 1) 静默期未过（MarkUnknown 刚刷新 updated_at）→ 不处理
	n, err := rec.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, 1, gw.calls, "must respect 2min silence backoff")

	// 2) 超出对账窗口（created_at 早于 24h）→ 不处理，保持 UNKNOWN
	require.NoError(t, execAge(t, pool, f.ID,
		`created_at = now() - interval '25 hours', updated_at = now() - interval '3 minutes'`))
	n, err = rec.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, 1, gw.calls, "must skip refunds outside reconcile window")

	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM refund_request WHERE id = $1`, f.ID).Scan(&status))
	assert.Equal(t, StatusUnknown, status)

	// 3) 回到窗口内 → 正常对账成功
	require.NoError(t, execAge(t, pool, f.ID,
		`created_at = now() - interval '1 hour', updated_at = now() - interval '3 minutes'`))
	n, err = rec.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM refund_request WHERE id = $1`, f.ID).Scan(&status))
	assert.Equal(t, StatusSucceeded, status)
}

func TestReconciler_ReplayDeclineMarksFailed(t *testing.T) {
	pool, cleanup := setupRefundDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	attemptID := uuid.New()
	seedRefundablePayment(t, pool, merchantID, attemptID, 5000)
	seedOpeningBalance(t, pool, merchantID, 5000)
	seedWebhookEndpoint(t, pool, merchantID)

	repo, txm, ledgerSvc := newRefundStack(t, pool)
	svc := NewService(pool, txm, repo)
	f, err := svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-rec-3", AmountMinor: 5000,
	})
	require.NoError(t, err)

	// 首次超时 → UNKNOWN；重放得到明确拒绝 → FAILED
	gw := &scriptGateway{errs: []error{checkout.ErrPaymentTimeout, checkout.ErrPaymentFailed}}
	w := NewWorker(pool, txm, repo, gw, ledgerSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload := []byte(fmt.Sprintf(`{"refund_request_id":%q}`, f.ID.String()))
	require.NoError(t, w.Handle(ctx, outbox.OutboxEvent{EventType: outbox.EventRefundRequested, Payload: payload}))

	require.NoError(t, execAge(t, pool, f.ID, `updated_at = now() - interval '3 minutes'`))
	rec := newTestReconciler(w)
	n, err := rec.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	var status, code string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT status, failure_code FROM refund_request WHERE id = $1
	`, f.ID).Scan(&status, &code))
	assert.Equal(t, StatusFailed, status)
	assert.Equal(t, "PROVIDER_DECLINED", code)

	// 资金未动
	assert.Equal(t, int64(5000), settledBalance(t, pool, merchantID))

	// 失败释放额度，可再次发起退款
	_, err = svc.Create(ctx, CreateCmd{
		MerchantID: merchantID, PaymentAttemptID: attemptID,
		IdempotencyKey: "refund-rec-3b", AmountMinor: 5000,
	})
	require.NoError(t, err)

	// 商户收到失败回调（首次 unknown + 重放 failed）
	assert.Equal(t, 1, refundCount(t, pool, `
		SELECT COUNT(*) FROM merchant_webhook_delivery WHERE merchant_id = $1 AND event_type = 'refund.failed'
	`, merchantID))
}
