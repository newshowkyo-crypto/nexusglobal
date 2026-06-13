-- 000008_webhook_proxy_fields.up.sql
ALTER TABLE webhook_receiver_proxy
    ADD COLUMN IF NOT EXISTS proxy_code              varchar(64)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS certificate_fingerprint  varchar(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS certificate_expires_at   timestamptz,
    ADD COLUMN IF NOT EXISTS secret_version           integer      NOT NULL DEFAULT 1;
