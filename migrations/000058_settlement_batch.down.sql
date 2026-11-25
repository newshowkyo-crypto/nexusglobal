-- 000058_settlement_batch.down.sql
DROP TABLE IF EXISTS projection_applied;
DROP INDEX IF EXISTS idx_payment_attempts_unsettled;
ALTER TABLE payment_attempts DROP COLUMN IF EXISTS settled_at;
DROP TABLE IF EXISTS settlement_item;
DROP TABLE IF EXISTS settlement_batch;
