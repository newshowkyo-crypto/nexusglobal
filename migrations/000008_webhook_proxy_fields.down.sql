ALTER TABLE webhook_receiver_proxy
    DROP COLUMN IF EXISTS proxy_code,
    DROP COLUMN IF EXISTS certificate_fingerprint,
    DROP COLUMN IF EXISTS certificate_expires_at,
    DROP COLUMN IF EXISTS secret_version;