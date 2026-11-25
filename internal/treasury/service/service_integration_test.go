package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/treasury/repository"
	"github.com/nexusglobal/mamr/internal/treasury/repository/db"
	"github.com/nexusglobal/mamr/pkg/external/bank"
)

func setupTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	ctx := context.Background()
	// OPS-007: 外部共享数据库优先（TEST_DATABASE_DSN），testcontainers 兜底。
	if dsn := os.Getenv("TEST_DATABASE_DSN"); dsn != "" {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("connect external TEST_DATABASE_DSN: %v", err)
		}
		if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO public;`); err != nil {
			t.Fatalf("reset external database: %v", err)
		}
		// 外部库同样执行真实迁移
		migrationDir, mErr := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
		if mErr != nil {
			t.Fatalf("resolve migrations dir: %v", mErr)
		}
		m, err := migrate.New("file://"+filepath.ToSlash(migrationDir), dsn)
		if err != nil {
			t.Fatalf("create migrate: %v", err)
		}
		defer m.Close()
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			t.Fatalf("apply migrations: %v", err)
		}
		return pool, func() { pool.Close() }
	}

	pgReq := testcontainers.ContainerRequest{
		Image:        "postgres:15",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "test",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(120 * time.Second),
	}
	pgContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: pgReq,
		Started:          true,
	})
	require.NoError(t, err)

	pgHost, _ := pgContainer.Host(ctx)
	pgPort, _ := pgContainer.MappedPort(ctx, "5432")
	dsn := fmt.Sprintf("postgres://test:test@%s:%s/test?sslmode=disable", pgHost, pgPort.Port())

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)

	for i := 0; i < 30; i++ {
		if err := pool.Ping(ctx); err == nil {
			break
		}
		time.Sleep(1 * time.Second)
		if i == 29 {
			require.NoError(t, pool.Ping(ctx), "failed to connect to database after 30 seconds")
		}
	}

	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	require.NoError(t, err)
	migrationPath := filepath.ToSlash(migrationDir)
	migrationURL := "file://" + migrationPath

	var m *migrate.Migrate
	for i := 0; i < 10; i++ {
		m, err = migrate.New(migrationURL, dsn)
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
		if i == 9 {
			require.NoError(t, err, "failed to create migrate instance after 10 retries")
		}
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err, "failed to apply migrations")
	}

	cleanup := func() {
		pool.Close()
		_ = pgContainer.Terminate(ctx)
	}

	return pool, cleanup
}

func getWeekdayTime() time.Time {
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Now().In(loc)
	for now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		now = now.AddDate(0, 0, 1)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, loc)
}

func createTestService(pool *pgxpool.Pool) *Service {
	repo := repository.New(pool)
	txm := postgres.NewTxManager(pool)
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	return New(repo, bank.NewMockBank(), txm, pool, nil, log)
}

func seedTestData(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	ctx := context.Background()

	_, err := pool.Exec(ctx, `INSERT INTO banking_calendar (id, calendar_name, jurisdiction, timezone, business_days, holidays, emergency_closure_dates, cut_off_matrix)
		VALUES ($1, 'US_FED', 'US', 'America/New_York', '["Monday","Tuesday","Wednesday","Thursday","Friday"]', '[]', '[]', '{"ACH":"17:00","WIRE":"15:00"}')`,
		uuid.New())
	require.NoError(t, err)

	sourceAccountID := uuid.New()
	targetAccountID := uuid.New()

	_, err = pool.Exec(ctx, `INSERT INTO bank_account_balance (id, bank_account_id, currency, accumulated_minor)
		VALUES ($1, $2, 'USD', 500000)`, uuid.New(), sourceAccountID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO sweep_rule (
		id, source_bank_account_id, target_bank_account_id, currency, rail, beneficiary_type,
		trigger_threshold_minor, sweep_ratio_bps, max_single_sweep_minor, min_residual_minor,
		max_daily_sweep_count, approval_threshold_minor, status, delay_queue_enabled
	) VALUES ($1, $2, $3, 'USD', 'ACH', 'INDIVIDUAL',
		100000, 10000, 1000000, 0,
		10, 1000000, 'Active', false)`,
		uuid.New(), sourceAccountID, targetAccountID)
	require.NoError(t, err)

	row := pool.QueryRow(ctx, `SELECT id FROM sweep_rule WHERE source_bank_account_id = $1`, sourceAccountID)
	var ruleIDReturned uuid.UUID
	err = row.Scan(&ruleIDReturned)
	require.NoError(t, err)

	return ruleIDReturned, sourceAccountID
}

