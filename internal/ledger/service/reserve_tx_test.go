package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

// ── P0-7 测试辅助函数 ──

// setupMerchantWithBalance 创建商户账户并初始化 SETTLED 余额
func setupMerchantWithBalance(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, currency string, settledBalance int64) {
	t.Helper()
	ctx := context.Background()

	// 创建 SETTLED 账户
	settledAccountID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'SETTLED', $3, 'settled account')
	`, settledAccountID, merchantID, currency)
	require.NoError(t, err)

	// 初始化 SETTLED 余额
	_, err = pool.Exec(ctx, `
		INSERT INTO account_balance (account_id, tenant_id, asset_code, balance, available, frozen)
		VALUES ($1, $2, $3, $4, $4, 0)
	`, settledAccountID, merchantID, currency, settledBalance)
	require.NoError(t, err)

	// 创建 FROZEN_PAYOUT 账户
	frozenAccountID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'FROZEN_PAYOUT', $3, 'frozen payout account')
	`, frozenAccountID, merchantID, currency)
	require.NoError(t, err)
}

// assertEntriesBalanced 验证指定 transaction 的借贷分录平衡
func assertEntriesBalanced(t *testing.T, pool *pgxpool.Pool, txnID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	var debitTotal, creditTotal int64
	err := pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN direction = 'DEBIT' THEN amount::bigint ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount::bigint ELSE 0 END), 0)
		FROM ledger_entry WHERE transaction_id = $1
	`, txnID).Scan(&debitTotal, &creditTotal)
	require.NoError(t, err)
	assert.Equal(t, debitTotal, creditTotal, "DEBIT total must equal CREDIT total (借=贷)")
	assert.Greater(t, debitTotal, int64(0), "must have non-zero DEBIT entries")
}

// ── P0-7: ReserveTx 复式记账与幂等性测试 ──

// TestReserveTx_WritesBalancedEntries 验证 ReserveTx 生成平衡的复式分录
func TestReserveTx_WritesBalancedEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	runAllMigrationsFromEmpty(t, pool)
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	merchantID := uuid.New()
	currency := "USD"
	setupMerchantWithBalance(t, pool, merchantID, currency, 10000)

	// 执行 ReserveTx
	var reservationID string
	err := txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		reservationID, err = svc.ReserveTx(ctx, tx, merchantID, 3000, currency, "test-balanced-reserve-001")
		return err
	})
	require.NoError(t, err)
	assert.NotEmpty(t, reservationID)

	// 验证 ledger_transaction 已创建
	var txnID uuid.UUID
	var txnType, txnStatus string
	err = pool.QueryRow(ctx, `
		SELECT id, transaction_type, status FROM ledger_transaction
		WHERE idempotency_key = 'reserve_test-balanced-reserve-001'
	`).Scan(&txnID, &txnType, &txnStatus)
	require.NoError(t, err)
	assert.Equal(t, "RESERVE", txnType)
	assert.Equal(t, "AUTHORIZED", txnStatus)

	// 验证成对分录存在
	var entryCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_entry WHERE transaction_id = $1
	`, txnID).Scan(&entryCount)
	require.NoError(t, err)
	assert.Equal(t, 2, entryCount, "should have exactly 2 entries (DEBIT + CREDIT)")

	// 验证借贷平衡
	assertEntriesBalanced(t, pool, txnID)

	// 验证有 DEBIT 和 CREDIT 各一条
	var debitCount, creditCount int
	err = pool.QueryRow(ctx, `
		SELECT
			count(CASE WHEN direction = 'DEBIT' THEN 1 END),
			count(CASE WHEN direction = 'CREDIT' THEN 1 END)
		FROM ledger_entry WHERE transaction_id = $1
	`, txnID).Scan(&debitCount, &creditCount)
	require.NoError(t, err)
	assert.Equal(t, 1, debitCount, "should have 1 DEBIT entry")
	assert.Equal(t, 1, creditCount, "should have 1 CREDIT entry")
}

