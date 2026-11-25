-- 000052: Issuing Ownership Backfill Hardening (R24 P0-1)
-- 修正迁移，覆盖可能已经执行过 000051 的数据库。
--
-- 规则：
--   1. ownership backfill 只处理 status='ACTIVE' AND adapter_type='NEXUS_ISSUING_HTTP_V1'
--   2. MOCK_LOCAL 不得查询、绑定或修改真实 adapter_credential_vault
--   3. ACTIVE NEXUS_ISSUING_HTTP_V1 出现以下任一情况，必须改为 status='DISABLED', adapter_type='UNSUPPORTED'：
--      - credential_vault_key 为 NULL、空串或 trim 后为空
--      - entity_id 或 adapter_provider_id 为空
--      - Vault 路径格式非法或 version < 1
--      - entity/路径/Vault tenant 不一致
--      - Vault 记录不存在
--      - key_version 不一致
--      - credential_type != 'API_KEY'
--      - status != 'ACTIVE'
--      - expires_at 已到期
--      - provider_id 与 adapter_provider_id 不一致
--      - issuing_provider_id 已属于其他 Provider
--      - 同一 Vault record 被多个 issuing provider 引用
--   4. 对合法记录执行：tenant_id = entity tenant, issuing_provider_id = 当前 issuing provider
--   5. UPDATE 必须允许 issuing_provider_id IS NULL OR issuing_provider_id = 当前 provider
--   6. 使用 GET DIAGNOSTICS 获取 ROW_COUNT；合法分支更新不是恰好 1 行时必须禁用当前 ACTIVE Provider
--   7. 迁移必须可重复执行，不允许 EXCEPTION WHEN OTHERS THEN NULL
--   8. down 只撤销 000052 自己新增的数据库对象；数据纠偏不可逆

-- ============================================================================
-- 1. 禁用 ACTIVE NEXUS_ISSUING_HTTP_V1 中凭证/绑定缺失的 Provider
--    这些 Provider 无法通过 ownership 校验，必须先禁用再处理
-- ============================================================================

UPDATE issuing_providers
SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
WHERE status = 'ACTIVE'
  AND adapter_type = 'NEXUS_ISSUING_HTTP_V1'
  AND (
      credential_vault_key IS NULL
      OR trim(credential_vault_key) = ''
      OR entity_id IS NULL
      OR adapter_provider_id IS NULL
  );

-- ============================================================================
-- 2. 禁用 ACTIVE NEXUS_ISSUING_HTTP_V1 中 Vault 路径格式非法的 Provider
-- ============================================================================

UPDATE issuing_providers
SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
WHERE status = 'ACTIVE'
  AND adapter_type = 'NEXUS_ISSUING_HTTP_V1'
  AND credential_vault_key !~ '^vault://[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/api_key/[1-9][0-9]*$';

-- ============================================================================
-- 3. 逐行校验剩余 ACTIVE NEXUS_ISSUING_HTTP_V1 Provider 的完整 ownership
--    使用 DO block + FOR LOOP + GET DIAGNOSTICS
-- ============================================================================

DO $$
DECLARE
    ip_rec RECORD;
    stripped_path TEXT;
    path_tenant_uuid UUID;
    path_record_uuid UUID;
    path_version_text TEXT;
    path_version_int INT;
    ce_tenant_id UUID;
    vault_key_version INT;
    vault_tenant_id UUID;
    vault_status TEXT;
    vault_credential_type TEXT;
    vault_provider_id UUID;
    vault_issuing_provider_id UUID;
    vault_expires_at TIMESTAMPTZ;
    vault_record_id UUID;
    ip_adapter_provider_id UUID;
    ref_count INT;
    update_count INT;
    regex_pattern TEXT := '^vault://[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/api_key/[1-9][0-9]*$';
