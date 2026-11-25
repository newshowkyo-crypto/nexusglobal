ALTER TABLE webhook_receiver_proxy
    DROP COLUMN IF EXISTS mq_topic,
    DROP COLUMN IF EXISTS region,
    DROP COLUMN IF EXISTS environment,
    DROP COLUMN IF EXISTS health_status,
    DROP COLUMN IF EXISTS tenant_scope,
    DROP COLUMN IF EXISTS version;