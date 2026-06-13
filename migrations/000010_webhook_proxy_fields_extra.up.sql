-- 000010_webhook_proxy_fields_extra.up.sql
ALTER TABLE webhook_receiver_proxy
    ADD COLUMN IF NOT EXISTS mq_topic        varchar(256) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS region          varchar(64)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS environment     varchar(32)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS health_status   varchar(32)  NOT NULL DEFAULT 'unknown',
    ADD COLUMN IF NOT EXISTS tenant_scope    jsonb        NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS version         integer      NOT NULL DEFAULT 1;
