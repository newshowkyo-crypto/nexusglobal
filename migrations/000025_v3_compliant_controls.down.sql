-- V3.0 合规升级补丁回滚

-- 删除索引
DROP INDEX IF EXISTS idx_adapter_channel_strategy_mode;
DROP INDEX IF EXISTS idx_adapter_channel_pipeline_group_id;
DROP INDEX IF EXISTS idx_compliance_rfi_documents_status;
DROP INDEX IF EXISTS idx_compliance_rfi_documents_order_id;
DROP INDEX IF EXISTS idx_pipeline_isolation_groups_status;
DROP INDEX IF EXISTS idx_settlement_fee_allocation_transaction_id;
DROP INDEX IF EXISTS idx_rolling_reserve_ledger_release_date;
DROP INDEX IF EXISTS idx_rolling_reserve_ledger_merchant_id;
DROP INDEX IF EXISTS idx_merchant_risk_profiles_merchant_id;
DROP INDEX IF EXISTS idx_admin_override_audit_log_entity;

-- 删除新增表
DROP TABLE IF EXISTS compliance_rfi_documents;
DROP TABLE IF EXISTS pipeline_isolation_groups;
DROP TABLE IF EXISTS settlement_fee_allocation;
DROP TABLE IF EXISTS rolling_reserve_ledger;
DROP TABLE IF EXISTS merchant_risk_profiles;
DROP TABLE IF EXISTS admin_override_audit_log;

-- 删除 adapter_channel 新增列
ALTER TABLE adapter_channel
DROP COLUMN IF EXISTS strategy_mode,
DROP COLUMN IF EXISTS chargeback_threshold_bps,
DROP COLUMN IF EXISTS refund_threshold_bps,
DROP COLUMN IF EXISTS failure_rate_threshold_bps,
DROP COLUMN IF EXISTS pipeline_group_id,
DROP COLUMN IF EXISTS risk_override_enabled,
DROP COLUMN IF EXISTS risk_override_reason,
DROP COLUMN IF EXISTS risk_override_operator,
DROP COLUMN IF EXISTS risk_override_expires_at;

-- 删除枚举类型
DROP TYPE IF EXISTS channel_strategy_mode;