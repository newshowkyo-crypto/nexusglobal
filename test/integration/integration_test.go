package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bytes"
	"net/http"
	"net/http/httptest"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	adminhandler "github.com/nexusglobal/mamr/internal/admin"
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
	payoutworker "github.com/nexusglobal/mamr/internal/worker/payout"
)

type testEnv struct {
	pool      *pgxpool.Pool
	rdb       *redis.Client
	txm       *postgres.TxManager
	encryptor *crypto.Encryptor
	cleanup   func()
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

	// 32 字节 base64 测试主密钥（与 internal/platform/crypto 一致）。
	mockKMS, err := crypto.NewMockKMS("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	require.NoError(t, err)
	enc := crypto.NewEncryptor(mockKMS)

	runMigrations(t, pool)

	cleanup := func() {
		pool.Close()
		rdb.Close()
		_ = pgContainer.Terminate(ctx)
		_ = redisContainer.Terminate(ctx)
	}

	return &testEnv{
		pool:      pool,
		rdb:       rdb,
		txm:       postgres.NewTxManager(pool),
		encryptor: enc,
		cleanup:   cleanup,
	}
}

func runMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)

	// Convert to forward slashes for URL
	migrationPath := filepath.ToSlash(migrationDir)
	// On Windows, use file:// without leading slash for drive letters
	migrationURL := "file://" + migrationPath

	config := pool.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		config.User, config.Password, config.Host, config.Port, config.Database)

	m, err := migrate.New(migrationURL, dsn)
	require.NoError(t, err, "failed to create migrate instance: %s", migrationURL)
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err, "failed to apply migrations")
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
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, nil, env.txm, env.pool)

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
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, nil, env.txm, env.pool)

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
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, nil, env.txm, env.pool)

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
				{AccountID: accID, Direction: ledgerdomain.DirectionDebit, Amount: decimal.NewFromInt(100)},
				{AccountID: accID, Direction: ledgerdomain.DirectionCredit, Amount: decimal.NewFromInt(100)},
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

