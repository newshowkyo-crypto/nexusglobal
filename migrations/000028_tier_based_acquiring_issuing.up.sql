DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'provider_tier') THEN
        CREATE TYPE provider_tier AS ENUM (
            'TIER_1_HIGH_RISK',
            'TIER_2_COMPLIANT'
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'channel_strategy_mode') THEN
        CREATE TYPE channel_strategy_mode AS ENUM (
            'DEFAULT',
            'BURN_MODE'
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'issuing_tier') THEN
        CREATE TYPE issuing_tier AS ENUM (
            'TIER_1_AGGREGATOR',
            'TIER_2_DIRECT'
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'issuing_funding_mode') THEN
        CREATE TYPE issuing_funding_mode AS ENUM (
            'CRYPTO_DESENSITIZED',
            'FIAT_DIRECT'
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'virtual_card_status') THEN
        CREATE TYPE virtual_card_status AS ENUM (
            'PENDING',
            'ACTIVE',
            'FROZEN',
            'TERMINATED',
            'UNKNOWN_EXTERNAL_STATE',
            'MANUAL_REVIEW'
        );
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS acquirer_channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id UUID NOT NULL REFERENCES corporate_entity(id),
    provider_tier provider_tier NOT NULL,
    provider_code TEXT NOT NULL,
    channel_code TEXT NOT NULL,
    credential_vault_key TEXT NOT NULL,
    strategy_mode channel_strategy_mode NOT NULL DEFAULT 'DEFAULT',
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    version INT NOT NULL DEFAULT 1,
    provider_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    routing_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_acquirer_channel_code UNIQUE (entity_id, channel_code),
    CONSTRAINT chk_acquirer_provider_code_not_empty CHECK (length(provider_code) > 0),
    CONSTRAINT chk_acquirer_vault_key_not_empty CHECK (length(credential_vault_key) > 0),
    CONSTRAINT chk_acquirer_provider_config_object CHECK (jsonb_typeof(provider_config) = 'object'),
    CONSTRAINT chk_acquirer_routing_config_object CHECK (jsonb_typeof(routing_config) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_acquirer_channels_entity ON acquirer_channels(entity_id);
CREATE INDEX IF NOT EXISTS idx_acquirer_channels_tier ON acquirer_channels(provider_tier);
CREATE INDEX IF NOT EXISTS idx_acquirer_channels_status ON acquirer_channels(status);

CREATE TABLE IF NOT EXISTS issuing_providers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id UUID NULL REFERENCES corporate_entity(id),
    issuing_tier issuing_tier NOT NULL,
    funding_mode issuing_funding_mode NOT NULL,
    provider_code TEXT NOT NULL,
    pool_code TEXT NOT NULL,
    credential_vault_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    version INT NOT NULL DEFAULT 1,
    provider_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    velocity_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_issuing_pool_code UNIQUE (entity_id, pool_code),
    CONSTRAINT chk_issuing_provider_code_not_empty CHECK (length(provider_code) > 0),
    CONSTRAINT chk_issuing_vault_key_not_empty CHECK (length(credential_vault_key) > 0),
    CONSTRAINT chk_issuing_provider_config_object CHECK (jsonb_typeof(provider_config) = 'object'),
    CONSTRAINT chk_issuing_velocity_config_object CHECK (jsonb_typeof(velocity_config) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_issuing_providers_tier ON issuing_providers(issuing_tier);
CREATE INDEX IF NOT EXISTS idx_issuing_providers_funding_mode ON issuing_providers(funding_mode);
CREATE INDEX IF NOT EXISTS idx_issuing_providers_status ON issuing_providers(status);

CREATE TABLE IF NOT EXISTS virtual_cards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id UUID NOT NULL,
    provider_id UUID NOT NULL REFERENCES issuing_providers(id),
    provider_card_ref TEXT NULL,
    last4 TEXT NULL,
    status virtual_card_status NOT NULL DEFAULT 'PENDING',
    currency TEXT NOT NULL DEFAULT 'USD',
    spending_limit_minor BIGINT NOT NULL DEFAULT 0,
    declined_count_sliding_window INT NOT NULL DEFAULT 0,
    declined_window_started_at TIMESTAMPTZ NULL,
    fraud_rate_bps INT NOT NULL DEFAULT 0,
    last_decline_reason TEXT NULL,
    idempotency_key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    created_by TEXT NOT NULL,
    frozen_reason TEXT NULL,
    terminated_reason TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_virtual_card_idempotency UNIQUE (merchant_id, idempotency_key),
    CONSTRAINT chk_virtual_card_limit_nonnegative CHECK (spending_limit_minor >= 0),
    CONSTRAINT chk_virtual_card_declines_nonnegative CHECK (declined_count_sliding_window >= 0)
);

CREATE INDEX IF NOT EXISTS idx_virtual_cards_merchant ON virtual_cards(merchant_id);
CREATE INDEX IF NOT EXISTS idx_virtual_cards_provider ON virtual_cards(provider_id);
CREATE INDEX IF NOT EXISTS idx_virtual_cards_status ON virtual_cards(status);