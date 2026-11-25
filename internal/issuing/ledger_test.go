package issuing

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

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/testenv"
)

func setupIssuingLedgerDB(t *testing.T) (*pgxpool.Pool, func()) {
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

func accountBalance(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, accountType, currency string) int64 {
	t.Helper()
	var balance int64
	err := pool.QueryRow(context.Background(), `
		SELECT COALESCE((
			SELECT ab.balance::bigint
			FROM ledger_account a
			JOIN account_balance ab
			  ON ab.account_id = a.id AND ab.asset_code = a.currency AND ab.tenant_id = a.merchant_id
			WHERE a.merchant_id = $1 AND a.account_type = $2 AND a.currency = $3
		), 0)
	`, merchantID, accountType, currency).Scan(&balance)
	require.NoError(t, err)
	return balance
}

// seedSettled 用真实分录建立商户可出金余额。
func seedSettled(t *testing.T, pool *pgxpool.Pool, ledgerSvc *ledgerservice.Service,
	merchantID uuid.UUID, amountMinor int64, currency string) {
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
		amount := decimal.NewFromInt(amountMinor)
		return ledgerSvc.CreateTransaction(ctx, tx, domain.Transaction{
			IdempotencyKey:  "seed_settled_" + merchantID.String(),
			TransactionType: domain.TransactionTypeDeposit,
			AggregateType:   "test_seed",
			AggregateID:     merchantID.String(),
			TenantID:        merchantID,
			AssetCode:       currency,
			Amount:          amount,
			Status:          domain.TransactionStatusConfirmed,
			Entries: []domain.Entry{
				{AccountID: receivableID, Direction: domain.DirectionDebit, Amount: amount, AssetCode: currency},
				{AccountID: settledID, Direction: domain.DirectionCredit, Amount: amount, AssetCode: currency},
			},
		})
	}))
}

// TestLedgerPoster_HoldReleaseClear 验证授权冻结 / 撤销 / 清算三段分录与幂等。
func TestLedgerPoster_HoldReleaseClear(t *testing.T) {
	pool, cleanup := setupIssuingLedgerDB(t)
	defer cleanup()
	ctx := context.Background()

	txm := postgres.NewTxManager(pool)
	ledgerSvc := ledgerservice.New(payoutrepo.NewLedgerRepo(pool), txm, pool)
	poster := NewLedgerPoster(ledgerSvc, txm)

	merchantID := uuid.New()
	const seed int64 = 10000
	seedSettled(t, pool, ledgerSvc, merchantID, seed, "USD")
	require.Equal(t, seed, accountBalance(t, pool, merchantID, "SETTLED", "USD"), "seed balance")

	// 1. 授权冻结 2000：SETTLED 10000 → 8000；ISSUING_POOL 2000
	require.NoError(t, poster.AuthorizeHold(ctx, merchantID, "USD", 2000, "auth-1"))
	assert.Equal(t, int64(8000), accountBalance(t, pool, merchantID, "SETTLED", "USD"))
	assert.Equal(t, int64(2000), accountBalance(t, pool, merchantID, "ISSUING_POOL", "USD"))
	assert.Equal(t, int64(0), accountBalance(t, pool, merchantID, "ISSUING_CLEARING", "USD"))

	// 2. 幂等重放：余额不变
	require.NoError(t, poster.AuthorizeHold(ctx, merchantID, "USD", 2000, "auth-1"))
	assert.Equal(t, int64(8000), accountBalance(t, pool, merchantID, "SETTLED", "USD"))
	assert.Equal(t, int64(2000), accountBalance(t, pool, merchantID, "ISSUING_POOL", "USD"))

	// 3. 清算 2000：ISSUING_POOL → 0；ISSUING_CLEARING 2000
	require.NoError(t, poster.ClearAuthorization(ctx, merchantID, "USD", 2000, "auth-1"))
	assert.Equal(t, int64(0), accountBalance(t, pool, merchantID, "ISSUING_POOL", "USD"))
	assert.Equal(t, int64(2000), accountBalance(t, pool, merchantID, "ISSUING_CLEARING", "USD"))

	// 4. 另一笔授权 + 撤销
	require.NoError(t, poster.AuthorizeHold(ctx, merchantID, "USD", 1500, "auth-2"))
	assert.Equal(t, int64(6500), accountBalance(t, pool, merchantID, "SETTLED", "USD"))
	assert.Equal(t, int64(1500), accountBalance(t, pool, merchantID, "ISSUING_POOL", "USD"))

	require.NoError(t, poster.ReleaseHold(ctx, merchantID, "USD", 1500, "auth-2"))
	assert.Equal(t, int64(8000), accountBalance(t, pool, merchantID, "SETTLED", "USD"))
	assert.Equal(t, int64(0), accountBalance(t, pool, merchantID, "ISSUING_POOL", "USD"))

	// 5. 借贷平衡 + 分录可由重算得到快照
	rows, err := pool.Query(ctx, `
		SELECT id FROM ledger_account WHERE merchant_id = $1 AND currency = 'USD'
	`, merchantID)
	require.NoError(t, err)
	var accountIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		accountIDs = append(accountIDs, id)
	}
	rows.Close()
	require.NotEmpty(t, accountIDs)

	for _, accountID := range accountIDs {
		snapshot, recomputed, err := ledgerSvc.RecomputeBalanceTx(ctx, pool, accountID, "USD", merchantID)
		require.NoError(t, err)
		assert.Equal(t, snapshot, recomputed, "account %s balance must be recomputable", accountID)
	}

	var imbalanced int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT transaction_id,
			       SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE 0 END) AS d,
			       SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE 0 END) AS c
			FROM ledger_entry
			WHERE tenant_id = $1 AND asset_code = 'USD'
			GROUP BY transaction_id
			HAVING SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE 0 END)
			     <> SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE 0 END)
		) t
	`, merchantID).Scan(&imbalanced))
	assert.Equal(t, 0, imbalanced, "all issuing transactions must be balanced")
}

// TestLedgerPoster_RejectsInvalidInput 验证参数校验。
func TestLedgerPoster_RejectsInvalidInput(t *testing.T) {
	poster := NewLedgerPoster(nil, nil)
	ctx := context.Background()
	require.Error(t, poster.AuthorizeHold(ctx, uuid.New(), "USD", 0, "auth-1"))
	require.Error(t, poster.AuthorizeHold(ctx, uuid.New(), "USD", 100, ""))
	require.Error(t, poster.AuthorizeHold(ctx, uuid.New(), "USD", 100, "auth-1"))
}

// TestMinorUnits 验证金额换算口径。
func TestMinorUnits(t *testing.T) {
	assert.Equal(t, int64(1234), MinorUnits(decimal.NewFromFloat(12.34)))
	assert.Equal(t, int64(0), MinorUnits(decimal.Zero))
}
