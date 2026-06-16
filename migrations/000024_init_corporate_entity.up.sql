-- 创建法人实体表
CREATE TABLE corporate_entity (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    name VARCHAR(100) NOT NULL,
    legal_name VARCHAR(255) NOT NULL,
    tax_id VARCHAR(50),
    country VARCHAR(2) NOT NULL,
    jurisdiction VARCHAR(100),
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    metadata JSONB,
    created_at TIMESTAMP NOT NULL DEFAULT now(),
    updated_at TIMESTAMP NOT NULL DEFAULT now()
);

-- 创建实体风险配置表
CREATE TABLE entity_risk_profile (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    corporate_entity_id UUID NOT NULL REFERENCES corporate_entity(id) ON DELETE CASCADE,
    max_daily_transaction_amount NUMERIC(30,18) NOT NULL DEFAULT 0,
    max_single_transaction_amount NUMERIC(30,18) NOT NULL DEFAULT 0,
    risk_score INT NOT NULL DEFAULT 0,
    allowed_countries TEXT[],
    blocked_countries TEXT[],
    created_at TIMESTAMP NOT NULL DEFAULT now(),
    updated_at TIMESTAMP NOT NULL DEFAULT now()
);

-- 创建实体资产限额表
CREATE TABLE entity_asset_limit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    corporate_entity_id UUID NOT NULL REFERENCES corporate_entity(id) ON DELETE CASCADE,
    asset_code VARCHAR(10) NOT NULL,
    max_balance NUMERIC(30,18) NOT NULL DEFAULT 0,
    max_daily_outflow NUMERIC(30,18) NOT NULL DEFAULT 0,
    max_daily_inflow NUMERIC(30,18) NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT now(),
    updated_at TIMESTAMP NOT NULL DEFAULT now()
);

-- 创建实体每日流水统计表
CREATE TABLE entity_daily_flow (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    corporate_entity_id UUID NOT NULL REFERENCES corporate_entity(id) ON DELETE CASCADE,
    asset_code VARCHAR(10) NOT NULL,
    date DATE NOT NULL,
    total_inflow NUMERIC(30,18) NOT NULL DEFAULT 0,
    total_outflow NUMERIC(30,18) NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT now(),
    updated_at TIMESTAMP NOT NULL DEFAULT now()
);

-- 创建索引
CREATE INDEX idx_corporate_entity_tenant_id ON corporate_entity(tenant_id);
CREATE INDEX idx_corporate_entity_status ON corporate_entity(status);
CREATE INDEX idx_entity_risk_profile_entity_id ON entity_risk_profile(corporate_entity_id);
CREATE INDEX idx_entity_asset_limit_entity_id ON entity_asset_limit(corporate_entity_id);
CREATE INDEX idx_entity_asset_limit_asset ON entity_asset_limit(corporate_entity_id, asset_code);
CREATE INDEX idx_entity_daily_flow_entity_date ON entity_daily_flow(corporate_entity_id, date);