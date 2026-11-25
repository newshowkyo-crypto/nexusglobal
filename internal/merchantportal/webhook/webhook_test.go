package webhook

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/testenv"
)

func TestSign_StableAndSecretBound(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	s1 := Sign([]byte("secret-a"), 1700000000, body)
	s2 := Sign([]byte("secret-a"), 1700000000, body)
	s3 := Sign([]byte("secret-b"), 1700000000, body)
	s4 := Sign([]byte("secret-a"), 1700000001, body)

	assert.Equal(t, s1, s2)
	assert.NotEqual(t, s1, s3, "different secret must produce different signature")
	assert.NotEqual(t, s1, s4, "different timestamp must produce different signature")
	assert.Len(t, s1, 64)
}

func TestBackoff(t *testing.T) {
	assert.Equal(t, time.Minute, Backoff(1))
	assert.Equal(t, 5*time.Minute, Backoff(2))
	assert.Equal(t, 24*time.Hour, Backoff(7))
	assert.Equal(t, 24*time.Hour, Backoff(99))
}

func TestMaskSecret(t *testing.T) {
	assert.Equal(t, "****", MaskSecret("short"))
	assert.Equal(t, "whse****cdef", MaskSecret("whsec_abcdef"))
}

// ── 集成测试（需要 PostgreSQL） ──

func setupWebhookDB(t *testing.T) (*Repository, func()) {
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

	kms, err := crypto.NewMockKMS("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	require.NoError(t, err)
	return NewRepository(pool, crypto.NewEncryptor(kms)), cleanup
}

func TestDelivery_SuccessAndSignature(t *testing.T) {
	repo, cleanup := setupWebhookDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	var gotSignature string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		ts := r.Header.Get("X-Nexus-Timestamp")
		var tsInt int64
		_, _ = fmt.Sscanf(ts, "%d", &tsInt)
		gotSignature = r.Header.Get("X-Nexus-Signature")
		want := "v1=" + Sign([]byte("whsec_test_secret"), tsInt, gotBody)
		if gotSignature != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// httptest 是 http，仓库层不做 scheme 校验（校验在 admin handler）
	ep, err := repo.CreateEndpoint(ctx, merchantID, merchantID, server.URL, "whsec_test_secret", "", nil)
	require.NoError(t, err)
	require.NotEmpty(t, ep.ID)

	tx, err := repo.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	require.NoError(t, EnqueueTx(ctx, tx, merchantID, EventPaymentSucceeded, "attempt-1",
		map[string]any{"amount_minor": 1000}))
	require.NoError(t, tx.Commit(ctx))

	w := NewWorker(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.ProcessOnce(ctx)

	deliveries, err := repo.ListDeliveries(ctx, merchantID, 10)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, StatusSucceeded, deliveries[0]["status"])
	assert.True(t, strings.HasPrefix(gotSignature, "v1="))
	assert.Contains(t, string(gotBody), `"payment.succeeded"`)
}

func TestDelivery_FailureBackoffAndReplay(t *testing.T) {
	repo, cleanup := setupWebhookDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := repo.CreateEndpoint(ctx, merchantID, merchantID, server.URL, "whsec_test_secret", "", nil)
	require.NoError(t, err)

	tx, err := repo.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	require.NoError(t, EnqueueTx(ctx, tx, merchantID, EventPaymentFailed, "attempt-2",
		map[string]any{"failure_code": "DECLINED"}))
	require.NoError(t, tx.Commit(ctx))

	w := NewWorker(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.ProcessOnce(ctx)
	var deliveryID uuid.UUID
	var status string
	var attempts int
	var lastError string
	require.NoError(t, repo.pool.QueryRow(ctx, `
		SELECT id, status, attempt_count, last_error FROM merchant_webhook_delivery WHERE event_id = 'attempt-2'
	`).Scan(&deliveryID, &status, &attempts, &lastError))
	assert.Equal(t, StatusFailed, status)
	assert.Equal(t, 1, attempts)
	assert.Contains(t, lastError, "500")

	// 标记为 DEAD 后 replay 应回到 PENDING
	for i := 0; i < MaxAttempts; i++ {
		require.NoError(t, repo.MarkFailed(ctx, deliveryID, MaxAttempts, 500, "dead"))
	}
	require.NoError(t, repo.pool.QueryRow(ctx, `
		SELECT status FROM merchant_webhook_delivery WHERE id = $1
	`, deliveryID).Scan(&status))
	assert.Equal(t, StatusDead, status)

	ok, err := repo.Replay(ctx, merchantID, deliveryID)
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, repo.pool.QueryRow(ctx, `
		SELECT status, attempt_count FROM merchant_webhook_delivery WHERE id = $1
	`, deliveryID).Scan(&status, &attempts))
	assert.Equal(t, StatusPending, status)
	assert.Equal(t, 0, attempts)
}
