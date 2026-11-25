package payment_dispatch

import (
	"context"
	"encoding/json"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	pooldomain "github.com/nexusglobal/mamr/internal/pool/domain"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
)

// getTestPool 启动空 PostgreSQL 并执行全部真实迁移。
// 优先使用 TEST_DATABASE_DSN 环境变量（外部 PostgreSQL），否则使用 testcontainers。
func getTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	ctx := context.Background()

	if externalDSN := os.Getenv("TEST_DATABASE_DSN"); externalDSN != "" {
		pool, err := postgres.NewPool(ctx, externalDSN)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO public;`)
		if err != nil {
			t.Fatal(err)
		}
		runMigrations(t, externalDSN)
		return pool
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	dsn := "postgres://test:test@" + host + ":" + port.Port() + "/test?sslmode=disable"
	pool, err := postgres.NewPool(ctx, dsn)
	require.NoError(t, err)

	runMigrations(t, dsn)
	return pool
}

func runMigrations(t *testing.T, dsn string) {
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	require.NoError(t, err)
	migrationURL := "file://" + filepath.ToSlash(migrationDir)
	m, err := migrate.New(migrationURL, dsn)
	require.NoError(t, err)
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}
}

// mockPaymentGateway 是 PaymentGateway 的测试 mock。
type mockPaymentGateway struct {
	chargeCount  int
	chargeResult *checkout.PaymentResult
	chargeErr    error
}

func (m *mockPaymentGateway) Charge(ctx context.Context, req checkout.PaymentRequest) (*checkout.PaymentResult, error) {
	m.chargeCount++
	if m.chargeErr != nil {
		return nil, m.chargeErr
	}
	if m.chargeResult != nil {
		return m.chargeResult, nil
	}
	return &checkout.PaymentResult{
		TransactionID: "txn-" + uuid.New().String(),
		Status:        "captured",
		ProviderRef:   "ref-" + uuid.New().String(),
	}, nil
}

// mockLedgerService 是 LedgerService 的测试 mock。
type mockLedgerService struct {
	captureCount int
	releaseCount int
}

func (m *mockLedgerService) CaptureTx(ctx context.Context, tx pgx.Tx, reservationID string, captureAmountMinor int64, transactionID string) (string, error) {
	m.captureCount++
	return "capture-" + uuid.New().String(), nil
}

func (m *mockLedgerService) ReleaseTx(ctx context.Context, tx pgx.Tx, reservationID string, transactionID string) (string, error) {
	m.releaseCount++
	return "release-" + uuid.New().String(), nil
}

// createTestAttempt 创建一个测试用 payment attempt 并返回其 ID。
func createTestAttempt(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	attemptRepo := checkout.NewPaymentAttemptRepository(pool)
	ctx := context.Background()

	attemptID := uuid.New()
	orderID := uuid.New()
	merchantID := uuid.New()

	err := attemptRepo.CreatePaymentAttempt(ctx, &checkout.PaymentAttempt{
		ID:              attemptID,
		OrderID:         orderID,
		MerchantID:      merchantID,
		TenantID:        merchantID,
		EntryChannel:    checkout.EntryS2S,
		EntryRef:        "test-ref",
		MerchantOrderID: "test-order-" + attemptID.String(),
		AmountMinor:     10000,
		Currency:        "USD",
		Status:          checkout.AttemptRequiresAction,
		// 不设置 PaymentMethodToken：本包测试不覆盖 token 加解密，
		// 且未注入 encryptor 时非空明文 token 会被 fail-closed 拒绝。
		Version: 0,
	})
	require.NoError(t, err)

	return attemptID, orderID, merchantID
}

// TestPaymentDispatch_RejectsUnexpectedEventType 验证 worker 拒绝非 PAYMENT_DISPATCH_REQUESTED 事件。
func TestPaymentDispatch_RejectsUnexpectedEventType(t *testing.T) {
	w := &Worker{}

	event := outbox.OutboxEvent{
		EventType: "UNEXPECTED_EVENT",
		Payload:   json.RawMessage(`{}`),
	}

	err := w.Handle(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected event type")
}

// TestPaymentDispatch_RejectsInvalidPayload 验证 worker 拒绝无效 payload。
func TestPaymentDispatch_RejectsInvalidPayload(t *testing.T) {
	w := &Worker{}

	event := outbox.OutboxEvent{
		EventType: outbox.EventPaymentDispatchRequested,
		Payload:   json.RawMessage(`{invalid json}`),
	}

	err := w.Handle(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// TestPaymentDispatch_RejectsInvalidAttemptID 验证 worker 拒绝无效 attempt ID。
func TestPaymentDispatch_RejectsInvalidAttemptID(t *testing.T) {
	w := &Worker{}

	payload := DispatchPayload{
		PaymentAttemptID: "not-a-uuid",
	}
	payloadBytes, _ := json.Marshal(payload)

	event := outbox.OutboxEvent{
		EventType: outbox.EventPaymentDispatchRequested,
		Payload:   payloadBytes,
	}

	err := w.Handle(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid attempt_id")
}

// TestPaymentDispatch_InvokesGatewayExactlyOnce 验证 worker 对每个事件恰好调用一次 Charge。
func TestPaymentDispatch_InvokesGatewayExactlyOnce(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	mockGW := &mockPaymentGateway{}
	txm := postgres.NewTxManager(pool)
	w := New(pool, mockGW, nil, txm, nil)

	attemptID, _, _ := createTestAttempt(t, pool)

	payload := DispatchPayload{
		PaymentAttemptID: attemptID.String(),
	}
	payloadBytes, _ := json.Marshal(payload)

	event := outbox.OutboxEvent{
		EventType: outbox.EventPaymentDispatchRequested,
		Payload:   payloadBytes,
	}

	err := w.Handle(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, 1, w.DispatchCount(), "PaymentGateway.Charge should be called exactly once")
}

// TestProviderTimeout_RemainsUnknownAndReserved 验证 Provider 超时后
// payment_attempt 状态为 UNKNOWN_EXTERNAL_STATE 且不释放 reservation。
func TestProviderTimeout_RemainsUnknownAndReserved(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	mockGW := &mockPaymentGateway{
		chargeErr: checkout.ErrPaymentTimeout,
	}
	txm := postgres.NewTxManager(pool)
	w := New(pool, mockGW, nil, txm, nil)

	attemptID, _, _ := createTestAttempt(t, pool)

	payload := DispatchPayload{
		PaymentAttemptID: attemptID.String(),
	}
	payloadBytes, _ := json.Marshal(payload)

	event := outbox.OutboxEvent{
		EventType: outbox.EventPaymentDispatchRequested,
		Payload:   payloadBytes,
	}

	err := w.Handle(context.Background(), event)
	require.NoError(t, err)

	// 验证 attempt 状态为 UNKNOWN_EXTERNAL_STATE
	attemptRepo := checkout.NewPaymentAttemptRepository(pool)
	attempt, err := attemptRepo.GetPaymentAttempt(context.Background(), attemptID)
	require.NoError(t, err)
	assert.Equal(t, checkout.AttemptUnknownExternalState, attempt.Status,
		"Provider timeout should result in UNKNOWN_EXTERNAL_STATE")

	// 验证没有调用 release
	assert.Equal(t, 0, w.ReleaseCount(), "reservation should NOT be released on timeout")
}

// TestProviderDecline_ReleasesReservationOnce 验证 Provider 明确拒绝后
// payment_attempt 状态为 FAILED 且恰好释放一次 reservation。
func TestProviderDecline_ReleasesReservationOnce(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	mockGW := &mockPaymentGateway{
		chargeErr: checkout.ErrPaymentFailed,
	}
	mockLedger := &mockLedgerService{}
	txm := postgres.NewTxManager(pool)
	w := New(pool, mockGW, mockLedger, txm, nil)

	attemptID, _, _ := createTestAttempt(t, pool)

	payload := DispatchPayload{
		PaymentAttemptID: attemptID.String(),
	}
	payloadBytes, _ := json.Marshal(payload)

	event := outbox.OutboxEvent{
		EventType: outbox.EventPaymentDispatchRequested,
		Payload:   payloadBytes,
	}

	err := w.Handle(context.Background(), event)
	require.NoError(t, err)

	// 验证 attempt 状态为 FAILED
	attemptRepo := checkout.NewPaymentAttemptRepository(pool)
	attempt, err := attemptRepo.GetPaymentAttempt(context.Background(), attemptID)
	require.NoError(t, err)
	assert.Equal(t, checkout.AttemptFailed, attempt.Status,
		"Provider decline should result in FAILED")

	// 验证恰好释放一次
	assert.Equal(t, 1, w.ReleaseCount(), "reservation should be released exactly once on decline")
}

// sequenceGateway 按顺序返回预设错误，用于验证失败切换。
type sequenceGateway struct {
	errs  []error
	count int
}

func (g *sequenceGateway) Charge(ctx context.Context, req checkout.PaymentRequest) (*checkout.PaymentResult, error) {
	i := g.count
	g.count++
	if i < len(g.errs) && g.errs[i] != nil {
		return nil, g.errs[i]
	}
	return &checkout.PaymentResult{TransactionID: "txn-failover", Status: "captured", ProviderRef: "ref-failover"}, nil
}

type fakePoolRouter struct {
	next      *pooldomain.Decision
	failovers int
	releases  int
	confirms  int
}

func (f *fakePoolRouter) Failover(ctx context.Context, attemptID uuid.UUID) (*pooldomain.Decision, error) {
	f.failovers++
	if f.next == nil {
		return nil, pooldomain.ErrLeaseNotFound
	}
	return f.next, nil
}

func (f *fakePoolRouter) Confirm(ctx context.Context, attemptID, channelID uuid.UUID, amount int64) error {
	f.confirms++
	return nil
}

func (f *fakePoolRouter) Release(ctx context.Context, attemptID, channelID uuid.UUID, amount int64) error {
	f.releases++
	return nil
}

// TestProviderDecline_FailoverToNextCandidate 验证明确拒绝时切下一候选并成功。
func TestProviderDecline_FailoverToNextCandidate(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	gw := &sequenceGateway{errs: []error{checkout.ErrPaymentFailed}}
	ledger := &mockLedgerService{}
	w := New(pool, gw, ledger, postgres.NewTxManager(pool), nil)

	attemptID, _, _ := createTestAttempt(t, pool)
	oldPoolChannel := uuid.New()
	newPoolChannel := uuid.New()
	newAcquirerChannel := uuid.New()
	_, err := pool.Exec(context.Background(), `
		UPDATE payment_attempts SET pool_channel_id = $1 WHERE id = $2
	`, oldPoolChannel, attemptID)
	require.NoError(t, err)

	router := &fakePoolRouter{next: &pooldomain.Decision{
		ChannelID:         newPoolChannel,
		AcquirerChannelID: newAcquirerChannel,
	}}
	w.SetPool(router)

	payload, _ := json.Marshal(DispatchPayload{PaymentAttemptID: attemptID.String()})
	err = w.Handle(context.Background(), outbox.OutboxEvent{
		EventType: outbox.EventPaymentDispatchRequested,
		Payload:   payload,
	})
	require.NoError(t, err)

	assert.Equal(t, 2, gw.count, "should retry once after explicit decline")
	assert.Equal(t, 1, router.failovers)
	assert.Equal(t, 1, router.releases, "old lease must be released")
	assert.Equal(t, 1, router.confirms, "successful lease must be confirmed")

	attempt, err := checkout.NewPaymentAttemptRepository(pool).GetPaymentAttempt(context.Background(), attemptID)
	require.NoError(t, err)
	assert.Equal(t, checkout.AttemptCaptured, attempt.Status)
}

// TestDispatchPayload_OnlyContainsAttemptID 验证 P0-4: DispatchPayload
// 只包含 payment_attempt_id 字段，不含金额、商户、币种等敏感/权威字段。
func TestDispatchPayload_OnlyContainsAttemptID(t *testing.T) {
	payload := DispatchPayload{
		PaymentAttemptID: "123e4567-e89b-12d3-a456-426614174000",
	}
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(payloadBytes, &raw))

	// 只允许 payment_attempt_id 字段
	assert.Len(t, raw, 1, "DispatchPayload should only have payment_attempt_id field")
	assert.Contains(t, raw, "payment_attempt_id")
	assert.NotContains(t, raw, "amount_minor")
	assert.NotContains(t, raw, "merchant_id")
	assert.NotContains(t, raw, "currency")
	assert.NotContains(t, raw, "ledger_reservation_id")
	assert.NotContains(t, raw, "payment_method_token")
}
