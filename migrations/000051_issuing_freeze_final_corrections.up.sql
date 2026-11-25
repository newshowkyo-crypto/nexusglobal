-- 000051: Issuing Freeze Final Corrections (R20/R21)
-- 可重复、依赖安全的修正迁移，兼容已执行过旧版 000050 的数据库。
--
-- R20 P0-1: 彻底修复 TEXT/UUID 兼容迁移
--   - 同时兼容 TEXT、VARCHAR、UUID，以及列不存在的情况
--   - TEXT 转换前先删除依赖该列的旧 CHECK、FK 和索引
--   - 对空白、非法 UUID、悬空引用、非 ACTIVE/CARD_ISSUER 绑定：先禁用 provider，再清空
--   - 安全转换后重建 UUID 类型、FK、CHECK、索引
--   - 禁止 EXCEPTION WHEN OTHERS THEN NULL 吞掉约束失败
--   - 不自动创造无法证明归属的 adapter_provider 或 Vault 绑定
--
-- R21 Step D: 修复引用顺序
--   - 列检测/创建移到任何引用之前；不存在时先新增 UUID 列
--   - UUID 路径也必须清理悬空/inactive/非CARD_ISSUER 绑定，然后重建 FK/CHECK/索引
--   - UUID 文本校验支持大小写

-- ============================================================================
-- 1. 扩展 adapter_provider.adapter_type CHECK，增加 CARD_ISSUER
--    处理旧约束名和新约束名
-- ============================================================================

ALTER TABLE adapter_provider
    DROP CONSTRAINT IF EXISTS adapter_provider_adapter_type_check;

ALTER TABLE adapter_provider
    DROP CONSTRAINT IF EXISTS chk_adapter_provider_adapter_type;

ALTER TABLE adapter_provider
    ADD CONSTRAINT adapter_provider_adapter_type_check CHECK (
        adapter_type IN (
            'CARD_ACQUIRER',
            'CARD_ISSUER',
            'BANK_COLLECTION',
            'CRYPTO_PAY',
            'BANK_RAIL',
            'EXCHANGE',
            'LP',
            'ONCHAIN_WALLET'
        )
    );

-- ============================================================================
-- 2. adapter_credential_vault 增加 issuing_provider_id
-- ============================================================================

ALTER TABLE adapter_credential_vault
    ADD COLUMN IF NOT EXISTS issuing_provider_id UUID NULL REFERENCES issuing_providers(id);

-- ============================================================================
-- 3. R21 Step D: 确保 issuing_providers.adapter_provider_id 列存在
--    在任何引用之前检测并创建，不存在时先新增 UUID 列
-- ============================================================================

DO $$
DECLARE
    col_type text;
BEGIN
    SELECT data_type INTO col_type
    FROM information_schema.columns
    WHERE table_name = 'issuing_providers'
      AND column_name = 'adapter_provider_id';

    IF col_type IS NULL THEN
        -- R21 Step D: 列不存在时先新增 UUID 列
        ALTER TABLE issuing_providers ADD COLUMN adapter_provider_id UUID NULL REFERENCES adapter_provider(id);
    END IF;
END $$;

-- ============================================================================
-- 4. 确保 issuing_providers.adapter_type 默认为 UNSUPPORTED
--    R20 P0-1: 明确执行 SET DEFAULT
--    R21 Step D: 现在可以安全引用 adapter_provider_id（列已确保存在）
-- ============================================================================

-- 处理旧约束名和新约束名（先删除依赖 adapter_provider_id 的 CHECK）
ALTER TABLE issuing_providers
    DROP CONSTRAINT IF EXISTS chk_issuing_adapter_type;

ALTER TABLE issuing_providers
    DROP CONSTRAINT IF EXISTS chk_issuing_adapter_provider_id;

-- 将 adapter_type 为空或为真实协议默认值的存量行改为 UNSUPPORTED
UPDATE issuing_providers
SET adapter_type = 'UNSUPPORTED', updated_at = now()
WHERE adapter_type IS NULL
   OR adapter_type = ''
   OR (adapter_type = 'NEXUS_ISSUING_HTTP_V1' AND adapter_provider_id IS NULL);

-- R20 P0-1: 明确设置 DEFAULT
ALTER TABLE issuing_providers
    ALTER COLUMN adapter_type SET DEFAULT 'UNSUPPORTED';

-- 重新添加 CHECK 约束
ALTER TABLE issuing_providers
    ADD CONSTRAINT chk_issuing_adapter_type CHECK (
        adapter_type IN ('NEXUS_ISSUING_HTTP_V1', 'MOCK_LOCAL', 'UNSUPPORTED')
    );

