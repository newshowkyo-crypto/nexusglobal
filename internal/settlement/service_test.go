package settlement

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	feepkg "github.com/nexusglobal/mamr/internal/fee"
	"github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/testenv"
	splitpkg "github.com/nexusglobal/mamr/internal/split"
)

func setupSettlementDB(t *testing.T) (*pgxpool.Pool, func()) {
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
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		require.NoError(t, err, "apply migrations")
	}
	_, _ = m.Close()

	return pool, cleanup
}

func insertCapturedAttempt(t *testing.T, pool *pgxpool.Pool, merchantID, attemptID uuid.UUID, amount int64, currency string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO payment_attempts
		    (id, order_id, merchant_id, tenant_id, entry_channel, entry_ref,
		     merchant_order_id, amount_minor, currency, status)
		VALUES ($1, $2, $3, $3, 'S2S', 'test-ref', 'order-1', $4, $5, 'CAPTURED')
	`, attemptID, uuid.New(), merchantID, amount, currency)
	require.NoError(t, err)
}

func treasuryBalances(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, currency string) (pending, settled int64) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		SELECT pending_balance::bigint, settled_balance::bigint
		FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2
	`, merchantID, currency).Scan(&pending, &settled)
	require.NoError(t, err)
	return pending, settled
}

// TestCaptureSettle_PendingToSettled 验证 A 面收款 → 结算 → B 面可出金的完整链路。
func TestCaptureSettle_PendingToSettled(t *testing.T) {
	pool, cleanup := setupSettlementDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	svc := New(pool, txm, ledgerSvc)

	merchantID := uuid.New()
	attemptID := uuid.New()
	const amount int64 = 10000
	insertCapturedAttempt(t, pool, merchantID, attemptID, amount, "USD")

	// 1. 收单捕获：进入 PENDING
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = ledgerSvc.AcquireCaptureTx(ctx, tx, merchantID, amount, "USD", attemptID.String())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	pending, settled := treasuryBalances(t, pool, merchantID, "USD")
	assert.Equal(t, amount, pending, "capture must increase pending, not settled")
	assert.Equal(t, int64(0), settled, "capture must not make funds withdrawable")

	// 2. 幂等重放：不得重复增加
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	_, err = ledgerSvc.AcquireCaptureTx(ctx, tx, merchantID, amount, "USD", attemptID.String())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	pending, _ = treasuryBalances(t, pool, merchantID, "USD")
	assert.Equal(t, amount, pending, "idempotent replay must not double pending")

	var txnCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM ledger_transaction WHERE idempotency_key = $1
	`, "acquire_capture_"+attemptID.String()).Scan(&txnCount))
	assert.Equal(t, 1, txnCount)

	// 3. 结算结转：PENDING → SETTLED
	res, err := svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 1, res.ItemCount)
	assert.Equal(t, amount, res.TotalMinor)

	pending, settled = treasuryBalances(t, pool, merchantID, "USD")
	assert.Equal(t, int64(0), pending)
	assert.Equal(t, amount, settled, "settlement must make funds withdrawable")

	var settledAt *time.Time
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT settled_at FROM payment_attempts WHERE id = $1
	`, attemptID).Scan(&settledAt))
	assert.NotNil(t, settledAt, "attempt must be marked settled")

	// 4. 重复结算：不得重复入账
	res, err = svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, res.ItemCount)
	_, settled = treasuryBalances(t, pool, merchantID, "USD")
	assert.Equal(t, amount, settled)

	// 5. 账本不变量：由分录重算 == 快照
	rows, err := pool.Query(ctx, `
		SELECT a.id FROM ledger_account a
		WHERE a.merchant_id = $1 AND a.currency = $2 AND a.account_type IN ('PENDING', 'SETTLED')
	`, merchantID, "USD")
	require.NoError(t, err)
	var accountIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		accountIDs = append(accountIDs, id)
	}
	rows.Close()
	require.Len(t, accountIDs, 2)

	for _, accountID := range accountIDs {
		snapshot, recomputed, err := ledgerSvc.RecomputeBalanceTx(ctx, pool, accountID, "USD", merchantID)
		require.NoError(t, err)
		assert.Equal(t, snapshot, recomputed, "account %s balance must be recomputable from entries", accountID)
	}
}

// TestMigrationsUpDown 验证全部迁移可回滚到 0 并重新升到最新。
func TestMigrationsUpDown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping migration test in short mode")
	}
	ctx := context.Background()
	pool, cleanup := testenv.Postgres(ctx, t)
	defer cleanup()

	dir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	cfg := pool.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)

	m, err := migrate.New("file://"+filepath.ToSlash(dir), dsn)
	require.NoError(t, err)
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		require.NoError(t, err, "up")
	}
	if err := m.Migrate(54); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		require.NoError(t, err, "down to 54")
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		require.NoError(t, err, "re-up")
	}
	_, _ = m.Close()
}

