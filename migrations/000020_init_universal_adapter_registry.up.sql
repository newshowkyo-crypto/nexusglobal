-- Migration: 000020_init_universal_adapter_registry
-- Description: Initialize Universal Adapter Registry for multi-modal asset flow
-- This migration creates the foundation for CARD_ACQUIRER, BANK_COLLECTION, CRYPTO_PAY,
-- BANK_RAIL, EXCHANGE, LP, and ONCHAIN_WALLET adapter types

-- ============================================================================
-- Asset Definition
-- ============================================================================

CREATE TABLE asset_definition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    asset_code TEXT NOT NULL,
    asset_type TEXT NOT NULL CHECK (asset_type IN ('FIAT', 'CRYPTO', 'STABLECOIN')),
    network TEXT NULL,
    decimals INT NOT NULL DEFAULT 2,
    canonical_symbol TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ux_asset_definition_code_network
ON asset_definition(asset_code, COALESCE(network, ''));

-- Insert default assets
INSERT INTO asset_definition (asset_code, asset_type, network, decimals, canonical_symbol) VALUES
('USD', 'FIAT', NULL, 2, 'USD'),
('EUR', 'FIAT', NULL, 2, 'EUR'),
('GBP', 'FIAT', NULL, 2, 'GBP'),
('CNY', 'FIAT', NULL, 2, 'CNY'),
('USDT', 'STABLECOIN', 'TRON', 6, 'USDT'),
('USDT', 'STABLECOIN', 'ETHEREUM', 6, 'USDT'),
('USDC', 'STABLECOIN', 'ETHEREUM', 6, 'USDC'),
('BTC', 'CRYPTO', 'BITCOIN', 8, 'BTC'),
('ETH', 'CRYPTO', 'ETHEREUM', 18, 'ETH');

-- ============================================================================
-- Adapter Provider
-- ============================================================================

CREATE TABLE adapter_provider (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_code TEXT NOT NULL UNIQUE,
    provider_name TEXT NOT NULL,
    adapter_type TEXT NOT NULL CHECK (
        adapter_type IN (
            'CARD_ACQUIRER',
            'BANK_COLLECTION',
            'CRYPTO_PAY',
            'BANK_RAIL',
            'EXCHANGE',
            'LP',
            'ONCHAIN_WALLET'
        )
    ),
    provider_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_adapter_provider_type
ON adapter_provider(adapter_type, status);

-- Insert default providers (migrate from existing payment_provider)
INSERT INTO adapter_provider (provider_code, provider_name, adapter_type, provider_config) VALUES
('mock', 'Mock Acquirer', 'CARD_ACQUIRER', '{"mode": "mock"}'),
('stripe', 'Stripe', 'CARD_ACQUIRER', '{"api_version": "2023-10-16"}'),
('nxtapay', 'Nxtapay', 'CARD_ACQUIRER', '{}'),
('airwallex', 'Airwallex', 'BANK_COLLECTION', '{}'),
('payoneer', 'Payoneer', 'BANK_COLLECTION', '{}'),
('mock_bank_collection', 'Mock Bank Collection', 'BANK_COLLECTION', '{"mode": "mock"}'),
('gateway', 'Crypto Gateway', 'CRYPTO_PAY', '{}'),
('custody', 'Crypto Custody', 'CRYPTO_PAY', '{}'),
('mock_crypto_pay', 'Mock Crypto Pay', 'CRYPTO_PAY', '{"mode": "mock"}'),
('binance', 'Binance Institutional', 'EXCHANGE', '{"max_allowed_slippage": "0.002"}'),
('kraken', 'Kraken', 'EXCHANGE', '{"max_allowed_slippage": "0.002"}'),
('mock_exchange', 'Mock Exchange', 'EXCHANGE', '{"mode": "mock"}'),
('lp_institutional', 'Institutional LP', 'LP', '{"quote_ttl_seconds": 60}'),
('mock_lp', 'Mock LP', 'LP', '{"mode": "mock"}'),
('hot_wallet', 'Hot Wallet', 'ONCHAIN_WALLET', '{}'),
('cold_wallet', 'Cold Wallet', 'ONCHAIN_WALLET', '{}'),
('mock_onchain_wallet', 'Mock Onchain Wallet', 'ONCHAIN_WALLET', '{"mode": "mock"}'),
('bank_rail_ach', 'Bank Rail ACH', 'BANK_RAIL', '{"rail": "ACH"}'),
('bank_rail_wire', 'Bank Rail Wire', 'BANK_RAIL', '{"rail": "WIRE"}'),
('bank_rail_sepa', 'Bank Rail SEPA', 'BANK_RAIL', '{"rail": "SEPA"}'),
('mock_bank_rail', 'Mock Bank Rail', 'BANK_RAIL', '{"mode": "mock"}');

-- ============================================================================
-- Adapter Channel
-- ============================================================================

CREATE TABLE adapter_channel (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL, -- V3: 法人实体隔离
    merchant_id UUID NULL,
    provider_id UUID NOT NULL REFERENCES adapter_provider(id),
    channel_code TEXT NOT NULL,
    channel_name TEXT NOT NULL,
    adapter_type TEXT NOT NULL,
    supported_assets JSONB NOT NULL DEFAULT '[]'::jsonb,
    supported_networks JSONB NOT NULL DEFAULT '[]'::jsonb,
    channel_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    routing_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED', 'RISK_HOLD')),
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, channel_code)
);

