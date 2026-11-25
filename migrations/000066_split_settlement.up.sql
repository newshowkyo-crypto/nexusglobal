-- 000066_split_settlement.up.sql
-- P5: 分账规则与分账记录（结算时按净额分账）

CREATE TABLE IF NOT EXISTS split_rule (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    merchant_id uuid        NOT NULL,
    currency    varchar(3)  NOT NULL DEFAULT '',
    name        varchar(64) NOT NULL DEFAULT '',
    receivers   jsonb       NOT NULL DEFAULT '[]'::jsonb,
    status      varchar(16) NOT NULL DEFAULT 'ACTIVE'
                  CHECK (status IN ('ACTIVE', 'DISABLED')),
    version     integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_split_rule_lookup
    ON split_rule(merchant_id, currency, status);

CREATE TABLE IF NOT EXISTS split_record (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid        NOT NULL,
    merchant_id        uuid        NOT NULL,
    payment_attempt_id uuid        NOT NULL REFERENCES payment_attempts(id),
    settlement_batch_id uuid       NULL,
    receiver           varchar(64) NOT NULL,
    amount_minor       bigint      NOT NULL CHECK (amount_minor >= 0),
    currency           varchar(3)  NOT NULL,
    status             varchar(16) NOT NULL DEFAULT 'ALLOCATED'
                         CHECK (status IN ('ALLOCATED', 'REVERSED')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (payment_attempt_id, receiver)
);

CREATE INDEX IF NOT EXISTS idx_split_record_merchant
    ON split_record(merchant_id, created_at DESC);
