-- 000002_init_webhook.up.sql
-- Webhook Receiver Proxy tables

CREATE TABLE webhook_receiver_proxy (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_type   varchar(32)  NOT NULL,
    provider_id     varchar(64)  NOT NULL,
    webhook_url     varchar(512) NOT NULL,
    signature_algo  varchar(32)  NOT NULL DEFAULT 'HMAC_SHA256',
    secret_ciphertext bytea      NOT NULL,
    ip_allowlist    jsonb        NOT NULL DEFAULT '[]',
    rate_limit      integer      NOT NULL DEFAULT 100,
    status          varchar(32)  NOT NULL DEFAULT 'Active',
    created_at      timestamptz  NOT NULL DEFAULT now(),
    updated_at      timestamptz  NOT NULL DEFAULT now(),
    UNIQUE (provider_type, provider_id)
);

CREATE TABLE webhook_ingress_event (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_type         varchar(32)  NOT NULL,
    provider_id           varchar(64)  NOT NULL,
    provider_event_id     varchar(128) NOT NULL DEFAULT '',
    event_type            varchar(64)  NOT NULL,
    idempotency_key       varchar(128) NOT NULL UNIQUE,
    payload_hash          varchar(128) NOT NULL,
    raw_payload_ciphertext bytea       NOT NULL,
    status                varchar(32)  NOT NULL DEFAULT 'Received',
    error_message         text         NOT NULL DEFAULT '',
    created_at            timestamptz  NOT NULL DEFAULT now(),
    updated_at            timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX idx_webhook_ingress_provider ON webhook_ingress_event(provider_type, provider_id);
CREATE INDEX idx_webhook_ingress_status ON webhook_ingress_event(status);
CREATE INDEX idx_webhook_ingress_created ON webhook_ingress_event(created_at);
