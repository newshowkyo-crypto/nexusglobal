CREATE TABLE bank_account_balance (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    bank_account_id     uuid        NOT NULL,
    currency            varchar(16) NOT NULL,
    accumulated_minor   bigint      NOT NULL DEFAULT 0,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (bank_account_id, currency)
);

CREATE INDEX idx_bank_account_balance_account ON bank_account_balance (bank_account_id);
