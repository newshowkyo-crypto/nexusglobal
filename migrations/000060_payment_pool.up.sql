-- 000060_payment_pool.up.sql
-- P1 支付池：多通道容量调度（A1）
--
-- 设计要点：
--   * pool_channel 依附于既有 tenant_acquirer_channel，不新建第二套通道注册表。
--   * 每个通道登记 entity / MCC / 已获批业务线，路由必须命中业务线白名单（fail-closed）。
--   * pool_policy 把站点/业务线绑定到允许的通道集合，禁止任意交叉。
--   * pool_watermark 记录日/月用量；Redis 为快路径，DB 为持久真相。
--   * pool_lease 记录按笔预占，支持后续失败切换（rank）。

CREATE TABLE IF NOT EXISTS pool_channel (
    id                  uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid          NOT NULL,
    merchant_id         uuid          NOT NULL,
    acquirer_channel_id uuid          NOT NULL REFERENCES tenant_acquirer_channel(id) ON DELETE CASCADE,
    entity_id           uuid          NULL,
    psp_code            varchar(32)   NOT NULL DEFAULT '',
    external_mid        varchar(128)  NOT NULL DEFAULT '',
    mcc                 varchar(4)    NOT NULL DEFAULT '',
    business_lines      text[]        NOT NULL DEFAULT '{}',
    currencies          text[]        NOT NULL DEFAULT '{}',
    card_brands         text[]        NOT NULL DEFAULT '{}',
    min_amount_minor    bigint        NOT NULL DEFAULT 0 CHECK (min_amount_minor >= 0),
    max_amount_minor    bigint        NOT NULL DEFAULT 0 CHECK (max_amount_minor >= 0),
    daily_limit_minor   bigint        NOT NULL DEFAULT 0 CHECK (daily_limit_minor >= 0),
    monthly_limit_minor bigint        NOT NULL DEFAULT 0 CHECK (monthly_limit_minor >= 0),
    cost_bps            integer       NOT NULL DEFAULT 0 CHECK (cost_bps >= 0),
    settlement_days     integer       NOT NULL DEFAULT 0 CHECK (settlement_days >= 0),
    weight              integer       NOT NULL DEFAULT 100 CHECK (weight > 0),
    health_score        integer       NOT NULL DEFAULT 10000 CHECK (health_score BETWEEN 0 AND 10000),
    status              varchar(16)   NOT NULL DEFAULT 'ACTIVE'
                          CHECK (status IN ('ACTIVE', 'COOLDOWN', 'TRIPPED', 'DISABLED')),
    created_at          timestamptz   NOT NULL DEFAULT now(),
    updated_at          timestamptz   NOT NULL DEFAULT now(),
    UNIQUE (acquirer_channel_id)
);

CREATE INDEX IF NOT EXISTS idx_pool_channel_merchant ON pool_channel(merchant_id, status);
CREATE INDEX IF NOT EXISTS idx_pool_channel_entity   ON pool_channel(entity_id);

CREATE TABLE IF NOT EXISTS pool_policy (
    id                  uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid          NOT NULL,
    merchant_id         uuid          NOT NULL,
    site_id             varchar(64)   NOT NULL DEFAULT '',
    business_line       varchar(64)   NOT NULL DEFAULT '',
    allowed_channel_ids uuid[]        NOT NULL DEFAULT '{}',
    strategy            varchar(32)   NOT NULL DEFAULT 'WEIGHTED'
                          CHECK (strategy IN ('WEIGHTED', 'COST_FIRST', 'SUCCESS_FIRST')),
    weights             jsonb         NOT NULL DEFAULT '{}'::jsonb,
    status              varchar(16)   NOT NULL DEFAULT 'ACTIVE'
                          CHECK (status IN ('ACTIVE', 'DISABLED')),
    version             integer       NOT NULL DEFAULT 0,
    created_at          timestamptz   NOT NULL DEFAULT now(),
    updated_at          timestamptz   NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, site_id, business_line)
);

CREATE INDEX IF NOT EXISTS idx_pool_policy_merchant ON pool_policy(merchant_id, status);

CREATE TABLE IF NOT EXISTS pool_watermark (
    channel_id           uuid        NOT NULL REFERENCES pool_channel(id) ON DELETE CASCADE,
    biz_date             date        NOT NULL,
    used_amount_minor    bigint      NOT NULL DEFAULT 0 CHECK (used_amount_minor >= 0),
    reserved_amount_minor bigint     NOT NULL DEFAULT 0 CHECK (reserved_amount_minor >= 0),
    used_count           bigint      NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, biz_date)
);

CREATE TABLE IF NOT EXISTS pool_lease (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id   uuid        NOT NULL REFERENCES pool_channel(id) ON DELETE CASCADE,
    attempt_id   uuid        NOT NULL,
    amount_minor bigint      NOT NULL CHECK (amount_minor > 0),
    rank         integer     NOT NULL DEFAULT 1 CHECK (rank > 0),
    state        varchar(16) NOT NULL DEFAULT 'HELD'
                   CHECK (state IN ('STANDBY', 'HELD', 'CONFIRMED', 'RELEASED', 'EXPIRED')),
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (attempt_id, channel_id)
);

CREATE INDEX IF NOT EXISTS idx_pool_lease_attempt ON pool_lease(attempt_id, state);
CREATE INDEX IF NOT EXISTS idx_pool_lease_expiry  ON pool_lease(state, expires_at);

-- 站点维度与选路结果需要落到订单/尝试，便于事后追溯"为什么走这个通道"
ALTER TABLE checkout_orders ADD COLUMN IF NOT EXISTS site_id varchar(64) NOT NULL DEFAULT '';
ALTER TABLE payment_attempts ADD COLUMN IF NOT EXISTS pool_channel_id uuid NULL;
