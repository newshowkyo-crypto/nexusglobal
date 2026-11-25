package middleware

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestDBMerchantVerifier_RealDB_MigrationColumnsExist 验证迁移 000032 新增列存在
func TestDBMerchantVerifier_RealDB_MigrationColumnsExist(t *testing.T) {
	ctx := context.Background()
	pool := setupRealTestDB(t, ctx)
	defer pool.Close()

	// 共享测试库自隔离：清理本测试涉及的历史表（顺序：先依赖表）
	_, err := pool.Exec(ctx, `DROP TABLE IF EXISTS merchant_risk_profiles CASCADE`)
	if err != nil {
		t.Fatalf("drop merchant_risk_profiles failed: %v", err)
	}
	_, err = pool.Exec(ctx, `DROP TABLE IF EXISTS corporate_entity CASCADE`)
	if err != nil {
		t.Fatalf("drop corporate_entity failed: %v", err)
	}

	// 执行迁移 000024
	_, err = pool.Exec(ctx, `
		CREATE TABLE corporate_entity (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL,
			name VARCHAR(100) NOT NULL,
			legal_name VARCHAR(255) NOT NULL,
			tax_id VARCHAR(50),
			country VARCHAR(2) NOT NULL,
			jurisdiction VARCHAR(100),
			status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
			metadata JSONB,
			created_at TIMESTAMP NOT NULL DEFAULT now(),
			updated_at TIMESTAMP NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		t.Fatalf("create corporate_entity failed: %v", err)
	}

	// 执行迁移 000025 (merchant_risk_profiles 表)
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS merchant_risk_profiles (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			merchant_id UUID NOT NULL REFERENCES corporate_entity(id),
			rolling_reserve_rate_bps INT NOT NULL DEFAULT 1000,
			dispute_reserve_rate_bps INT NOT NULL DEFAULT 500,
			reserve_release_days INT NOT NULL DEFAULT 30,
			status TEXT NOT NULL DEFAULT 'ACTIVE',
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("migration 000025 failed: %v", err)
	}

	execMigrationFile(t, ctx, pool, "../../../../migrations/000032_merchant_auth_profile_columns.up.sql")

	// 验证 sign_token_hash 列存在
	var columnName string
	err = pool.QueryRow(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'corporate_entity' AND column_name = 'sign_token_hash'
	`).Scan(&columnName)
	if err != nil {
		t.Errorf("sign_token_hash column should exist: %v", err)
	}

	// 验证 provider_tier 列存在
	err = pool.QueryRow(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'corporate_entity' AND column_name = 'provider_tier'
	`).Scan(&columnName)
	if err != nil {
		t.Errorf("provider_tier column should exist: %v", err)
	}
}

// TestDBMerchantVerifier_RealDB_AllowsMissingRiskProfile 验证缺失 risk profile 时不报错
func TestDBMerchantVerifier_RealDB_AllowsMissingRiskProfile(t *testing.T) {
	ctx := context.Background()
	pool := setupRealTestDB(t, ctx)
	defer pool.Close()

	// 共享测试库自隔离：清理本测试涉及的历史表（顺序：先依赖表）
	_, err := pool.Exec(ctx, `DROP TABLE IF EXISTS merchant_risk_profiles CASCADE`)
	if err != nil {
		t.Fatalf("drop merchant_risk_profiles failed: %v", err)
	}
	_, err = pool.Exec(ctx, `DROP TABLE IF EXISTS corporate_entity CASCADE`)
	if err != nil {
		t.Fatalf("drop corporate_entity failed: %v", err)
	}

	// 执行迁移 000024
	_, err = pool.Exec(ctx, `
		CREATE TABLE corporate_entity (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL,
			name VARCHAR(100) NOT NULL,
			legal_name VARCHAR(255) NOT NULL,
			tax_id VARCHAR(50),
			country VARCHAR(2) NOT NULL,
			jurisdiction VARCHAR(100),
			status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
			metadata JSONB,
			created_at TIMESTAMP NOT NULL DEFAULT now(),
			updated_at TIMESTAMP NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		t.Fatalf("create corporate_entity failed: %v", err)
	}

	// 执行迁移 000025 (merchant_risk_profiles 表)
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS merchant_risk_profiles (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			merchant_id UUID NOT NULL REFERENCES corporate_entity(id),
			rolling_reserve_rate_bps INT NOT NULL DEFAULT 1000,
			dispute_reserve_rate_bps INT NOT NULL DEFAULT 500,
			reserve_release_days INT NOT NULL DEFAULT 30,
			status TEXT NOT NULL DEFAULT 'ACTIVE',
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("migration 000025 failed: %v", err)
	}

	execMigrationFile(t, ctx, pool, "../../../../migrations/000032_merchant_auth_profile_columns.up.sql")

	// 创建测试商户（不创建 merchant_risk_profiles 记录）
	merchantID := uuid.New()
	tenantID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, status, sign_token_hash, provider_tier)
		VALUES ($1, $2, 'Test Merchant', 'Test Legal Name', 'US', 'ACTIVE', 'test_hash_123', 'TIER_2_COMPLIANT')
	`, merchantID, tenantID)
	if err != nil {
		t.Fatalf("insert merchant failed: %v", err)
	}

	// 使用 DBMerchantVerifier 获取 profile
	verifier := NewDBMerchantVerifier(pool)
	profile, err := verifier.GetMerchantProfile(ctx, merchantID)
	if err != nil {
		t.Fatalf("GetMerchantProfile failed: %v", err)
	}

	// 验证 RiskProfileID 为空 UUID
	if profile.RiskProfileID != uuid.Nil {
		t.Errorf("RiskProfileID should be Nil when no merchant_risk_profiles record, got %s", profile.RiskProfileID)
	}

	// 验证其他字段正确
	if profile.ID != merchantID {
		t.Errorf("ID mismatch: got %s, want %s", profile.ID, merchantID)
	}
	if profile.EntityID != merchantID {
		t.Errorf("EntityID should equal MerchantID: got %s, want %s", profile.EntityID, merchantID)
	}
	if profile.ProviderTier != "TIER_2_COMPLIANT" {
		t.Errorf("ProviderTier should be TIER_2_COMPLIANT, got %s", profile.ProviderTier)
	}
}

func setupRealTestDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}
	// OPS-007: 外部共享数据库优先（TEST_DATABASE_DSN），testcontainers 兜底。
	if dsn := os.Getenv("TEST_DATABASE_DSN"); dsn != "" {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("connect external TEST_DATABASE_DSN: %v", err)
		}
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
		WaitingFor: wait.ForListeningPort("5432/tcp"),
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

	dsn := "postgres://test:test@" + host + ":" + port.Port() + "/test"
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	return pool
}

func execMigrationFile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, relPath string) {
	t.Helper()

	sqlBytes, err := os.ReadFile(filepath.Clean(relPath))
	if err != nil {
		t.Fatalf("read migration %s failed: %v", relPath, err)
	}
	if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("execute migration %s failed: %v", relPath, err)
	}
}
