package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

// setupPostgresContainer 启动空 PostgreSQL 容器并返回连接池
// 优先使用 TEST_DATABASE_DSN 环境变量（外部 PostgreSQL），否则使用 testcontainers
func setupPostgresContainer(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	ctx := context.Background()

	// 优先使用外部数据库（当 testcontainers 不可用时）
	if externalDSN := os.Getenv("TEST_DATABASE_DSN"); externalDSN != "" {
		pool, err := postgres.NewPool(ctx, externalDSN)
		require.NoError(t, err, "connect to external TEST_DATABASE_DSN")

		// 清空数据库以确保从空库开始
		_, err = pool.Exec(ctx, `
			DROP SCHEMA public CASCADE;
			CREATE SCHEMA public;
			GRANT ALL ON SCHEMA public TO public;
		`)
		require.NoError(t, err, "reset external database")

		cleanup := func() { pool.Close() }
		return pool, cleanup
	}

	req := testcontainers.ContainerRequest{
		Image:        "postgres:15",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "test",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(120 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	dsn := fmt.Sprintf("postgres://test:test@%s:%s/test?sslmode=disable", host, port.Port())
	pool, err := postgres.NewPool(ctx, dsn)
	require.NoError(t, err)

	cleanup := func() {
		pool.Close()
		_ = container.Terminate(ctx)
	}
	return pool, cleanup
}

// runAllMigrationsFromEmpty 从空库执行全部迁移
func runAllMigrationsFromEmpty(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	require.NoError(t, err)

	migrationPath := filepath.ToSlash(migrationDir)
	migrationURL := "file://" + migrationPath

	config := pool.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		config.User, config.Password, config.Host, config.Port, config.Database)

	m, err := migrate.New(migrationURL, dsn)
	require.NoError(t, err, "failed to create migrate instance: %s", migrationURL)
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err, "failed to apply migrations")
	}
	_ = ctx
}

// runMigrationsUpTo 从空库执行到指定版本（含）
func runMigrationsUpTo(t *testing.T, pool *pgxpool.Pool, version uint) {
	t.Helper()

	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	require.NoError(t, err)

	migrationPath := filepath.ToSlash(migrationDir)
	migrationURL := "file://" + migrationPath

	config := pool.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		config.User, config.Password, config.Host, config.Port, config.Database)

	m, err := migrate.New(migrationURL, dsn)
	require.NoError(t, err)
	defer m.Close()

	if err := m.Migrate(version); err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err, "failed to migrate to version %d", version)
	}
}

