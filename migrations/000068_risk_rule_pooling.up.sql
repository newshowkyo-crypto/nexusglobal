-- 000068_risk_rule_pooling.up.sql
-- P6: 风控规则表化 + 维度扩展到 channel / site
--
-- 目标：
--   * 规则从硬编码默认值升级为可配置表，按作用域（全局/商户/站点/通道）覆盖。
--   * 审计日志补充 channel_id / site_id，支持按池子维度回溯。

CREATE TABLE IF NOT EXISTS risk_rule (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NULL,
    merchant_id      uuid        NULL,
    scope            varchar(16) NOT NULL DEFAULT 'GLOBAL'
                       CHECK (scope IN ('GLOBAL', 'MERCHANT', 'SITE', 'CHANNEL')),
    scope_id         varchar(64) NOT NULL DEFAULT '',
    dimension        varchar(32) NOT NULL
                       CHECK (dimension IN ('buyer_ip', 'card_bin_hash', 'app_id',
                                            'device_hash', 'channel_id', 'site_id',
                                            'merchant_id')),
    window_type      varchar(8)  NOT NULL DEFAULT '60s'
                       CHECK (window_type IN ('60s', '5m', '1h')),
    threshold        integer     NOT NULL DEFAULT 0 CHECK (threshold >= 0),
    failure_rate     numeric(5,2) NOT NULL DEFAULT 0 CHECK (failure_rate >= 0),
    min_sample       integer     NOT NULL DEFAULT 0 CHECK (min_sample >= 0),
    priority         integer     NOT NULL DEFAULT 100,
    status           varchar(16) NOT NULL DEFAULT 'ACTIVE'
                       CHECK (status IN ('ACTIVE', 'DISABLED')),
    version          integer     NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scope, scope_id, dimension, window_type)
);

CREATE INDEX IF NOT EXISTS idx_risk_rule_lookup
    ON risk_rule(scope, scope_id, status, priority DESC);

ALTER TABLE risk_event
    ADD COLUMN IF NOT EXISTS channel_id uuid NULL;

ALTER TABLE risk_event
    ADD COLUMN IF NOT EXISTS site_id varchar(64) NULL;

CREATE INDEX IF NOT EXISTS idx_risk_event_channel
    ON risk_event(channel_id) WHERE channel_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_risk_event_site
    ON risk_event(site_id) WHERE site_id IS NOT NULL;

-- 默认全局规则（与 domain.DefaultRuleConfig 对齐）
INSERT INTO risk_rule (scope, scope_id, dimension, window_type, threshold) VALUES
    ('GLOBAL', '', 'buyer_ip', '60s', 5),
    ('GLOBAL', '', 'card_bin_hash', '5m', 3)
ON CONFLICT (scope, scope_id, dimension, window_type) DO NOTHING;

INSERT INTO risk_rule (scope, scope_id, dimension, window_type, threshold, failure_rate, min_sample) VALUES
    ('GLOBAL', '', 'app_id', '1h', 0, 30.00, 10)
ON CONFLICT (scope, scope_id, dimension, window_type) DO NOTHING;
