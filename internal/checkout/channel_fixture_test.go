// channel_fixture_test.go — G10/G12 集成测试的通道种子数据。
//
// P0-3 之后 CreateSession/ProcessPaymentIntent 必须 SelectChannel（fail-closed）。
// 这些测试验证的是生命周期/幂等语义，需要真实的最小通道路由链路：
//
//	corporate_entity → merchant_routing_policy → acquirer_channels
//	                       → tenant_acquirer_channel(绑定 acquirer_channel_id)
//
// SelectChannel 要求 merchant+currency 恰好一条匹配通道（唯一性断言），
// 因此每个测试商户独立播种一条 TIER_2_COMPLIANT / USD 通道。
package checkout

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	acquirerrepo "github.com/nexusglobal/mamr/internal/acquirer/repository"
)

// seedAcquirerChannelForMerchant 播种完整通道路由链并返回 DB-backed registry。
// 之后 SelectChannel(merchantID, "USD", amount) 将唯一命中该通道。
// merchant_routing_policy.merchant_id 外键指向 corporate_entity(id)，
// 因此商户本身必须以 corporate_entity 形式存在（id = merchantID）。
func seedAcquirerChannelForMerchant(ctx context.Context, t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID) *registry.ChannelRegistry {
	t.Helper()

	channelID := uuid.New()
	providerID := uuid.New()

	// 1. corporate_entity —— id 即商户 ID
	_, err := pool.Exec(ctx, `
		INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, status)
		VALUES ($1, $1, 'Fixture Entity', 'Fixture Entity Legal', 'US', 'ACTIVE')
	`, merchantID)
	require.NoError(t, err, "seed corporate_entity")

	// 2. merchant_routing_policy（SelectChannel 第一步的权威 tier）
	_, err = pool.Exec(ctx, `
		INSERT INTO merchant_routing_policy (merchant_id, provider_tier, status)
		VALUES ($1, 'TIER_2_COMPLIANT', 'ACTIVE')
	`, merchantID)
	require.NoError(t, err, "seed merchant_routing_policy")

	// 3. payment_provider（tenant_acquirer_channel.provider_id 外键）
	_, err = pool.Exec(ctx, `
		INSERT INTO payment_provider (id, provider_code, provider_name, provider_type)
		VALUES ($1, $2, 'Fixture Provider', 'MAE')
	`, providerID, "fixture-"+merchantID.String()[:8])
	require.NoError(t, err, "seed payment_provider")

	// 4. acquirer_channels（控制面通道，决定 provider_tier / status）
	_, err = pool.Exec(ctx, `
		INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, status)
		VALUES ($1, $2, 'TIER_2_COMPLIANT', 'fixture', $3, 'fixture-vault-key', 'ACTIVE')
	`, channelID, merchantID, "fixture-channel-"+merchantID.String()[:8])
	require.NoError(t, err, "seed acquirer_channels")

	// 5. tenant_acquirer_channel（runtime 通道，绑定控制面通道）
	_, err = pool.Exec(ctx, `
		INSERT INTO tenant_acquirer_channel
			(id, tenant_id, merchant_id, channel_code, provider_id, channel_name, currencies, is_active, acquirer_channel_id)
		VALUES ($1, $2, $2, $3, $4, 'Fixture Channel', '["USD"]', true, $5)
	`, uuid.New(), merchantID, "fixture-runtime-"+merchantID.String()[:8], providerID, channelID)
	require.NoError(t, err, "seed tenant_acquirer_channel")

	reg := registry.NewChannelRegistry(registry.RegistryConfig{
		Repo: acquirerrepo.New(pool),
	})

	// PAY-002: 默认播种 example.com 回跳 allowlist（fixture 的 success/cancel URL host）
	seedReturnURLAllowlist(ctx, t, pool, merchantID)

	return reg
}

// seedReturnURLAllowlist 播种商户回跳域名 allowlist（PAY-002，迁移 000053）。
// 测试统一使用 example.com 作为 success_url/cancel_url 的 host。
func seedReturnURLAllowlist(ctx context.Context, t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID, hosts ...string) {
	t.Helper()
	if len(hosts) == 0 {
		hosts = []string{"example.com"}
	}
	for _, host := range hosts {
		_, err := pool.Exec(ctx, `
			INSERT INTO merchant_return_url_allowlist (merchant_id, host)
			VALUES ($1, $2)
			ON CONFLICT (merchant_id, host) DO NOTHING
		`, merchantID, host)
		require.NoError(t, err, "seed merchant_return_url_allowlist")
	}
}

// testMasterKeyB64 是测试专用 32 字节 master key（仅用于本地 fixture，非生产值）。
const testMasterKeyB64 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// newTestTokenEncryptor 构造测试用 TokenEncryptor（ProcessPaymentIntent 的
// payment_method_token 加密为必选项，缺省 fail-closed）。
func newTestTokenEncryptor(t *testing.T) TokenEncryptor {
	t.Helper()
	enc, err := NewTokenEncryptorFromMasterKey(testMasterKeyB64)
	require.NoError(t, err, "construct test token encryptor")
	return enc
}
