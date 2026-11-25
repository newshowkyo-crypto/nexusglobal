package checkout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nexusglobal/mamr/internal/platform/httpx/middleware"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

// getTestPool 启动空 PostgreSQL 并执行全部真实迁移（移除手工建表捷径 G01）
// 优先使用 TEST_DATABASE_DSN 环境变量（外部 PostgreSQL），否则使用 testcontainers
func getTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	ctx := context.Background()

	// 优先使用外部数据库
	if externalDSN := os.Getenv("TEST_DATABASE_DSN"); externalDSN != "" {
		pool, err := postgres.NewPool(ctx, externalDSN)
		if err != nil {
			t.Fatal(err)
		}
		// 重置数据库
		_, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO public;`)
		if err != nil {
			t.Fatal(err)
		}
		runMigrationsExternalWithDSN(t, externalDSN)
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

	t.Cleanup(func() {
		container.Terminate(ctx)
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}

	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatal(err)
	}

	dsn := "postgres://test:test@" + host + ":" + port.Port() + "/test?sslmode=disable"
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	// 执行全部真实迁移（不再手工 CREATE TABLE）
	runMigrationsExternal(t, pool)
	return pool
}

// runMigrationsExternal 执行全部真实迁移
func runMigrationsExternal(t *testing.T, pool *pgxpool.Pool) {
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	migrationPath := filepath.ToSlash(migrationDir)
	migrationURL := "file://" + migrationPath

	config := pool.Config().ConnConfig
	migrateDSN := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		config.User, config.Password, config.Host, config.Port, config.Database)

	m, err := migrate.New(migrationURL, migrateDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("failed to apply migrations: %v", err)
	}
}

// runMigrationsExternalWithDSN 使用显式 DSN 执行全部真实迁移
func runMigrationsExternalWithDSN(t *testing.T, dsn string) {
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	migrationPath := filepath.ToSlash(migrationDir)
	migrationURL := "file://" + migrationPath

	m, err := migrate.New(migrationURL, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("failed to apply migrations: %v", err)
	}
}

type mockMerchantVerifier struct{}

func (m *mockMerchantVerifier) VerifyMerchant(ctx context.Context, merchantID uuid.UUID, signToken string) (bool, error) {
	return true, nil
}

func (m *mockMerchantVerifier) IsMerchantActive(ctx context.Context, merchantID uuid.UUID) (bool, error) {
	return true, nil
}

func (m *mockMerchantVerifier) GetMerchantProfile(ctx context.Context, merchantID uuid.UUID) (*middleware.MerchantProfile, error) {
	return &middleware.MerchantProfile{
		ID:            merchantID,
		EntityID:      uuid.New(),
		RiskProfileID: uuid.New(),
		Status:        "ACTIVE",
		ProviderTier:  "TIER_2_COMPLIANT",
		TenantID:      "",
	}, nil
}

func (m *mockMerchantVerifier) GetAllowedChannels(ctx context.Context, merchantID uuid.UUID) ([]string, error) {
	return []string{"card", "bank"}, nil
}

func TestCheckoutSession_ReturnsHostedURL(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	handler := NewSessionHandler(service, "http://localhost:8080")

	mw := middleware.MerchantAuth(&mockMerchantVerifier{})

	reqBody, _ := json.Marshal(map[string]interface{}{
		"merchant_order_id": "order_001",
		"amount_minor":      1000,
		"currency":          "USD",
		"success_url":       "https://example.com/success",
		"cancel_url":        "https://example.com/cancel",
	})

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", bytes.NewReader(reqBody))
	req.Header.Set("X-Merchant-Id", merchantID.String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(handler.CreateSession)).ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp)

	if resp["session_id"] == nil {
		t.Error("expected session_id in response")
	}
	if resp["checkout_url"] == nil {
		t.Error("expected checkout_url in response")
	}
}

func TestCheckoutSession_RejectsNonHTTPSURL(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))
	handler := NewSessionHandler(service, "http://localhost:8080")

	mw := middleware.MerchantAuth(&mockMerchantVerifier{})

	reqBody, _ := json.Marshal(map[string]interface{}{
		"merchant_order_id": "order_001",
		"amount_minor":      1000,
		"currency":          "USD",
		"success_url":       "http://example.com/success",
		"cancel_url":        "https://example.com/cancel",
	})

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", bytes.NewReader(reqBody))
	req.Header.Set("X-Merchant-Id", uuid.New().String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(handler.CreateSession)).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}
