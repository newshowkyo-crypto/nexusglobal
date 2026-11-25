-- 000062_merchant_webhook.up.sql
-- A2 商户出站回调：端点 + 投递记录（含重试与死信）
--
-- 约束：
--   * 每个 endpoint 的 secret 只存密文（AES-GCM + AAD），创建时返回一次明文。
--   * 投递按 (endpoint_id, event_type, event_id) 唯一，保证事件幂等。
--   * PENDING/DELIVERING 可重试；DEAD 为死信，可人工 replay。

CREATE TABLE IF NOT EXISTS merchant_webhook_endpoint (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid        NOT NULL,
    merchant_id    uuid        NOT NULL,
    url            text        NOT NULL,
    secret_enc     bytea       NOT NULL,
    secret_masked  varchar(64) NOT NULL DEFAULT '',
    event_types    text[]      NOT NULL DEFAULT '{}',
    description    varchar(256) NOT NULL DEFAULT '',
    status         varchar(16) NOT NULL DEFAULT 'ACTIVE'
                     CHECK (status IN ('ACTIVE', 'DISABLED')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, url)
);

CREATE INDEX IF NOT EXISTS idx_merchant_webhook_endpoint_merchant
    ON merchant_webhook_endpoint(merchant_id, status);

CREATE TABLE IF NOT EXISTS merchant_webhook_delivery (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id      uuid        NOT NULL REFERENCES merchant_webhook_endpoint(id) ON DELETE CASCADE,
    merchant_id      uuid        NOT NULL,
    event_type       varchar(64) NOT NULL,
    event_id         varchar(128) NOT NULL,
    payload          jsonb       NOT NULL,
    status           varchar(16) NOT NULL DEFAULT 'PENDING'
                       CHECK (status IN ('PENDING', 'DELIVERING', 'SUCCEEDED', 'FAILED', 'DEAD')),
    attempt_count    integer     NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    last_status_code integer     NOT NULL DEFAULT 0,
    last_error       text        NOT NULL DEFAULT '',
    delivered_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (endpoint_id, event_type, event_id)
);

CREATE INDEX IF NOT EXISTS idx_merchant_webhook_delivery_due
    ON merchant_webhook_delivery(status, next_attempt_at);

CREATE INDEX IF NOT EXISTS idx_merchant_webhook_delivery_merchant
    ON merchant_webhook_delivery(merchant_id, created_at DESC);
