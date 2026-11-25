-- 000063_merchant_refund.up.sql
-- A2: 商户退款请求（部分退款、累计校验、幂等、异步执行）

CREATE TABLE IF NOT EXISTS refund_request (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid        NOT NULL,
    merchant_id        uuid        NOT NULL,
    payment_attempt_id uuid        NOT NULL REFERENCES payment_attempts(id),
    merchant_refund_id varchar(128) NOT NULL,
    idempotency_key    varchar(128) NOT NULL,
    amount_minor       bigint      NOT NULL CHECK (amount_minor > 0),
    currency           varchar(3)  NOT NULL,
    status             varchar(24) NOT NULL DEFAULT 'PENDING'
                         CHECK (status IN ('PENDING', 'PROCESSING', 'SUCCEEDED', 'FAILED', 'UNKNOWN')),
    reason             varchar(256) NOT NULL DEFAULT '',
    provider_refund_id varchar(128) NOT NULL DEFAULT '',
    failure_code       varchar(64)  NOT NULL DEFAULT '',
    failure_reason     text         NOT NULL DEFAULT '',
    version            integer      NOT NULL DEFAULT 0,
    created_at         timestamptz  NOT NULL DEFAULT now(),
    updated_at         timestamptz  NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, idempotency_key),
    UNIQUE (merchant_id, merchant_refund_id)
);

CREATE INDEX IF NOT EXISTS idx_refund_request_attempt
    ON refund_request(payment_attempt_id, status);
CREATE INDEX IF NOT EXISTS idx_refund_request_merchant
    ON refund_request(merchant_id, created_at DESC);
