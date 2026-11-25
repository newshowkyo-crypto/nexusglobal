-- 000041_vcc_funding_idempotency.up.sql
-- P0-A4 (R6): VCC 事件幂等 — crypto_pool_ledger_entry 稳定唯一键
--
-- 问题：
--   1. crypto_pool_ledger_entry 表从未在任何迁移中创建，
--      LocalLedgerService.CreateCryptoPoolLedgerEntry 的 INSERT 必然失败。
--   2. Ledger entry 主键由 worker 每次随机生成（uuid.New()），
--      重复消费同一 Crypto Bridge 事件会写入重复账本条目。
--
-- 修复：
--   1. 创建 crypto_pool_ledger_entry 表。
--   2. 增加稳定业务唯一键 UNIQUE (bridge_batch_id, entry_type)：
--      同一 bridge batch 的同一 entry_type 只允许一条账本记录。
--      重复消费时 INSERT 冲突 → 跳过（幂等），可安全标记原事件 Processed。
--
-- down：只回滚本迁移新增对象（本表）。

CREATE TABLE IF NOT EXISTS crypto_pool_ledger_entry (
    id                uuid PRIMARY KEY,
    bridge_batch_id   varchar(128)  NOT NULL,
    merchant_id       uuid          NOT NULL,
    amount_minor      bigint        NOT NULL,
    currency          varchar(16)   NOT NULL,
    entry_type        varchar(64)   NOT NULL,
    source_tier       varchar(64)   NOT NULL DEFAULT '',
    lp_conversion_ref varchar(128)  NOT NULL DEFAULT '',
    provenance        jsonb         NOT NULL DEFAULT '{}'::jsonb,
    created_at        timestamptz   NOT NULL DEFAULT now(),

    -- P0-A4: 稳定业务唯一键 — 同一 bridge batch + entry_type 只允许一条账本记录
    CONSTRAINT uq_crypto_pool_ledger_bridge_batch_entry UNIQUE (bridge_batch_id, entry_type)
);

CREATE INDEX IF NOT EXISTS idx_crypto_pool_ledger_merchant
    ON crypto_pool_ledger_entry(merchant_id);