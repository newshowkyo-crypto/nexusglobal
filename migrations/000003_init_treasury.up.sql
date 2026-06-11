-- 000003_init_treasury.up.sql
-- Treasury / Sweep module tables

CREATE TABLE banking_calendar (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    calendar_name           varchar(64)  NOT NULL UNIQUE,
    jurisdiction            varchar(8)   NOT NULL,
    timezone                varchar(64)  NOT NULL,
    business_days           jsonb        NOT NULL DEFAULT '[]',
    holidays                jsonb        NOT NULL DEFAULT '[]',
    emergency_closure_dates jsonb        NOT NULL DEFAULT '[]',
    cut_off_matrix          jsonb        NOT NULL DEFAULT '{}',
    status                  varchar(32)  NOT NULL DEFAULT 'Active',
    created_at              timestamptz  NOT NULL DEFAULT now(),
    updated_at              timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE sweep_rule (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_bank_account_id  uuid           NOT NULL,
    target_bank_account_id  uuid           NOT NULL,
    currency                varchar(16)    NOT NULL,
    rail                    varchar(16)    NOT NULL,
    beneficiary_type        varchar(32)    NOT NULL,
    same_name_verified      boolean        NOT NULL DEFAULT false,
    trigger_threshold_minor bigint         NOT NULL CHECK (trigger_threshold_minor >= 0),
    sweep_ratio_bps         integer        NOT NULL DEFAULT 10000,
    max_single_sweep_minor  bigint         NOT NULL CHECK (max_single_sweep_minor >= 0),
    min_residual_minor      bigint         NOT NULL DEFAULT 0,
    time_window_matrix      jsonb          NOT NULL DEFAULT '{}',
    scheduled_sweep_times   jsonb          NOT NULL DEFAULT '[]',
    sweep_mode              varchar(32)    NOT NULL DEFAULT 'TIERED_SCHEDULED',
    max_daily_sweep_count   integer        NOT NULL DEFAULT 2,
    delay_queue_enabled     boolean        NOT NULL DEFAULT true,
    require_four_eyes       boolean        NOT NULL DEFAULT false,
    approval_threshold_minor bigint        NOT NULL DEFAULT 0,
    status                  varchar(32)    NOT NULL DEFAULT 'Active',
    version                 integer        NOT NULL DEFAULT 0,
    created_at              timestamptz    NOT NULL DEFAULT now(),
    updated_at              timestamptz    NOT NULL DEFAULT now()
);

CREATE TABLE sweep_transfer (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sweep_rule_id    uuid           NOT NULL REFERENCES sweep_rule(id),
    source_account_id uuid          NOT NULL,
    target_account_id uuid          NOT NULL,
    amount_minor     bigint         NOT NULL CHECK (amount_minor > 0),
    currency         varchar(16)    NOT NULL,
    rail             varchar(16)    NOT NULL,
    status           varchar(32)    NOT NULL DEFAULT 'Locked',
    idempotency_key  varchar(128)   NOT NULL UNIQUE,
    bank_ref         varchar(128)   NOT NULL DEFAULT '',
    version          integer        NOT NULL DEFAULT 0,
    created_at       timestamptz    NOT NULL DEFAULT now(),
    updated_at       timestamptz    NOT NULL DEFAULT now()
);

CREATE INDEX idx_sweep_transfer_status ON sweep_transfer(status);
CREATE INDEX idx_sweep_transfer_rule ON sweep_transfer(sweep_rule_id);
