-- V3.0 合规升级补丁 - 通道风控、准备金、费用摊销、管道隔离、合规资料包

-- 1. 创建通道风控模式枚举
CREATE TYPE channel_strategy_mode AS ENUM ('DEFAULT', 'STRICT', 'MANUAL_REVIEW');

-- 2. 修改 adapter_channel 表
ALTER TABLE adapter_channel
ADD COLUMN IF NOT EXISTS strategy_mode channel_strategy_mode NOT NULL DEFAULT 'DEFAULT',
ADD COLUMN IF NOT EXISTS chargeback_threshold_bps INT NOT NULL DEFAULT 100,
ADD COLUMN IF NOT EXISTS refund_threshold_bps INT NOT NULL DEFAULT 500,
ADD COLUMN IF NOT EXISTS failure_rate_threshold_bps INT NOT NULL DEFAULT 3000,
ADD COLUMN IF NOT EXISTS pipeline_group_id UUID NULL,
ADD COLUMN IF NOT EXISTS risk_override_enabled BOOLEAN NOT NULL DEFAULT FALSE,
ADD COLUMN IF NOT EXISTS risk_override_reason TEXT NULL,
ADD COLUMN IF NOT EXISTS risk_override_operator TEXT NULL,
ADD COLUMN IF NOT EXISTS risk_override_expires_at TIMESTAMPTZ NULL;

-- 3. 新增 admin_override_audit_log 表
CREATE TABLE IF NOT EXISTS admin_override_audit_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type TEXT NOT NULL,
    entity_id UUID NOT NULL,
    override_type TEXT NOT NULL,
    reason TEXT NOT NULL,
    operator TEXT NOT NULL,
    expires_at TIMESTAMPTZ NULL,
    old_value JSONB NULL,
    new_value JSONB NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 4. 新增 merchant_risk_profiles 表
CREATE TABLE IF NOT EXISTS merchant_risk_profiles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id UUID NOT NULL REFERENCES corporate_entity(id),
    rolling_reserve_rate_bps INT NOT NULL DEFAULT 1000,
    dispute_reserve_rate_bps INT NOT NULL DEFAULT 500,
    reserve_release_days INT NOT NULL DEFAULT 30,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 5. 新增 rolling_reserve_ledger 表
CREATE TABLE IF NOT EXISTS rolling_reserve_ledger (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id UUID NOT NULL REFERENCES corporate_entity(id),
    transaction_id UUID NOT NULL,
    amount NUMERIC(36,18) NOT NULL,
    reserve_type TEXT NOT NULL, -- 'ROLLING_RESERVE', 'DISPUTE_RESERVE'
    release_date TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'HELD', -- 'HELD', 'RELEASED', 'FORFEITED'
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 6. 新增 settlement_fee_allocation 表
CREATE TABLE IF NOT EXISTS settlement_fee_allocation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id UUID NOT NULL,
    merchant_id UUID NOT NULL REFERENCES corporate_entity(id),
    fee_type TEXT NOT NULL, -- 'LP_FEE', 'FX_SLIPPAGE', 'GAS_FEE', 'BANK_FEE', 'NETWORK_FEE'
    amount NUMERIC(36,18) NOT NULL,
    currency TEXT NOT NULL,
    source_data_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ALLOCATED',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 7. 新增 pipeline_isolation_groups 表
CREATE TABLE IF NOT EXISTS pipeline_isolation_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    description TEXT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE', -- 'ACTIVE', 'FROZEN', 'RISK_HOLD'
    frozen_reason TEXT NULL,
    frozen_by TEXT NULL,
    frozen_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 8. 新增 compliance_rfi_documents 表
CREATE TABLE IF NOT EXISTS compliance_rfi_documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL,
    merchant_id UUID NOT NULL REFERENCES corporate_entity(id),
    document_type TEXT NOT NULL, -- 'INVOICE', 'CONTRACT', 'SHIPPING', 'PROOF_OF_DELIVERY'
    source_data_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING', -- 'PENDING', 'REVIEW_REQUIRED', 'APPROVED', 'REJECTED'
    pdf_content BYTEA NULL,
    metadata JSONB NULL,
    approved_by TEXT NULL,
    approved_at TIMESTAMPTZ NULL,
    rejected_by TEXT NULL,
    rejected_at TIMESTAMPTZ NULL,
    rejected_reason TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 9. 创建索引
CREATE INDEX IF NOT EXISTS idx_admin_override_audit_log_entity ON admin_override_audit_log(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_merchant_risk_profiles_merchant_id ON merchant_risk_profiles(merchant_id);
CREATE INDEX IF NOT EXISTS idx_rolling_reserve_ledger_merchant_id ON rolling_reserve_ledger(merchant_id);
CREATE INDEX IF NOT EXISTS idx_rolling_reserve_ledger_release_date ON rolling_reserve_ledger(release_date, status);
CREATE INDEX IF NOT EXISTS idx_settlement_fee_allocation_transaction_id ON settlement_fee_allocation(transaction_id);
CREATE INDEX IF NOT EXISTS idx_pipeline_isolation_groups_status ON pipeline_isolation_groups(status);
CREATE INDEX IF NOT EXISTS idx_compliance_rfi_documents_order_id ON compliance_rfi_documents(order_id);
CREATE INDEX IF NOT EXISTS idx_compliance_rfi_documents_status ON compliance_rfi_documents(status);
CREATE INDEX IF NOT EXISTS idx_adapter_channel_pipeline_group_id ON adapter_channel(pipeline_group_id);
CREATE INDEX IF NOT EXISTS idx_adapter_channel_strategy_mode ON adapter_channel(strategy_mode);