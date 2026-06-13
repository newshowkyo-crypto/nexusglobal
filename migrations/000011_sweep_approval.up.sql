-- 000011_sweep_approval.up.sql
CREATE TABLE sweep_approval (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    sweep_rule_id   uuid        NOT NULL,
    merchant_id     uuid        NOT NULL,
    amount_minor    bigint      NOT NULL,
    currency        varchar(16) NOT NULL,
    rail            varchar(16) NOT NULL,
    beneficiary_type varchar(32) NOT NULL,
    status          varchar(32) NOT NULL DEFAULT 'Pending',
    required_approvals integer   NOT NULL DEFAULT 2,
    current_approvals  integer   NOT NULL DEFAULT 0,
    idempotency_key varchar(128) NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_sweep_approval_status ON sweep_approval (status);
CREATE INDEX idx_sweep_approval_rule ON sweep_approval (sweep_rule_id);
