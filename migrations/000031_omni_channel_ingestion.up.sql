CREATE TABLE checkout_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id VARCHAR(64) UNIQUE NOT NULL,
    merchant_id UUID NOT NULL,
    order_id UUID NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency VARCHAR(3) NOT NULL,
    success_url TEXT NOT NULL,
    cancel_url TEXT NOT NULL,
    checkout_url TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'ACTIVE',
    expires_at TIMESTAMPTZ NOT NULL,
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_checkout_sessions_merchant_id ON checkout_sessions(merchant_id);
CREATE INDEX idx_checkout_sessions_session_id ON checkout_sessions(session_id);

CREATE TABLE payment_intents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id UUID NOT NULL,
    merchant_order_id VARCHAR(128) NOT NULL,
    idempotency_key VARCHAR(256) NOT NULL,
    payload_hash VARCHAR(64) NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency VARCHAR(3) NOT NULL,
    payment_method_token VARCHAR(256) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    customer JSONB,
    metadata JSONB,
    ledger_reservation_id VARCHAR(128),
    transaction_id VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_payment_intents_idempotency ON payment_intents(merchant_id, idempotency_key);
CREATE INDEX idx_payment_intents_merchant_id ON payment_intents(merchant_id);
CREATE INDEX idx_payment_intents_status ON payment_intents(status);

CREATE TABLE payment_links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash VARCHAR(64) UNIQUE NOT NULL,
    merchant_id UUID NOT NULL,
    merchant_order_id VARCHAR(128) NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency VARCHAR(3) NOT NULL,
    success_url TEXT NOT NULL,
    cancel_url TEXT NOT NULL,
    payment_url TEXT NOT NULL,
    single_use BOOLEAN NOT NULL DEFAULT TRUE,
    expires_at TIMESTAMPTZ,
    status VARCHAR(32) NOT NULL DEFAULT 'ACTIVE',
    customer JSONB,
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_links_token_hash ON payment_links(token_hash);
CREATE INDEX idx_payment_links_merchant_id ON payment_links(merchant_id);