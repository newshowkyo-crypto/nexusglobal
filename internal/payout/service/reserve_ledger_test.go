package service

import (
	"context"
	"errors"
	"fmt"
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

	"github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/testenv"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
)

func setupPayoutDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping database integration test in short mode")
	}
	ctx := context.Background()
	pool, cleanup := testenv.Postgres(ctx, t)

	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
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

func seedMerchantSettled(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, amount int64) {
	t.Helper()
	ctx := context.Background()

	ledgerRepo := repository.NewLedgerRepo(pool)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	settledID, err := ledgerRepo.GetOrCreateAccount(ctx, tx, merchantID, "SETTLED", "USD")
	require.NoError(t, err)
	receivableID, err := ledgerRepo.GetOrCreateAccount(ctx, tx, merchantID, "PROVIDER_RECEIVABLE", "USD")
	require.NoError(t, err)

	opening := decimal.NewFromInt(amount)
	_, err = ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
		IdempotencyKey:  "seed_settled_" + merchantID.String(),
		TransactionType: ledgerdomain.TransactionType("DEPOSIT"),
		AggregateType:   "test_seed",
		AggregateID:     merchantID.String(),
		Description:     "seed settled balance",
		Amount:          opening,
		UpdateBalances:  true,
		Entries: []ledgerdomain.Entry{
			{AccountID: receivableID, Direction: ledgerdomain.DirectionDebit, Amount: opening},
			{AccountID: settledID, Direction: ledgerdomain.DirectionCredit, Amount: opening},
		},
	})
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO merchant_treasury_ledger
		    (merchant_id, currency, settled_balance, available_payout_balance)
		VALUES ($1, 'USD', $2, $2)
	`, merchantID, amount)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

func treasurySnapshot(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID) (settled, reserve, available int64) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT settled_balance::bigint, reserve_balance::bigint, available_payout_balance::bigint
		FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = 'USD'
	`, merchantID).Scan(&settled, &reserve, &available))
	return settled, reserve, available
}

// TestReserveHoldRelease_LedgerAndProjection 验证准备金占用/释放走账本 + projection。
func TestReserveHoldRelease_LedgerAndProjection(t *testing.T) {
	pool, cleanup := setupPayoutDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	const seed int64 = 10000
	seedMerchantSettled(t, pool, merchantID, seed)

	svc := New(repository.New(pool), repository.NewLedgerRepo(pool), nil,
		postgres.NewTxManager(pool), pool)

	// 占用 3000
	_, err := svc.ReserveHold(ctx, ReserveHoldCmd{
		MerchantID: merchantID, Amount: decimal.NewFromInt(3000),
		FiatCurrency: "USD", IdempotencyKey: "reserve-hold-1",
	})
	require.NoError(t, err)

	settled, reserve, available := treasurySnapshot(t, pool, merchantID)
	assert.Equal(t, int64(7000), settled, "settled must decrease on reserve hold")
	assert.Equal(t, int64(3000), reserve)
	assert.Equal(t, int64(4000), available)

	// 幂等重放：不重复扣减
	_, err = svc.ReserveHold(ctx, ReserveHoldCmd{
		MerchantID: merchantID, Amount: decimal.NewFromInt(3000),
		FiatCurrency: "USD", IdempotencyKey: "reserve-hold-1",
	})
	require.NoError(t, err)
	settled, reserve, _ = treasurySnapshot(t, pool, merchantID)
	assert.Equal(t, int64(7000), settled)
	assert.Equal(t, int64(3000), reserve)

	// 释放 1000
	_, err = svc.ReserveRelease(ctx, ReserveReleaseCmd{
		MerchantID: merchantID, Amount: decimal.NewFromInt(1000),
		FiatCurrency: "USD", IdempotencyKey: "reserve-release-1",
	})
	require.NoError(t, err)
	settled, reserve, available = treasurySnapshot(t, pool, merchantID)
	assert.Equal(t, int64(8000), settled)
	assert.Equal(t, int64(2000), reserve)
	assert.Equal(t, int64(6000), available)

	// 账本：SETTLED / RESERVE 余额与分录重算一致
	var settledBal, reserveBal int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN a.account_type = 'SETTLED'
		            THEN CASE WHEN e.direction = a.normal_side THEN e.amount ELSE -e.amount END ELSE 0 END), 0)::bigint,
		       COALESCE(SUM(CASE WHEN a.account_type = 'RESERVE'
		            THEN CASE WHEN e.direction = a.normal_side THEN e.amount ELSE -e.amount END ELSE 0 END), 0)::bigint
		FROM ledger_entry e
		JOIN ledger_account a ON a.id = e.account_id
		WHERE a.merchant_id = $1
	`, merchantID).Scan(&settledBal, &reserveBal))
	assert.Equal(t, int64(8000), settledBal)
	assert.Equal(t, int64(2000), reserveBal)

	// 借贷平衡
	var imbalanced int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT transaction_id FROM ledger_entry
			WHERE tenant_id = $1
			GROUP BY transaction_id
			HAVING SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE 0 END)
			     <> SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE 0 END)
		) t
	`, merchantID).Scan(&imbalanced))
	assert.Equal(t, 0, imbalanced)
}
