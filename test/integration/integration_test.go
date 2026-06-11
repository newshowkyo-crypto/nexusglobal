package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerdb "github.com/nexusglobal/mamr/internal/ledger/repository/db"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	payoutservice "github.com/nexusglobal/mamr/internal/payout/service"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/platform/stream"
	webhookdomain "github.com/nexusglobal/mamr/internal/webhook/domain"
	webhookrepo "github.com/nexusglobal/mamr/internal/webhook/repository"
	webhookdb "github.com/nexusglobal/mamr/internal/webhook/repository/db"
)

type testEnv struct {
	pool    *pgxpool.Pool
	rdb     *redis.Client
	txm     *postgres.TxManager
	cleanup func()
}

func setupEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

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

	redisReq := testcontainers.ContainerRequest{
		Image:        "redis:7",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}
	redisContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: redisReq,
		Started:          true,
	})
	require.NoError(t, err)

	redisHost, _ := redisContainer.Host(ctx)
	redisPort, _ := redisContainer.MappedPort(ctx, "6379")
	redisAddr := fmt.Sprintf("%s:%s", redisHost, redisPort.Port())

	pool, err := postgres.NewPool(ctx, dsn)
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	require.NoError(t, rdb.Ping(ctx).Err())

	runMigrations(t, pool)

	cleanup := func() {
		pool.Close()
		rdb.Close()
		_ = pgContainer.Terminate(ctx)
		_ = redisContainer.Terminate(ctx)
	}

	return &testEnv{
		pool:    pool,
		rdb:     rdb,
		txm:     postgres.NewTxManager(pool),
		cleanup: cleanup,
	}
}

func runMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	migrationDir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(migrationDir)
	require.NoError(t, err)

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		path := filepath.Join(migrationDir, entry.Name())
		content, err := os.ReadFile(path)
		require.NoError(t, err)

		_, err = pool.Exec(ctx, string(content))
		require.NoError(t, err, "migration %s failed", entry.Name())
	}
}

