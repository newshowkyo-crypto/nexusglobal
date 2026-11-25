package statement

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/testenv"
)

func setupStatementDB(t *testing.T) (*pgxpool.Pool, func()) {
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

func TestGenerate_DailyStatement(t *testing.T) {
	pool, cleanup := setupStatementDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	attemptID := uuid.New()
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

	_, err := pool.Exec(ctx, `
		INSERT INTO payment_attempts
		    (id, order_id, merchant_id, tenant_id, acquirer_channel_id, entry_channel, entry_ref,
		     merchant_order_id, amount_minor, currency, status, captured_at)
		VALUES ($1, $2, $3, $3, $4, 'S2S', 'stmt-test', 'order-stmt-1', 10000, 'USD', 'CAPTURED', $5)
	`, attemptID, uuid.New(), merchantID, uuid.New(), day.Add(10*time.Hour))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO fee_record
		    (tenant_id, merchant_id, payment_attempt_id, currency, base_amount_minor,
		     rate_bps, fixed_fee_minor, fee_amount_minor, source)
		VALUES ($1, $1, $2, 'USD', 10000, 250, 30, 280, 'CAPTURE')
	`, merchantID, attemptID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO refund_request
		    (tenant_id, merchant_id, payment_attempt_id, merchant_refund_id, idempotency_key,
		     amount_minor, currency, status, succeeded_at)
		VALUES ($1, $1, $2, 'mref-1', 'idem-ref-1', 3000, 'USD', 'SUCCEEDED', $3)
	`, merchantID, attemptID, day.Add(11*time.Hour))
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO settlement_batch
		    (tenant_id, merchant_id, currency, business_date, status, total_amount_minor, item_count, settled_at)
		VALUES ($1, $1, 'USD', $2::date, 'SETTLED', 7000, 1, $3)
	`, merchantID, day, day.Add(12*time.Hour))
	require.NoError(t, err)

	repo := NewRepository(pool, postgres.NewTxManager(pool))
	svc := NewService(repo)

	st, err := svc.Generate(ctx, merchantID, "USD", day)
	require.NoError(t, err)
	assert.Equal(t, 1, st.PaymentCount)
	assert.Equal(t, int64(10000), st.PaymentAmountMinor)
	assert.Equal(t, 1, st.RefundCount)
	assert.Equal(t, int64(3000), st.RefundAmountMinor)
	assert.Equal(t, int64(280), st.FeeAmountMinor)
	assert.Equal(t, int64(7000), st.NetAmountMinor)
	assert.Equal(t, int64(7000), st.SettledAmountMinor)
	assert.Equal(t, "2026-10-20", st.BusinessDate)

	lines, err := svc.Lines(ctx, st.ID)
	require.NoError(t, err)
	assert.Len(t, lines, 3)
	counts := map[string]int{}
	for _, l := range lines {
		counts[l.LineType]++
	}
	assert.Equal(t, 1, counts[LinePayment])
	assert.Equal(t, 1, counts[LineRefund])
	assert.Equal(t, 1, counts[LineSettlement])

	// 重复生成：幂等，不重复明细
	st2, err := svc.Generate(ctx, merchantID, "USD", day)
	require.NoError(t, err)
	assert.Equal(t, st.ID, st2.ID)
	lines2, err := svc.Lines(ctx, st2.ID)
	require.NoError(t, err)
	assert.Len(t, lines2, 3, "regeneration must replace lines, not append")

	// CSV
	data, filename, err := svc.CSV(ctx, merchantID, st.ID)
	require.NoError(t, err)
	assert.Contains(t, filename, "2026-10-20")
	assert.Contains(t, string(data), "line_type")
	assert.Contains(t, string(data), "PAYMENT")
	assert.Contains(t, string(data), "REFUND")
	assert.Contains(t, string(data), "SETTLEMENT")

	// ListActivity 命中
	activities, err := repo.ListActivity(ctx, day)
	require.NoError(t, err)
	require.Len(t, activities, 1)
	assert.Equal(t, merchantID, activities[0].MerchantID)
}

// TestSignedDownload_SignAndVerify 验证签名下载链接的生成、校验与失效。
func TestSignedDownload_SignAndVerify(t *testing.T) {
	pool, cleanup := setupStatementDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	attemptID := uuid.New()
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

	_, err := pool.Exec(ctx, `
		INSERT INTO payment_attempts
		    (id, order_id, merchant_id, tenant_id, entry_channel, entry_ref,
		     merchant_order_id, amount_minor, currency, status, captured_at)
		VALUES ($1, $2, $3, $3, 'S2S', 'stmt-sign', 'order-sign-1', 5000, 'USD', 'CAPTURED', $4)
	`, attemptID, uuid.New(), merchantID, day.Add(10*time.Hour))
	require.NoError(t, err)

	repo := NewRepository(pool, postgres.NewTxManager(pool))
	svc := NewService(repo)

	// 未配置密钥：fail-closed
	_, err = svc.SignDownload(ctx, merchantID, uuid.New(), time.Hour)
	require.ErrorIs(t, err, ErrSigningDisabled)

	svc.SetSigningKey([]byte("statement-signing-key-for-test"))
	st, err := svc.Generate(ctx, merchantID, "USD", day)
	require.NoError(t, err)

	link, err := svc.SignDownload(ctx, merchantID, st.ID, time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, link.Signature)
	require.Contains(t, link.URL, st.ID.String())
	require.Greater(t, link.ExpiresAt, time.Now().Unix())

	// 正确签名：通过并返回归属商户
	gotMerchant, err := svc.VerifyDownload(ctx, st.ID, link.ExpiresAt, link.Signature)
	require.NoError(t, err)
	assert.Equal(t, merchantID, gotMerchant)

	// 篡改签名：拒绝
	_, err = svc.VerifyDownload(ctx, st.ID, link.ExpiresAt, strings.Repeat("a", 64))
	require.ErrorIs(t, err, ErrSignatureInvalid)

	// 篡改过期时间：拒绝（签名与 expires 绑定）
	_, err = svc.VerifyDownload(ctx, st.ID, link.ExpiresAt+60, link.Signature)
	require.ErrorIs(t, err, ErrSignatureInvalid)

	// 已过期：拒绝
	expiredAt := time.Now().Add(-time.Minute).Unix()
	_, err = svc.VerifyDownload(ctx, st.ID, expiredAt, svc.sign(downloadPayload(merchantID, st.ID, expiredAt)))
	require.ErrorIs(t, err, ErrSignatureInvalid)

	// CSV 内容签名稳定且可复算
	data, _, err := svc.CSV(ctx, merchantID, st.ID)
	require.NoError(t, err)
	sig := svc.SignBody(data)
	require.NotEmpty(t, sig)
	assert.Equal(t, sig, svc.SignBody(data))
}

// TestPublicDownload_HTTP 验证公开签名下载端点。
func TestPublicDownload_HTTP(t *testing.T) {
	pool, cleanup := setupStatementDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	attemptID := uuid.New()
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	_, err := pool.Exec(ctx, `
		INSERT INTO payment_attempts
		    (id, order_id, merchant_id, tenant_id, entry_channel, entry_ref,
		     merchant_order_id, amount_minor, currency, status, captured_at)
		VALUES ($1, $2, $3, $3, 'S2S', 'stmt-pub', 'order-pub-1', 5000, 'USD', 'CAPTURED', $4)
	`, attemptID, uuid.New(), merchantID, day.Add(9*time.Hour))
	require.NoError(t, err)

	repo := NewRepository(pool, postgres.NewTxManager(pool))
	svc := NewService(repo)
	svc.SetSigningKey([]byte("statement-signing-key-for-test"))
	st, err := svc.Generate(ctx, merchantID, "USD", day)
	require.NoError(t, err)

	h := NewHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := chi.NewRouter()
	r.Get("/public/statements/{id}/download", h.PublicDownload)

	link, err := svc.SignDownload(ctx, merchantID, st.ID, time.Hour)
	require.NoError(t, err)

	// 有效签名 → 200 + CSV + 内容签名头
	req := httptest.NewRequest("GET", link.URL, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/csv")
	assert.Contains(t, rec.Header().Get("X-Statement-Signature"), "hmac-sha256=")
	assert.Contains(t, rec.Body.String(), "PAYMENT")

	// 缺少 expires → 400
	req = httptest.NewRequest("GET", "/public/statements/"+st.ID.String()+"/download", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 错误签名 → 403
	req = httptest.NewRequest("GET",
		"/public/statements/"+st.ID.String()+"/download?expires="+strconv.FormatInt(link.ExpiresAt, 10)+"&signature=bad",
		nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGenerateAll_SkipsEmptyDay(t *testing.T) {
	pool, cleanup := setupStatementDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewRepository(pool, postgres.NewTxManager(pool))
	svc := NewService(repo)
	res, err := svc.GenerateAll(ctx, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Empty(t, res)
}