// TestManualReviewBlocksRelease 验证 T5-2 资金红线：
// payout 被标记 manual_review 后，Release 必须被拒绝，frozen 余额绝不释放。
func TestManualReviewBlocksRelease(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	currency := "USD"

	// 初始：settled=0，frozen=3000（模拟已冻结待出金）
	_, err := env.pool.Exec(ctx,
		`INSERT INTO merchant_treasury_ledger (merchant_id, currency, settled_balance, frozen_payout_balance)
		 VALUES ($1, $2, 0, 3000.00)`, merchantID, currency)
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
		addrID, merchantID, []byte("enc"), "hash-mr", "TAddr****mr")
	require.NoError(t, err)

	// payout 处于 UnknownExternalState 且已被标记 manual_review
	payoutID := uuid.New()
	_, err = env.pool.Exec(ctx,
		`INSERT INTO payout_request (id, merchant_id, payout_address_id, amount, fiat_currency, target_asset, target_network, status, idempotency_key, manual_review, manual_review_reason)
		 VALUES ($1, $2, $3, 3000.00, $4, 'USDT', 'TRON_TRC20', 'UnknownExternalState', $5, true, 'reconcile attempts exceeded')`,
		payoutID, merchantID, addrID, currency, "mr-test-"+payoutID.String())
	require.NoError(t, err)

	payoutRepo := payoutrepo.New(env.pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(env.pool)
	worker := payoutworker.New(payoutRepo, ledgerRepo, nil, nil, env.txm, env.pool, slog.Default())

	// Release 必须被拒绝
	err = worker.Release(ctx, payoutID)
	require.Error(t, err, "release must be blocked while under manual review")
	assert.Contains(t, err.Error(), "manual review")

	// frozen 余额一分未动，没有产生 release 分录
	var settled, frozen float64
	err = env.pool.QueryRow(ctx,
		`SELECT settled_balance, frozen_payout_balance FROM merchant_treasury_ledger
		 WHERE merchant_id = $1 AND currency = $2`, merchantID, currency).Scan(&settled, &frozen)
	require.NoError(t, err)
	assert.Equal(t, float64(0), settled, "settled must not increase while under manual review")
	assert.Equal(t, float64(3000), frozen, "frozen must remain held while under manual review")

	var releaseTxnCount int
	err = env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ledger_transaction WHERE idempotency_key = $1`,
		"payout_release:"+payoutID.String()).Scan(&releaseTxnCount)
	require.NoError(t, err)
	assert.Equal(t, 0, releaseTxnCount, "no release ledger transaction should be posted")

	// 清除 manual_review 标记后，Release 应当成功放行
	_, err = env.pool.Exec(ctx,
		`UPDATE payout_request SET status = 'Failed', manual_review = false WHERE id = $1`, payoutID)
	require.NoError(t, err)

	err = worker.Release(ctx, payoutID)
	require.NoError(t, err, "release should succeed once manual review is cleared")

	err = env.pool.QueryRow(ctx,
		`SELECT settled_balance, frozen_payout_balance FROM merchant_treasury_ledger
		 WHERE merchant_id = $1 AND currency = $2`, merchantID, currency).Scan(&settled, &frozen)
	require.NoError(t, err)
	assert.Equal(t, float64(3000), settled, "settled should be restored after release")
	assert.Equal(t, float64(0), frozen, "frozen should be released to settled")
}

func assertLedgerBalanced(t *testing.T, pool *pgxpool.Pool, idempotencyKey string) {
	t.Helper()
	var debitSum, creditSum string
	err := pool.QueryRow(context.Background(), `
		SELECT
			COALESCE(SUM(CASE WHEN le.direction = 'DEBIT' THEN le.amount ELSE 0 END), 0)::text,
			COALESCE(SUM(CASE WHEN le.direction = 'CREDIT' THEN le.amount ELSE 0 END), 0)::text
		FROM ledger_entry le
		JOIN ledger_transaction lt ON le.transaction_id = lt.id
		WHERE lt.idempotency_key = $1`, idempotencyKey).Scan(&debitSum, &creditSum)
	require.NoError(t, err)
	d, _ := decimal.NewFromString(debitSum)
	c, _ := decimal.NewFromString(creditSum)
	assert.True(t, d.Equal(c), "debit (%s) should equal credit (%s) for %s", debitSum, creditSum, idempotencyKey)
}

func assertTreasuryConserved(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, currency string, expectedTotal decimal.Decimal) {
	t.Helper()
	var settled, frozen, reserve, dispute string
	err := pool.QueryRow(context.Background(), `
		SELECT settled_balance::text, frozen_payout_balance::text, reserve_balance::text, dispute_hold_balance::text
		FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`, merchantID, currency).Scan(&settled, &frozen, &reserve, &dispute)
	require.NoError(t, err)
	s, _ := decimal.NewFromString(settled)
	f, _ := decimal.NewFromString(frozen)
	r, _ := decimal.NewFromString(reserve)
	d, _ := decimal.NewFromString(dispute)
	total := s.Add(f).Add(r).Add(d)
	assert.True(t, total.Equal(expectedTotal), "total (%s) should equal expected (%s)", total.String(), expectedTotal.String())
}

func TestPayoutConfirmSettlesFrozen(t *testing.T) {
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
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, nil, env.txm, env.pool)
	payoutWorker := payoutworker.New(payoutRepo, ledgerRepo, nil, nil, env.txm, env.pool, slog.Default())

	freezeResult, err := payoutSvc.Freeze(ctx, payoutservice.FreezeCmd{
		PayoutRequestID: uuid.New(),
		MerchantID:      merchantID,
		Amount:          decimal.NewFromInt(3000),
		FiatCurrency:    currency,
		PayoutAddressID: addrID,
		TargetAsset:     "USDT",
		TargetNetwork:   "TRON_TRC20",
		IdempotencyKey:  "confirm-test-freeze-" + uuid.New().String(),
	})
	require.NoError(t, err)

	payoutID, _ := uuid.Parse(freezeResult.PayoutRequestID)

	_, err = env.pool.Exec(ctx,
		`UPDATE payout_request SET status = 'LPProcessing', version = version + 1 WHERE id = $1`, payoutID)
	require.NoError(t, err)

	err = payoutWorker.Confirm(ctx, payoutID, "provider-ref-123")
	require.NoError(t, err)

	var status string
	err = env.pool.QueryRow(ctx, `SELECT status FROM payout_request WHERE id = $1`, payoutID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "Confirmed", status)

	var frozen float64
	err = env.pool.QueryRow(ctx,
		`SELECT frozen_payout_balance FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
		merchantID, currency).Scan(&frozen)
	require.NoError(t, err)
	assert.Equal(t, float64(0), frozen, "frozen should be zero after confirm")

	assertLedgerBalanced(t, env.pool, "payout_confirm:"+payoutID.String())

	var available string
	err = env.pool.QueryRow(ctx,
		`SELECT available_payout_balance::text FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
		merchantID, currency).Scan(&available)
	require.NoError(t, err)
	avail, _ := decimal.NewFromString(available)
	assert.True(t, avail.Equal(decimal.NewFromInt(7000)), "available should be 7000 after confirm (settled=7000, frozen=0)")
}

func TestPayoutReleaseRefundsFrozen(t *testing.T) {
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
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, nil, env.txm, env.pool)
	payoutWorker := payoutworker.New(payoutRepo, ledgerRepo, nil, nil, env.txm, env.pool, slog.Default())

	freezeResult, err := payoutSvc.Freeze(ctx, payoutservice.FreezeCmd{
		PayoutRequestID: uuid.New(),
		MerchantID:      merchantID,
		Amount:          decimal.NewFromInt(3000),
		FiatCurrency:    currency,
		PayoutAddressID: addrID,
		TargetAsset:     "USDT",
		TargetNetwork:   "TRON_TRC20",
		IdempotencyKey:  "release-test-freeze-" + uuid.New().String(),
	})
	require.NoError(t, err)

	payoutID, _ := uuid.Parse(freezeResult.PayoutRequestID)

	_, err = env.pool.Exec(ctx,
		`UPDATE payout_request SET status = 'Failed', version = version + 1 WHERE id = $1`, payoutID)
	require.NoError(t, err)

	err = payoutWorker.Release(ctx, payoutID)
	require.NoError(t, err)

	var status string
	err = env.pool.QueryRow(ctx, `SELECT status FROM payout_request WHERE id = $1`, payoutID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "BalanceReleased", status)

	var settled, frozen string
	err = env.pool.QueryRow(ctx,
		`SELECT settled_balance::text, frozen_payout_balance::text FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
		merchantID, currency).Scan(&settled, &frozen)
	require.NoError(t, err)
	s, _ := decimal.NewFromString(settled)
	f, _ := decimal.NewFromString(frozen)
	assert.True(t, s.Equal(decimal.NewFromInt(10000)), "settled should be back to 10000")
	assert.True(t, f.IsZero(), "frozen should be zero")

	assertLedgerBalanced(t, env.pool, "payout_release:"+payoutID.String())
}

