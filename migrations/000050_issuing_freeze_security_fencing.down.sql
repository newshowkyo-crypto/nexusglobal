-- 000050 down: 按依赖逆序删除

-- 3. card_provider_freeze_syncs
DROP INDEX IF EXISTS idx_cpfs_lease_version;
DROP INDEX IF EXISTS idx_cpfs_claim_token;
ALTER TABLE card_provider_freeze_syncs DROP CONSTRAINT IF EXISTS ck_cpfs_lease_version;
ALTER TABLE card_provider_freeze_syncs DROP CONSTRAINT IF EXISTS ck_cpfs_terminal_clears_lease;
ALTER TABLE card_provider_freeze_syncs DROP CONSTRAINT IF EXISTS ck_cpfs_claim_token;
ALTER TABLE card_provider_freeze_syncs DROP COLUMN IF EXISTS lease_version;
ALTER TABLE card_provider_freeze_syncs DROP COLUMN IF EXISTS claim_token;

-- 2. adapter_credential_vault
DROP INDEX IF EXISTS idx_adapter_credential_vault_tenant_provider;
ALTER TABLE adapter_credential_vault DROP COLUMN IF EXISTS tenant_id;

-- 1. issuing_providers
DROP INDEX IF EXISTS idx_issuing_providers_adapter_provider_id;
DROP INDEX IF EXISTS idx_issuing_providers_adapter_type;
ALTER TABLE issuing_providers DROP CONSTRAINT IF EXISTS chk_issuing_adapter_provider_id;
ALTER TABLE issuing_providers DROP CONSTRAINT IF EXISTS chk_issuing_adapter_type;
ALTER TABLE issuing_providers DROP COLUMN IF EXISTS adapter_type;
ALTER TABLE issuing_providers DROP COLUMN IF EXISTS adapter_provider_id;