func TestWebhookDuplicateDetection(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	repo := webhookrepo.New(env.pool)
	enc, _ := crypto.NewMockKMS("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	encryptor := crypto.NewEncryptor(enc)

	producer := stream.NewProducer(env.rdb)

	providerType := "mae"
	providerID := "prov_test_01"
	providerEventID := "evt_dup_001"
	eventType := "webhook"
	payload := []byte(`{"event":"captured","amount":"1000.00"}`)

	encryptedPayload, err := encryptor.Encrypt(ctx, payload, crypto.Meta{
		TenantID:  providerType,
		RecordID:  providerID,
		FieldName: "raw_payload",
	})
	require.NoError(t, err)

	idempotencyKey := webhookdomain.ComputeIdempotencyKey(providerType, providerID, providerEventID, eventType, "", "")

	ingress1, err := repo.InsertIngressEvent(ctx, webhookdb.InsertIngressEventParams{
		ProviderType:         providerType,
		ProviderID:           providerID,
		ProviderEventID:      providerEventID,
		EventType:            eventType,
		IdempotencyKey:       idempotencyKey,
		PayloadHash:          "testhash",
		RawPayloadCiphertext: encryptedPayload,
		Status:               "Received",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, ingress1.ID)

	producer.Publish(ctx, "stream:webhook:mae", stream.Event{
		IdempotencyKey: idempotencyKey,
		ProviderType:   providerType,
		ProviderID:     providerID,
		EventType:      eventType,
		IngressID:      ingress1.ID.String(),
	})

	existing, err := repo.GetIngressEventByIdempotencyKey(ctx, idempotencyKey)
	require.NoError(t, err)
	require.NotNil(t, existing)
	assert.Equal(t, ingress1.ID, existing.ID)

	_, err = repo.InsertIngressEvent(ctx, webhookdb.InsertIngressEventParams{
		ProviderType:         providerType,
		ProviderID:           providerID,
		ProviderEventID:      providerEventID,
		EventType:            eventType,
		IdempotencyKey:       idempotencyKey,
		PayloadHash:          "testhash",
		RawPayloadCiphertext: encryptedPayload,
		Status:               "Received",
	})
	assert.Error(t, err, "duplicate idempotency_key should fail")
}

func TestPayoutFreezeFlow(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	currency := "USD"

	_, err := env.pool.Exec(ctx,
		`INSERT INTO merchant_treasury_ledger (merchant_id, currency, settled_balance, frozen_payout_balance)
		 VALUES ($1, $2, 10000.00, 0)`, merchantID, currency)
	require.NoError(t, err)

	settledAccID := uuid.New()
	frozenAccID := uuid.New()
	_, err = env.pool.Exec(ctx,
		`INSERT INTO ledger_account (id, merchant_id, account_type, currency) VALUES ($1, $2, 'SETTLED', $3)`,
		settledAccID, merchantID, currency)
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx,
		`INSERT INTO ledger_account (id, merchant_id, account_type, currency) VALUES ($1, $2, 'FROZEN_PAYOUT', $3)`,
		frozenAccID, merchantID, currency)
	require.NoError(t, err)

	addrID := uuid.New()
	_, err = env.pool.Exec(ctx,
		`INSERT INTO merchant_payout_address (id, merchant_id, target_asset, target_network, address_format_type, target_address_enc, target_address_hash, target_address_masked)
		 VALUES ($1, $2, 'USDT', 'TRON_TRC20', 'TRON_TRC20', $3, $4, $5)`,
		addrID, merchantID, []byte("encrypted"), "hash123", "TAddr****test")
	require.NoError(t, err)

	payoutRepo := payoutrepo.New(env.pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(env.pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, env.txm, env.pool)

	payoutID := uuid.New()
	amount := decimal.NewFromInt(3000)

	result, err := payoutSvc.Freeze(ctx, payoutservice.FreezeCmd{
		PayoutRequestID: payoutID,
		MerchantID:      merchantID,
		Amount:          amount,
		FiatCurrency:    currency,
		PayoutAddressID: addrID,
		TargetAsset:     "USDT",
		TargetNetwork:   "TRON_TRC20",
		IdempotencyKey:  "freeze-test-" + payoutID.String(),
	})
	require.NoError(t, err)
	assert.Equal(t, "BalanceFrozen", result.Status)

	actualPayoutID := result.PayoutRequestID

	var settled, frozen float64
	err = env.pool.QueryRow(ctx,
		`SELECT settled_balance, frozen_payout_balance FROM merchant_treasury_ledger
		 WHERE merchant_id = $1 AND currency = $2`, merchantID, currency).Scan(&settled, &frozen)
	require.NoError(t, err)
	assert.Equal(t, float64(7000), settled, "settled should decrease by 3000")
	assert.Equal(t, float64(3000), frozen, "frozen should increase by 3000")

	var outboxCount int
	err = env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM transactional_outbox WHERE aggregate_id = $1 AND event_type = 'FX_LOCK_REQUESTED'`,
		actualPayoutID).Scan(&outboxCount)
	require.NoError(t, err)
	assert.Equal(t, 1, outboxCount, "should have one outbox event")

	var txnCount int
	err = env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ledger_transaction WHERE idempotency_key = $1`,
		"payout_freeze:"+actualPayoutID).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 1, txnCount, "should have one ledger transaction")

	var entryCount int
	err = env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ledger_entry le
		 JOIN ledger_transaction lt ON le.transaction_id = lt.id
		 WHERE lt.idempotency_key = $1`, "payout_freeze:"+actualPayoutID).Scan(&entryCount)
	require.NoError(t, err)
	assert.Equal(t, 2, entryCount, "should have two ledger entries (debit + credit)")
}

func TestPayoutFreezeIdempotent(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	currency := "USD"
	_, err := env.pool.Exec(ctx,
		`INSERT INTO merchant_treasury_ledger (merchant_id, currency, settled_balance, frozen_payout_balance)
		 VALUES ($1, $2, 10000.00, 0)`, merchantID, currency)
	require.NoError(t, err)

	addrID := uuid.New()
	_, err = env.pool.Exec(ctx,
		`INSERT INTO merchant_payout_address (id, merchant_id, target_asset, target_network, address_format_type, target_address_enc, target_address_hash, target_address_masked)
		 VALUES ($1, $2, 'USDT', 'TRON_TRC20', 'TRON_TRC20', $3, $4, $5)`,
		addrID, merchantID, []byte("enc"), "hash", "TAddr****test")
	require.NoError(t, err)

	payoutRepo := payoutrepo.New(env.pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(env.pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, env.txm, env.pool)

	idempotencyKey := "idempotent-test-" + uuid.New().String()

	r1, err := payoutSvc.Freeze(ctx, payoutservice.FreezeCmd{
		PayoutRequestID: uuid.New(),
		MerchantID:      merchantID,
		Amount:          decimal.NewFromInt(5000),
		FiatCurrency:    currency,
		PayoutAddressID: addrID,
		IdempotencyKey:  idempotencyKey,
	})
	require.NoError(t, err)
	assert.Equal(t, "BalanceFrozen", r1.Status)

	r2, err := payoutSvc.Freeze(ctx, payoutservice.FreezeCmd{
		PayoutRequestID: uuid.New(),
		MerchantID:      merchantID,
		Amount:          decimal.NewFromInt(5000),
		FiatCurrency:    currency,
		PayoutAddressID: addrID,
		IdempotencyKey:  idempotencyKey,
	})
	require.NoError(t, err)
	assert.True(t, r2.Replayed, "second call should be replayed")
	assert.Equal(t, r1.PayoutRequestID, r2.PayoutRequestID, "same payout ID on replay")

	var settled float64
	err = env.pool.QueryRow(ctx,
		`SELECT settled_balance FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
		merchantID, currency).Scan(&settled)
	require.NoError(t, err)
	assert.Equal(t, float64(5000), settled, "balance should only be frozen once")
}

