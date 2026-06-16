-- Migration: 000021_init_bank_collection
-- Description: Bank Collection specific tables and enhancements
-- This migration adds bank collection specific functionality to the universal adapter registry

-- ============================================================================
-- Bank Collection Inbound Intent Table
-- ============================================================================

CREATE TABLE bank_collection_intent (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL,
    merchant_id UUID NULL,
    order_id TEXT NOT NULL,
    asset_code TEXT NOT NULL,
    expected_amount NUMERIC(36, 18) NOT NULL,
    buyer_ref TEXT NULL,
    payment_reference TEXT NOT NULL UNIQUE,
    virtual_account_type TEXT NOT NULL DEFAULT 'DYNAMIC' CHECK (virtual_account_type IN ('DYNAMIC', 'STATIC')),
    provider_ref TEXT NULL,
    state TEXT NOT NULL DEFAULT 'CREATED' CHECK (
        state IN (
            'CREATED',
            'WAITING_FUNDS',
            'PARTIALLY_MATCHED',
            'CONFIRMED',
            'RECONCILE_MISMATCH',
            'EXPIRED',
            'FAILED'
        )
    ),
    received_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    confirmed_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    expires_at TIMESTAMPTZ NOT NULL,
    retry_count INT NOT NULL DEFAULT 0,
    last_error TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_bank_collection_intent_tenant
ON bank_collection_intent(tenant_id, state);

CREATE INDEX idx_bank_collection_intent_reference
ON bank_collection_intent(payment_reference);

CREATE INDEX idx_bank_collection_intent_provider
ON bank_collection_intent(provider_ref);

CREATE INDEX idx_bank_collection_intent_expires
ON bank_collection_intent(expires_at);

-- ============================================================================
-- Bank Collection Virtual Account Pool
-- ============================================================================

CREATE TABLE bank_collection_virtual_account_pool (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    channel_id UUID NOT NULL REFERENCES adapter_channel(id),
    account_number TEXT NOT NULL,
    routing_number TEXT NULL,
    iban TEXT NULL,
    swift TEXT NULL,
    bank_name TEXT NOT NULL,
    bank_address TEXT NULL,
    asset_code TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'AVAILABLE' CHECK (status IN ('AVAILABLE', 'ALLOCATED', 'DISABLED')),
    allocated_intent_id UUID NULL REFERENCES bank_collection_intent(id),
    allocated_at TIMESTAMPTZ NULL,
    released_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_virtual_account_pool_tenant
ON bank_collection_virtual_account_pool(tenant_id, status);

CREATE INDEX idx_virtual_account_pool_channel
ON bank_collection_virtual_account_pool(channel_id);

CREATE UNIQUE INDEX ux_virtual_account_pool_account
ON bank_collection_virtual_account_pool(account_number);

-- ============================================================================
-- Bank Collection Webhook Event Log
-- ============================================================================

CREATE TABLE bank_collection_webhook_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    channel_id UUID NOT NULL REFERENCES adapter_channel(id),
    event_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    provider_ref TEXT NULL,
    payment_reference TEXT NULL,
    account_number TEXT NULL,
    amount NUMERIC(36, 18) NULL,
    asset_code TEXT NULL,
    transaction_ref TEXT NULL,
    signature_valid BOOLEAN NOT NULL DEFAULT FALSE,
    processed BOOLEAN NOT NULL DEFAULT FALSE,
    processed_at TIMESTAMPTZ NULL,
    error_message TEXT NULL,
    raw_payload BYTEA NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_webhook_log_tenant
ON bank_collection_webhook_log(tenant_id, processed);

CREATE INDEX idx_webhook_log_event
ON bank_collection_webhook_log(event_id);

CREATE INDEX idx_webhook_log_reference
ON bank_collection_webhook_log(payment_reference);

-- ============================================================================
-- Bank Collection Reconciliation Log
-- ============================================================================

CREATE TABLE bank_collection_reconciliation_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    intent_id UUID NOT NULL REFERENCES bank_collection_intent(id),
    reconciliation_date DATE NOT NULL,
    matched BOOLEAN NOT NULL,
    mismatch_type TEXT NULL CHECK (mismatch_type IN ('AMOUNT_MISMATCH', 'CURRENCY_MISMATCH', 'REFERENCE_MISMATCH')),
    received_amount NUMERIC(36, 18) NULL,
    expected_amount NUMERIC(36, 18) NULL,
    received_reference TEXT NULL,
    expected_reference TEXT NULL,
    notes TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reconciliation_log_tenant
ON bank_collection_reconciliation_log(tenant_id, reconciliation_date);

CREATE INDEX idx_reconciliation_log_intent
ON bank_collection_reconciliation_log(intent_id);