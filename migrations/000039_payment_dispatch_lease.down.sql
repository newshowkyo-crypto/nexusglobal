-- 000039_payment_dispatch_lease.down.sql
-- B2/B6: 回滚 dispatch lease 字段和 token 加密列

DROP INDEX IF EXISTS idx_payment_attempts_dispatch_lease;

ALTER TABLE payment_attempts
    DROP COLUMN IF EXISTS dispatch_started_at,
    DROP COLUMN IF EXISTS dispatch_lease_until,
    DROP COLUMN IF EXISTS dispatch_owner,
    DROP COLUMN IF EXISTS dispatch_attempt_count,
    DROP COLUMN IF EXISTS payment_method_token_encrypted;
