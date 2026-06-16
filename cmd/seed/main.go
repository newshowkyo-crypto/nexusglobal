package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Mock Acquirer Seed Data
// 本地演示用的 mock acquirer 数据
// 表名必须与 migrations/000017_init_acquirer_registry.up.sql 一致

func main() {
	ctx := context.Background()

	// 从环境变量获取数据库连接
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/mamr?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	// 检查连接
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("failed to ping database: %v", err)
	}

	log.Println("Seeding mock acquirer data...")

	// 固定 tenant/merchant ID 用于演示
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	merchantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	// 1. 创建 Mock Provider (使用 payment_provider 表)
	mockProviderID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO payment_provider (id, provider_code, provider_name, provider_type, is_active, version, created_at, updated_at)
		VALUES ($1, 'mock', 'Mock Provider', 'CARD', true, 1, $2, $2)
		ON CONFLICT (provider_code) DO UPDATE SET
			provider_name = EXCLUDED.provider_name,
			provider_type = EXCLUDED.provider_type,
			is_active = EXCLUDED.is_active,
			updated_at = $2
	`, mockProviderID, time.Now())
	if err != nil {
		log.Printf("failed to create mock provider: %v", err)
	} else {
		log.Printf("Created mock provider: %s", mockProviderID)
	}

	// 获取 provider ID（如果已存在）
	var existingProviderID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM payment_provider WHERE provider_code = 'mock'`).Scan(&existingProviderID)
	if err == nil {
		mockProviderID = existingProviderID
		log.Printf("Using existing mock provider: %s", mockProviderID)
	}

	// 2. 创建 Mock Channel (使用 tenant_acquirer_channel 表)
	// 将 mock scenarios 写入 channel_config JSONB
	channelConfig := map[string]interface{}{
		"scenarios": map[string]string{
			"order_success": "success",
			"order_fail":    "failure",
			"order_timeout": "timeout",
		},
		"timeout_ms": 5000,
	}
	channelConfigJSON, _ := json.Marshal(channelConfig)

	mockChannelID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO tenant_acquirer_channel (
			id, tenant_id, merchant_id, provider_id, channel_code, channel_name,
			payment_method, currencies, channel_config, is_active, version, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, 'mock_default', 'Mock Default Channel',
			'card', '["USD","EUR","GBP"]', $5, true, 1, $6, $6)
		ON CONFLICT (tenant_id, merchant_id, channel_code) DO UPDATE SET
			channel_name = EXCLUDED.channel_name,
			payment_method = EXCLUDED.payment_method,
			currencies = EXCLUDED.currencies,
			channel_config = EXCLUDED.channel_config,
			is_active = EXCLUDED.is_active,
			updated_at = $6
	`, mockChannelID, tenantID, merchantID, mockProviderID, channelConfigJSON, time.Now())
	if err != nil {
		log.Printf("failed to create mock channel: %v", err)
	} else {
		log.Printf("Created mock channel: %s", mockChannelID)
	}

	// 获取 channel ID（如果已存在）
	var existingChannelID uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT id FROM tenant_acquirer_channel 
		WHERE tenant_id = $1 AND merchant_id = $2 AND channel_code = 'mock_default'
	`, tenantID, merchantID).Scan(&existingChannelID)
	if err == nil {
		mockChannelID = existingChannelID
		log.Printf("Using existing mock channel: %s", mockChannelID)
	}

	// 3. 创建 Provider Credential（mock 不需要真实 credential，使用 provider 级别）
	// 注意：provider_credential_vault 表结构不包含 has_api_key/has_secret
	// 是否有密钥通过真实的 credential rows 判断
	_, err = pool.Exec(ctx, `
		INSERT INTO provider_credential_vault (
			id, provider_id, channel_id, credential_type, key_version,
			ciphertext, aad_context, expires_at, created_at, updated_at
		)
		VALUES ($1, $2, NULL, 'API_KEY', 1,
			'\\x4d6f636b4170694b6579'::bytea, -- mock ciphertext: "MockApiKey"
			'mock-provider-key-v1', $3, $4, $4)
		ON CONFLICT (provider_id, channel_id, credential_type, key_version) DO NOTHING
	`, uuid.New(), mockProviderID, time.Now().AddDate(1, 0, 0), time.Now())
	if err != nil {
		log.Printf("failed to create mock credential: %v", err)
	} else {
		log.Printf("Created mock credential vault entry")
	}

	log.Println("Mock acquirer seed completed!")
	log.Println("")
	log.Println("Mock Channel Scenarios:")
	log.Println("  - order_success -> success (200)")
	log.Println("  - order_fail    -> failure (402)")
	log.Println("  - order_timeout -> timeout (504)")
	log.Println("")
	log.Println("Demo Commands:")
	log.Println("  1. Create checkout token:")
	log.Println("     curl -X POST http://localhost:8080/api/checkout/token -d '{\"order_id\":\"order_success\",\"amount\":\"100.00\",\"currency\":\"USD\",\"merchant_id\":\"00000000-0000-0000-0000-000000000001\"}'")
	log.Println("  2. Open checkout page:")
	log.Println("     http://localhost:8080/checkout?token=<token>")
	log.Println("  3. Charge success:")
	log.Println("     curl -X POST http://localhost:8080/api/checkout/charge -d '{\"token\":\"<token>\",\"card_number\":\"4242424242424242\",\"card_expiry\":\"12/25\",\"card_cvv\":\"123\"}'")
	log.Println("  4. Dashboard:")
	log.Println("     http://localhost:8080/admin/dashboard")
	log.Println("  5. Channel operations:")
	log.Println("     POST /admin/acquirer/channels/<id>/enable")
	log.Println("     POST /admin/acquirer/channels/<id>/disable")
	log.Println("     POST /admin/acquirer/channels/<id>/reload")

	fmt.Printf("\nProvider ID: %s\n", mockProviderID)
	fmt.Printf("Channel ID: %s\n", mockChannelID)
	fmt.Printf("Tenant ID: %s\n", tenantID)
	fmt.Printf("Merchant ID: %s\n", merchantID)
}
