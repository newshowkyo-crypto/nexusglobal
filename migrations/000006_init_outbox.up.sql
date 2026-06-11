-- 000006_init_outbox.up.sql
-- Transactional Outbox for async event dispatch

CREATE TABLE transactional_outbox (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type  varchar(64)  NOT NULL,
    aggregate_id    uuid         NOT NULL,
    event_type      varchar(64)  NOT NULL,
    payload         jsonb        NOT NULL,
    idempotency_key varchar(128) NOT NULL UNIQUE,
    status          varchar(32)  NOT NULL DEFAULT 'Pending',
    retry_count     integer      NOT NULL DEFAULT 0,
    next_retry_at   timestamptz  NOT NULL DEFAULT now(),
    locked_by       varchar(128),
    locked_at       timestamptz,
    created_at      timestamptz  NOT NULL DEFAULT now(),
    updated_at      timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX idx_outbox_dispatch ON transactional_outbox(status, next_retry_at)
    WHERE status IN ('Pending', 'Failed');

CREATE INDEX idx_outbox_aggregate ON transactional_outbox(aggregate_type, aggregate_id);