// TestReserveTx_UpdatesBalancesAtomically 验证 ReserveTx 同事务原子更新 account_balance
func TestReserveTx_UpdatesBalancesAtomically(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	runAllMigrationsFromEmpty(t, pool)
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	merchantID := uuid.New()
	currency := "USD"
	setupMerchantWithBalance(t, pool, merchantID, currency, 10000)

	// 执行 ReserveTx
	reserveAmount := int64(3000)
	_, err := svc.Reserve(ctx, merchantID, reserveAmount, currency, "test-atomic-reserve-001")
	require.NoError(t, err)

	// 验证 SETTLED 账户余额已更新（available 减少）
	var settledAvailable int64
	err = pool.QueryRow(ctx, `
		SELECT available::bigint FROM account_balance
		WHERE account_id = (
			SELECT id FROM ledger_account
			WHERE merchant_id = $1 AND account_type = 'SETTLED' AND currency = $2
		) AND asset_code = $2
	`, merchantID, currency).Scan(&settledAvailable)
	require.NoError(t, err)
	assert.Equal(t, int64(10000-reserveAmount), settledAvailable,
		"SETTLED available should be reduced by reserve amount")

	// 验证 FROZEN_PAYOUT 账户余额已更新（balance 增加）
	var frozenBalance int64
	err = pool.QueryRow(ctx, `
		SELECT balance::bigint FROM account_balance
		WHERE account_id = (
			SELECT id FROM ledger_account
			WHERE merchant_id = $1 AND account_type = 'FROZEN_PAYOUT' AND currency = $2
		) AND asset_code = $2
	`, merchantID, currency).Scan(&frozenBalance)
	require.NoError(t, err)
	assert.Equal(t, reserveAmount, frozenBalance,
		"FROZEN_PAYOUT balance should be increased by reserve amount")
}

// TestReserveTx_ReplayReturnsOriginalReservation 验证幂等性：重放返回原 reservation
func TestReserveTx_ReplayReturnsOriginalReservation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	runAllMigrationsFromEmpty(t, pool)
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	merchantID := uuid.New()
	currency := "USD"
	setupMerchantWithBalance(t, pool, merchantID, currency, 10000)

	transactionID := "test-idempotent-reserve-001"

	// 第一次执行
	var reservationID1 string
	err := txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		reservationID1, err = svc.ReserveTx(ctx, tx, merchantID, 3000, currency, transactionID)
		return err
	})
	require.NoError(t, err)
	assert.NotEmpty(t, reservationID1)

	// 第二次执行（相同 transactionID，应返回原 reservation）
	var reservationID2 string
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		reservationID2, err = svc.ReserveTx(ctx, tx, merchantID, 3000, currency, transactionID)
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, reservationID1, reservationID2,
		"replay should return original reservation ID (幂等)")

	// 验证只有一笔 transaction
	var txnCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_transaction
		WHERE idempotency_key = 'reserve_`+transactionID+`'
	`).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 1, txnCount, "should have exactly 1 transaction (幂等)")

	// 验证只有一条 reservation
	var resCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_reservations WHERE external_transaction_id = $1
	`, transactionID).Scan(&resCount)
	require.NoError(t, err)
	assert.Equal(t, 1, resCount, "should have exactly 1 reservation (幂等)")

	// 验证只有两条 entry（不重复）
	var entryCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_entry e
		JOIN ledger_transaction t ON e.transaction_id = t.id
		WHERE t.idempotency_key = 'reserve_`+transactionID+`'
	`).Scan(&entryCount)
	require.NoError(t, err)
	assert.Equal(t, 2, entryCount, "should have exactly 2 entries (不重复)")
}

// TestReserveTx_ConcurrentRequestsCannotOverReserve 验证并发请求不会超额预留
func TestReserveTx_ConcurrentRequestsCannotOverReserve(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	runAllMigrationsFromEmpty(t, pool)
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	merchantID := uuid.New()
	currency := "USD"
	// 余额只有 5000，但两个并发请求各要 4000
	setupMerchantWithBalance(t, pool, merchantID, currency, 5000)

	var wg sync.WaitGroup
	successCount := 0
	failCount := 0
	var mu sync.Mutex

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			txnID := fmt.Sprintf("test-concurrent-reserve-%d", idx)
			_, err := svc.Reserve(ctx, merchantID, 4000, currency, txnID)
			mu.Lock()
			if err == nil {
				successCount++
			} else {
				failCount++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// 只有一个应该成功，另一个应该因余额不足失败
	assert.Equal(t, 1, successCount, "exactly one concurrent reserve should succeed")
	assert.Equal(t, 1, failCount, "exactly one concurrent reserve should fail (insufficient balance)")

	// 验证最终 FROZEN 余额不超过原始余额
	var frozenBalance int64
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(balance::bigint, 0) FROM account_balance
		WHERE account_id = (
			SELECT id FROM ledger_account
			WHERE merchant_id = $1 AND account_type = 'FROZEN_PAYOUT' AND currency = $2
		) AND asset_code = $2
	`, merchantID, currency).Scan(&frozenBalance)
	require.NoError(t, err)
	assert.Equal(t, int64(4000), frozenBalance, "frozen balance should be exactly 4000 (one reserve)")

	// 验证 SETTLED available 不会为负
	var settledAvailable int64
	err = pool.QueryRow(ctx, `
		SELECT available::bigint FROM account_balance
		WHERE account_id = (
			SELECT id FROM ledger_account
			WHERE merchant_id = $1 AND account_type = 'SETTLED' AND currency = $2
		) AND asset_code = $2
	`, merchantID, currency).Scan(&settledAvailable)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), settledAvailable, "SETTLED available should be 1000 (5000-4000)")
	assert.GreaterOrEqual(t, settledAvailable, int64(0), "SETTLED available must not be negative")
}

