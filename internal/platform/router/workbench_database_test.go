package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
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
			"DELETE FROM ledger_transaction WHERE aggregate_type='payout_request' AND aggregate_id IN (SELECT id FROM payout_request WHERE merchant_id IN ($1,$2))",
			"DELETE FROM payout_request WHERE merchant_id IN ($1,$2)",
			"DELETE FROM merchant_payout_address WHERE merchant_id IN ($1,$2)",
		} {
			_, err := db.Exec(ctx, query, merchant, foreignMerchant)
			require.NoError(t, err)
		}
		for _, table := range []string{"bank_collection_reconciliation_log", "bank_collection_intent", "settlement_batch"} {
			_, err := db.Exec(ctx, "DELETE FROM "+table+" WHERE tenant_id IN ($1,$2)", owner, neighbor)
			require.NoError(t, err)
		}
		for _, query := range []string{
			"DELETE FROM provider_credential_vault WHERE channel_id IN (SELECT id FROM tenant_acquirer_channel WHERE merchant_id=$1)",
			"DELETE FROM tenant_acquirer_channel WHERE merchant_id=$1",
			"DELETE FROM acquirer_channels WHERE entity_id=$1",
			"DELETE FROM merchant_routing_policy WHERE merchant_id=$1",
		} {
			_, err := db.Exec(ctx, query, merchant)
			require.NoError(t, err)
		}
		for _, table := range []string{"refund_request", "payment_attempts", "merchant_site", "corporate_entity"} {
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
	fixture(`INSERT INTO refund_request(tenant_id,merchant_id,payment_attempt_id,merchant_refund_id,idempotency_key,amount_minor,currency,status)
		SELECT p.tenant_id,p.merchant_id,p.id,'RF-UI-00'||s.i,'atlas-ui-refund-'||s.i,
			1000+s.i*100,'CNY',CASE s.i WHEN 1 THEN 'PENDING' WHEN 2 THEN 'PROCESSING' ELSE 'SUCCEEDED' END
		FROM generate_series(1,3) s(i)
		JOIN payment_attempts p ON p.tenant_id=$1 AND p.merchant_order_id='DEMO-ORDER-'||lpad(s.i::text,2,'0')`, owner)
	fixture(`INSERT INTO settlement_batch(tenant_id,merchant_id,currency,business_date,status,total_amount_minor,item_count)
		VALUES ($1,$2,'CNY',(now() AT TIME ZONE 'Asia/Shanghai')::date,'SETTLED',59900,1),
		       ($3,$4,'USD',(now() AT TIME ZONE 'Asia/Shanghai')::date,'SETTLED',88800,1)`, owner, merchant, neighbor, foreignMerchant)
	fixture(`WITH intent AS (
		INSERT INTO bank_collection_intent(tenant_id,merchant_id,order_id,asset_code,expected_amount,payment_reference,expires_at)
		VALUES ($1,$2,'atlas-order','EUR',100.50,$1::uuid::text||'-atlas',now()),
		       ($3,$4,'other-order','GBP',200.25,$3::uuid::text||'-atlas',now())
		RETURNING id,tenant_id,expected_amount
	)
	INSERT INTO bank_collection_reconciliation_log(tenant_id,intent_id,reconciliation_date,matched,mismatch_type,expected_amount)
	SELECT tenant_id,id,(now() AT TIME ZONE 'Asia/Shanghai')::date,false,'AMOUNT_MISMATCH',expected_amount FROM intent`, owner, merchant, neighbor, foreignMerchant)
	fixture(`WITH addresses AS (
		INSERT INTO merchant_payout_address(merchant_id,target_asset,target_network,address_format_type,target_address_enc,target_address_hash,target_address_masked)
		VALUES ($1,'USDT','TEST','TEST','\x00','atlas-local','masked'),($2,'USDT','TEST','TEST','\x00','atlas-neighbor','masked')
		RETURNING id,merchant_id
	), payouts AS (
		INSERT INTO payout_request(merchant_id,payout_address_id,amount,fiat_currency,target_asset,target_network,status,idempotency_key)
		SELECT merchant_id,id,CASE WHEN merchant_id=$1 THEN 55550 ELSE 99990 END,
		 CASE WHEN merchant_id=$1 THEN 'CNY' ELSE 'GBP' END,'USDT','TEST','Confirmed',merchant_id::text||'-payout' FROM addresses
		RETURNING id
	)
	INSERT INTO ledger_transaction(idempotency_key,transaction_type,aggregate_type,aggregate_id,created_at)
	SELECT id::text||'-confirm','PAYOUT_CONFIRM','payout_request',id,now() FROM payouts
	UNION ALL SELECT id::text||'-freeze','PAYOUT_FREEZE','payout_request',id,now() FROM payouts`, merchant, foreignMerchant)

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
	var stats struct {
		Metrics struct {
			SettledBatches           int               `json:"settled_batches"`
			ReconciliationExceptions int               `json:"reconciliation_exceptions"`
			OpenRefunds              int               `json:"open_refunds"`
			CapturedFlow             []json.RawMessage `json:"captured_flow"`
			PaidFlow                 []json.RawMessage `json:"paid_flow"`
		} `json:"metrics"`
	}
	require.NoError(t, json.Unmarshal(summary.Body.Bytes(), &stats))
	require.Equal(t, 1, stats.Metrics.SettledBatches)
	require.Equal(t, 1, stats.Metrics.ReconciliationExceptions)
	require.Len(t, stats.Metrics.CapturedFlow, 1)
	require.Contains(t, string(stats.Metrics.CapturedFlow[0]), `"amount_minor":"1677200"`)
	require.Len(t, stats.Metrics.PaidFlow, 1)
	require.Contains(t, string(stats.Metrics.PaidFlow[0]), `"amount":"55550.0000"`)
	export := get(owner, base+"funds.csv")
	require.Equal(t, http.StatusOK, export.Code, export.Body.String())
	require.Contains(t, export.Header().Get("Content-Disposition"), "attachment;")
	require.Equal(t, "no-store", export.Header().Get("Cache-Control"))
	require.Contains(t, export.Body.String(), "CNY,已收款,1677200")
	require.Contains(t, export.Body.String(), "CNY,已确认出款,55550.0000")
	require.NotContains(t, export.Body.String(), "GBP")
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, base+"funds.csv", nil))
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	require.NotContains(t, get(neighbor, base+"funds.csv").Body.String(), "CNY")
	unsettled := get(owner, base+"records/unsettled-orders")
	require.Equal(t, http.StatusOK, unsettled.Code, unsettled.Body.String())
	require.Contains(t, unsettled.Body.String(), "DEMO-ORDER-28")
	require.NotContains(t, unsettled.Body.String(), "DEMO-ORDER-20")
	exceptions := get(owner, base+"records/reconciliation-exceptions")
	require.Equal(t, http.StatusOK, exceptions.Code, exceptions.Body.String())
	require.Contains(t, exceptions.Body.String(), "AMOUNT_MISMATCH")
	require.Contains(t, exceptions.Body.String(), "EUR")
	require.NotContains(t, exceptions.Body.String(), "GBP")
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
	openRefunds := get(owner, base+"records/refunds?status=PENDING,PROCESSING,UNKNOWN")
	require.Equal(t, http.StatusOK, openRefunds.Code, openRefunds.Body.String())
	var openRefundItems struct {
		Items []json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(openRefunds.Body.Bytes(), &openRefundItems))
	require.Len(t, openRefundItems.Items, 2)
	require.NotContains(t, openRefunds.Body.String(), "SUCCEEDED")
	require.Contains(t, openRefunds.Body.String(), "RF-UI-001")
	require.Contains(t, openRefunds.Body.String(), "RF-UI-002")
	require.Equal(t, stats.Metrics.OpenRefunds, len(openRefundItems.Items))
	pendingRefunds := get(owner, base+"records/refunds?status=PENDING")
	require.Equal(t, http.StatusOK, pendingRefunds.Code, pendingRefunds.Body.String())
	require.Contains(t, pendingRefunds.Body.String(), "RF-UI-001")
	require.NotContains(t, pendingRefunds.Body.String(), "RF-UI-002")
	require.Equal(t, http.StatusBadRequest, get(owner, base+"records/refunds?status="+strings.Repeat("A", 129)).Code)
	require.Equal(t, http.StatusBadRequest, get(owner, base+"records/refunds?status=PENDING,,PROCESSING").Code)
	require.Equal(t, http.StatusBadRequest, get(owner, base+"records/refunds?status=%20%20").Code)
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
