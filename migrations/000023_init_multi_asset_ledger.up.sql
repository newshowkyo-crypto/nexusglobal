-- Migration: 000023_init_multi_asset_ledger
-- Description: Multi-Asset Ledger extensions and Asset Bridge Engine tables
-- Note: ledger_account, ledger_transaction, ledger_entry already exist in 000004

-- ============================================================================
-- Extend Ledger Account for Multi-Asset
-- ============================================================================

ALTER TABLE ledger_account ADD COLUMN IF NOT EXISTS tenant_id UUID NULL;
ALTER TABLE ledger_account ADD COLUMN IF NOT EXISTS corporate_entity_id UUID NULL;
ALTER TABLE ledger_account ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'ACTIVE';
ALTER TABLE ledger_account ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE ledger_account ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- ============================================================================
-- Extend Ledger Transaction for Multi-Asset
-- ============================================================================

ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS tenant_id UUID NULL;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS corporate_entity_id UUID NULL;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS source_account_id UUID NULL;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS target_account_id UUID NULL;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS asset_code TEXT NOT NULL DEFAULT '';
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS amount NUMERIC(36, 18) NOT NULL DEFAULT 0;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS fee_amount NUMERIC(36, 18) NOT NULL DEFAULT 0;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS fee_asset_code TEXT NULL;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'PENDING';
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS external_ref TEXT NULL;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS bridge_profile_id UUID NULL;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE ledger_transaction ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- ============================================================================
-- Extend Ledger Entry for Multi-Asset
-- ============================================================================

ALTER TABLE ledger_entry ADD COLUMN IF NOT EXISTS tenant_id UUID NULL;
ALTER TABLE ledger_entry ADD COLUMN IF NOT EXISTS corporate_entity_id UUID NULL;
ALTER TABLE ledger_entry ADD COLUMN IF NOT EXISTS transaction_type TEXT NOT NULL DEFAULT '';
ALTER TABLE ledger_entry ADD COLUMN IF NOT EXISTS asset_code TEXT NOT NULL DEFAULT '';
ALTER TABLE ledger_entry ADD COLUMN IF NOT EXISTS balance_before NUMERIC(36, 18) NOT NULL DEFAULT 0;
ALTER TABLE ledger_entry ADD COLUMN IF NOT EXISTS balance_after NUMERIC(36, 18) NOT NULL DEFAULT 0;
ALTER TABLE ledger_entry ADD COLUMN IF NOT EXISTS description TEXT NULL;
ALTER TABLE ledger_entry ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

-- ============================================================================
-- Account Balance Table (NEW)
-- ============================================================================

CREATE TABLE IF NOT EXISTS account_balance (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES ledger_account(id),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL,
    asset_code TEXT NOT NULL,
    balance NUMERIC(36, 18) NOT NULL DEFAULT 0,
    available NUMERIC(36, 18) NOT NULL DEFAULT 0,
    frozen NUMERIC(36, 18) NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_account_balance_account
ON account_balance(account_id, asset_code);

CREATE UNIQUE INDEX IF NOT EXISTS ux_account_balance_account_asset
ON account_balance(account_id, asset_code);

-- ============================================================================
-- Bridge Profile Table (NEW)
-- ============================================================================

CREATE TABLE IF NOT EXISTS bridge_profile (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL,
    name TEXT NOT NULL,
    description TEXT NULL,
    source_asset TEXT NOT NULL,
    target_asset TEXT NOT NULL,
    source_network TEXT NULL,
    target_network TEXT NULL,
    buffer_percent NUMERIC(5, 2) NOT NULL DEFAULT 0,
    batching_policy TEXT NOT NULL DEFAULT 'IMMEDIATE' CHECK (batching_policy IN ('IMMEDIATE', 'ACCUMULATE', 'SCHEDULED')),
    batch_size INT NOT NULL DEFAULT 1,
    batch_frequency_ms BIGINT NOT NULL DEFAULT 0,
    fx_conversion_policy TEXT NULL,
    execution_policy TEXT NOT NULL DEFAULT 'MARKET' CHECK (execution_policy IN ('MARKET', 'LIMIT', 'SLICED')),
    slippage_bps INT NOT NULL DEFAULT 0,
    max_slippage_bps INT NOT NULL DEFAULT 20,
    withdrawal_policy TEXT NULL,
    max_transaction_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    min_transaction_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    daily_limit NUMERIC(36, 18) NOT NULL DEFAULT 0,
    allowlisted_wallets TEXT[] NOT NULL DEFAULT '{}'::text[],
    require_approval BOOLEAN NOT NULL DEFAULT FALSE,
    approval_threshold NUMERIC(36, 18) NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bridge_profile_tenant
ON bridge_profile(tenant_id, enabled);

CREATE UNIQUE INDEX IF NOT EXISTS ux_bridge_profile_name
ON bridge_profile(tenant_id, name);

-- ============================================================================
-- Bridge Operation Table (NEW)
-- ============================================================================

CREATE TABLE IF NOT EXISTS bridge_operation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL,
    profile_id UUID NOT NULL REFERENCES bridge_profile(id),
    order_id TEXT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('IN', 'OUT')),
    source_asset TEXT NOT NULL,
    target_asset TEXT NOT NULL,
    source_amount NUMERIC(36, 18) NOT NULL,
    target_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    rate NUMERIC(36, 18) NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (
        status IN ('PENDING', 'PROCESSING', 'AWAITING_APPROVAL', 'EXECUTING', 'COMPLETED', 'FAILED', 'REVERSED', 'UNKNOWN_EXTERNAL_STATE')
    ),
    source_transaction_ref TEXT NULL,
    target_transaction_ref TEXT NULL,
    slippage_bps INT NOT NULL DEFAULT 0,
    actual_slippage_bps INT NOT NULL DEFAULT 0,
    execution_policy TEXT NOT NULL,
    allowlisted_wallet_used BOOLEAN NOT NULL DEFAULT FALSE,
    approval_required BOOLEAN NOT NULL DEFAULT FALSE,
    approved_by UUID NULL,
    approved_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bridge_operation_tenant
ON bridge_operation(tenant_id, status);

CREATE INDEX IF NOT EXISTS idx_bridge_operation_profile
ON bridge_operation(profile_id);

CREATE INDEX IF NOT EXISTS idx_bridge_operation_order
ON bridge_operation(order_id);

-- ============================================================================
-- Bridge Daily Limit Tracking (NEW)
-- ============================================================================

CREATE TABLE IF NOT EXISTS bridge_daily_tracking (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    profile_id UUID NOT NULL REFERENCES bridge_profile(id),
    track_date DATE NOT NULL DEFAULT CURRENT_DATE,
    total_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    transaction_count INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bridge_daily_tracking
ON bridge_daily_tracking(tenant_id, profile_id, track_date);

CREATE UNIQUE INDEX IF NOT EXISTS ux_bridge_daily_tracking
ON bridge_daily_tracking(tenant_id, profile_id, track_date);