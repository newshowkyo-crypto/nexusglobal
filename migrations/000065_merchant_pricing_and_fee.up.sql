-- 000065_merchant_pricing_and_fee.up.sql
-- P5: 费率配置与手续费记录（结算时计费）

CREATE TABLE IF NOT EXISTS merchant_pricing (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL,
    merchant_id       uuid        NOT NULL,
    currency          varchar(3)  NOT NULL DEFAULT '',
    card_brand        varchar(32) NOT NULL DEFAULT '',
    channel_id        uuid        NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    rate_bps          integer     NOT NULL DEFAULT 0 CHECK (rate_bps >= 0 AND rate_bps <= 10000),
    fixed_fee_minor   bigint      NOT NULL DEFAULT 0 CHECK (fixed_fee_minor >= 0),
    min_fee_minor     bigint      NOT NULL DEFAULT 0 CHECK (min_fee_minor >= 0),
    max_fee_minor     bigint      NOT NULL DEFAULT 0 CHECK (max_fee_minor >= 0),
    status            varchar(16) NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE', 'DISABLED')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, currency, card_brand, channel_id)
);

CREATE INDEX IF NOT EXISTS idx_merchant_pricing_lookup
    ON merchant_pricing(merchant_id, currency, card_brand);

CREATE TABLE IF NOT EXISTS fee_record (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid        NOT NULL,
    merchant_id        uuid        NOT NULL,
    payment_attempt_id uuid        NOT NULL REFERENCES payment_attempts(id),
    currency           varchar(3)  NOT NULL,
    base_amount_minor  bigint      NOT NULL CHECK (base_amount_minor >= 0),
    rate_bps           integer     NOT NULL DEFAULT 0,
    fixed_fee_minor    bigint      NOT NULL DEFAULT 0,
    fee_amount_minor   bigint      NOT NULL CHECK (fee_amount_minor >= 0),
    pricing_id         uuid        NULL,
    source             varchar(16) NOT NULL DEFAULT 'CAPTURE'
                         CHECK (source IN ('CAPTURE', 'REFUND', 'PAYOUT')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (payment_attempt_id, source)
);

CREATE INDEX IF NOT EXISTS idx_fee_record_merchant
    ON fee_record(merchant_id, created_at DESC);
