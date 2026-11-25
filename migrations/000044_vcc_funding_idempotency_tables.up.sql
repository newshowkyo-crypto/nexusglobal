-- 000044_vcc_funding_idempotency_tables.up.sql
-- R8: VCC 资金闭环纠偏 — 业务幂等所有权表
--
-- 问题：
--   1. bridge_batch_id 每次 uuid.New()，同一 source_ledger_tx_id 重试会重复扣款。
--   2. Outbox 的 idempotency_key 仅是补充，不能替代余额变动前的业务幂等判定。
--   3. 每一个资金阶段必须先取得业务幂等所有权，再变动余额。
--
-- 修复：
--   1. vcc_funding_requests：源资金阶段幂等表
--      唯一键：merchant_id + source_ledger_tx_id + target_provider_id
--      在事务开头 INSERT ... ON CONFLICT DO NOTHING RETURNING
--      未取得所有权时返回此前结果，绝不继续 debit/credit
--   2. vcc_funding_finalizations：最终入金阶段幂等表
--      唯一键：bridge_batch_id + target_provider_id
--      同样在事务开头 INSERT ... ON CONFLICT DO NOTHING RETURNING

CREATE TABLE IF NOT EXISTS vcc_funding_requests (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id         uuid NOT NULL,
    source_ledger_tx_id varchar(256) NOT NULL,
    target_provider_id  uuid NOT NULL,
    bridge_batch_id     varchar(128) NOT NULL,
    -- bridge_batch_id 从稳定业务键派生：uuid.NewSHA1(NamespaceOID, source_ledger_tx_id + ":" + target_provider_id)
    amount_minor        bigint NOT NULL,
    currency            varchar(16) NOT NULL,
    route_type          varchar(32) NOT NULL,
    -- FIAT_DIRECT 或 CRYPTO_DESENSITIZED
    status              varchar(32) NOT NULL DEFAULT 'REQUESTED',
    -- REQUESTED → COMPLETED（Outbox 已写入）/ FAILED
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    -- 源阶段业务幂等：同一 merchant + source_ledger_tx_id + target_provider_id 只允许一次
    CONSTRAINT uq_vcc_funding_request_source UNIQUE (merchant_id, source_ledger_tx_id, target_provider_id),
    CONSTRAINT chk_vcc_funding_request_status CHECK (
        status IN ('REQUESTED', 'COMPLETED', 'FAILED')
    )
);

CREATE INDEX IF NOT EXISTS idx_vcc_funding_requests_merchant
    ON vcc_funding_requests(merchant_id);
CREATE INDEX IF NOT EXISTS idx_vcc_funding_requests_bridge_batch
    ON vcc_funding_requests(bridge_batch_id);

CREATE TABLE IF NOT EXISTS vcc_funding_finalizations (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bridge_batch_id     varchar(128) NOT NULL,
    target_provider_id  uuid NOT NULL,
    merchant_id         uuid NOT NULL,
    amount_minor        bigint NOT NULL,
    currency            varchar(16) NOT NULL,
    status              varchar(32) NOT NULL DEFAULT 'FINALIZING',
    -- FINALIZING → FINALIZED（资金已转入 issuing pool）/ FAILED
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    -- 最终阶段业务幂等：同一 bridge_batch_id + target_provider_id 只允许一次
    CONSTRAINT uq_vcc_funding_finalization_bridge_provider UNIQUE (bridge_batch_id, target_provider_id),
    CONSTRAINT chk_vcc_funding_finalization_status CHECK (
        status IN ('FINALIZING', 'FINALIZED', 'FAILED')
    )
);

CREATE INDEX IF NOT EXISTS idx_vcc_funding_finalizations_bridge_batch
    ON vcc_funding_finalizations(bridge_batch_id);
CREATE INDEX IF NOT EXISTS idx_vcc_funding_finalizations_merchant
    ON vcc_funding_finalizations(merchant_id);