package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// Run against a migrated, disposable PostgreSQL database.
func TestWorkbenchDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_WORKBENCH_DATABASE_DSN")
	if dsn == "" {
		t.Skip("requires an isolated, migrated TEST_WORKBENCH_DATABASE_DSN")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer db.Close()
	owner, neighbor := uuid.New(), uuid.New()
	merchant, foreignMerchant := uuid.New(), uuid.New()
	fixture := func(query string, args ...any) {
		t.Helper()
		_, err := db.Exec(ctx, query, args...)
		require.NoError(t, err)
	}
	defer func() {
		for _, query := range []string{
			"DELETE FROM provider_credential_vault WHERE channel_id IN (SELECT id FROM tenant_acquirer_channel WHERE merchant_id=$1)",
			"DELETE FROM tenant_acquirer_channel WHERE merchant_id=$1",
			"DELETE FROM acquirer_channels WHERE entity_id=$1",
			"DELETE FROM merchant_routing_policy WHERE merchant_id=$1",
		} {
			_, err := db.Exec(ctx, query, merchant)
			require.NoError(t, err)
		}
		for _, table := range []string{"payment_attempts", "merchant_site", "corporate_entity"} {
			_, err := db.Exec(ctx, "DELETE FROM "+table+" WHERE tenant_id IN ($1,$2)", owner, neighbor)
			require.NoError(t, err)
		}
	}()
	fixture(`INSERT INTO corporate_entity(id,tenant_id,name,legal_name,country)
		VALUES ($1,$2,'海岸运营测试商户','Demo Company','SG'),($3,$4,'另一租户测试商户','Other Company','GB')`, merchant, owner, foreignMerchant, neighbor)
	fixture(`INSERT INTO merchant_site(tenant_id,merchant_id,site_code,display_name,origin)
		VALUES ($1,$2,'store-demo','跨境测试站','https://shop.example.test')`, owner, merchant)
	fixture(`INSERT INTO merchant_routing_policy(merchant_id,provider_tier) VALUES ($1,'TIER_2_COMPLIANT')`, merchant)
	fixture(`INSERT INTO payment_attempts(order_id,merchant_id,tenant_id,entry_channel,entry_ref,merchant_order_id,amount_minor,currency,status,created_at,captured_at,settled_at)
		SELECT gen_random_uuid(),$1::uuid,$2::uuid,'S2S',i::text,'DEMO-ORDER-'||lpad(i::text,2,'0'),
			59900,'CNY',CASE WHEN i=30 THEN 'UNKNOWN_EXTERNAL_STATE' WHEN i=29 THEN 'FAILED' WHEN i>20 THEN 'CAPTURED' ELSE 'SETTLED' END,
			date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai' + (i/3) * interval '1 hour',
			CASE WHEN i<29 THEN now() END, CASE WHEN i<=20 THEN now() END
		FROM generate_series(1,30) i
		UNION ALL SELECT gen_random_uuid(),$3::uuid,$4::uuid,'S2S','foreign','OTHER-TENANT-ORDER',100,'USD','UNKNOWN_EXTERNAL_STATE',now(),NULL,NULL`,
		merchant, owner, foreignMerchant, neighbor)

	deps := makeDeps()
	deps.Pool = db
	deps.Rdb = redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	defer deps.Rdb.Close()
	deps.Cfg.AppEnv = "local"
	deps.Cfg.AdminSessionSecret = "workbench-local-test-session-secret-32-bytes"
	kms, err := crypto.NewMockKMS(deps.Cfg.MasterKeyBase64)
	require.NoError(t, err)
	deps.Enc = crypto.NewEncryptor(kms)
	handler := BuildRouter(deps)
	signed := func(tenant uuid.UUID) string {
		now := time.Now()
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": "workbench-demo", "role": httpx.AdminRoleSecurityAdmin, "tenant_id": tenant.String(),
			"iss": httpx.AdminIssuerExpected, "aud": httpx.AdminAudienceExpected,
			"iat": now.Unix(), "exp": now.Add(12 * time.Minute).Unix(),
		}).SignedString([]byte(deps.Cfg.JWTSecret))
		require.NoError(t, err)
		return token
	}
	get := func(tenant uuid.UUID, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+signed(tenant))
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	const base = "/admin/operations/"
	summary := get(owner, base+"summary")
	require.Equal(t, http.StatusOK, summary.Code, summary.Body.String())
	require.Contains(t, summary.Body.String(), `"unknown_payments":1`)
	require.Contains(t, summary.Body.String(), `"attempts":30`)
	require.Contains(t, summary.Body.String(), `"captured":28`)
	require.Contains(t, summary.Body.String(), `"amount":"479200"`)
	var first struct {
		Items []json.RawMessage `json:"items"`
		Next  string            `json:"next"`
	}
	page := get(owner, base+"records/orders")
	require.Equal(t, http.StatusOK, page.Code, page.Body.String())
	require.NoError(t, json.Unmarshal(page.Body.Bytes(), &first))
	require.Len(t, first.Items, 25)
	require.NotEmpty(t, first.Next)
	require.NotContains(t, page.Body.String(), "OTHER-TENANT-ORDER")
	require.NotContains(t, page.Body.String(), "payment_method_token")
	older := get(owner, base+"records/orders?before="+first.Next)
	require.Equal(t, http.StatusOK, older.Code, older.Body.String())
	require.NotContains(t, older.Body.String(), "DEMO-ORDER-30")
	require.Contains(t, older.Body.String(), "DEMO-ORDER-01")
	filtered := get(owner, base+"records/orders?status=UNKNOWN_EXTERNAL_STATE")
	require.Equal(t, http.StatusOK, filtered.Code, filtered.Body.String())
	require.Contains(t, filtered.Body.String(), "DEMO-ORDER-30")
	require.NotContains(t, filtered.Body.String(), "DEMO-ORDER-29")
	for _, resource := range []string{"refunds", "settlements", "payouts", "authorizations"} {
		result := get(owner, base+"records/"+resource)
		require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	}
	require.Contains(t, get(owner, base+"records/authorizations").Body.String(), "UNCONFIGURED")
	require.NotContains(t, get(neighbor, base+"records/authorizations").Body.String(), "store-demo")

	// Optional headless acceptance uses the same real routes, database and browser session exchange.
	if script := os.Getenv("TEST_WORKBENCH_BROWSER_SCRIPT"); script != "" {
		server := httptest.NewServer(handler)
		defer server.Close()
		command := exec.Command(os.Getenv("TEST_WORKBENCH_NODE"), script)
		command.Env = append(os.Environ(), "WORKBENCH_BASE="+server.URL, "WORKBENCH_TOKEN="+signed(owner))
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s", output)
		t.Logf("%s", output)
		var currencyCount, encryptedCount int
		var active bool
		err = db.QueryRow(ctx, `SELECT jsonb_array_length(c.currencies),c.is_active,
			(SELECT count(*) FROM provider_credential_vault WHERE channel_id=c.id AND
			octet_length(ciphertext)>0 AND position(convert_to('workbench-test-only','UTF8') in ciphertext)=0)
			FROM tenant_acquirer_channel c WHERE c.merchant_id=$1 AND c.channel_code='stripe_browser_demo'`, merchant).
			Scan(&currencyCount, &active, &encryptedCount)
		require.NoError(t, err)
		require.Equal(t, 2, currencyCount)
		require.False(t, active)
		require.Equal(t, 2, encryptedCount)
	}
}