-- F-03: 使用 IS NOT NULL 而非 length() > 0，防止 NULL 绕过
ALTER TABLE issuing_providers
    ADD CONSTRAINT chk_issuing_adapter_provider_id CHECK (
        adapter_type != 'NEXUS_ISSUING_HTTP_V1' OR adapter_provider_id IS NOT NULL
    );

-- ============================================================================
-- 5. R20 P0-1 / R21 Step D: 兼容旧版 000050 中 adapter_provider_id 为 TEXT/VARCHAR 的情况
--    检查 information_schema，仅在列为 TEXT/VARCHAR 时执行转换
--    转换前先删除依赖该列的旧 CHECK、FK 和索引
--    UUID 路径也必须清理悬空/inactive/非CARD_ISSUER 绑定
-- ============================================================================

DO $$
DECLARE
    col_type text;
BEGIN
    SELECT data_type INTO col_type
    FROM information_schema.columns
    WHERE table_name = 'issuing_providers'
      AND column_name = 'adapter_provider_id';

    IF col_type = 'text' OR col_type = 'character varying' THEN
        -- R20 P0-1: 转换前先删除依赖该列的旧 CHECK、FK 和索引
        ALTER TABLE issuing_providers DROP CONSTRAINT IF EXISTS chk_issuing_adapter_provider_id;
        ALTER TABLE issuing_providers DROP CONSTRAINT IF EXISTS issuing_providers_adapter_provider_id_fkey;
        DROP INDEX IF EXISTS idx_issuing_providers_adapter_provider_id;

        -- R20 P0-1: 对空白、非法 UUID 格式的值：先禁用 provider，再清空
        -- R21 P0-3: 合法 UUID 检查支持大小写
        UPDATE issuing_providers
        SET adapter_provider_id = NULL,
            status = 'DISABLED',
            adapter_type = 'UNSUPPORTED',
            updated_at = now()
        WHERE adapter_provider_id IS NOT NULL
          AND (
              adapter_provider_id::text = ''
              OR adapter_provider_id::text !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
          );

        -- R20 P0-1: 对合法 UUID 但悬空引用（不存在于 adapter_provider(id)）：先禁用，再清空
        UPDATE issuing_providers ip
        SET adapter_provider_id = NULL,
            status = 'DISABLED',
            adapter_type = 'UNSUPPORTED',
            updated_at = now()
        WHERE ip.adapter_provider_id IS NOT NULL
          AND NOT EXISTS (
              SELECT 1 FROM adapter_provider ap WHERE ap.id::text = ip.adapter_provider_id::text
          );

        -- R20 P0-1: 对合法 UUID 但绑定到非 ACTIVE 或非 CARD_ISSUER 的 adapter_provider：先禁用，再清空
        UPDATE issuing_providers ip
        SET adapter_provider_id = NULL,
            status = 'DISABLED',
            adapter_type = 'UNSUPPORTED',
            updated_at = now()
        WHERE ip.adapter_provider_id IS NOT NULL
          AND EXISTS (
              SELECT 1 FROM adapter_provider ap
              WHERE ap.id::text = ip.adapter_provider_id::text
                AND (ap.status != 'ACTIVE' OR ap.adapter_type != 'CARD_ISSUER')
          );

        -- 安全转换为 UUID 类型
        ALTER TABLE issuing_providers ADD COLUMN IF NOT EXISTS _adapter_provider_id_uuid UUID NULL;

        UPDATE issuing_providers ip
        SET _adapter_provider_id_uuid = ip.adapter_provider_id::uuid
        WHERE ip.adapter_provider_id IS NOT NULL
          AND EXISTS (
              SELECT 1 FROM adapter_provider ap
              WHERE ap.id::text = ip.adapter_provider_id::text
                AND ap.status = 'ACTIVE'
                AND ap.adapter_type = 'CARD_ISSUER'
          );

        ALTER TABLE issuing_providers DROP COLUMN adapter_provider_id;
        ALTER TABLE issuing_providers RENAME COLUMN _adapter_provider_id_uuid TO adapter_provider_id;

        -- R20 P0-1: 重建 UUID 外键（不吞错，失败则迁移失败）
        ALTER TABLE issuing_providers
            ADD CONSTRAINT issuing_providers_adapter_provider_id_fkey
            FOREIGN KEY (adapter_provider_id) REFERENCES adapter_provider(id);

        -- R20 P0-1: 重建 CHECK 约束
        ALTER TABLE issuing_providers
            ADD CONSTRAINT chk_issuing_adapter_provider_id CHECK (
                adapter_type != 'NEXUS_ISSUING_HTTP_V1' OR adapter_provider_id IS NOT NULL
            );

        -- R20 P0-1: 重建索引
        CREATE INDEX IF NOT EXISTS idx_issuing_providers_adapter_provider_id
            ON issuing_providers(adapter_provider_id)
            WHERE adapter_provider_id IS NOT NULL;

    ELSIF col_type = 'uuid' THEN
        -- R21 Step D: UUID 路径也必须清理悬空/inactive/非CARD_ISSUER 绑定
        -- 先删除依赖该列的 CHECK/FK，清理后重建

        -- 删除旧约束（可能存在也可能不存在）
        ALTER TABLE issuing_providers DROP CONSTRAINT IF EXISTS chk_issuing_adapter_provider_id;
        ALTER TABLE issuing_providers DROP CONSTRAINT IF EXISTS issuing_providers_adapter_provider_id_fkey;
        DROP INDEX IF EXISTS idx_issuing_providers_adapter_provider_id;

        -- 清理悬空引用：先禁用 provider，再清空 adapter_provider_id
        UPDATE issuing_providers ip
        SET adapter_provider_id = NULL,
            status = 'DISABLED',
            adapter_type = 'UNSUPPORTED',
            updated_at = now()
        WHERE ip.adapter_provider_id IS NOT NULL
          AND NOT EXISTS (
              SELECT 1 FROM adapter_provider ap WHERE ap.id = ip.adapter_provider_id
          );

        -- 清理非 ACTIVE 或非 CARD_ISSUER 绑定：先禁用，再清空
        UPDATE issuing_providers ip
        SET adapter_provider_id = NULL,
            status = 'DISABLED',
            adapter_type = 'UNSUPPORTED',
            updated_at = now()
        WHERE ip.adapter_provider_id IS NOT NULL
          AND EXISTS (
              SELECT 1 FROM adapter_provider ap
              WHERE ap.id = ip.adapter_provider_id
                AND (ap.status != 'ACTIVE' OR ap.adapter_type != 'CARD_ISSUER')
          );

        -- 重建 FK
        ALTER TABLE issuing_providers
            ADD CONSTRAINT issuing_providers_adapter_provider_id_fkey
            FOREIGN KEY (adapter_provider_id) REFERENCES adapter_provider(id);

        -- 重建 CHECK 约束
        ALTER TABLE issuing_providers
            ADD CONSTRAINT chk_issuing_adapter_provider_id CHECK (
                adapter_type != 'NEXUS_ISSUING_HTTP_V1' OR adapter_provider_id IS NOT NULL
            );

        -- 重建索引
        CREATE INDEX IF NOT EXISTS idx_issuing_providers_adapter_provider_id
            ON issuing_providers(adapter_provider_id)
            WHERE adapter_provider_id IS NOT NULL;
    END IF;
