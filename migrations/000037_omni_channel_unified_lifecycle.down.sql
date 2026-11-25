-- 000037_omni_channel_unified_lifecycle.down.sql

DROP TABLE IF EXISTS payment_link_access_log;

ALTER TABLE payment_intents
    DROP COLUMN IF EXISTS provider_idempotency_key,
    DROP COLUMN IF EXISTS tenant_id,
    DROP COLUMN IF EXISTS payment_attempt_id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'payment_intents' AND constraint_name = 'chk_payment_intents_status'
    ) THEN
        ALTER TABLE payment_intents DROP CONSTRAINT chk_payment_intents_status;
    END IF;
END
$$;

ALTER TABLE checkout_sessions
    DROP COLUMN IF EXISTS tenant_id,
    DROP COLUMN IF EXISTS payment_attempt_id;

DROP TABLE IF EXISTS unified_webhook_events;
DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS payment_attempts;

DROP INDEX IF EXISTS idx_payment_links_rotated_from;
DROP INDEX IF EXISTS idx_payment_links_token_hash_active;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'payment_links' AND constraint_name = 'chk_payment_links_status'
    ) THEN
        ALTER TABLE payment_links DROP CONSTRAINT chk_payment_links_status;
    END IF;
END
$$;

ALTER TABLE payment_links
    DROP COLUMN IF EXISTS revoked_reason,
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS webhook_secret_hash,
    DROP COLUMN IF EXISTS rotated_from_id,
    DROP COLUMN IF EXISTS claimed_by_attempt_id,
    DROP COLUMN IF EXISTS claimed_at;