// TestMigration_EmptyDB_AllRuntimeTablesExist 验证从空 PostgreSQL 执行全部真实迁移后
// 所有运行时表/列/约束存在（G01 Gate 1）
func TestMigration_EmptyDB_AllRuntimeTablesExist(t *testing.T) {
	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()

	runAllMigrationsFromEmpty(t, pool)
	ctx := context.Background()

	// 验证 checkout_orders 表及其关键列/约束
	tables := []string{
		"checkout_orders",
		"ledger_reservations",
		"admin_audit_log",
		"system_control_state",
		"ledger_transaction",
		"ledger_entry",
		"ledger_account",
		"account_balance",
		"transactional_outbox",
	}
	for _, table := range tables {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables WHERE table_name = $1
			)
		`, table).Scan(&exists)
		require.NoError(t, err, "check table %s", table)
		assert.True(t, exists, "table %s should exist", table)
	}

	// 验证 checkout_orders 关键列
	checkoutOrderCols := []string{
		"id", "tenant_id", "merchant_id", "merchant_order_id", "amount_minor",
		"currency", "status", "success_url", "cancel_url", "customer", "metadata",
		"version", "created_at", "updated_at",
	}
	for _, col := range checkoutOrderCols {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'checkout_orders' AND column_name = $1
			)
		`, col).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "checkout_orders.%s should exist", col)
	}

	// 验证 ledger_reservations 关键列与外键
	reservationCols := []string{
		"id", "tenant_id", "merchant_id", "ledger_transaction_id", "amount_minor",
		"currency", "status", "external_transaction_id", "version",
	}
	for _, col := range reservationCols {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'ledger_reservations' AND column_name = $1
			)
		`, col).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "ledger_reservations.%s should exist", col)
	}

	// 验证 ledger_reservations.ledger_transaction_id 外键指向 ledger_transaction
	var fkExists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.table_constraints tc
			JOIN information_schema.constraint_column_usage ccu ON tc.constraint_name = ccu.constraint_name
			WHERE tc.table_name = 'ledger_reservations'
			  AND tc.constraint_type = 'FOREIGN KEY'
			  AND ccu.table_name = 'ledger_transaction'
		)
	`).Scan(&fkExists)
	require.NoError(t, err)
	assert.True(t, fkExists, "ledger_reservations should have FK to ledger_transaction")

	// 验证 admin_audit_log 表（单数，非 admin_audit_logs）
	var auditLogExists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'admin_audit_log'
		)
	`).Scan(&auditLogExists)
	require.NoError(t, err)
	assert.True(t, auditLogExists, "admin_audit_log (singular) should exist")

	// 验证 admin_audit_logs（复数）不存在
	var auditLogsPluralExists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'admin_audit_logs'
		)
	`).Scan(&auditLogsPluralExists)
	require.NoError(t, err)
	assert.False(t, auditLogsPluralExists, "admin_audit_logs (plural) should NOT exist")

	// 验证 system_control_state 单行约束
	var stateCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM system_control_state`).Scan(&stateCount)
	require.NoError(t, err)
	assert.Equal(t, 1, stateCount, "system_control_state should have exactly one row")

	// 验证 amount_minor > 0 CHECK 约束
	_, err = pool.Exec(ctx, `
		INSERT INTO checkout_orders (tenant_id, merchant_id, merchant_order_id, amount_minor, currency, success_url, cancel_url)
		VALUES ($1, $2, 'test_neg', -100, 'USD', 'https://s.com', 'https://c.com')
	`, uuid.New(), uuid.New())
	assert.Error(t, err, "checkout_orders.amount_minor should reject negative values")

	// 验证 currency 大写约束
	_, err = pool.Exec(ctx, `
		INSERT INTO checkout_orders (tenant_id, merchant_id, merchant_order_id, amount_minor, currency, success_url, cancel_url)
		VALUES ($1, $2, 'test_lower', 100, 'usd', 'https://s.com', 'https://c.com')
	`, uuid.New(), uuid.New())
	assert.Error(t, err, "checkout_orders.currency should reject lowercase")

	// 验证 account_balance 非负 CHECK 约束
	tenantID := uuid.New()
	accountID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'TEST', 'USD', 'test account')
	`, accountID, tenantID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO account_balance (account_id, tenant_id, asset_code, balance, available, frozen)
		VALUES ($1, $2, 'USD', -100, 0, 0)
	`, accountID, tenantID)
	assert.Error(t, err, "account_balance.balance should reject negative values")
}

// TestMigration_UpgradeFrom033_PreservesData 验证从 000033 状态升级后原数据未丢失
func TestMigration_UpgradeFrom033_PreservesData(t *testing.T) {
	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	ctx := context.Background()

	// 执行到 000033
	runMigrationsUpTo(t, pool, 33)

	// 插入代表性历史数据到 ledger_account（000004 创建）
	merchantID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'SETTLED', 'USD', 'historical settled account')
	`, uuid.New(), merchantID)
	require.NoError(t, err)

	// 插入代表性历史数据到 ledger_transaction
	historicalTxnID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO ledger_transaction (id, idempotency_key, transaction_type, aggregate_type, description)
		VALUES ($1, 'historical-txn-001', 'DEPOSIT', 'merchant', 'historical deposit')
	`, historicalTxnID)
	require.NoError(t, err)

	// 插入代表性历史数据到 checkout_sessions（000031 创建）
	sessionID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO checkout_sessions (id, session_id, merchant_id, order_id, amount_minor, currency,
		                                success_url, cancel_url, checkout_url, status, expires_at)
		VALUES ($1, 'sess_hist_001', $2, $3, 1000, 'USD',
		        'https://s.com', 'https://c.com', 'https://co.com', 'ACTIVE', now() + interval '1 day')
	`, sessionID, merchantID, uuid.New())
	require.NoError(t, err)

	// 插入代表性历史数据到 payment_intents（000031 创建）
	paymentIntentID := uuid.New()
	// 注意：000040 起禁止 payment_intents.payment_method_token 存明文 token，
	// 该迁移会拒绝非空明文。因此历史夹具使用空串，仅验证数据不丢失。
	_, err = pool.Exec(ctx, `
		INSERT INTO payment_intents (id, merchant_id, merchant_order_id, idempotency_key, payload_hash,
		                             amount_minor, currency, payment_method_token, status)
		VALUES ($1, $2, 'hist_order', 'hist-idem-key', 'hist-hash',
		        500, 'USD', '', 'PENDING')
	`, paymentIntentID, merchantID)
	require.NoError(t, err)

	// 升级到最新（执行 000034）
	runAllMigrationsFromEmpty(t, pool)

	// 验证历史数据未丢失
	var txnCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_transaction WHERE idempotency_key = 'historical-txn-001'
	`).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 1, txnCount, "historical ledger_transaction should be preserved")

	var sessionCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM checkout_sessions WHERE session_id = 'sess_hist_001'
	`).Scan(&sessionCount)
	require.NoError(t, err)
	assert.Equal(t, 1, sessionCount, "historical checkout_session should be preserved")

	var intentCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM payment_intents WHERE idempotency_key = 'hist-idem-key'
	`).Scan(&intentCount)
	require.NoError(t, err)
	assert.Equal(t, 1, intentCount, "historical payment_intent should be preserved")

	// 验证新表可用：可以插入 checkout_orders
	_, err = pool.Exec(ctx, `
		INSERT INTO checkout_orders (tenant_id, merchant_id, merchant_order_id, amount_minor, currency,
		                              success_url, cancel_url)
		VALUES ($1, $2, 'upgrade_test_order', 1000, 'USD', 'https://s.com', 'https://c.com')
	`, merchantID, merchantID)
	require.NoError(t, err, "should be able to insert into new checkout_orders after upgrade")
}

// TestLedger_CreateTransaction_BalancedEntries 验证平衡的借贷分录可以创建
func TestLedger_CreateTransaction_BalancedEntries(t *testing.T) {
	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	ctx := context.Background()

	runAllMigrationsFromEmpty(t, pool)

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	tenantID := uuid.New()
	assetCode := "USD"

	// 创建两个账户
	sourceAccountID := uuid.New()
	targetAccountID := uuid.New()

	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'SOURCE', 'USD', 'source account')
	`, sourceAccountID, tenantID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'TARGET', 'USD', 'target account')
	`, targetAccountID, tenantID)
	require.NoError(t, err)

	// 初始化 source 账户余额（存入 1000）
	_, err = pool.Exec(ctx, `
		INSERT INTO account_balance (account_id, tenant_id, asset_code, balance, available, frozen)
		VALUES ($1, $2, $3, 1000, 1000, 0)
	`, sourceAccountID, tenantID, assetCode)
	require.NoError(t, err)

	// 创建平衡的转账交易：DEBIT target 500, CREDIT source 500
	amount := decimal.NewFromInt(500)
	txn := domain.Transaction{
		IdempotencyKey:  "test-balanced-txn-001",
		TransactionType: domain.TransactionTypeTransfer,
		TenantID:        tenantID,
		AssetCode:       assetCode,
		Amount:          amount,
		Status:          domain.TransactionStatusConfirmed,
		SourceAccountID: sourceAccountID,
		TargetAccountID: targetAccountID,
		Entries: []domain.LedgerEntry{
			{
				AccountID: targetAccountID,
				Direction: domain.DirectionDebit,
				Amount:    amount,
				AssetCode: assetCode,
				TenantID:  tenantID,
			},
			{
				AccountID: sourceAccountID,
				Direction: domain.DirectionCredit,
				Amount:    amount,
				AssetCode: assetCode,
				TenantID:  tenantID,
			},
		},
	}

	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return svc.CreateTransaction(ctx, tx, txn)
	})
	require.NoError(t, err, "balanced transaction should succeed")

	// 验证 ledger_transaction 已创建
	var txnCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_transaction WHERE idempotency_key = 'test-balanced-txn-001'
	`).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 1, txnCount)

	// 验证 ledger_entry 成对创建
	var entryCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_entry e
		JOIN ledger_transaction t ON e.transaction_id = t.id
		WHERE t.idempotency_key = 'test-balanced-txn-001'
	`).Scan(&entryCount)
	require.NoError(t, err)
	assert.Equal(t, 2, entryCount, "should have 2 entries (DEBIT + CREDIT)")

	// 验证 DEBIT 总额 == CREDIT 总额
	var debitTotal, creditTotal decimal.Decimal
	err = pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN e.direction = 'DEBIT' THEN e.amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN e.direction = 'CREDIT' THEN e.amount ELSE 0 END), 0)
		FROM ledger_entry e
		JOIN ledger_transaction t ON e.transaction_id = t.id
		WHERE t.idempotency_key = 'test-balanced-txn-001'
	`).Scan(&debitTotal, &creditTotal)
	require.NoError(t, err)
	assert.True(t, debitTotal.Equal(creditTotal), "DEBIT total should equal CREDIT total")
	assert.True(t, debitTotal.Equal(decimal.NewFromInt(500)), "DEBIT total should be 500")
}

