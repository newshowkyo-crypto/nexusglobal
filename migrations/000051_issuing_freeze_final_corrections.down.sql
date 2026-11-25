-- 000051 down: 回滚 issuing freeze final corrections (R20)
-- R20 P0-1: 修复依赖顺序，不删除用户已有 Provider。
-- R20 P0-1: 不恢复旧 CHECK（移除 CARD_ISSUER），因为用户可能已创建 CARD_ISSUER 记录。
-- R20 P0-1: 不恢复旧 default NEXUS_ISSUING_HTTP_V1，保留 UNSUPPORTED 作为安全默认。

-- 1. 删除索引（先删依赖索引，再删列）
DROP INDEX IF EXISTS idx_adapter_credential_vault_full_ownership;
DROP INDEX IF EXISTS idx_adapter_credential_vault_issuing_provider;
DROP INDEX IF EXISTS idx_issuing_providers_adapter_provider_id;

-- 2. 删除 issuing_providers 上的 CHECK 约束（依赖 adapter_provider_id）
ALTER TABLE issuing_providers
    DROP CONSTRAINT IF EXISTS chk_issuing_adapter_provider_id;

-- 3. 删除 issuing_providers 上的 FK 约束（依赖 adapter_provider_id）
ALTER TABLE issuing_providers
    DROP CONSTRAINT IF EXISTS issuing_providers_adapter_provider_id_fkey;

-- 4. 删除 issuing_providers 上的 adapter_type CHECK 约束
ALTER TABLE issuing_providers
    DROP CONSTRAINT IF EXISTS chk_issuing_adapter_type;

-- 5. 删除 adapter_credential_vault.issuing_provider_id 列（依赖 issuing_providers FK）
ALTER TABLE adapter_credential_vault
    DROP COLUMN IF EXISTS issuing_provider_id;

-- 6. 恢复 adapter_provider.adapter_type CHECK（移除 CARD_ISSUER）
--    仅在没有 CARD_ISSUER 记录时才恢复旧约束，否则跳过
DO $$
DECLARE
    has_card_issuer boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM adapter_provider WHERE adapter_type = 'CARD_ISSUER'
    ) INTO has_card_issuer;

    IF NOT has_card_issuer THEN
        ALTER TABLE adapter_provider
            DROP CONSTRAINT IF EXISTS adapter_provider_adapter_type_check;

        ALTER TABLE adapter_provider
            ADD CONSTRAINT adapter_provider_adapter_type_check CHECK (
                adapter_type IN (
                    'CARD_ACQUIRER',
                    'BANK_COLLECTION',
                    'CRYPTO_PAY',
                    'BANK_RAIL',
                    'EXCHANGE',
                    'LP',
                    'ONCHAIN_WALLET'
                )
            );
    END IF;
END $$;

-- 7. R20 P0-1: 不恢复旧 default NEXUS_ISSUING_HTTP_V1
--    保留 UNSUPPORTED 作为安全默认值，防止回滚后出现非法 default
--    旧 default NEXUS_ISSUING_HTTP_V1 已在 up migration 中被重置为 UNSUPPORTED
