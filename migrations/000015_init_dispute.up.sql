CREATE TABLE payment_dispute (
    id UUID PRIMARY KEY,
    order_id VARCHAR(255) NOT NULL,
    merchant_id UUID NOT NULL,
    dispute_type VARCHAR(50) NOT NULL CHECK (dispute_type IN ('REFUND', 'CHARGEBACK')),
    status VARCHAR(50) NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'UNDER_REVIEW', 'WON', 'LOST', 'REVERSED')),
    amount NUMERIC(20, 8) NOT NULL CHECK (amount > 0),
    currency VARCHAR(10) NOT NULL,
    reason TEXT,
    provider_dispute_id VARCHAR(255),
    chargeback_fee NUMERIC(20, 8) NOT NULL DEFAULT 0 CHECK (chargeback_fee >= 0),
    idempotency_key VARCHAR(255) UNIQUE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_dispute_order_id ON payment_dispute(order_id);
CREATE INDEX idx_payment_dispute_merchant_id ON payment_dispute(merchant_id);
CREATE INDEX idx_payment_dispute_status ON payment_dispute(status);
CREATE INDEX idx_payment_dispute_idempotency ON payment_dispute(idempotency_key);

CREATE TABLE platform_dispute_reserve_ledger (
    id UUID PRIMARY KEY,
    payment_dispute_id UUID NOT NULL REFERENCES payment_dispute(id),
    merchant_id UUID NOT NULL,
    amount NUMERIC(20, 8) NOT NULL CHECK (amount > 0),
    currency VARCHAR(10) NOT NULL,
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('CREDIT', 'DEBIT')),
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_platform_dispute_reserve_merchant_id ON platform_dispute_reserve_ledger(merchant_id);
CREATE INDEX idx_platform_dispute_reserve_dispute_id ON platform_dispute_reserve_ledger(payment_dispute_id);

CREATE TABLE merchant_negative_balance (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL,
    currency VARCHAR(10) NOT NULL,
    amount NUMERIC(20, 8) NOT NULL DEFAULT 0,
    dispute_id UUID REFERENCES payment_dispute(id),
    reason TEXT,
    status VARCHAR(50) NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'RECOVERED', 'WRITTEN_OFF')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(merchant_id, currency, dispute_id)
);

CREATE INDEX idx_merchant_negative_balance_merchant ON merchant_negative_balance(merchant_id, currency);
CREATE INDEX idx_merchant_negative_balance_status ON merchant_negative_balance(status);