END $$;

-- ============================================================================
-- 6. Backfill tenant_id 从 adapter_channel.tenant_id
-- ============================================================================

UPDATE adapter_credential_vault acv
SET tenant_id = ac.tenant_id,
    updated_at = now()
FROM adapter_channel ac
WHERE acv.channel_id = ac.id
  AND acv.tenant_id IS NULL
  AND ac.tenant_id IS NOT NULL;

-- ============================================================================
-- 7. Backfill issuing_provider_id 和 tenant_id（严格安全验证）
--    R22 Item 5: 替换简单 LIKE 'vault://%' 为严格正则 + 多项条件检查
--    R23 Item 2: 增加额外安全验证：
--      - path version 使用 [1-9][0-9]*，拒绝 0
--      - credential_type='API_KEY'
--      - vault.status='ACTIVE'
--      - expires_at 为空或未来
--      - vault.provider_id == issuing_providers.adapter_provider_id
--      - vault.issuing_provider_id 为空或等于当前 provider
--      - 已有 issuing_provider_id 属于其他 provider 时禁用当前 ACTIVE provider
--    任一条件不满足：不得回填，禁用对应 ACTIVE issuing provider
-- ============================================================================

DO $$
DECLARE
    ip_rec RECORD;
    stripped_path TEXT;
    path_tenant_uuid UUID;
    path_record_uuid UUID;
    path_version_int INT;
    ce_tenant_id UUID;
    vault_key_version INT;
    vault_tenant_id UUID;
    vault_status TEXT;
    vault_credential_type TEXT;
    vault_provider_id UUID;
    vault_issuing_provider_id UUID;
    vault_expires_at TIMESTAMPTZ;
    ip_adapter_provider_id UUID;
    ref_count INT;
    regex_pattern TEXT := '^vault://[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/api_key/[1-9][0-9]*$';