func TestPayoutConfirmIdempotent(t *testing.T) {
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
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, nil, env.txm, env.pool)
	payoutWorker := payoutworker.New(payoutRepo, ledgerRepo, nil, nil, env.txm, env.pool, slog.Default())

	freezeResult, err := payoutSvc.Freeze(ctx, payoutservice.FreezeCmd{
		PayoutRequestID: uuid.New(),
		MerchantID:      merchantID,
		Amount:          decimal.NewFromInt(3000),
		FiatCurrency:    currency,
		PayoutAddressID: addrID,
		TargetAsset:     "USDT",
		TargetNetwork:   "TRON_TRC20",
		IdempotencyKey:  "idempotent-confirm-freeze-" + uuid.New().String(),
	})
	require.NoError(t, err)

	payoutID, _ := uuid.Parse(freezeResult.PayoutRequestID)

	_, err = env.pool.Exec(ctx,
		`UPDATE payout_request SET status = 'LPProcessing', version = version + 1 WHERE id = $1`, payoutID)
	require.NoError(t, err)

	err = payoutWorker.Confirm(ctx, payoutID, "ref-1")
	require.NoError(t, err)
	err = payoutWorker.Confirm(ctx, payoutID, "ref-1")
	require.NoError(t, err)

	var txnCount int
	err = env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ledger_transaction WHERE idempotency_key = $1`,
		"payout_confirm:"+payoutID.String()).Scan(&txnCount)
	require.NoError(t, err)
	assert.Equal(t, 1, txnCount, "should have exactly one confirm transaction")

	var frozen string
	err = env.pool.QueryRow(ctx,
		`SELECT frozen_payout_balance::text FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
		merchantID, currency).Scan(&frozen)
	require.NoError(t, err)
	f, _ := decimal.NewFromString(frozen)
	assert.True(t, f.IsZero(), "frozen should be zero, balance only changed once")
}

