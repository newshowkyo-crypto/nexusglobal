-- 000057_ledger_entry_amount_precision.down.sql
DROP TABLE IF EXISTS asset_precision;
ALTER TABLE ledger_entry
    ALTER COLUMN amount TYPE numeric(20, 4) USING amount::numeric(20, 4);
