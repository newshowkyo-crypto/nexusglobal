-- Migration: 000022_init_crypto_pay
-- Description: Crypto Pay specific tables
-- This migration adds crypto pay specific functionality to the universal adapter registry

-- ============================================================================
-- Crypto Pay Deposit Intent Table
-- ============================================================================

CREATE TABLE crypto_pay_deposit_intent (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL,
    merchant_id UUID NULL,
    order_id TEXT NOT NULL,
    asset_code TEXT NOT NULL,
    network TEXT NOT NULL,
    expected_amount NUMERIC(36, 18) NOT NULL,
    deposit_address TEXT NOT NULL,
    memo_tag TEXT NULL,
    confirmations_required INT NOT NULL DEFAULT 6,
    provider_ref TEXT NULL,
    state TEXT NOT NULL DEFAULT 'CREATED' CHECK (
        state IN (
            'CREATED',
            'WAITING_FUNDS',
            'CONFIRMING',
            'CONFIRMED',
            'RECONCILE_MISMATCH',
            'EXPIRED',
            'FAILED'
        )
    ),
    received_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    confirmed_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    confirmations INT NOT NULL DEFAULT 0,
    transaction_ref TEXT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    expires_at TIMESTAMPTZ NOT NULL,
    retry_count INT NOT NULL DEFAULT 0,
    last_error TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crypto_pay_deposit_intent_tenant
ON crypto_pay_deposit_intent(tenant_id, state);

CREATE INDEX idx_crypto_pay_deposit_intent_address
ON crypto_pay_deposit_intent(deposit_address);

CREATE INDEX idx_crypto_pay_deposit_intent_provider
ON crypto_pay_deposit_intent(provider_ref);

CREATE INDEX idx_crypto_pay_deposit_intent_expires
ON crypto_pay_deposit_intent(expires_at);

-- ============================================================================
-- Crypto Pay Deposit Address Pool
-- ============================================================================

CREATE TABLE crypto_pay_address_pool (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    channel_id UUID NOT NULL REFERENCES adapter_channel(id),
    asset_code TEXT NOT NULL,
    network TEXT NOT NULL,
    address TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'AVAILABLE' CHECK (status IN ('AVAILABLE', 'ALLOCATED', 'USED', 'DISABLED')),
    allocated_intent_id UUID NULL REFERENCES crypto_pay_deposit_intent(id),
    allocated_at TIMESTAMPTZ NULL,
    released_at TIMESTAMPTZ NULL,
    used_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crypto_pay_address_pool_tenant
ON crypto_pay_address_pool(tenant_id, status);

CREATE INDEX idx_crypto_pay_address_pool_channel
ON crypto_pay_address_pool(channel_id);

CREATE INDEX idx_crypto_pay_address_pool_asset_network
ON crypto_pay_address_pool(asset_code, network, status);

CREATE UNIQUE INDEX ux_crypto_pay_address_pool_address
ON crypto_pay_address_pool(address);

-- ============================================================================
-- Crypto Pay Chain Transaction Log
-- ============================================================================

CREATE TABLE crypto_pay_chain_transaction (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    intent_id UUID NOT NULL REFERENCES crypto_pay_deposit_intent(id),
    transaction_hash TEXT NOT NULL,
    block_hash TEXT NULL,
    block_number BIGINT NULL,
    from_address TEXT NOT NULL,
    to_address TEXT NOT NULL,
    amount NUMERIC(36, 18) NOT NULL,
    asset_code TEXT NOT NULL,
    network TEXT NOT NULL,
    confirmations INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'CONFIRMED', 'FAILED')),
    memo_tag TEXT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crypto_pay_chain_tx_tenant
ON crypto_pay_chain_transaction(tenant_id, status);

CREATE INDEX idx_crypto_pay_chain_tx_intent
ON crypto_pay_chain_transaction(intent_id);

CREATE UNIQUE INDEX ux_crypto_pay_chain_tx_hash
ON crypto_pay_chain_transaction(transaction_hash);

-- ============================================================================
-- Crypto Pay Listener Configuration
-- ============================================================================

CREATE TABLE crypto_pay_listener_config (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    channel_id UUID NOT NULL REFERENCES adapter_channel(id),
    network TEXT NOT NULL,
    rpc_url TEXT NOT NULL,
    indexer_url TEXT NULL,
    polling_interval_ms INT NOT NULL DEFAULT 10000,
    confirmation_threshold INT NOT NULL DEFAULT 6,
    start_block BIGINT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crypto_pay_listener_tenant
ON crypto_pay_listener_config(tenant_id);

CREATE INDEX idx_crypto_pay_listener_channel
ON crypto_pay_listener_config(channel_id);

CREATE UNIQUE INDEX ux_crypto_pay_listener_network
ON crypto_pay_listener_config(tenant_id, network);