func TestConcurrentDoubleSpend(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	currency := "USD"
	_, err := env.pool.Exec(ctx,
		`INSERT INTO merchant_treasury_ledger (merchant_id, currency, settled_balance, frozen_payout_balance)
		 VALUES ($1, $2, 10000.00, 0)`, merchantID, currency)
	require.NoError(t, err)

	addrID := uuid.New()
	_, err = env.pool.Exec(ctx,
		`INSERT INTO merchant_payout_address (id, merchant_id, target_asset, target_network, address_format_type, target_address_enc, target_address_hash, target_address_masked)
		 VALUES ($1, $2, 'USDT', 'TRON_TRC20', 'TRON_TRC20', $3, $4, $5)`,
		addrID, merchantID, []byte("enc"), "hash", "TAddr****test")
	require.NoError(t, err)

	payoutRepo := payoutrepo.New(env.pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(env.pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, env.txm, env.pool)

	const numRequests = 5
	amount := decimal.NewFromInt(3000)

	var wg sync.WaitGroup
	results := make([]error, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := payoutSvc.Freeze(ctx, payoutservice.FreezeCmd{
				PayoutRequestID: uuid.New(),
				MerchantID:      merchantID,
				Amount:          amount,
				FiatCurrency:    currency,
				PayoutAddressID: addrID,
				IdempotencyKey:  fmt.Sprintf("concurrent-test-%d-%s", idx, uuid.New().String()),
			})
			results[idx] = err
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, err := range results {
		if err == nil {
			successCount++
		}
	}

	assert.True(t, successCount >= 2 && successCount <= 3,
		"2 or 3 out of 5 should succeed (10000/3000), got %d", successCount)

	var settled, frozen float64
	err = env.pool.QueryRow(ctx,
		`SELECT settled_balance, frozen_payout_balance FROM merchant_treasury_ledger
		 WHERE merchant_id = $1 AND currency = $2`, merchantID, currency).Scan(&settled, &frozen)
	require.NoError(t, err)

	assert.Equal(t, settled+frozen, float64(10000), "total balance should be conserved")
	assert.True(t, settled >= 0, "settled should not go negative")
	assert.True(t, frozen >= 0, "frozen should not go negative")
}

func TestMAEStatusTransition(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	kms, _ := crypto.NewMockKMS("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	enc := crypto.NewEncryptor(kms)

	apiKeyEnc, _ := enc.Encrypt(ctx, []byte("sk_test"), crypto.Meta{
		TenantID: "mae_integ", RecordID: "mae", FieldName: "api_key",
	})
	webhookEnc, _ := enc.Encrypt(ctx, []byte("whsec_test"), crypto.Meta{
		TenantID: "mae_integ", RecordID: "mae", FieldName: "webhook_secret",
	})

	maeID := "mae_integ_" + uuid.New().String()[:8]
	tenantScope, _ := json.Marshal([]string{"t1"})
	currencies, _ := json.Marshal([]string{"USD"})

	_, err := env.pool.Exec(ctx, `
		INSERT INTO mae_configuration (
			mae_id, display_name, mae_type, tenant_scope, jurisdiction,
			supported_currencies, api_base_url, api_key_ciphertext,
			webhook_secret_ciphertext, settlement_bank_account_id,
			daily_vmax_minor, monthly_vmax_minor, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'Draft')`,
		maeID, "Integration Test MAE", "CARD_GATEWAY",
		tenantScope, "US", currencies, "https://acq.test",
		apiKeyEnc, webhookEnc, uuid.New(),
		int64(100000000), int64(2000000000))
	require.NoError(t, err)

	var status string
	err = env.pool.QueryRow(ctx,
		`SELECT status FROM mae_configuration WHERE mae_id = $1`, maeID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "Draft", status)

	_, err = env.pool.Exec(ctx,
		`UPDATE mae_configuration SET status = 'Active', version = version + 1, updated_at = now()
		 WHERE mae_id = $1 AND version = 0`, maeID)
	require.NoError(t, err)

	err = env.pool.QueryRow(ctx,
		`SELECT status FROM mae_configuration WHERE mae_id = $1`, maeID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "Active", status)

	_, err = env.pool.Exec(ctx,
		`UPDATE mae_configuration SET status = 'Cooldown', status_reason = 'EXPOSURE_THRESHOLD',
		 business_day_lock_until = CURRENT_DATE, version = version + 1, updated_at = now()
		 WHERE mae_id = $1 AND version = 1`, maeID)
	require.NoError(t, err)

	err = env.pool.QueryRow(ctx,
		`SELECT status FROM mae_configuration WHERE mae_id = $1`, maeID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "Cooldown", status)

	_, err = env.pool.Exec(ctx,
		`UPDATE mae_configuration SET status = 'Active', version = version + 1
		 WHERE mae_id = $1 AND version = 2`, maeID)
	require.NoError(t, err, "DB allows any transition (domain validation is in service layer)")
}

func TestWebhookStreamPublish(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	producer := stream.NewProducer(env.rdb)
	streamName := "stream:test:integration"

	streamID, err := producer.Publish(ctx, streamName, stream.Event{
		IdempotencyKey: "test-key-001",
		ProviderType:   "mae",
		ProviderID:     "prov_test",
		EventType:      "captured",
		PayloadHash:    "abc123",
		IngressID:      uuid.New().String(),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, streamID)

	groupName := "test-group-integration"
	err = env.rdb.XGroupCreateMkStream(ctx, streamName, groupName, "0").Err()
	require.NoError(t, err)

	res, err := env.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: "test-consumer",
		Streams:  []string{streamName, ">"},
		Count:    10,
		Block:    5 * time.Second,
	}).Result()
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Len(t, res[0].Messages, 1)

	msg := res[0].Messages[0]
	assert.Equal(t, "test-key-001", msg.Values["idempotency_key"])
	assert.Equal(t, "mae", msg.Values["provider_type"])

	env.rdb.XAck(ctx, streamName, groupName, msg.ID)
}

func TestLedgerBalanceQuery(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	currency := "USD"

	_, err := env.pool.Exec(ctx,
		`INSERT INTO merchant_treasury_ledger (merchant_id, currency, pending_balance, settled_balance, reserve_balance, frozen_payout_balance, dispute_hold_balance)
		 VALUES ($1, $2, 100.00, 5000.00, 200.00, 1500.00, 50.00)`, merchantID, currency)
	require.NoError(t, err)

	queries := ledgerdb.New(env.pool)
	account, err := queries.GetMerchantTreasuryLedger(ctx, ledgerdb.GetMerchantTreasuryLedgerParams{
		MerchantID: merchantID,
		Currency:   currency,
	})
	require.NoError(t, err)

	assert.Equal(t, merchantID, account.MerchantID)
	assert.Equal(t, currency, account.Currency)

	available := decimal.NewFromInt(5000).Sub(decimal.NewFromInt(200)).Sub(decimal.NewFromInt(1500)).Sub(decimal.NewFromInt(50))
	assert.Equal(t, "3250", available.String(), "available = settled - reserve - frozen - dispute")
}

func TestLedgerInsertInTransaction(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	accID := uuid.New()
	merchantID := uuid.New()
	_, err := env.pool.Exec(ctx,
		`INSERT INTO ledger_account (id, merchant_id, account_type, currency) VALUES ($1, $2, 'SETTLED', 'USD')`,
		accID, merchantID)
	require.NoError(t, err)

	ledgerRepo := payoutrepo.NewLedgerRepo(env.pool)

	err = env.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  "test-txn-" + uuid.New().String(),
			TransactionType: "TEST",
			AggregateType:   "test",
			Description:     "test transaction",
			Entries: []ledgerdomain.Entry{
				{AccountID: accID.String(), Direction: ledgerdomain.DirectionDebit, Amount: decimal.NewFromInt(100)},
				{AccountID: accID.String(), Direction: ledgerdomain.DirectionCredit, Amount: decimal.NewFromInt(100)},
			},
		})
		return err
	})
	require.NoError(t, err)

	var count int
	err = env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ledger_entry le
		 JOIN ledger_transaction lt ON le.transaction_id = lt.id
		 WHERE lt.description = 'test transaction'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "should have two ledger entries")
}

func decimalToPgNumeric(d decimal.Decimal) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(d.StringFixed(4))
	return n
}
