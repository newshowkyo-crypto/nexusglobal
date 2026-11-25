-- 000043_issuing_provider_funding_balance.up.sql
-- R8: VCC 资金闭环纠偏 — 发卡 Provider 可用余额表
--
-- 问题：
--   FinalizeVCCFunding 把资金从 pending_crypto_bridge 加回 merchant settled_balance，
--   这是错误的。资金必须进入目标 VCC 卡池/发卡可用余额。
--   issuing_providers 表没有"可用余额"字段，无法表达卡池资金。
--
-- 修复：
--   创建 issuing_provider_funding_balance 表，记录每个 issuing provider 的可用余额。
--   FinalizeVCCFunding 将 debit pending_crypto_bridge → credit 此表。
--   VCC_ISSUING_FUNDING_REQUESTED 消费者调用外部 issuing Provider 后更新此表状态。

CREATE TABLE IF NOT EXISTS issuing_provider_funding_balance (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id       uuid NOT NULL REFERENCES issuing_providers(id),
    merchant_id       uuid NOT NULL,
    bridge_batch_id   varchar(128) NOT NULL,
    amount_minor      bigint NOT NULL,
    currency          varchar(16) NOT NULL,
    status            varchar(32) NOT NULL DEFAULT 'FUNDING_PENDING',
    -- FUNDING_PENDING → FUNDED（Provider 确认入金）/ FAILED（Provider 明确拒绝）
    provider_idempotency_key varchar(256) NOT NULL,
    -- 稳定幂等键：bridge_batch_id + target_provider_id
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT uq_issuing_funding_balance_provider_idem UNIQUE (provider_idempotency_key),
    CONSTRAINT uq_issuing_funding_balance_bridge_provider UNIQUE (bridge_batch_id, provider_id),
    CONSTRAINT chk_issuing_funding_status CHECK (
        status IN ('FUNDING_PENDING', 'FUNDED', 'FAILED', 'UNKNOWN_EXTERNAL_STATE')
    )
);

CREATE INDEX IF NOT EXISTS idx_issuing_funding_balance_provider
    ON issuing_provider_funding_balance(provider_id);
CREATE INDEX IF NOT EXISTS idx_issuing_funding_balance_merchant
    ON issuing_provider_funding_balance(merchant_id);
CREATE INDEX IF NOT EXISTS idx_issuing_funding_balance_status
    ON issuing_provider_funding_balance(status);