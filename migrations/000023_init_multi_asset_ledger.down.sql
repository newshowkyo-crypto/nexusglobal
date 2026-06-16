-- Migration: 000023_init_multi_asset_ledger (DOWN)
-- Description: Rollback Multi-Asset Ledger and Asset Bridge Engine tables
-- Note: Do NOT drop core ledger tables (ledger_account, ledger_transaction, ledger_entry)
-- as they were created in 000004_init_ledger.up.sql

-- Drop new tables created by this migration
DROP TABLE IF EXISTS bridge_daily_tracking;
DROP TABLE IF EXISTS bridge_operation;
DROP TABLE IF EXISTS bridge_profile;
DROP TABLE IF EXISTS account_balance;

-- Drop columns added by this migration (if they exist)
ALTER TABLE ledger_entry DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE ledger_entry DROP COLUMN IF EXISTS corporate_entity_id;
ALTER TABLE ledger_entry DROP COLUMN IF EXISTS transaction_type;
ALTER TABLE ledger_entry DROP COLUMN IF EXISTS asset_code;
ALTER TABLE ledger_entry DROP COLUMN IF EXISTS balance_before;
ALTER TABLE ledger_entry DROP COLUMN IF EXISTS balance_after;
ALTER TABLE ledger_entry DROP COLUMN IF EXISTS description;
ALTER TABLE ledger_entry DROP COLUMN IF EXISTS metadata;

ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS corporate_entity_id;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS source_account_id;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS target_account_id;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS asset_code;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS amount;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS fee_amount;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS fee_asset_code;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS status;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS external_ref;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS bridge_profile_id;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS metadata;
ALTER TABLE ledger_transaction DROP COLUMN IF EXISTS updated_at;

ALTER TABLE ledger_account DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE ledger_account DROP COLUMN IF EXISTS corporate_entity_id;
ALTER TABLE ledger_account DROP COLUMN IF EXISTS status;
ALTER TABLE ledger_account DROP COLUMN IF EXISTS metadata;
ALTER TABLE ledger_account DROP COLUMN IF EXISTS updated_at;