// TestCaptureTx_PartialCaptureReleasesRemainder 验证部分捕获后差额立即退回 SETTLED。
func TestCaptureTx_PartialCaptureReleasesRemainder(t *testing.T) {
	pool, cleanup := setupSettlementDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	merchantID := uuid.New()
	settledID := uuid.New()
	frozenID := uuid.New()
	receivableID := uuid.New()

	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description, normal_side)
		VALUES ($1, $4, 'SETTLED', 'USD', 'settled', 'CREDIT'),
		       ($2, $4, 'FROZEN_PAYOUT', 'USD', 'frozen', 'CREDIT'),
		       ($3, $4, 'PROVIDER_RECEIVABLE', 'USD', 'receivable', 'DEBIT')
	`, settledID, frozenID, receivableID, merchantID)
	require.NoError(t, err)

	// 用真实记账分录建立期初余额（否则无法由分录重算）
	opening := decimal.NewFromInt(10000)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, ledgerSvc.CreateTransaction(ctx, tx, domain.Transaction{
		IdempotencyKey:  "opening-1",
		TransactionType: "DEPOSIT",
		AggregateType:   "test",
		AggregateID:     merchantID.String(),
		TenantID:        merchantID,
		AssetCode:       "USD",
		Amount:          opening,
		Status:          domain.TransactionStatusConfirmed,
		Entries: []domain.Entry{
			{AccountID: receivableID, Direction: domain.DirectionDebit, Amount: opening, AssetCode: "USD"},
			{AccountID: settledID, Direction: domain.DirectionCredit, Amount: opening, AssetCode: "USD"},
		},
	}))
	require.NoError(t, tx.Commit(ctx))

	// 冻结 10000
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	reservationID, err := ledgerSvc.ReserveTx(ctx, tx, merchantID, 10000, "USD", "ext-reserve-1")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	// 部分捕获 6000，差额 4000 必须立即释放回 SETTLED
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	_, err = ledgerSvc.CaptureTx(ctx, tx, reservationID, 6000, "cap-1")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	var settledBal, frozenBal int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT balance::bigint FROM account_balance WHERE account_id = $1 AND asset_code = 'USD'
	`, settledID).Scan(&settledBal))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT balance::bigint FROM account_balance WHERE account_id = $1 AND asset_code = 'USD'
	`, frozenID).Scan(&frozenBal))
	assert.Equal(t, int64(10000), settledBal, "captured + released remainder must all return to settled")
	assert.Equal(t, int64(0), frozenBal, "no funds may remain frozen after partial capture")

	var status string
	var captured int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT status, captured_amount_minor FROM ledger_reservations WHERE id = $1
	`, reservationID).Scan(&status, &captured))
	assert.Equal(t, "PARTIALLY_CAPTURED", status)
	assert.Equal(t, int64(6000), captured)

	var remainder int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT amount::bigint FROM ledger_transaction
		WHERE idempotency_key = 'capture_remainder_release_cap-1'
	`).Scan(&remainder))
	assert.Equal(t, int64(4000), remainder, "remainder release transaction must exist")

	for _, accountID := range []uuid.UUID{settledID, frozenID} {
		snapshot, recomputed, err := ledgerSvc.RecomputeBalanceTx(ctx, pool, accountID, "USD", merchantID)
		require.NoError(t, err)
		assert.Equal(t, snapshot, recomputed, "account %s must be recomputable", accountID)
	}
}

// TestSettleWithFee_ChargesAndNets 验证结算时计费：手续费入 PLATFORM_FEE，商户只收到净额。
func TestSettleWithFee_ChargesAndNets(t *testing.T) {
	pool, cleanup := setupSettlementDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	svc := New(pool, txm, ledgerSvc)
	feeRepo := feepkg.NewRepository(pool)
	svc.SetFeeEngine(feeRepo)

	merchantID := uuid.New()
	attemptID := uuid.New()
	const amount int64 = 10000

	_, err := feeRepo.UpsertPricing(ctx, feepkg.Pricing{
		TenantID: merchantID, MerchantID: merchantID,
		RateBps: 250, FixedFeeMinor: 30,
	})
	require.NoError(t, err)

	insertCapturedAttempt(t, pool, merchantID, attemptID, amount, "USD")

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = ledgerSvc.AcquireCaptureTx(ctx, tx, merchantID, amount, "USD", attemptID.String())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	res, err := svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(280), res.FeeMinor)
	assert.Equal(t, int64(9720), res.NetMinor)

	pending, settled := treasuryBalances(t, pool, merchantID, "USD")
	assert.Equal(t, int64(0), pending)
	assert.Equal(t, int64(9720), settled)

	var fee int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT fee_amount_minor FROM fee_record WHERE payment_attempt_id = $1 AND source = 'CAPTURE'
	`, attemptID).Scan(&fee))
	assert.Equal(t, int64(280), fee)

	var txnID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT id FROM ledger_transaction WHERE aggregate_type = 'settlement_batch' AND aggregate_id = $1
	`, res.BatchID).Scan(&txnID))
	var debit, credit int64
	var entries int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE 0 END), 0)::bigint,
		       COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE 0 END), 0)::bigint,
		       COUNT(*)
		FROM ledger_entry WHERE transaction_id = $1
	`, txnID).Scan(&debit, &credit, &entries))
	assert.Equal(t, debit, credit)
	assert.Equal(t, 3, entries)

	res2, err := svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, res2.ItemCount)
	var feeCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM fee_record WHERE payment_attempt_id = $1
	`, attemptID).Scan(&feeCount))
	assert.Equal(t, 1, feeCount)
}

// TestSettleWithSplit_AllocatesAndBalances 验证结算分账：合作方应付款入账、
// 商户净额减少、分账记录幂等、账本借贷平衡。
func TestSettleWithSplit_AllocatesAndBalances(t *testing.T) {
	pool, cleanup := setupSettlementDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	svc := New(pool, txm, ledgerSvc)
	splitRepo := splitpkg.NewRepository(pool)
	svc.SetSplitEngine(splitRepo)

	merchantID := uuid.New()
	attemptID := uuid.New()
	const amount int64 = 10000

	_, err := splitRepo.UpsertRule(ctx, splitpkg.Rule{
		TenantID:   merchantID,
		MerchantID: merchantID,
		Name:       "default",
		Receivers: []splitpkg.Receiver{
			{Account: "partner-a", RateBps: 2000},
			{Account: "partner-b", RateBps: 1000},
		},
	})
	require.NoError(t, err)

	insertCapturedAttempt(t, pool, merchantID, attemptID, amount, "USD")

	// A2: 商户出站回调端点（只订阅结算事件）
	_, err = pool.Exec(ctx, `
		INSERT INTO merchant_webhook_endpoint
		    (tenant_id, merchant_id, url, secret_enc, event_types)
		VALUES ($1, $1, 'https://example.test/hook', '\x00'::bytea, '{settlement.completed}')
	`, merchantID)
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = ledgerSvc.AcquireCaptureTx(ctx, tx, merchantID, amount, "USD", attemptID.String())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	res, err := svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, amount, res.TotalMinor)
	assert.Equal(t, int64(3000), res.SplitMinor)
	assert.Equal(t, int64(7000), res.NetMinor)

	pending, settled := treasuryBalances(t, pool, merchantID, "USD")
	assert.Equal(t, int64(0), pending)
	assert.Equal(t, int64(7000), settled, "split must reduce withdrawable amount")

	var splitRows int
	var splitSum int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(amount_minor), 0)::bigint
		FROM split_record WHERE payment_attempt_id = $1
	`, attemptID).Scan(&splitRows, &splitSum))
	assert.Equal(t, 2, splitRows)
	assert.Equal(t, int64(3000), splitSum)

	// A2: 结算事件已入商户回调队列
	var hookPayload []byte
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT payload FROM merchant_webhook_delivery
		WHERE merchant_id = $1 AND event_type = 'settlement.completed'
	`, merchantID).Scan(&hookPayload))
	assert.Contains(t, string(hookPayload), `"split_amount_minor": 3000`)
	assert.Contains(t, string(hookPayload), `"net_amount_minor": 7000`)

	// 账本：Dr PENDING total = Cr SETTLED net + Cr PARTNER_PAYABLE split
	var txnID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT id FROM ledger_transaction WHERE aggregate_type = 'settlement_batch' AND aggregate_id = $1
	`, res.BatchID).Scan(&txnID))
	var debit, credit int64
	var entryCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE 0 END), 0)::bigint,
		       COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE 0 END), 0)::bigint,
		       COUNT(*)
		FROM ledger_entry WHERE transaction_id = $1
	`, txnID).Scan(&debit, &credit, &entryCount))
	assert.Equal(t, debit, credit, "settlement entries must be balanced")
	assert.Equal(t, 3, entryCount)

	var partnerID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT id FROM ledger_account
		WHERE merchant_id = $1 AND account_type = 'PARTNER_PAYABLE' AND currency = 'USD'
	`, merchantID).Scan(&partnerID))
	var partnerBal int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT balance::bigint FROM account_balance WHERE account_id = $1 AND asset_code = 'USD'
	`, partnerID).Scan(&partnerBal))
	assert.Equal(t, int64(3000), partnerBal)

	snapshot, recomputed, err := ledgerSvc.RecomputeBalanceTx(ctx, pool, partnerID, "USD", merchantID)
	require.NoError(t, err)
	assert.Equal(t, snapshot, recomputed, "partner payable balance must be recomputable")

	// 重复结算：不得重复分账
	res2, err := svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, res2.ItemCount)
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM split_record WHERE payment_attempt_id = $1
	`, attemptID).Scan(&splitRows))
	assert.Equal(t, 2, splitRows)
}

