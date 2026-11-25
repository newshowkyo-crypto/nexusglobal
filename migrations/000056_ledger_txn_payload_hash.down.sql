-- 000056_ledger_txn_payload_hash.down.sql
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS payload_hash;