BEGIN
    FOR ip_rec IN
        SELECT ip.id, ip.credential_vault_key, ip.entity_id, ip.adapter_provider_id
        FROM issuing_providers ip
        WHERE ip.status = 'ACTIVE'
          AND ip.adapter_type = 'NEXUS_ISSUING_HTTP_V1'
          AND ip.credential_vault_key IS NOT NULL
          AND trim(ip.credential_vault_key) != ''
          AND ip.entity_id IS NOT NULL
          AND ip.adapter_provider_id IS NOT NULL
          AND ip.credential_vault_key ~ regex_pattern
    LOOP
        -- 解析路径段
        stripped_path := regexp_replace(ip_rec.credential_vault_key, '^vault://', '');
        path_tenant_uuid := split_part(stripped_path, '/', 1)::uuid;
        path_record_uuid := split_part(stripped_path, '/', 2)::uuid;
        path_version_text := split_part(stripped_path, '/', 4);

        -- R25 P0: 在转换为 INT 前检查 key_version 是否超过 PostgreSQL INT 上限 2147483647
        -- 正则已确保只含数字且无前导零，但超大值会导致 integer out of range
        IF length(path_version_text) > 10
           OR (length(path_version_text) = 10 AND path_version_text > '2147483647') THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        path_version_int := path_version_text::int;

        -- 条件1: path tenant == corporate_entity.tenant_id
        SELECT ce.tenant_id INTO ce_tenant_id
        FROM corporate_entity ce
        WHERE ce.id = ip_rec.entity_id;

        IF ce_tenant_id IS NULL OR ce_tenant_id != path_tenant_uuid THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- 条件2-7: 查询 Vault 记录并校验全部字段
        SELECT acv.id, acv.key_version, acv.tenant_id, acv.status, acv.credential_type,
               acv.provider_id, acv.issuing_provider_id, acv.expires_at
        INTO vault_record_id, vault_key_version, vault_tenant_id, vault_status, vault_credential_type,
             vault_provider_id, vault_issuing_provider_id, vault_expires_at
        FROM adapter_credential_vault acv
        WHERE acv.id = path_record_uuid;

        -- Vault 记录不存在
        IF NOT FOUND THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- key_version 不一致
        IF vault_key_version != path_version_int THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- vault.tenant_id 不一致
        IF vault_tenant_id IS NOT NULL AND vault_tenant_id != ce_tenant_id THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- credential_type != API_KEY
        IF vault_credential_type IS NULL OR vault_credential_type != 'API_KEY' THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- vault.status != ACTIVE
        IF vault_status IS NULL OR vault_status != 'ACTIVE' THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- expires_at 已到期
        IF vault_expires_at IS NOT NULL AND vault_expires_at <= now() THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- provider_id 与 adapter_provider_id 不一致
        ip_adapter_provider_id := ip_rec.adapter_provider_id;
        IF vault_provider_id IS NULL OR vault_provider_id != ip_adapter_provider_id THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- issuing_provider_id 已属于其他 Provider
        IF vault_issuing_provider_id IS NOT NULL AND vault_issuing_provider_id != ip_rec.id THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
            CONTINUE;
        END IF;

        -- 条件8: 同一 Vault record 被多个 issuing provider 引用
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
        -- UPDATE 必须允许 issuing_provider_id IS NULL OR issuing_provider_id = 当前 provider
        UPDATE adapter_credential_vault
        SET issuing_provider_id = ip_rec.id,
            tenant_id = COALESCE(tenant_id, ce_tenant_id),
            updated_at = now()
        WHERE id = path_record_uuid
          AND (issuing_provider_id IS NULL OR issuing_provider_id = ip_rec.id);

        -- 使用 GET DIAGNOSTICS 获取 ROW_COUNT
        GET DIAGNOSTICS update_count = ROW_COUNT;

        -- 合法分支更新不是恰好 1 行时，必须禁用当前 ACTIVE Provider
        IF update_count != 1 THEN
            UPDATE issuing_providers
            SET status = 'DISABLED', adapter_type = 'UNSUPPORTED', updated_at = now()
            WHERE id = ip_rec.id;
        END IF;
    END LOOP;
END $$;