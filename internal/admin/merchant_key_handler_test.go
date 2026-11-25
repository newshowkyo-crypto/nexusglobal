package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/testenv"
)

func setupKeyDB(t *testing.T) (*pgxpool.Pool, func()) {
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

func TestMerchantKey_IssueStatusRevoke(t *testing.T) {
	pool, cleanup := setupKeyDB(t)
	defer cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country)
		VALUES ($1, $1, 'Test Co', 'Test Co LLC', 'US')
	`, merchantID)
	require.NoError(t, err)

	h := NewMerchantKeyHandler(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := chi.NewRouter()
	r.Post("/merchants/{id}/api-keys", h.Issue)
	r.Get("/merchants/{id}/api-keys", h.Status)
	r.Delete("/merchants/{id}/api-keys", h.Revoke)

	// Issue
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/merchants/"+merchantID.String()+"/api-keys", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var issued struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &issued))
	apiKey, _ := issued.Data["api_key"].(string)
	require.NotEmpty(t, apiKey)

	var stored *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT sign_token_hash FROM corporate_entity WHERE id = $1`, merchantID).Scan(&stored))
	require.NotNil(t, stored)
	sum := sha256.Sum256([]byte(apiKey))
	assert.Equal(t, hex.EncodeToString(sum[:]), *stored, "DB must store SHA-256 hex")

	// Status
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/merchants/"+merchantID.String()+"/api-keys", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var status struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, true, status.Data["configured"])
	fp, _ := status.Data["fingerprint"].(string)
	assert.Len(t, fp, 8)

	// Revoke
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/merchants/"+merchantID.String()+"/api-keys", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, pool.QueryRow(ctx, `SELECT sign_token_hash FROM corporate_entity WHERE id = $1`, merchantID).Scan(&stored))
	assert.Nil(t, stored)

	// 不存在的商户
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/merchants/"+uuid.NewString()+"/api-keys", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
