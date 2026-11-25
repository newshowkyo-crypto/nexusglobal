-- 000034_runtime_schema_and_control_state.down.sql
-- 回滚 000034

-- 先删除触发器与函数
DROP TRIGGER IF EXISTS trg_admin_audit_log_no_delete ON admin_audit_log;
DROP TRIGGER IF EXISTS trg_admin_audit_log_no_update ON admin_audit_log;
DROP FUNCTION IF EXISTS prevent_admin_audit_log_mutation();

-- 删除新增表
DROP TABLE IF EXISTS system_control_state;
DROP TABLE IF EXISTS admin_audit_log;
DROP TABLE IF EXISTS ledger_reservations;
DROP TABLE IF EXISTS checkout_orders;

-- 回滚 account_balance 补强约束
ALTER TABLE account_balance
    DROP CONSTRAINT IF EXISTS chk_account_balance_frozen_nonneg,
    DROP CONSTRAINT IF EXISTS chk_account_balance_available_nonneg,
    DROP CONSTRAINT IF EXISTS chk_account_balance_balance_nonneg;

-- 回滚 ledger_transaction 补强约束
ALTER TABLE ledger_transaction
    DROP CONSTRAINT IF EXISTS chk_ledger_txn_status;