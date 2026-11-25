-- 000050: Issuing Freeze Security & Fencing
-- P0-7 最终闭环修复：
--   1. issuing_providers 增加 adapter_provider_id（UUID 外键引用 adapter_provider(id)）
--   2. issuing_providers 增加 adapter_type（默认 UNSUPPORTED，不默认为真实适配器）
--   3. card_provider_freeze_syncs 增加 claim_token（fencing token）
--   4. card_provider_freeze_syncs 增加 lease_version（乐观并发版本）
--   5. adapter_credential_vault 增加 tenant_id（权威租户归属）
-- F-01: adapter_provider_id 必须是 UUID 并有外键
-- F-02: 存量行默认 UNSUPPORTED，不默认为 NEXUS_ISSUING_HTTP_V1
-- F-03: CHECK 约束使用 IS NOT NULL，不可被 NULL 绕过
-- F-07: adapter_credential_vault 增加 tenant_id 列

-- ============================================================================
-- 1. issuing_providers: adapter_provider_id (UUID FK) + adapter_type
-- ============================================================================

ALTER TABLE issuing_providers
    ADD COLUMN IF NOT EXISTS adapter_provider_id UUID NULL REFERENCES adapter_provider(id);

ALTER TABLE issuing_providers
    ADD COLUMN IF NOT EXISTS adapter_type TEXT NOT NULL DEFAULT 'UNSUPPORTED';

-- adapter_type 只允许以下值
ALTER TABLE issuing_providers
    ADD CONSTRAINT chk_issuing_adapter_type CHECK (
        adapter_type IN ('NEXUS_ISSUING_HTTP_V1', 'MOCK_LOCAL', 'UNSUPPORTED')
    );

-- F-03: 使用 IS NOT NULL 而非 length() > 0，防止 NULL 绕过
-- NEXUS_ISSUING_HTTP_V1 要求 adapter_provider_id 非空
-- MOCK_LOCAL 允许 adapter_provider_id 为空
-- UNSUPPORTED 允许 adapter_provider_id 为空
ALTER TABLE issuing_providers
    ADD CONSTRAINT chk_issuing_adapter_provider_id CHECK (
        adapter_type != 'NEXUS_ISSUING_HTTP_V1' OR adapter_provider_id IS NOT NULL
    );

CREATE INDEX IF NOT EXISTS idx_issuing_providers_adapter_type
    ON issuing_providers(adapter_type);

CREATE INDEX IF NOT EXISTS idx_issuing_providers_adapter_provider_id
    ON issuing_providers(adapter_provider_id)
    WHERE adapter_provider_id IS NOT NULL;

-- ============================================================================
-- 2. adapter_credential_vault: tenant_id (F-07)
-- ============================================================================

ALTER TABLE adapter_credential_vault
    ADD COLUMN IF NOT EXISTS tenant_id UUID NULL;

-- 索引覆盖 tenant, provider, credential_type, status, key_version
CREATE INDEX IF NOT EXISTS idx_adapter_credential_vault_tenant_provider
    ON adapter_credential_vault(tenant_id, provider_id, credential_type, status, key_version);

-- ============================================================================
-- 3. card_provider_freeze_syncs: claim_token + lease_version (fencing)
-- ============================================================================

ALTER TABLE card_provider_freeze_syncs
    ADD COLUMN IF NOT EXISTS claim_token UUID NULL;

ALTER TABLE card_provider_freeze_syncs
    ADD COLUMN IF NOT EXISTS lease_version BIGINT NOT NULL DEFAULT 0;

-- PROCESSING 状态下 claim_token、locked_by、locked_at 必须非空
ALTER TABLE card_provider_freeze_syncs
    ADD CONSTRAINT ck_cpfs_claim_token CHECK (
        status != 'PROCESSING' OR (claim_token IS NOT NULL AND locked_by IS NOT NULL AND locked_at IS NOT NULL)
    );

-- 终态必须清除 claim_token、locked_by、locked_at
ALTER TABLE card_provider_freeze_syncs
    ADD CONSTRAINT ck_cpfs_terminal_clears_lease CHECK (
        status NOT IN ('SUCCEEDED', 'FAILED', 'UNKNOWN_EXTERNAL_STATE')
        OR (claim_token IS NULL AND locked_by IS NULL AND locked_at IS NULL)
    );

-- lease_version 非负
ALTER TABLE card_provider_freeze_syncs
    ADD CONSTRAINT ck_cpfs_lease_version CHECK (lease_version >= 0);

-- 索引：按 claim_token 查询（fencing 校验）
CREATE INDEX IF NOT EXISTS idx_cpfs_claim_token
    ON card_provider_freeze_syncs(claim_token)
    WHERE claim_token IS NOT NULL;

-- 索引：按 lease_version 查询（并发控制）
CREATE INDEX IF NOT EXISTS idx_cpfs_lease_version
    ON card_provider_freeze_syncs(lease_version);