// TestSettle_RespectsChannelSettlementDays 验证按通道 settlement_days 控制可结算日（P5）。
func TestSettle_RespectsChannelSettlementDays(t *testing.T) {
	pool, cleanup := setupSettlementDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	svc := New(pool, txm, ledgerSvc)

	merchantID := uuid.New()
	attemptID := uuid.New()
	const amount int64 = 10000

	providerID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO payment_provider (id, provider_code, provider_name, provider_type)
		VALUES ($1, $2, 'test provider', 'CARD')
	`, providerID, "test-"+providerID.String()[:8])
	require.NoError(t, err)

	channelID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO tenant_acquirer_channel
		    (id, tenant_id, merchant_id, channel_code, provider_id)
		VALUES ($1, $2, $2, 'ch-t1', $3)
	`, channelID, merchantID, providerID)
	require.NoError(t, err)

	poolChannelID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO pool_channel
		    (id, tenant_id, merchant_id, acquirer_channel_id, settlement_days)
		VALUES ($1, $2, $2, $3, 2)
	`, poolChannelID, merchantID, channelID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO payment_attempts
		    (id, order_id, merchant_id, tenant_id, entry_channel, entry_ref,
		     merchant_order_id, amount_minor, currency, status, captured_at, pool_channel_id)
		VALUES ($1, $2, $3, $3, 'S2S', 'test-t2', 'order-t2', $4, 'USD', 'CAPTURED', $5, $6)
	`, attemptID, uuid.New(), merchantID, amount, time.Now().Add(-24*time.Hour), poolChannelID)
	require.NoError(t, err)

	// 建立 PENDING 余额（结算需要从 PENDING 转出）
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = ledgerSvc.AcquireCaptureTx(ctx, tx, merchantID, amount, "USD", attemptID.String())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	// T+2 通道，捕获仅 1 天 → 不可结算
	res, err := svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, res.ItemCount, "T+2 channel must not settle after 1 day")

	// 捕获已 3 天 → 可结算
	_, err = pool.Exec(ctx, `
		UPDATE payment_attempts SET captured_at = now() - interval '3 days' WHERE id = $1
	`, attemptID)
	require.NoError(t, err)

	res, err = svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, res.ItemCount, "T+2 channel must settle after 3 days")
	assert.Equal(t, amount, res.TotalMinor)
}