func TestEvaluate_Submit_CreatesTransfer(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ruleID, _ := seedTestData(t, pool)
	svc := createTestService(pool)

	weekdayTime := getWeekdayTime()
	result, err := svc.Evaluate(context.Background(), EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        weekdayTime,
	})
	require.NoError(t, err)
	assert.Equal(t, "SUBMIT", string(result.Decision))
	assert.NotEmpty(t, result.TransferID)

	q := db.New(pool)
	transferID, _ := uuid.Parse(result.TransferID)
	transfer, err := q.GetSweepTransfer(context.Background(), transferID)
	require.NoError(t, err)
	assert.Equal(t, "Locked", transfer.Status)
	assert.Equal(t, int64(500000), transfer.AmountMinor)
	assert.Equal(t, ruleID, transfer.SweepRuleID)
}

func TestEvaluate_Submit_Idempotent(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ruleID, _ := seedTestData(t, pool)
	svc := createTestService(pool)

	weekdayTime := getWeekdayTime()
	result1, err := svc.Evaluate(context.Background(), EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        weekdayTime,
	})
	require.NoError(t, err)
	assert.Equal(t, "SUBMIT", string(result1.Decision))
	assert.NotEmpty(t, result1.TransferID)

	result2, err := svc.Evaluate(context.Background(), EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        weekdayTime,
	})
	require.NoError(t, err)
	assert.Equal(t, "SUBMIT", string(result2.Decision))
	assert.Equal(t, result1.TransferID, result2.TransferID)

	var count int64
	err = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM sweep_transfer WHERE sweep_rule_id = $1`, ruleID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestEvaluate_Approval_CreatesApprovalRecord(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	sourceAccountID := uuid.New()
	targetAccountID := uuid.New()

	_, err := pool.Exec(ctx, `INSERT INTO banking_calendar (id, calendar_name, jurisdiction, timezone, business_days, holidays, emergency_closure_dates, cut_off_matrix)
		VALUES ($1, 'US_FED', 'US', 'America/New_York', '["Monday","Tuesday","Wednesday","Thursday","Friday"]', '[]', '[]', '{"ACH":"17:00","WIRE":"15:00"}')`,
		uuid.New())
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO bank_account_balance (id, bank_account_id, currency, accumulated_minor)
		VALUES ($1, $2, 'USD', 2000000)`, uuid.New(), sourceAccountID)
	require.NoError(t, err)

	ruleID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sweep_rule (
		id, source_bank_account_id, target_bank_account_id, currency, rail, beneficiary_type,
		trigger_threshold_minor, sweep_ratio_bps, max_single_sweep_minor, min_residual_minor,
		max_daily_sweep_count, approval_threshold_minor, status, delay_queue_enabled
	) VALUES ($1, $2, $3, 'USD', 'ACH', 'THIRD_PARTY',
		100000, 10000, 1000000, 0,
		10, 500000, 'Active', false)`,
		ruleID, sourceAccountID, targetAccountID)
	require.NoError(t, err)

	svc := createTestService(pool)

	weekdayTime := getWeekdayTime()
	result, err := svc.Evaluate(context.Background(), EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        weekdayTime,
	})
	require.NoError(t, err)
	assert.Equal(t, "APPROVAL_REQUIRED", string(result.Decision))

	var count int64
	err = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM sweep_approval WHERE sweep_rule_id = $1`, ruleID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	q := db.New(pool)
	approvals, err := q.ListPendingSweepApprovals(ctx, ruleID)
	require.NoError(t, err)
	assert.Len(t, approvals, 1)
	assert.Equal(t, "Pending", approvals[0].Status)
	assert.Equal(t, int64(1000000), approvals[0].AmountMinor)
}