// TestReserveTx_InsufficientBalanceRollsBack 验证余额不足时完整回滚
func TestReserveTx_InsufficientBalanceRollsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	runAllMigrationsFromEmpty(t, pool)
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	merchantID := uuid.New()
	currency := "USD"
	// 余额只有 100，但要预留 500
	setupMerchantWithBalance(t, pool, merchantID, currency, 100)

	transactionID := "test-insufficient-reserve-001"
	_, err := svc.Reserve(ctx, merchantID, 500, currency, transactionID)
	assert.Error(t, err, "should fail with insufficient balance")
	assert.ErrorIs(t, err, domain.ErrInsufficientBalance)

	// 验证没有创建 transaction
	var txnCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_transaction
		WHERE idempotency_key = 'reserve_`+transactionID+`'
	`).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 0, txnCount, "no transaction should exist (rolled back)")

	// 验证没有创建 reservation
	var resCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_reservations WHERE external_transaction_id = $1
	`, transactionID).Scan(&resCount)
	require.NoError(t, err)
	assert.Equal(t, 0, resCount, "no reservation should exist (rolled back)")

	// 验证没有创建 entry
	var entryCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_entry e
		JOIN ledger_transaction t ON e.transaction_id = t.id
		WHERE t.idempotency_key = 'reserve_`+transactionID+`'
	`).Scan(&entryCount)
	require.NoError(t, err)
	assert.Equal(t, 0, entryCount, "no entries should exist (rolled back)")

	// 验证余额未变
	var settledAvailable int64
	err = pool.QueryRow(ctx, `
		SELECT available::bigint FROM account_balance
		WHERE account_id = (
			SELECT id FROM ledger_account
			WHERE merchant_id = $1 AND account_type = 'SETTLED' AND currency = $2
		) AND asset_code = $2
	`, merchantID, currency).Scan(&settledAvailable)
	require.NoError(t, err)
	assert.Equal(t, int64(100), settledAvailable, "balance should be unchanged after rollback")
}

// TestReservationCapture_BalancedAndIdempotent 验证 Capture 复式平衡且幂等
func TestReservationCapture_BalancedAndIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	runAllMigrationsFromEmpty(t, pool)
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	merchantID := uuid.New()
	currency := "USD"
	setupMerchantWithBalance(t, pool, merchantID, currency, 10000)

	// 先创建预留
	reservationID, err := svc.Reserve(ctx, merchantID, 5000, currency, "test-capture-reserve-001")
	require.NoError(t, err)

	// 执行 Capture
	var captureTxnID string
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		captureTxnID, err = svc.CaptureTx(ctx, tx, reservationID, 5000, "test-capture-001")
		return err
	})
	require.NoError(t, err)
	assert.NotEmpty(t, captureTxnID)

	// 验证 capture transaction 有平衡的分录
	captureUUID, err := uuid.Parse(captureTxnID)
	require.NoError(t, err)
	assertEntriesBalanced(t, pool, captureUUID)

	// 验证 reservation 状态已更新
	var resStatus string
	err = pool.QueryRow(ctx, `
		SELECT status FROM ledger_reservations WHERE id = $1
	`, reservationID).Scan(&resStatus)
	require.NoError(t, err)
	assert.Equal(t, "CAPTURED", resStatus)

	// 幂等：同 idempotency_key 重放应返回原结果
	var captureTxnID2 string
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		captureTxnID2, err = svc.CaptureTx(ctx, tx, reservationID, 5000, "test-capture-001")
		return err
	})
	require.NoError(t, err, "replay with same idempotency_key should succeed (idempotent)")
	assert.Equal(t, captureTxnID, captureTxnID2, "replay should return original capture txn ID")

	// 非法状态转换：用不同 idempotency_key 但 reservation 已不是 HELD
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := svc.CaptureTx(ctx, tx, reservationID, 5000, "test-capture-different-001")
		return err
	})
	assert.Error(t, err, "capture on non-HELD reservation with new idempotency_key should fail")
}

// TestReservationRelease_BalancedAndIdempotent 验证 Release 复式平衡且幂等
func TestReservationRelease_BalancedAndIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	runAllMigrationsFromEmpty(t, pool)
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	merchantID := uuid.New()
	currency := "USD"
	setupMerchantWithBalance(t, pool, merchantID, currency, 10000)

	// 创建预留
	reservationID, err := svc.Reserve(ctx, merchantID, 5000, currency, "test-release-reserve-001")
	require.NoError(t, err)

	// 执行 Release
	var releaseTxnID string
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		releaseTxnID, err = svc.ReleaseTx(ctx, tx, reservationID, "test-release-001")
		return err
	})
	require.NoError(t, err)
	assert.NotEmpty(t, releaseTxnID)

	// 验证 release transaction 有平衡的分录
	releaseUUID, err := uuid.Parse(releaseTxnID)
	require.NoError(t, err)
	assertEntriesBalanced(t, pool, releaseUUID)

	// 验证 reservation 状态已更新
	var resStatus string
	err = pool.QueryRow(ctx, `
		SELECT status FROM ledger_reservations WHERE id = $1
	`, reservationID).Scan(&resStatus)
	require.NoError(t, err)
	assert.Equal(t, "RELEASED", resStatus)

	// 验证 SETTLED 余额已恢复（5000 预留后又释放，应回到 10000）
	var settledAvailable int64
	err = pool.QueryRow(ctx, `
		SELECT available::bigint FROM account_balance
		WHERE account_id = (
			SELECT id FROM ledger_account
			WHERE merchant_id = $1 AND account_type = 'SETTLED' AND currency = $2
		) AND asset_code = $2
	`, merchantID, currency).Scan(&settledAvailable)
	require.NoError(t, err)
	assert.Equal(t, int64(10000), settledAvailable,
		"SETTLED available should be restored to original after release")

	// 验证 FROZEN 余额已归零
	var frozenBalance int64
	err = pool.QueryRow(ctx, `
		SELECT COALESCE(balance::bigint, 0) FROM account_balance
		WHERE account_id = (
			SELECT id FROM ledger_account
			WHERE merchant_id = $1 AND account_type = 'FROZEN_PAYOUT' AND currency = $2
		) AND asset_code = $2
	`, merchantID, currency).Scan(&frozenBalance)
	require.NoError(t, err)
	assert.Equal(t, int64(0), frozenBalance,
		"FROZEN balance should be 0 after release")

	// 幂等：同 idempotency_key 重放应返回原结果
	var releaseTxnID2 string
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		releaseTxnID2, err = svc.ReleaseTx(ctx, tx, reservationID, "test-release-001")
		return err
	})
	require.NoError(t, err, "replay with same idempotency_key should succeed (idempotent)")
	assert.Equal(t, releaseTxnID, releaseTxnID2, "replay should return original release txn ID")

	// 非法状态转换：用不同 idempotency_key 但 reservation 已不是 HELD
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := svc.ReleaseTx(ctx, tx, reservationID, "test-release-different-001")
		return err
	})
	assert.Error(t, err, "release on non-HELD reservation with new idempotency_key should fail")
}