CREATE INDEX idx_adapter_channel_tenant_type
ON adapter_channel(tenant_id, adapter_type, status);

CREATE INDEX idx_adapter_channel_provider
ON adapter_channel(provider_id);

CREATE INDEX idx_adapter_channel_entity
ON adapter_channel(corporate_entity_id);

-- ============================================================================
-- Adapter Credential Vault
-- ============================================================================

CREATE TABLE adapter_credential_vault (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id UUID NOT NULL REFERENCES adapter_provider(id),
    channel_id UUID NULL REFERENCES adapter_channel(id),
    credential_type TEXT NOT NULL CHECK (
        credential_type IN (
            'API_KEY',
            'API_SECRET',
            'WEBHOOK_SECRET',
            'CLIENT_CERT',
            'SIGNING_KEY_REF',
            'RPC_TOKEN',
            'WITHDRAW_KEY_REF'
        )
    ),
    key_version INT NOT NULL DEFAULT 1,
    ciphertext BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    aad_context TEXT NOT NULL,
    expires_at TIMESTAMPTZ NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'REVOKED', 'EXPIRED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_adapter_credential_provider_channel
ON adapter_credential_vault(provider_id, channel_id, credential_type, status);

-- ============================================================================
-- Asset Bridge Profile
-- ============================================================================

CREATE TABLE asset_bridge_profile (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL, -- V3: 法人实体隔离
    profile_code TEXT NOT NULL,
    source_asset_id UUID NOT NULL REFERENCES asset_definition(id),
    target_asset_id UUID NOT NULL REFERENCES asset_definition(id),
    route_steps JSONB NOT NULL DEFAULT '[]'::jsonb,
    max_allowed_slippage NUMERIC(10, 8) NOT NULL DEFAULT 0.002,
    min_sweep_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    reserve_floor NUMERIC(36, 18) NOT NULL DEFAULT 0,
    buffer_percent NUMERIC(8, 6) NOT NULL DEFAULT 0, -- V3
    batching_policy JSONB NOT NULL DEFAULT '{}'::jsonb, -- V3
    fx_conversion_policy JSONB NOT NULL DEFAULT '{}'::jsonb, -- V3
    execution_policy JSONB NOT NULL DEFAULT '{}'::jsonb, -- V3
    withdrawal_policy JSONB NOT NULL DEFAULT '{}'::jsonb, -- V3
    compliance_policy JSONB NOT NULL DEFAULT '{}'::jsonb, -- V3
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED')),
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, profile_code)
);

CREATE INDEX idx_asset_bridge_profile_tenant
ON asset_bridge_profile(tenant_id, status);

CREATE INDEX idx_asset_bridge_profile_assets
ON asset_bridge_profile(source_asset_id, target_asset_id);

