-- 000009_reserve_batch.up.sql
CREATE TABLE reserve_batch (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id     uuid        NOT NULL,
    currency        varchar(16) NOT NULL,
    amount_minor    bigint      NOT NULL,
    hold_date       date        NOT NULL,
    release_date    date        NOT NULL,
    status          varchar(32) NOT NULL DEFAULT 'Held',
    idempotency_key varchar(128) NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_reserve_batch_release ON reserve_batch (release_date, status);
CREATE INDEX idx_reserve_batch_merchant ON reserve_batch (merchant_id, status);
