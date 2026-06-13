-- 000014_idempotency_payload_hash.down.sql
-- Remove payload_hash column

DROP INDEX IF EXISTS idx_payout_request_idempotency;

ALTER TABLE payout_request
    DROP COLUMN IF EXISTS payload_hash;