-- 000055_ledger_account_normal_side.down.sql
DROP TRIGGER IF EXISTS trg_ledger_account_normal_side ON ledger_account;
DROP FUNCTION IF EXISTS ledger_account_default_normal_side();
ALTER TABLE ledger_account DROP CONSTRAINT IF EXISTS chk_ledger_account_normal_side;
ALTER TABLE ledger_account DROP COLUMN IF EXISTS normal_side;