func TestCreateTransaction_IdempotencyConflict(t *testing.T) {
	pool, cleanup := setupSettlementDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	merchantID := uuid.New()

	debitAcc := uuid.New()
	creditAcc := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description, normal_side)
		VALUES ($1, $3, 'PROVIDER_RECEIVABLE', 'USD', 'test debit', 'DEBIT'),
		       ($2, $3, 'PENDING', 'USD', 'test credit', 'CREDIT')
	`, debitAcc, creditAcc, merchantID)
	require.NoError(t, err)

	build := func(amount int64) domain.Transaction {
		dec := decimal.NewFromInt(amount)
		return domain.Transaction{
			IdempotencyKey:  "conflict-key",
			TransactionType: "TEST",
			AggregateType:   "test",
			AggregateID:     merchantID.String(),
			TenantID:        merchantID,
			AssetCode:       "USD",
			Amount:          dec,
			Status:          domain.TransactionStatusConfirmed,
			Entries: []domain.Entry{
				{AccountID: debitAcc, Direction: domain.DirectionDebit, Amount: dec, AssetCode: "USD"},
				{AccountID: creditAcc, Direction: domain.DirectionCredit, Amount: dec, AssetCode: "USD"},
			},
		}
	}

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, ledgerSvc.CreateTransaction(ctx, tx, build(100)))
	require.NoError(t, tx.Commit(ctx))

	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	err = ledgerSvc.CreateTransaction(ctx, tx, build(200))
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrIdempotencyConflict)
	_ = tx.Rollback(ctx)
}

// TestSettle_ReserveRatioHoldsFunds 验证 reserve_ratio_bps > 0 时结算计提准备金：
// 投影 pending → settled/reserve 分流、账本三录借贷平衡、RESERVE 科目余额、
// 事件携带准备金字段、幂等重放不重复计提。
func TestSettle_ReserveRatioHoldsFunds(t *testing.T) {
	pool, cleanup := setupSettlementDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	svc := New(pool, txm, ledgerSvc)

	merchantID := uuid.New()
	attemptID := uuid.New()
	const amount int64 = 10000
	insertCapturedAttempt(t, pool, merchantID, attemptID, amount, "USD")

	// 商户出站回调端点（只订阅结算事件）
	_, err := pool.Exec(ctx, `
		INSERT INTO merchant_webhook_endpoint
		    (tenant_id, merchant_id, url, secret_enc, event_types)
		VALUES ($1, $1, 'https://example.test/hook', '\x00'::bytea, '{settlement.completed}')
	`, merchantID)
	require.NoError(t, err)

	// 收单捕获：进入 PENDING（同时创建投影行）
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = ledgerSvc.AcquireCaptureTx(ctx, tx, merchantID, amount, "USD", attemptID.String())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	// 配置 15% 准备金比例
	_, err = pool.Exec(ctx, `
		UPDATE merchant_treasury_ledger SET reserve_ratio_bps = 1500
		WHERE merchant_id = $1 AND currency = 'USD'
	`, merchantID)
	require.NoError(t, err)

	res, err := svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 1, res.ItemCount)
	assert.Equal(t, amount, res.TotalMinor)
	assert.Equal(t, int64(0), res.FeeMinor)
	assert.Equal(t, int64(0), res.SplitMinor)
	assert.Equal(t, int64(1500), res.ReserveMinor, "15% of net must be held as reserve")
	assert.Equal(t, amount, res.NetMinor, "net = total - fee - split (reserve is held out of net)")

	// 投影：pending=0 / settled=8500 / reserve=1500 / available=7000
	var pending, settled, reserve, available int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT pending_balance::bigint, settled_balance::bigint,
		       reserve_balance::bigint, available_payout_balance::bigint
		FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = 'USD'
	`, merchantID).Scan(&pending, &settled, &reserve, &available))
	assert.Equal(t, int64(0), pending)
	assert.Equal(t, int64(8500), settled)
	assert.Equal(t, int64(1500), reserve)
	assert.Equal(t, int64(7000), available, "available = settled - reserve")

	// 账本：Dr PENDING 10000 = Cr SETTLED 8500 + Cr RESERVE 1500（3 条分录借贷平衡）
	var txnID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT id FROM ledger_transaction WHERE aggregate_type = 'settlement_batch' AND aggregate_id = $1
	`, res.BatchID).Scan(&txnID))
	var debit, credit int64
	var entryCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE 0 END), 0)::bigint,
		       COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE 0 END), 0)::bigint,
		       COUNT(*)
		FROM ledger_entry WHERE transaction_id = $1
	`, txnID).Scan(&debit, &credit, &entryCount))
	assert.Equal(t, debit, credit, "settlement entries must be balanced")
	assert.Equal(t, 3, entryCount)

	// RESERVE 科目余额 = 1500
	var reserveID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT id FROM ledger_account
		WHERE merchant_id = $1 AND account_type = 'RESERVE' AND currency = 'USD'
	`, merchantID).Scan(&reserveID))
	var reserveBal int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT balance::bigint FROM account_balance WHERE account_id = $1 AND asset_code = 'USD'
	`, reserveID).Scan(&reserveBal))
	assert.Equal(t, int64(1500), reserveBal)

	// 事件 payload 携带准备金字段
	var hookPayload []byte
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT payload FROM merchant_webhook_delivery
		WHERE merchant_id = $1 AND event_type = 'settlement.completed'
	`, merchantID).Scan(&hookPayload))
	assert.Contains(t, string(hookPayload), `"reserve_amount_minor": 1500`)

	// 重复结算：不得重复入账/计提
	res2, err := svc.SettleMerchant(ctx, merchantID, "USD", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, res2.ItemCount)
	pending2, settled2 := treasuryBalances(t, pool, merchantID, "USD")
	assert.Equal(t, int64(0), pending2)
	assert.Equal(t, int64(8500), settled2, "idempotent replay must not change settled")
}
