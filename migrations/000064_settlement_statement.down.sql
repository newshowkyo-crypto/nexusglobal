-- 000064_settlement_statement.down.sql
DROP TABLE IF EXISTS settlement_statement_line;
DROP TABLE IF EXISTS settlement_statement;
DROP INDEX IF EXISTS idx_refund_request_succeeded_at;
DROP INDEX IF EXISTS idx_payment_attempts_captured_at;
ALTER TABLE refund_request DROP COLUMN IF EXISTS succeeded_at;
ALTER TABLE payment_attempts DROP COLUMN IF EXISTS captured_at;
