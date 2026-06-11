-- 000005_init_payout.up.sql
-- Payout module tables

CREATE TABLE merchant_treasury_ledger (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id               uuid           NOT NULL,
    currency                  varchar(16)    NOT NULL,
    pending_balance           numeric(20,4)  NOT NULL DEFAULT 0,
    settled_balance           numeric(20,4)  NOT NULL DEFAULT 0,
    reserve_balance           numeric(20,4)  NOT NULL DEFAULT 0,
    frozen_payout_balance     numeric(20,4)  NOT NULL DEFAULT 0,
    dispute_hold_balance      numeric(20,4)  NOT NULL DEFAULT 0,
    available_payout_balance  numeric(20,4)  NOT NULL DEFAULT 0,
    version                   integer        NOT NULL DEFAULT 0,
    updated_at                timestamptz    NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, currency),
    CHECK (settled_balance >= 0),
    CHECK (frozen_payout_balance >= 0)
);

CREATE TABLE merchant_payout_address (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id           uuid         NOT NULL,
    target_asset          varchar(32)  NOT NULL,
    target_network        varchar(32)  NOT NULL,
    address_format_type   varchar(32)  NOT NULL,
    target_address_enc    bytea        NOT NULL,
    target_address_hash   varchar(128) NOT NULL,
    target_address_masked varchar(128) NOT NULL,
    address_label         varchar(128) NOT NULL DEFAULT '',
    screening_status      varchar(32)  NOT NULL DEFAULT 'Pending',
    whitelist_status      varchar(32)  NOT NULL DEFAULT 'Pending',
    created_at            timestamptz  NOT NULL DEFAULT now(),
    updated_at            timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX idx_payout_address_merchant ON merchant_payout_address(merchant_id);

CREATE TABLE payout_request (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id      uuid           NOT NULL,
    payout_address_id uuid          NOT NULL REFERENCES merchant_payout_address(id),
    amount           numeric(20,4)  NOT NULL CHECK (amount > 0),
    fiat_currency    varchar(16)    NOT NULL,
    target_asset     varchar(32)    NOT NULL,
    target_network   varchar(32)    NOT NULL,
    status           varchar(32)    NOT NULL DEFAULT 'Draft',
    idempotency_key  varchar(128)   NOT NULL UNIQUE,
    provider_ref     varchar(128)   NOT NULL DEFAULT '',
    lock_id          varchar(128)   NOT NULL DEFAULT '',
    version          integer        NOT NULL DEFAULT 0,
    created_at       timestamptz    NOT NULL DEFAULT now(),
    updated_at       timestamptz    NOT NULL DEFAULT now()
);

CREATE INDEX idx_payout_request_merchant ON payout_request(merchant_id);
CREATE INDEX idx_payout_request_status ON payout_request(status);
