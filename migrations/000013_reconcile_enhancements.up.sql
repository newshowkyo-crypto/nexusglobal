-- 000013_reconcile_enhancements.up.sql
-- Add reconcile tracking and manual review fields to payout_request

ALTER TABLE payout_request
    ADD COLUMN reconcile_attempts integer NOT NULL DEFAULT 0;

ALTER TABLE payout_request
    ADD COLUMN last_reconcile_at timestamptz;

ALTER TABLE payout_request
    ADD COLUMN manual_review boolean NOT NULL DEFAULT false;

ALTER TABLE payout_request
    ADD COLUMN manual_review_reason text NOT NULL DEFAULT '';

CREATE INDEX idx_payout_request_manual_review ON payout_request(manual_review) WHERE manual_review = true;