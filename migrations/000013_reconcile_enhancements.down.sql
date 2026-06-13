-- 000013_reconcile_enhancements.down.sql
-- Remove reconcile tracking and manual review fields from payout_request

DROP INDEX IF EXISTS idx_payout_request_manual_review;

ALTER TABLE payout_request
    DROP COLUMN IF EXISTS manual_review_reason;

ALTER TABLE payout_request
    DROP COLUMN IF EXISTS manual_review;

ALTER TABLE payout_request
    DROP COLUMN IF EXISTS last_reconcile_at;

ALTER TABLE payout_request
    DROP COLUMN IF EXISTS reconcile_attempts;