// TestLedger_CreateTransaction_ImbalancedRollsBack 验证不平衡分录整笔回滚
func TestLedger_CreateTransaction_ImbalancedRollsBack(t *testing.T) {
	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	ctx := context.Background()

	runAllMigrationsFromEmpty(t, pool)

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	tenantID := uuid.New()
	assetCode := "USD"

	accountID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'TEST', 'USD', 'test account')
	`, accountID, tenantID)
	require.NoError(t, err)

	// 不平衡：DEBIT 100, CREDIT 50
	txn := domain.Transaction{
		IdempotencyKey:  "test-imbalanced-txn-001",
		TransactionType: domain.TransactionTypeTransfer,
		TenantID:        tenantID,
		AssetCode:       assetCode,
		Amount:          decimal.NewFromInt(100),
		Status:          domain.TransactionStatusConfirmed,
		Entries: []domain.LedgerEntry{
			{
				AccountID: accountID,
				Direction: domain.DirectionDebit,
				Amount:    decimal.NewFromInt(100),
				AssetCode: assetCode,
				TenantID:  tenantID,
			},
			{
				AccountID: accountID,
				Direction: domain.DirectionCredit,
				Amount:    decimal.NewFromInt(50),
				AssetCode: assetCode,
				TenantID:  tenantID,
			},
		},
	}

	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return svc.CreateTransaction(ctx, tx, txn)
	})
	assert.Error(t, err, "imbalanced transaction should fail")

	// 验证整笔回滚：ledger_transaction 不应存在
	var txnCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_transaction WHERE idempotency_key = 'test-imbalanced-txn-001'
	`).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 0, txnCount, "imbalanced transaction should be rolled back entirely")

	// 验证 ledger_entry 也不存在
	var entryCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_entry e
		JOIN ledger_transaction t ON e.transaction_id = t.id
		WHERE t.idempotency_key = 'test-imbalanced-txn-001'
	`).Scan(&entryCount)
	require.NoError(t, err)
	assert.Equal(t, 0, entryCount, "no entries should exist for rolled back transaction")
}

// TestLedger_CreateTransaction_IdempotencyNoDuplicate 验证重复 idempotency key 不重复分录
func TestLedger_CreateTransaction_IdempotencyNoDuplicate(t *testing.T) {
	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	ctx := context.Background()

	runAllMigrationsFromEmpty(t, pool)

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	tenantID := uuid.New()
	assetCode := "USD"

	sourceAccountID := uuid.New()
	targetAccountID := uuid.New()

	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'SOURCE', 'USD', 'source')
	`, sourceAccountID, tenantID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'TARGET', 'USD', 'target')
	`, targetAccountID, tenantID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO account_balance (account_id, tenant_id, asset_code, balance, available, frozen)
		VALUES ($1, $2, $3, 10000, 10000, 0)
	`, sourceAccountID, tenantID, assetCode)
	require.NoError(t, err)

	makeTxn := func() domain.Transaction {
		amount := decimal.NewFromInt(100)
		return domain.Transaction{
			IdempotencyKey:  "test-idempotency-txn-001",
			TransactionType: domain.TransactionTypeTransfer,
			TenantID:        tenantID,
			AssetCode:       assetCode,
			Amount:          amount,
			Status:          domain.TransactionStatusConfirmed,
			Entries: []domain.LedgerEntry{
				{
					AccountID: targetAccountID,
					Direction: domain.DirectionDebit,
					Amount:    amount,
					AssetCode: assetCode,
					TenantID:  tenantID,
				},
				{
					AccountID: sourceAccountID,
					Direction: domain.DirectionCredit,
					Amount:    amount,
					AssetCode: assetCode,
					TenantID:  tenantID,
				},
			},
		}
	}

	// 第一次执行
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return svc.CreateTransaction(ctx, tx, makeTxn())
	})
	require.NoError(t, err)

	// 第二次执行（相同 idempotency_key）
	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return svc.CreateTransaction(ctx, tx, makeTxn())
	})
	require.NoError(t, err, "duplicate idempotency key should be no-op, not error")

	// 验证只有一笔 transaction
	var txnCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_transaction WHERE idempotency_key = 'test-idempotency-txn-001'
	`).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 1, txnCount, "should have exactly 1 transaction (idempotent)")

	// 验证只有两条 entry（不重复）
	var entryCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_entry e
		JOIN ledger_transaction t ON e.transaction_id = t.id
		WHERE t.idempotency_key = 'test-idempotency-txn-001'
	`).Scan(&entryCount)
	require.NoError(t, err)
	assert.Equal(t, 2, entryCount, "should have exactly 2 entries (not duplicated)")
}

