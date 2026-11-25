package treasury

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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/testenv"
	"github.com/nexusglobal/mamr/internal/settlement/projection"
)

func setupTreasuryDB(t *testing.T) (*pgxpool.Pool, func()) {
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

func seedSettledWithProjection(t *testing.T, pool *pgxpool.Pool, ledgerSvc *ledgerservice.Service,
	merchantID uuid.UUID, amount int64, currency string) {
	t.Helper()
	ctx := context.Background()
	txm := postgres.NewTxManager(pool)

	require.NoError(t, txm.WithinTx(ctx, func(tx pgx.Tx) error {
		settledID, err := ledgerSvc.GetOrCreateAccountTx(ctx, tx, merchantID, "SETTLED", currency)
		if err != nil {
			return err
		}
		receivableID, err := ledgerSvc.GetOrCreateAccountTx(ctx, tx, merchantID, "PROVIDER_RECEIVABLE", currency)
		if err != nil {
			return err
		}
		dec := decimal.NewFromInt(amount)
		if err := ledgerSvc.CreateTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  "seed_" + merchantID.String(),
			TransactionType: ledgerdomain.TransactionTypeDeposit,
			AggregateType:   "test_seed",
			AggregateID:     merchantID.String(),
			TenantID:        merchantID,
			AssetCode:       currency,
			Amount:          dec,
			Status:          ledgerdomain.TransactionStatusConfirmed,
			Entries: []ledgerdomain.Entry{
				{AccountID: receivableID, Direction: ledgerdomain.DirectionDebit, Amount: dec, AssetCode: currency},
				{AccountID: settledID, Direction: ledgerdomain.DirectionCredit, Amount: dec, AssetCode: currency},
			},
		}); err != nil {
			return err
		}
		return projection.AddSettledTx(ctx, tx, merchantID, currency, amount)
	}))
}

func treasuryRows(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, currency string) (settled, pending int64) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		SELECT settled_balance::bigint, pending_balance::bigint
		FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2
	`, merchantID, currency).Scan(&settled, &pending)
	require.NoError(t, err)
	return settled, pending
}

func ledgerBalance(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, accountType, currency string) int64 {
	t.Helper()
	var balance int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT COALESCE((
			SELECT ab.balance::bigint FROM ledger_account a
			JOIN account_balance ab ON ab.account_id = a.id
			 AND ab.asset_code = a.currency AND ab.tenant_id = a.merchant_id
			WHERE a.merchant_id = $1 AND a.account_type = $2 AND a.currency = $3
		), 0)
	`, merchantID, accountType, currency).Scan(&balance))
	return balance
}

// TestVCCFunding_LedgerDrivenBridge 验证归集桥接走账本 + projection（P7）。
func TestVCCFunding_LedgerDrivenBridge(t *testing.T) {
	pool, cleanup := setupTreasuryDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	repo := NewVCCFundingDBRepository(pool)
	repo.SetLedger(ledgerSvc)

	merchantID := uuid.New()
	const amount int64 = 10000
	seedSettledWithProjection(t, pool, ledgerSvc, merchantID, amount, "USD")

	// 1. 桥接发起：SETTLED → VCC_BRIDGE_PENDING，投影 settled→pending
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.DebitMerchantSettledBalanceTx(ctx, tx, merchantID, 4000, uuid.NewString()))
	require.NoError(t, tx.Commit(ctx))

	settled, pending := treasuryRows(t, pool, merchantID, "USD")
	assert.Equal(t, int64(6000), settled)
	assert.Equal(t, int64(4000), pending)
	assert.Equal(t, int64(6000), ledgerBalance(t, pool, merchantID, "SETTLED", "USD"))
	assert.Equal(t, int64(4000), ledgerBalance(t, pool, merchantID, "VCC_BRIDGE_PENDING", "USD"))

	// 2. 桥接完成：在途 → 发卡池
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.DebitPendingCryptoBridgeBalanceTx(ctx, tx, merchantID, 4000))
	require.NoError(t, tx.Commit(ctx))

	settled, pending = treasuryRows(t, pool, merchantID, "USD")
	assert.Equal(t, int64(6000), settled)
	assert.Equal(t, int64(0), pending)
	assert.Equal(t, int64(0), ledgerBalance(t, pool, merchantID, "VCC_BRIDGE_PENDING", "USD"))
	assert.Equal(t, int64(4000), ledgerBalance(t, pool, merchantID, "ISSUING_POOL", "USD"))

	// 3. 余额不足必须拒绝
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	err = repo.DebitMerchantSettledBalanceTx(ctx, tx, merchantID, 999999, uuid.NewString())
	require.ErrorIs(t, err, ErrInsufficientBalance)
	require.NoError(t, tx.Rollback(ctx))

	// 4. 账本借贷平衡
	var imbalanced int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT transaction_id
			FROM ledger_entry
			WHERE tenant_id = $1 AND asset_code = 'USD'
			GROUP BY transaction_id
			HAVING SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE 0 END)
			     <> SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE 0 END)
		) t
	`, merchantID).Scan(&imbalanced))
	assert.Equal(t, 0, imbalanced)
}