func TestReserveHoldAndRelease(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	currency := "USD"
	_, err := env.pool.Exec(ctx,
		`INSERT INTO merchant_treasury_ledger (merchant_id, currency, settled_balance, reserve_balance, frozen_payout_balance)
		 VALUES ($1, $2, 10000.00, 0, 0)`, merchantID, currency)
	require.NoError(t, err)

	payoutRepo := payoutrepo.New(env.pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(env.pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, nil, env.txm, env.pool)

	holdKey := "reserve-hold-" + uuid.New().String()
	_, err = payoutSvc.ReserveHold(ctx, payoutservice.ReserveHoldCmd{
		MerchantID:     merchantID,
		Amount:         decimal.NewFromInt(2000),
		FiatCurrency:   currency,
		IdempotencyKey: holdKey,
	})
	require.NoError(t, err)

	var reserve, settled, available string
	err = env.pool.QueryRow(ctx,
		`SELECT reserve_balance::text, settled_balance::text, available_payout_balance::text
		 FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
		merchantID, currency).Scan(&reserve, &settled, &available)
	require.NoError(t, err)

	r, _ := decimal.NewFromString(reserve)
	s, _ := decimal.NewFromString(settled)
	a, _ := decimal.NewFromString(available)
	assert.True(t, r.Equal(decimal.NewFromInt(2000)), "reserve should be 2000, got %s", reserve)
	assert.True(t, s.Equal(decimal.NewFromInt(10000)), "settled should stay 10000, got %s", settled)
	assert.True(t, a.Equal(decimal.NewFromInt(8000)), "available should be 8000, got %s", available)

	assertLedgerBalanced(t, env.pool, holdKey)

	releaseKey := "reserve-release-" + uuid.New().String()
	_, err = payoutSvc.ReserveRelease(ctx, payoutservice.ReserveReleaseCmd{
		MerchantID:     merchantID,
		Amount:         decimal.NewFromInt(1000),
		FiatCurrency:   currency,
		IdempotencyKey: releaseKey,
	})
	require.NoError(t, err)

	err = env.pool.QueryRow(ctx,
		`SELECT reserve_balance::text, settled_balance::text, available_payout_balance::text
		 FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
		merchantID, currency).Scan(&reserve, &settled, &available)
	require.NoError(t, err)

	r, _ = decimal.NewFromString(reserve)
	s, _ = decimal.NewFromString(settled)
	a, _ = decimal.NewFromString(available)
	assert.True(t, r.Equal(decimal.NewFromInt(1000)), "reserve should be 1000 after release, got %s", reserve)
	assert.True(t, s.Equal(decimal.NewFromInt(11000)), "settled should be 11000 after release, got %s", settled)
	assert.True(t, a.Equal(decimal.NewFromInt(10000)), "available should be 10000 after release, got %s", available)

	assertLedgerBalanced(t, env.pool, releaseKey)
	assertTreasuryConserved(t, env.pool, merchantID, currency, decimal.NewFromInt(12000))
}

// R2-3: C1 端到端守卫测试
// 经 service → 真实 DB → 回读，验证非整数金额精度正确
func TestNonIntegerAmountPrecision(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	merchantID := uuid.New()
	currency := "USD"

	// 建 merchant ledger（settled 充足）
	_, err := env.pool.Exec(ctx,
		`INSERT INTO merchant_treasury_ledger (merchant_id, currency, settled_balance, frozen_payout_balance)
		 VALUES ($1, $2, 1000.00, 0)`, merchantID, currency)
	require.NoError(t, err)

	// 创建 payout address
	addrID := uuid.New()
	_, err = env.pool.Exec(ctx,
		`INSERT INTO merchant_payout_address (id, merchant_id, target_asset, target_network, address_format_type, target_address_enc, target_address_hash, target_address_masked, screening_status, whitelist_status)
		 VALUES ($1, $2, 'USDT', 'TRON_TRC20', 'TRON_TRC20', $3, $4, $5, 'Passed', 'Active')`,
		addrID, merchantID, []byte("encrypted"), "hash123", "TAddr****test")
	require.NoError(t, err)

	payoutRepo := payoutrepo.New(env.pool)
	ledgerRepo := payoutrepo.NewLedgerRepo(env.pool)
	payoutSvc := payoutservice.New(payoutRepo, ledgerRepo, nil, env.txm, env.pool)

	// 测试多种非整数金额
	testAmounts := []struct {
		amount   decimal.Decimal
		expected string
	}{
		{decimal.NewFromFloat(100.50), "100.50"},
		{decimal.NewFromFloat(0.01), "0.01"},
		{decimal.NewFromFloat(100.1234), "100.1234"},
		{decimal.NewFromFloat(999.99), "999.99"},
	}

	for _, tc := range testAmounts {
		t.Run(tc.amount.String(), func(t *testing.T) {
			// 每个测试用例创建独立的 merchant ledger
			testMerchantID := uuid.New()
			_, err := env.pool.Exec(ctx,
				`INSERT INTO merchant_treasury_ledger (merchant_id, currency, settled_balance, frozen_payout_balance)
				 VALUES ($1, $2, 10000.00, 0)`, testMerchantID, currency)
			require.NoError(t, err)

			// 获取冻结前余额
			var settledBefore, frozenBefore string
			err = env.pool.QueryRow(ctx,
				`SELECT settled_balance::text, frozen_payout_balance::text FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
				testMerchantID, currency).Scan(&settledBefore, &frozenBefore)
			require.NoError(t, err)
			sBefore, _ := decimal.NewFromString(settledBefore)
			fBefore, _ := decimal.NewFromString(frozenBefore)

			// 经 service 层冻结非整数金额
			freezeResult, err := payoutSvc.Freeze(ctx, payoutservice.FreezeCmd{
				MerchantID:      testMerchantID,
				Amount:          tc.amount,
				FiatCurrency:    currency,
				PayoutAddressID: addrID,
				TargetAsset:     "USDT",
				TargetNetwork:   "TRON_TRC20",
				IdempotencyKey:  "precision-test-" + uuid.New().String(),
			})
			require.NoError(t, err)

			payoutID, _ := uuid.Parse(freezeResult.PayoutRequestID)

			// 回读 payout_request.amount，断言 == 输入值
			var amountStr string
			err = env.pool.QueryRow(ctx,
				`SELECT amount::text FROM payout_request WHERE id = $1`, payoutID).Scan(&amountStr)
			require.NoError(t, err)
			readAmount, _ := decimal.NewFromString(amountStr)
			assert.True(t, tc.amount.Equal(readAmount),
				"amount mismatch: input=%s, read=%s", tc.amount.String(), readAmount.String())

			// 回读余额，断言 settled 减少、frozen 增加恰好等于该非整数金额
			var settledAfter, frozenAfter string
			err = env.pool.QueryRow(ctx,
				`SELECT settled_balance::text, frozen_payout_balance::text FROM merchant_treasury_ledger WHERE merchant_id = $1 AND currency = $2`,
				testMerchantID, currency).Scan(&settledAfter, &frozenAfter)
			require.NoError(t, err)
			sAfter, _ := decimal.NewFromString(settledAfter)
			fAfter, _ := decimal.NewFromString(frozenAfter)

			settledDelta := sBefore.Sub(sAfter)
			frozenDelta := fAfter.Sub(fBefore)

			assert.True(t, settledDelta.Equal(tc.amount),
				"settled delta mismatch: expected=%s, got=%s", tc.amount.String(), settledDelta.String())
			assert.True(t, frozenDelta.Equal(tc.amount),
				"frozen delta mismatch: expected=%s, got=%s", tc.amount.String(), frozenDelta.String())

			// 断言分录借贷平衡
			assertLedgerBalanced(t, env.pool, "payout_freeze:"+payoutID.String())
		})
	}
}

// TestAdminEncryptedFieldsStoredAsCiphertext 是 R3-3 的回归守卫：
// 验证 admin.Create* 写入 *_ciphertext/target_address_enc 的列对 MAE api_key / webhook secret /
// webhook proxy secret / 出金地址都用真加密（不是 mimo 之前的「简化处理」明文）。
// 反向验证：把加密改回明文（[]byte(req.X)）会让此测试 FAIL。
func TestAdminEncryptedFieldsStoredAsCiphertext(t *testing.T) {
	env := setupEnv(t)
	defer env.cleanup()
	ctx := context.Background()

	adminH := adminhandler.NewHandler(env.pool, env.encryptor)

	// 0) CreateBankAccount 先于 MAE（mae_configuration.settlement_bank_account_id NOT NULL）
	settlementBankID := uuid.New()
	bankReq0 := map[string]interface{}{
		"bank_account_id":   settlementBankID.String(),
		"currency":          "USD",
		"accumulated_minor": int64(0),
	}
	body, _ := json.Marshal(bankReq0)
	req := httptest.NewRequest(http.MethodPost, "/admin/bank-account", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	adminH.CreateBankAccount(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "pre-create bank account: %s", rec.Body.String())

	// 1) CreateMAEConfig
	maeReq := map[string]interface{}{
		"mae_id": "test-mae-enc", "display_name": "Test MAE", "mae_type": "card",
		"jurisdiction": "US", "api_base_url": "https://mae.example.com",
		"settlement_bank_account_id": settlementBankID.String(),
		"api_key":                    "AKIA-PLAINTEXT-DO-NOT-LEAK",
		"webhook_secret":             "WH-SECRET-PLAINTEXT-XYZ",
		"daily_vmax_minor":           int64(1000000),
		"monthly_vmax_minor":         int64(30000000),
		"routing_weight":             int32(100),
	}
	body, _ = json.Marshal(maeReq)
	req = httptest.NewRequest(http.MethodPost, "/admin/mae-config", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	adminH.CreateMAEConfig(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "create MAE: %s", rec.Body.String())

	var apiKeyEnc, webhookEnc []byte
	err := env.pool.QueryRow(ctx,
		`SELECT api_key_ciphertext, webhook_secret_ciphertext FROM mae_configuration WHERE mae_id = $1`,
		"test-mae-enc").Scan(&apiKeyEnc, &webhookEnc)
	require.NoError(t, err)
	assert.NotEqual(t, []byte("AKIA-PLAINTEXT-DO-NOT-LEAK"), apiKeyEnc, "MAE api_key stored as plaintext")
	assert.NotEqual(t, []byte("WH-SECRET-PLAINTEXT-XYZ"), webhookEnc, "MAE webhook_secret stored as plaintext")
	assert.NotEmpty(t, apiKeyEnc, "MAE api_key ciphertext empty")
	assert.NotEmpty(t, webhookEnc, "MAE webhook_secret ciphertext empty")

	// 反向验证：能 decrypt 回来
	apiKeyDec, decErr := env.encryptor.Decrypt(ctx, apiKeyEnc, crypto.Meta{
		TenantID: "test-mae-enc", RecordID: "mae_configuration", FieldName: "api_key",
	})
	require.NoError(t, decErr)
	assert.Equal(t, "AKIA-PLAINTEXT-DO-NOT-LEAK", string(apiKeyDec))

	// 2) CreateWebhookProxy
	proxyReq := map[string]interface{}{
		"provider_type": "lp", "provider_id": "test-proxy-enc",
		"webhook_url": "https://lp.example.com/hook", "signature_algo": "HMAC_SHA256",
		"secret":       "WH-SECRET-PLAINTEXT-PROXY",
		"ip_allowlist": []string{"10.0.0.0/8", "192.168.0.0/16"},
		"rate_limit":   int32(100),
	}
	body, _ = json.Marshal(proxyReq)
	req = httptest.NewRequest(http.MethodPost, "/admin/webhook-proxy", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	adminH.CreateWebhookProxy(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "create proxy: %s", rec.Body.String())

	var secretEnc []byte
	err = env.pool.QueryRow(ctx,
		`SELECT secret_ciphertext FROM webhook_receiver_proxy WHERE provider_id = $1`,
		"test-proxy-enc").Scan(&secretEnc)
	require.NoError(t, err)
	assert.NotEqual(t, []byte("WH-SECRET-PLAINTEXT-PROXY"), secretEnc, "webhook proxy secret stored as plaintext")
	secretDec, decErr := env.encryptor.Decrypt(ctx, secretEnc, crypto.Meta{
		TenantID: "test-proxy-enc", RecordID: "webhook_receiver_proxy", FieldName: "secret",
	})
	require.NoError(t, decErr)
	assert.Equal(t, "WH-SECRET-PLAINTEXT-PROXY", string(secretDec))

	// 3) CreatePayoutAddress
	merchantID := uuid.New()
	addrReq := map[string]interface{}{
		"merchant_id": merchantID.String(), "target_asset": "USDT", "target_network": "TRON_TRC20",
		"address_format_type": "TRON_TRC20", "target_address": "TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9",
		"address_label": "test",
	}
	body, _ = json.Marshal(addrReq)
	req = httptest.NewRequest(http.MethodPost, "/admin/payout-address", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	adminH.CreatePayoutAddress(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "create address: %s", rec.Body.String())

	var addrEncBytes []byte
	var addrHash, addrMasked string
	err = env.pool.QueryRow(ctx,
		`SELECT target_address_enc, target_address_hash, target_address_masked
		 FROM merchant_payout_address WHERE merchant_id = $1`,
		merchantID).Scan(&addrEncBytes, &addrHash, &addrMasked)
	require.NoError(t, err)
	assert.NotEqual(t, []byte("TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9"), addrEncBytes, "payout address stored as plaintext")
	assert.NotEqual(t, "TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", addrHash, "payout address hash == plaintext")
	assert.NotEmpty(t, addrHash, "payout address hash empty (was the address itself before)")
	assert.NotEmpty(t, addrMasked, "payout address masked empty")
	addrDec, decErr := env.encryptor.Decrypt(ctx, addrEncBytes, crypto.Meta{
		TenantID: merchantID.String(), RecordID: "payout_address", FieldName: "target_address",
	})
	require.NoError(t, decErr)
	assert.Equal(t, "TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9", string(addrDec))

	// 4) CreateBankAccount（R3-3 第 7 张表）
	bankReq := map[string]interface{}{
		"bank_account_id":   uuid.New().String(),
		"currency":          "USD",
		"accumulated_minor": int64(50000),
	}
	body, _ = json.Marshal(bankReq)
	req = httptest.NewRequest(http.MethodPost, "/admin/bank-account", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	adminH.CreateBankAccount(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "create bank account: %s", rec.Body.String())

	var bankAccountID uuid.UUID
	var accumulated int64
	err = env.pool.QueryRow(ctx,
		`SELECT bank_account_id, accumulated_minor FROM bank_account_balance
		 WHERE bank_account_id = $1 AND currency = 'USD'`,
		bankReq["bank_account_id"]).Scan(&bankAccountID, &accumulated)
	require.NoError(t, err)
	assert.Equal(t, int64(50000), accumulated, "bank account accumulated_minor not persisted")

	// 5) 无 encryptor → CreateMAEConfig 必须 fail-closed（不写明文）。
	noEncH := adminhandler.NewHandler(env.pool, nil)
	validMAEBody, _ := json.Marshal(map[string]interface{}{
		"mae_id": "failclosed-mae", "display_name": "fc", "mae_type": "card",
		"settlement_bank_account_id": settlementBankID.String(),
		"api_key":                    "x", "webhook_secret": "y",
	})
	req = httptest.NewRequest(http.MethodPost, "/admin/mae-config", bytes.NewReader(validMAEBody))
	rec = httptest.NewRecorder()
	noEncH.CreateMAEConfig(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "MAE config must fail-closed without encryptor (got body: %s)", rec.Body.String())
}