// TestLedger_CreateTransaction_InsufficientBalance_Fails 验证可用余额不足时失败
func TestLedger_CreateTransaction_InsufficientBalance_Fails(t *testing.T) {
	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	ctx := context.Background()

	runAllMigrationsFromEmpty(t, pool)

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	tenantID := uuid.New()
	assetCode := "USD"

	sourceAccountID := uuid.New()
	targetAccountID := uuid.New()

	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'SOURCE', 'USD', 'source')
	`, sourceAccountID, tenantID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'TARGET', 'USD', 'target')
	`, targetAccountID, tenantID)
	require.NoError(t, err)

	// source 只有 100，但要转出 500
	_, err = pool.Exec(ctx, `
		INSERT INTO account_balance (account_id, tenant_id, asset_code, balance, available, frozen)
		VALUES ($1, $2, $3, 100, 100, 0)
	`, sourceAccountID, tenantID, assetCode)
	require.NoError(t, err)

	amount := decimal.NewFromInt(500)
	txn := domain.Transaction{
		IdempotencyKey:  "test-insufficient-txn-001",
		TransactionType: domain.TransactionTypeTransfer,
		TenantID:        tenantID,
		AssetCode:       assetCode,
		Amount:          amount,
		Status:          domain.TransactionStatusConfirmed,
		Entries: []domain.LedgerEntry{
			{
				AccountID: targetAccountID,
				Direction: domain.DirectionDebit,
				Amount:    amount,
				AssetCode: assetCode,
				TenantID:  tenantID,
			},
			{
				AccountID: sourceAccountID,
				Direction: domain.DirectionCredit,
				Amount:    amount,
				AssetCode: assetCode,
				TenantID:  tenantID,
			},
		},
	}

	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return svc.CreateTransaction(ctx, tx, txn)
	})
	assert.Error(t, err, "should fail with insufficient balance")

	// 验证整笔回滚
	var txnCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_transaction WHERE idempotency_key = 'test-insufficient-txn-001'
	`).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 0, txnCount, "insufficient balance transaction should be rolled back")
}

// TestLedger_CrossAssetImbalance 验证不同资产不得直接相加
func TestLedger_CrossAssetImbalance(t *testing.T) {
	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()
	ctx := context.Background()

	runAllMigrationsFromEmpty(t, pool)

	txm := postgres.NewTxManager(pool)
	svc := New(nil, txm, pool)

	tenantID := uuid.New()

	accountID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description)
		VALUES ($1, $2, 'TEST', 'USD', 'test')
	`, accountID, tenantID)
	require.NoError(t, err)

	// 跨资产：DEBIT USD 100, CREDIT EUR 100 — 即使金额相同，不同资产也不平衡
	txn := domain.Transaction{
		IdempotencyKey:  "test-cross-asset-txn-001",
		TransactionType: domain.TransactionTypeExchange,
		TenantID:        tenantID,
		AssetCode:       "USD",
		Amount:          decimal.NewFromInt(100),
		Status:          domain.TransactionStatusConfirmed,
		Entries: []domain.LedgerEntry{
			{
				AccountID: accountID,
				Direction: domain.DirectionDebit,
				Amount:    decimal.NewFromInt(100),
				AssetCode: "USD",
				TenantID:  tenantID,
			},
			{
				AccountID: accountID,
				Direction: domain.DirectionCredit,
				Amount:    decimal.NewFromInt(100),
				AssetCode: "EUR",
				TenantID:  tenantID,
			},
		},
	}

	err = txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return svc.CreateTransaction(ctx, tx, txn)
	})
	assert.Error(t, err, "cross-asset entries should not balance")
}
