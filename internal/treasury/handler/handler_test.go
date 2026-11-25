package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
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
	dsn := "postgres://test:test@" + pgHost + ":" + pgPort.Port() + "/test?sslmode=disable"

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

func setupRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/admin/banking-calendar", h.CreateCalendar)
	r.Post("/admin/sweep-rule", h.CreateSweepRule)
	r.Patch("/admin/sweep-rule/{id}", h.UpdateSweepRule)
	r.Post("/internal/sweep/evaluate", h.Evaluate)
	r.Post("/internal/sweep/{id}/submit", h.Submit)
	r.Post("/internal/sweep/{id}/reconcile", h.Reconcile)
	return r
}

func TestCreateCalendar_Success(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := New(nil, pool)
	r := setupRouter(h)

	body := `{"calendar_name":"US_FED","jurisdiction":"US","timezone":"America/New_York"}`
	req := httptest.NewRequest("POST", "/admin/banking-calendar", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestCreateSweepRule_Success(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := New(nil, pool)
	r := setupRouter(h)

	sourceID := uuid.New()
	targetID := uuid.New()
	body := fmt.Sprintf(`{"currency":"USD","rail":"ACH","beneficiary_type":"SAME_NAME","source_bank_account_id":"%s","target_bank_account_id":"%s"}`, sourceID, targetID)
	req := httptest.NewRequest("POST", "/admin/sweep-rule", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestUpdateSweepRule_InvalidID(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := New(nil, pool)
	r := setupRouter(h)

	req := httptest.NewRequest("PATCH", "/admin/sweep-rule/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "INVALID_ARGUMENT", errObj["code"])
}

func TestUpdateSweepRule_Success(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := New(nil, pool)
	r := setupRouter(h)

	id := uuid.New()
	// PATCH 是「存在且版本匹配」的更新：必须先有一条规则
	_, err := pool.Exec(context.Background(), `
		INSERT INTO sweep_rule
		    (id, source_bank_account_id, target_bank_account_id, currency, rail,
		     beneficiary_type, trigger_threshold_minor, max_single_sweep_minor)
		VALUES ($1, $2, $3, 'USD', 'ACH', 'SAME_NAME', 1000, 100000)
	`, id, uuid.New(), uuid.New())
	require.NoError(t, err)

	body := `{"max_daily_sweep_count":3,"currency":"USD","rail":"ACH","beneficiary_type":"SAME_NAME"}`
	req := httptest.NewRequest("PATCH", "/admin/sweep-rule/"+id.String(), strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var count int
	var version int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT max_daily_sweep_count, version FROM sweep_rule WHERE id = $1`, id).Scan(&count, &version))
	assert.Equal(t, 3, count)
	assert.Equal(t, 1, version)
}

func TestEvaluate_NilService(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := New(nil, pool)
	r := setupRouter(h)

	req := httptest.NewRequest("POST", "/internal/sweep/evaluate", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp EvaluateResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "ACCUMULATE", resp.Decision)
}

func TestSubmit_NilService(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := New(nil, pool)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/sweep/"+id.String()+"/submit", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestReconcile_NilService(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := New(nil, pool)
	r := setupRouter(h)

	id := uuid.New()
	req := httptest.NewRequest("POST", "/internal/sweep/"+id.String()+"/reconcile", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
