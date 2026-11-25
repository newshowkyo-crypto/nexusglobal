package query

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/testenv"
)

func setupQueryDB(t *testing.T) (*pgxpool.Pool, func()) {
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
	require.NoError(t, m.Up())
	_, _ = m.Close()
	return pool, cleanup
}

func TestQuery_OrderPaymentAndTransactions(t *testing.T) {
	pool, cleanup := setupQueryDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	orderID := uuid.New()
	attemptID := uuid.New()
	now := time.Now().UTC()

	_, err := pool.Exec(ctx, `
		INSERT INTO checkout_orders (id, tenant_id, merchant_id, merchant_order_id, amount_minor, currency, status, success_url, cancel_url, site_id)
		VALUES ($1, $2, $2, 'order-query-1', 10000, 'USD', 'PENDING', '', '', 'site-a')
	`, orderID, merchantID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO payment_attempts
		    (id, order_id, merchant_id, tenant_id, acquirer_channel_id, entry_channel, entry_ref,
		     merchant_order_id, amount_minor, currency, status, provider_ref, captured_at, created_at)
		VALUES ($1, $2, $3, $3, $4, 'S2S', 'ref', 'order-query-1', 10000, 'USD', 'PARTIALLY_REFUNDED', 'pi_1', $5, $5)
	`, attemptID, orderID, merchantID, uuid.New(), now)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO refund_request
		    (tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		     amount_minor, currency, status, succeeded_at)
		VALUES ($1, $1, $2, 'mref-q1', 'idem-q1', 3000, 'USD', 'SUCCEEDED', $3)
	`, merchantID, attemptID, now)
	require.NoError(t, err)

	repo := NewRepository(pool)

	order, err := repo.OrderByMerchantOrderID(ctx, merchantID, "order-query-1")
	require.NoError(t, err)
	assert.Equal(t, orderID, order.OrderID)
	assert.Equal(t, "site-a", order.SiteID)
	require.Len(t, order.Attempts, 1)
	assert.Equal(t, attemptID, order.Attempts[0].AttemptID)
	assert.Equal(t, int64(3000), order.Attempts[0].RefundedAmountMin)

	attempt, err := repo.Attempt(ctx, merchantID, attemptID)
	require.NoError(t, err)
	assert.Equal(t, "PARTIALLY_REFUNDED", attempt.Status)
	assert.Equal(t, int64(3000), attempt.RefundedAmountMin)

	list, err := repo.ListAttempts(ctx, merchantID, "PARTIALLY_REFUNDED", 10, 0)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	list, err = repo.ListAttempts(ctx, merchantID, "FAILED", 10, 0)
	require.NoError(t, err)
	assert.Empty(t, list)

	_, err = repo.OrderByMerchantOrderID(ctx, merchantID, "missing")
	assert.True(t, errors.Is(err, ErrNotFound))

	// 商户隔离：其他商户查不到
	_, err = repo.Attempt(ctx, uuid.New(), attemptID)
	assert.True(t, errors.Is(err, ErrNotFound))
}
