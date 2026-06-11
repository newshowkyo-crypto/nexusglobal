-- 000004_init_ledger.up.sql
-- Ledger: dual-entry accounting tables

CREATE TABLE ledger_account (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id     uuid         NOT NULL,
    account_type    varchar(64)  NOT NULL,
    currency        varchar(16)  NOT NULL,
    description     varchar(256) NOT NULL DEFAULT '',
    created_at      timestamptz  NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, account_type, currency)
);

CREATE INDEX idx_ledger_account_merchant ON ledger_account(merchant_id);

CREATE TABLE ledger_transaction (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key  varchar(128) NOT NULL UNIQUE,
    transaction_type varchar(64)  NOT NULL,
    aggregate_type   varchar(64)  NOT NULL DEFAULT '',
    aggregate_id     uuid,
    description      varchar(256) NOT NULL DEFAULT '',
    created_at       timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX idx_ledger_txn_aggregate ON ledger_transaction(aggregate_type, aggregate_id);
CREATE INDEX idx_ledger_txn_created ON ledger_transaction(created_at);

CREATE TABLE ledger_entry (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id     uuid           NOT NULL REFERENCES ledger_transaction(id),
    account_id         uuid           NOT NULL REFERENCES ledger_account(id),
    direction          varchar(8)     NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount             numeric(20,4)  NOT NULL CHECK (amount > 0),
    created_at         timestamptz    NOT NULL DEFAULT now()
);

CREATE INDEX idx_ledger_entry_txn ON ledger_entry(transaction_id);
CREATE INDEX idx_ledger_entry_account ON ledger_entry(account_id);
