package checkout

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestPaymentIntent_RealMigration_HasPaymentMethodTokenColumn 验证迁移 payment_intents 有 payment_method_token 列。
// P0-3 加密契约：明文列只允许 ”/NULL（chk_payment_intents_no_plaintext_token），
// 真实 token 加密后写入 payment_method_token_encrypted。
func TestPaymentIntent_RealMigration_HasPaymentMethodTokenColumn(t *testing.T) {
	ctx := context.Background()
	pool := getTestPool(t)
	defer pool.Close()

	// 验证 payment_method_token 列存在
	var columnName string
	err := pool.QueryRow(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'payment_intents' AND column_name = 'payment_method_token'
	`).Scan(&columnName)
	if err != nil {
		t.Errorf("payment_method_token column should exist: %v", err)
	}

	merchantID := uuid.New()
	uniq := uuid.New().String()

	// P0-3: 明文 token 必须被 chk_payment_intents_no_plaintext_token 拒绝
	_, err = pool.Exec(ctx, `
		INSERT INTO payment_intents (id, merchant_id, merchant_order_id, idempotency_key, payload_hash, amount_minor, currency, payment_method_token)
		VALUES (gen_random_uuid(), $1, 'order-plaintext', 'key-plaintext-'||$2, 'hash-plaintext-'||$2, 1000, 'USD', 'tok_test_123')
	`, merchantID, uniq)
	if err == nil {
		t.Error("insert payment_intent with PLAINTEXT payment_method_token should fail (chk_payment_intents_no_plaintext_token)")
	}

	// 空字符串允许（真实 token 走 payment_method_token_encrypted）
	_, err = pool.Exec(ctx, `
		INSERT INTO payment_intents (id, merchant_id, merchant_order_id, idempotency_key, payload_hash, amount_minor, currency, payment_method_token)
		VALUES (gen_random_uuid(), $1, 'order-001', 'key-001-'||$2, 'hash-001-'||$2, 1000, 'USD', '')
	`, merchantID, uniq)
	if err != nil {
		t.Errorf("insert payment_intent with empty payment_method_token failed: %v", err)
	}

	// 验证 NOT NULL 约束生效（插入 NULL 应该失败）
	_, err = pool.Exec(ctx, `
		INSERT INTO payment_intents (id, merchant_id, merchant_order_id, idempotency_key, payload_hash, amount_minor, currency, payment_method_token)
		VALUES (gen_random_uuid(), $1, 'order-002', 'key-002-'||$2, 'hash-002-'||$2, 1000, 'USD', NULL)
	`, merchantID, uniq)
	if err == nil {
		t.Error("insert payment_intent with NULL payment_method_token should fail (NOT NULL constraint)")
	}
}