-- ============================================================================
-- Asset Bridge Job
-- ============================================================================

CREATE TABLE asset_bridge_job (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL,
    merchant_id UUID NULL,
    profile_id UUID NOT NULL REFERENCES asset_bridge_profile(id),
    source_asset_id UUID NOT NULL REFERENCES asset_definition(id),
    target_asset_id UUID NOT NULL REFERENCES asset_definition(id),
    source_amount NUMERIC(36, 18) NOT NULL,
    target_amount NUMERIC(36, 18) NULL,
    state TEXT NOT NULL CHECK (
        state IN (
            'CREATED',
            'PENDING_APPROVAL',
            'APPROVED',
            'EXECUTING',
            'STEP_RUNNING',
            'STEP_COMPLETED',
            'STEP_FAILED',
            'STEP_UNKNOWN',
            'COMPLETED',
            'FAILED',
            'CANCELLED',
            'APPROVAL_REQUIRED',
            'UNKNOWN_EXTERNAL_STATE'
        )
    ),
    current_step INT NOT NULL DEFAULT 0,
    idempotency_key TEXT NOT NULL,
    external_refs JSONB NOT NULL DEFAULT '{}'::jsonb,
    risk_flags JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_error_code TEXT NULL,
    last_error_message TEXT NULL,
    next_attempt_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX idx_asset_bridge_job_state
ON asset_bridge_job(state, next_attempt_at);

CREATE INDEX idx_asset_bridge_job_profile
ON asset_bridge_job(profile_id);

-- ============================================================================
-- Asset Bridge Outbox
-- ============================================================================

CREATE TABLE asset_bridge_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bridge_job_id UUID NOT NULL REFERENCES asset_bridge_job(id),
    tenant_id UUID NOT NULL,
    adapter_channel_id UUID NOT NULL REFERENCES adapter_channel(id),
    operation TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (
        status IN ('PENDING', 'SUBMITTED', 'COMPLETED', 'FAILED', 'UNKNOWN_EXTERNAL_STATE')
    ),
    external_ref TEXT NULL,
    attempt_count INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NULL,
    last_error_code TEXT NULL,
    last_error_message TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (operation, idempotency_key)
);

CREATE INDEX idx_asset_bridge_outbox_status
ON asset_bridge_outbox(status, next_attempt_at);

-- ============================================================================
-- Transit Wallet Policy (V3)
-- ============================================================================

CREATE TABLE transit_wallet_policy (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL,
    policy_code TEXT NOT NULL,
    asset_id UUID NOT NULL REFERENCES asset_definition(id),
    network TEXT NOT NULL,
    destination_type TEXT NOT NULL CHECK (destination_type IN ('TRANSIT', 'COLD_WALLET', 'CUSTODY')),
    address TEXT NOT NULL,
    label TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED', 'RISK_HOLD')),
    max_single_withdrawal NUMERIC(36, 18) NULL,
    daily_limit NUMERIC(36, 18) NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, network, address)
);

CREATE INDEX idx_transit_wallet_policy_asset_network
ON transit_wallet_policy(asset_id, network, status);

-- ============================================================================
-- Ledger Updates for Multi-Asset Support
-- ============================================================================

-- Add asset_id to ledger_account
ALTER TABLE ledger_account
ADD COLUMN IF NOT EXISTS asset_id UUID NULL REFERENCES asset_definition(id);

-- Add asset_id to ledger_entry (真实表名是 ledger_entry，不是 ledger_transaction_entry)
ALTER TABLE ledger_entry
ADD COLUMN IF NOT EXISTS asset_id UUID NULL REFERENCES asset_definition(id);

-- Add new account types for multi-asset ledger
-- Note: These are logical account types, not new tables
-- The existing ledger_account.account_type will support these values:
-- ASSET_BRIDGE_IN_TRANSIT
-- EXCHANGE_CLEARING
-- ONCHAIN_CLEARING
-- ASSET_EXCHANGE_GAIN_LOSS
-- MERCHANT_CRYPTO_SETTLED