-- 000058_settlement_batch.up.sql
-- P0 4.5: 结算结转（A 面收款 → B 面可出金）。
--
-- 旧链路：capture 直接 CREDIT SETTLED，只写 account_balance；
--         出金读 merchant_treasury_ledger，两本账之间无结转。
-- 新链路：capture → PENDING，结算批次 → SETTLED，并同步 projection。

CREATE TABLE IF NOT EXISTS settlement_batch (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid          NOT NULL,
    merchant_id        uuid          NOT NULL,
    currency           varchar(16)   NOT NULL,
    business_date      date          NOT NULL,
    status             varchar(32)   NOT NULL DEFAULT 'SETTLED',
    total_amount_minor bigint        NOT NULL DEFAULT 0 CHECK (total_amount_minor >= 0),
    item_count         integer       NOT NULL DEFAULT 0 CHECK (item_count >= 0),
    created_at         timestamptz   NOT NULL DEFAULT now(),
    settled_at         timestamptz,
    UNIQUE (merchant_id, currency, business_date)
);

CREATE INDEX IF NOT EXISTS idx_settlement_batch_merchant
    ON settlement_batch(merchant_id, business_date DESC);

CREATE TABLE IF NOT EXISTS settlement_item (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id            uuid          NOT NULL REFERENCES settlement_batch(id) ON DELETE CASCADE,
    payment_attempt_id  uuid          NOT NULL,
    merchant_id         uuid          NOT NULL,
    currency            varchar(16)   NOT NULL,
    amount_minor        bigint        NOT NULL CHECK (amount_minor > 0),
    status              varchar(32)   NOT NULL DEFAULT 'SETTLED',
    created_at          timestamptz   NOT NULL DEFAULT now(),
    UNIQUE (payment_attempt_id)
);

CREATE INDEX IF NOT EXISTS idx_settlement_item_batch ON settlement_item(batch_id);

-- 幂等标记：同一 payment_attempt 只能结算一次
ALTER TABLE payment_attempts
    ADD COLUMN IF NOT EXISTS settled_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_payment_attempts_unsettled
    ON payment_attempts(merchant_id, currency, status)
    WHERE settled_at IS NULL;

-- 投影恰好一次标记：保证账本幂等重放时不会重复写 merchant_treasury_ledger
CREATE TABLE IF NOT EXISTS projection_applied (
    marker     varchar(160) PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);
