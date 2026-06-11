-- 000001_init_mae.up.sql
-- MAE module tables

CREATE TABLE mae_configuration (
    id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mae_id                      varchar(64)  NOT NULL UNIQUE,
    display_name                varchar(128) NOT NULL,
    mae_type                    varchar(32)  NOT NULL,
    tenant_scope                jsonb        NOT NULL DEFAULT '[]',
    jurisdiction                varchar(8)   NOT NULL,
    supported_currencies        jsonb        NOT NULL DEFAULT '[]',
    api_base_url                text         NOT NULL,
    api_key_ciphertext          bytea        NOT NULL,
    webhook_secret_ciphertext   bytea        NOT NULL,
    webhook_proxy_url           varchar(512) NOT NULL DEFAULT '',
    risk_label                  varchar(128) NOT NULL DEFAULT '',
    settlement_bank_account_id  uuid         NOT NULL,
    daily_vmax_minor            bigint       NOT NULL CHECK (daily_vmax_minor >= 0),
    monthly_vmax_minor          bigint       NOT NULL CHECK (monthly_vmax_minor >= 0),
    cmax_bps                    integer      NOT NULL DEFAULT 0,
    card_testing_limit          integer      NOT NULL DEFAULT 0,
    routing_weight              integer      NOT NULL DEFAULT 0,
    cooldown_minutes            integer      NOT NULL DEFAULT 30,
    testing_traffic_bps         integer      NOT NULL DEFAULT 500,
    testing_min_txn_count       integer      NOT NULL DEFAULT 0,
    testing_min_success_rate_bps integer     NOT NULL DEFAULT 0,
    testing_max_p95_latency_ms  integer      NOT NULL DEFAULT 0,
    status                      varchar(32)  NOT NULL DEFAULT 'Draft',
    status_reason               varchar(128) NOT NULL DEFAULT '',
    business_day_lock_until     date,
    version                     integer      NOT NULL DEFAULT 0,
    created_at                  timestamptz  NOT NULL DEFAULT now(),
    updated_at                  timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX idx_mae_status ON mae_configuration(status);

CREATE TABLE mae_runtime_metric_snapshot (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mae_id           varchar(64) NOT NULL,
    business_date    date        NOT NULL,
    metric_key       varchar(64) NOT NULL,
    metric_value     bigint      NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (mae_id, business_date, metric_key)
);

CREATE INDEX idx_mae_snapshot_mae_date ON mae_runtime_metric_snapshot(mae_id, business_date);