BEGIN
    FOR ip_rec IN
        SELECT ip.id, ip.credential_vault_key, ip.entity_id, ip.adapter_provider_id
        FROM issuing_providers ip
        WHERE ip.credential_vault_key IS NOT NULL
          AND ip.credential_vault_key != ''
          AND ip.status = 'ACTIVE'
    LOOP
        -- 条件1: Vault 路径严格匹配正则（R23: version 拒绝 0）
        IF ip_rec.credential_vault_key !~ regex_pattern THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- 解析路径段: vault://<tenant_uuid>/<record_uuid>/api_key/<version>
        stripped_path := regexp_replace(ip_rec.credential_vault_key, '^vault://', '');
        path_tenant_uuid := split_part(stripped_path, '/', 1)::uuid;
        path_record_uuid := split_part(stripped_path, '/', 2)::uuid;
        path_version_int := split_part(stripped_path, '/', 4)::int;

        -- 条件2: path tenant == corporate_entity.tenant_id
        SELECT ce.tenant_id INTO ce_tenant_id
        FROM corporate_entity ce
        WHERE ce.id = ip_rec.entity_id;

        IF ce_tenant_id IS NULL OR ce_tenant_id != path_tenant_uuid THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- 条件3 & 4: path record == vault.id 且 path version == vault.key_version
        -- R23 Item 2: 同时读取 status, credential_type, provider_id, issuing_provider_id, expires_at
        SELECT acv.key_version, acv.tenant_id, acv.status, acv.credential_type,
               acv.provider_id, acv.issuing_provider_id, acv.expires_at
        INTO vault_key_version, vault_tenant_id, vault_status, vault_credential_type,
             vault_provider_id, vault_issuing_provider_id, vault_expires_at
        FROM adapter_credential_vault acv
        WHERE acv.id = path_record_uuid;

        IF NOT FOUND OR vault_key_version != path_version_int THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- 条件5: vault.tenant_id 为空或等于 entity tenant
        IF vault_tenant_id IS NOT NULL AND vault_tenant_id != ce_tenant_id THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- R23 Item 2 条件6: credential_type 必须为 API_KEY
        IF vault_credential_type IS NULL OR vault_credential_type != 'API_KEY' THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- R23 Item 2 条件7: vault.status 必须为 ACTIVE
        IF vault_status IS NULL OR vault_status != 'ACTIVE' THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- R23 Item 2 条件8: expires_at 为空或未来
        IF vault_expires_at IS NOT NULL AND vault_expires_at <= now() THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- R23 Item 2 条件9: vault.provider_id == issuing_providers.adapter_provider_id
        ip_adapter_provider_id := ip_rec.adapter_provider_id;
        IF ip_adapter_provider_id IS NULL OR vault_provider_id IS NULL OR vault_provider_id != ip_adapter_provider_id THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- R23 Item 2 条件10: vault.issuing_provider_id 为空或等于当前 provider
        -- 如果已有 issuing_provider_id 属于其他 provider，禁用当前 ACTIVE provider
        IF vault_issuing_provider_id IS NOT NULL AND vault_issuing_provider_id != ip_rec.id THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- 条件11: 记录只被一个 issuing provider 引用（唯一性）
        SELECT COUNT(DISTINCT ip2.id) INTO ref_count
        FROM issuing_providers ip2
        WHERE ip2.credential_vault_key IS NOT NULL
          AND ip2.credential_vault_key != ''
          AND ip2.credential_vault_key ~ regex_pattern
          AND split_part(regexp_replace(ip2.credential_vault_key, '^vault://', ''), '/', 2)::uuid = path_record_uuid;

        IF ref_count != 1 THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- 全部条件通过：执行 backfill
        UPDATE adapter_credential_vault
        SET issuing_provider_id = ip_rec.id,
            tenant_id = COALESCE(tenant_id, ce_tenant_id),
            updated_at = now()
        WHERE id = path_record_uuid
          AND issuing_provider_id IS NULL;
    END LOOP;
END $$;

-- ============================================================================
-- 8. 索引
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_adapter_credential_vault_issuing_provider
    ON adapter_credential_vault(issuing_provider_id)
    WHERE issuing_provider_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_adapter_credential_vault_full_ownership
    ON adapter_credential_vault(tenant_id, provider_id, issuing_provider_id, credential_type, status, key_version)
    WHERE tenant_id IS NOT NULL;