func TestEvaluate_Approval_Idempotent(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	sourceAccountID := uuid.New()
	targetAccountID := uuid.New()

	_, err := pool.Exec(ctx, `INSERT INTO banking_calendar (id, calendar_name, jurisdiction, timezone, business_days, holidays, emergency_closure_dates, cut_off_matrix)
		VALUES ($1, 'US_FED', 'US', 'America/New_York', '["Monday","Tuesday","Wednesday","Thursday","Friday"]', '[]', '[]', '{"ACH":"17:00","WIRE":"15:00"}')`,
		uuid.New())
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO bank_account_balance (id, bank_account_id, currency, accumulated_minor)
		VALUES ($1, $2, 'USD', 2000000)`, uuid.New(), sourceAccountID)
	require.NoError(t, err)

	ruleID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sweep_rule (
		id, source_bank_account_id, target_bank_account_id, currency, rail, beneficiary_type,
		trigger_threshold_minor, sweep_ratio_bps, max_single_sweep_minor, min_residual_minor,
		max_daily_sweep_count, approval_threshold_minor, status, delay_queue_enabled
	) VALUES ($1, $2, $3, 'USD', 'ACH', 'THIRD_PARTY',
		100000, 10000, 1000000, 0,
		10, 500000, 'Active', false)`,
		ruleID, sourceAccountID, targetAccountID)
	require.NoError(t, err)

	svc := createTestService(pool)

	weekdayTime := getWeekdayTime()
	_, err = svc.Evaluate(context.Background(), EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        weekdayTime,
	})
	require.NoError(t, err)

	_, err = svc.Evaluate(context.Background(), EvaluateCmd{
		SweepRuleID: ruleID,
		AsOf:        weekdayTime,
	})
	require.NoError(t, err)

	var count int64
	err = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM sweep_approval WHERE sweep_rule_id = $1`, ruleID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestApproveSweep_CreatesTransfer(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	sourceAccountID := uuid.New()
	targetAccountID := uuid.New()

	_, err := pool.Exec(ctx, `INSERT INTO banking_calendar (id, calendar_name, jurisdiction, timezone, business_days, holidays, emergency_closure_dates, cut_off_matrix)
		VALUES ($1, 'US_FED', 'US', 'America/New_York', '["Monday","Tuesday","Wednesday","Thursday","Friday"]', '[]', '[]', '{"ACH":"17:00","WIRE":"15:00"}')`,
		uuid.New())
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO bank_account_balance (id, bank_account_id, currency, accumulated_minor)
		VALUES ($1, $2, 'USD', 2000000)`, uuid.New(), sourceAccountID)
	require.NoError(t, err)

	ruleID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sweep_rule (
		id, source_bank_account_id, target_bank_account_id, currency, rail, beneficiary_type,
		trigger_threshold_minor, sweep_ratio_bps, max_single_sweep_minor, min_residual_minor,
		max_daily_sweep_count, approval_threshold_minor, status
	) VALUES ($1, $2, $3, 'USD', 'ACH', 'INDIVIDUAL',
		100000, 10000, 1000000, 0,
		10, 1000000, 'Active')`,
		ruleID, sourceAccountID, targetAccountID)
	require.NoError(t, err)

	approvalID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sweep_approval (
		id, sweep_rule_id, merchant_id, amount_minor, currency, rail, beneficiary_type, status,
		required_approvals, current_approvals, idempotency_key
	) VALUES ($1, $2, $3, $4, $5, $6, $7, 'Pending', 1, 0, $8)`,
		approvalID, ruleID, uuid.Nil, int64(2000000), "USD", "ACH", "INDIVIDUAL", "test-key")
	require.NoError(t, err)

	svc := createTestService(pool)

	err = svc.ApproveSweep(ctx, approvalID)
	require.NoError(t, err)

	var count int64
	err = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM sweep_transfer WHERE sweep_rule_id = $1`, ruleID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	q := db.New(pool)
	approvals, err := q.ListPendingSweepApprovals(ctx, ruleID)
	require.NoError(t, err)
	assert.Len(t, approvals, 0)

	approval, err := q.GetSweepApproval(ctx, approvalID)
	require.NoError(t, err)
	assert.Equal(t, "Approved", approval.Status)
}

func TestSubmit_CallsBankAPI(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	_, err := pool.Exec(ctx, `INSERT INTO banking_calendar (id, calendar_name, jurisdiction, timezone, business_days, holidays, emergency_closure_dates, cut_off_matrix)
		VALUES ($1, 'US_FED', 'US', 'America/New_York', '["Monday","Tuesday","Wednesday","Thursday","Friday"]', '[]', '[]', '{"ACH":"17:00","WIRE":"15:00"}')`,
		uuid.New())
	require.NoError(t, err)

	sourceAccountID := uuid.New()
	targetAccountID := uuid.New()

	_, err = pool.Exec(ctx, `INSERT INTO bank_account_balance (id, bank_account_id, currency, accumulated_minor)
		VALUES ($1, $2, 'USD', 500000)`, uuid.New(), sourceAccountID)
	require.NoError(t, err)

	ruleID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sweep_rule (
		id, source_bank_account_id, target_bank_account_id, currency, rail, beneficiary_type,
		trigger_threshold_minor, sweep_ratio_bps, max_single_sweep_minor, min_residual_minor,
		max_daily_sweep_count, approval_threshold_minor, status
	) VALUES ($1, $2, $3, 'USD', 'ACH', 'INDIVIDUAL',
		100000, 10000, 1000000, 0,
		10, 1000000, 'Active')`,
		ruleID, sourceAccountID, targetAccountID)
	require.NoError(t, err)

	transferID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sweep_transfer (
		id, sweep_rule_id, source_account_id, target_account_id, amount_minor,
		currency, rail, status, idempotency_key, version
	) VALUES ($1, $2, $3, $4, $5, $6, $7, 'Locked', $8, 0)`,
		transferID, ruleID, sourceAccountID, targetAccountID, int64(500000), "USD", "ACH", "test-key")
	require.NoError(t, err)

	svc := createTestService(pool)

	err = svc.Submit(ctx, transferID)
	require.NoError(t, err)

	q := db.New(pool)
	transfer, err := q.GetSweepTransfer(ctx, transferID)
	require.NoError(t, err)
	assert.Equal(t, "Submitted", transfer.Status)
	assert.Equal(t, int32(1), transfer.Version)
}
