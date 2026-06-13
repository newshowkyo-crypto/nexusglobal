-- 000014_idempotency_payload_hash.up.sql
-- Add payload_hash column for idempotency conflict detection

ALTER TABLE payout_request
    ADD COLUMN payload_hash varchar(64) NOT NULL DEFAULT '';

CREATE INDEX idx_payout_request_idempotency ON payout_request(idempotency_key, payload_hash);