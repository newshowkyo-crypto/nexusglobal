-- 000037_omni_channel_unified_lifecycle.up.sql
-- G09: Payment Link token 安全 — payment_url 不再存明文 token，改为非敏感 path
-- G10: 三入口统一生命周期 — payment_attempts 表 + checkout_sessions/payment_intents 增强
-- G12: 并发幂等 — idempotency_records 表（tenant+merchant+operation+key UNIQUE）
-- Webhook 统一 Receiver — unified_webhook_events 表
--
-- 设计原则：
--   * token_hash 已存在（000031），本迁移补强 claim/rotate/revoke 生命周期列
--   * payment_attempts 是三入口（Hosted/S2S/PaymentLink）的统一 attempt 概念
--   * idempotency_records 用 INSERT ... ON CONFLICT 实现并发认领（G12）
--   * unified_webhook_events 用 idempotency_key UNIQUE 实现去重（RED-10）
--   * 金额一律 int64 amount_minor（NF-03），状态有 CHECK 约束
--   * 敏感字段加密 + json:"-"（RED-15）

-- ============================================================================
-- 1. payment_links 补强 — G09 Payment Link token 安全
--    token_hash 已存在（000031），本迁移添加 claim/rotate/revoke 生命周期列
-- ============================================================================

ALTER TABLE payment_links
    ADD COLUMN IF NOT EXISTS claimed_at         timestamptz,
    ADD COLUMN IF NOT EXISTS claimed_by_attempt_id uuid,
    ADD COLUMN IF NOT EXISTS rotated_from_id    uuid,
    ADD COLUMN IF NOT EXISTS webhook_secret_hash varchar(64),
    ADD COLUMN IF NOT EXISTS revoked_at         timestamptz,
    ADD COLUMN IF NOT EXISTS revoked_reason     text DEFAULT '';

-- 补强 status CHECK 约束（支持 CLAIMED/REVOKED 状态）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'payment_links' AND constraint_name = 'chk_payment_links_status'
    ) THEN
        ALTER TABLE payment_links ADD CONSTRAINT chk_payment_links_status
            CHECK (status IN ('ACTIVE', 'CLAIMED', 'PAID', 'EXPIRED', 'REVOKED'));
    END IF;
END
$$;

-- 索引：支持恒时 token 查找 + single-use 原子 claim
CREATE INDEX IF NOT EXISTS idx_payment_links_token_hash_active
    ON payment_links(token_hash) WHERE status = 'ACTIVE';
CREATE INDEX IF NOT EXISTS idx_payment_links_rotated_from ON payment_links(rotated_from_id);

-- G09: 吊销所有历史明文 token 链接
--   旧代码将 payment_url 设为 '/pay/{明文token}'（64 hex chars after /pay/）
--   新代码将 payment_url 设为 '/pay/{paymentLinkID}'（UUID format）
--   识别特征：/pay/ 后跟 64 个 hex 字符（32 bytes token）即为明文 token 链接
--   这些链接必须吊销，因为 token 已暴露在 DB 中
UPDATE payment_links
SET status = 'REVOKED',
    revoked_at = now(),
    revoked_reason = 'G09: plaintext token in payment_url revoked during migration 000037',
    updated_at = now()
WHERE status = 'ACTIVE'
  AND payment_url ~ '^/pay/[0-9a-f]{64}$';

-- ============================================================================
-- 2. payment_attempts — G10 三入口统一生命周期
--    Hosted Checkout / S2S Payment Intent / Payment Link 都生成 payment_attempt_id
--    共享 merchant、tenant、amount、currency、merchant_order_id、metadata 和状态机
-- ============================================================================

CREATE TABLE payment_attempts (
    id                      uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id                uuid          NOT NULL,
    merchant_id             uuid          NOT NULL,
    tenant_id               uuid          NOT NULL,
    -- 入口渠道：HOSTED（Hosted Checkout）/ S2S（Server-to-Server）/ PAYMENT_LINK
    entry_channel           varchar(32)   NOT NULL
                              CHECK (entry_channel IN ('HOSTED', 'S2S', 'PAYMENT_LINK')),
    -- 入口引用：session_id / idempotency_key / payment_link_id
    entry_ref               varchar(256)  NOT NULL,
    merchant_order_id       varchar(128)  NOT NULL,
    amount_minor            bigint        NOT NULL CHECK (amount_minor > 0),
    currency                varchar(3)    NOT NULL CHECK (currency = upper(currency) AND length(currency) = 3),
    -- 统一状态机（G10）：PENDING 不是完成态
    status                  varchar(32)   NOT NULL DEFAULT 'REQUIRES_ACTION'
                              CHECK (status IN ('REQUIRES_ACTION', 'PROCESSING', 'AUTHORIZED',
                                                'CAPTURED', 'SETTLED', 'FAILED', 'CANCELED',
                                                'UNKNOWN_EXTERNAL_STATE', 'PARTIALLY_CAPTURED',
                                                'REFUNDED', 'PARTIALLY_REFUNDED', 'CHARGED_BACK')),
    -- provider 集成
    provider_idempotency_key varchar(256),
    provider_ref            varchar(128),
    provider_transaction_id varchar(128),
    -- 3DS / redirect
    requires_action_url     text,
    requires_action_type    varchar(32),
    -- 失败信息
    failure_code            varchar(64),
    failure_reason          text,
    -- 关联
    payment_method_token    varchar(256),
    ledger_reservation_id   varchar(128),
    customer                jsonb,
    metadata                jsonb,
    version                 integer       NOT NULL DEFAULT 0,
    created_at              timestamptz   NOT NULL DEFAULT now(),
    updated_at              timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX idx_payment_attempts_order       ON payment_attempts(order_id);
CREATE INDEX idx_payment_attempts_merchant    ON payment_attempts(merchant_id, status);
CREATE INDEX idx_payment_attempts_entry       ON payment_attempts(entry_channel, entry_ref);
CREATE INDEX idx_payment_attempts_provider    ON payment_attempts(provider_idempotency_key) WHERE provider_idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX idx_payment_attempts_order_channel
    ON payment_attempts(order_id, entry_channel) WHERE status IN ('REQUIRES_ACTION', 'PROCESSING', 'AUTHORIZED');

-- ============================================================================
-- 3. idempotency_records — G12 并发幂等
--    scope = tenant + merchant + operation + idempotency_key（UNIQUE）
--    并发认领使用 INSERT ... ON CONFLICT
--    同 payload_hash 返回首次稳定响应，不同 payload_hash 返回 409
-- ============================================================================

CREATE TABLE idempotency_records (
    id                      uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               uuid          NOT NULL,
    merchant_id             uuid          NOT NULL,
    operation               varchar(64)   NOT NULL,
    idempotency_key         varchar(256)  NOT NULL,
    payload_hash            varchar(64)   NOT NULL,
    -- 首次响应快照（加密存储，RED-15）
    response_status         integer,
    response_body_encrypted bytea,
    response_headers        jsonb,
    -- 关联 attempt
    payment_attempt_id      uuid,
    -- 状态：PENDING（认领中）/ COMMITTED（已完成）/ FAILED
    state                   varchar(32)   NOT NULL DEFAULT 'PENDING'
                              CHECK (state IN ('PENDING', 'COMMITTED', 'FAILED')),
    created_at              timestamptz   NOT NULL DEFAULT now(),
    committed_at            timestamptz,
    expires_at              timestamptz   NOT NULL DEFAULT (now() + interval '24 hours')
);

-- 并发幂等核心约束：同一 tenant+merchant+operation+key 只能有一行
CREATE UNIQUE INDEX idx_idempotency_records_scope
    ON idempotency_records(tenant_id, merchant_id, operation, idempotency_key);

CREATE INDEX idx_idempotency_records_expires ON idempotency_records(expires_at);
CREATE INDEX idx_idempotency_records_state   ON idempotency_records(state, created_at);

-- ============================================================================
-- 4. unified_webhook_events — Webhook 统一 Receiver
--    验证签名、保存加密原始事件、入队/stream
--    重复事件不得重复分录（RED-10）
--    timeout 进入 UNKNOWN，不盲目重试（RED-03）
-- ============================================================================

CREATE TABLE unified_webhook_events (
    id                      uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_type           varchar(32)   NOT NULL,
    provider_id             varchar(128)  NOT NULL,
    provider_event_id       varchar(256),
    event_type              varchar(128)  NOT NULL DEFAULT 'webhook',
    -- 签名验证
    signature_hash         varchar(64)   NOT NULL,
    -- 加密原始 payload（RED-15）
    payload_encrypted      bytea         NOT NULL,
    payload_hash           varchar(64)   NOT NULL,
    -- 幂等键：provider_type + provider_id + provider_event_id + payload_hash
    idempotency_key        varchar(256)  NOT NULL UNIQUE,
    -- 处理状态
    status                 varchar(32)   NOT NULL DEFAULT 'RECEIVED'
                              CHECK (status IN ('RECEIVED', 'PROCESSING', 'PROCESSED',
                                                'FAILED', 'UNKNOWN', 'DUPLICATED')),
    -- 关联 attempt / ledger
    payment_attempt_id     uuid,
    ledger_transaction_id  uuid,
    -- 错误信息
    error_message          text,
    -- 乱序处理：provider 提供的 event_sequence
    provider_sequence      bigint,
    created_at             timestamptz   NOT NULL DEFAULT now(),
    processed_at           timestamptz
);

CREATE INDEX idx_unified_webhook_status    ON unified_webhook_events(status, created_at);
CREATE INDEX idx_unified_webhook_attempt   ON unified_webhook_events(payment_attempt_id) WHERE payment_attempt_id IS NOT NULL;
CREATE INDEX idx_unified_webhook_provider  ON unified_webhook_events(provider_type, provider_id, created_at DESC);
CREATE INDEX idx_unified_webhook_sequence  ON unified_webhook_events(provider_type, provider_id, provider_sequence) WHERE provider_sequence IS NOT NULL;

-- ============================================================================
-- 5. checkout_sessions / payment_intents 增强 — G10 三入口统一
--    添加 payment_attempt_id 和 tenant_id 列
-- ============================================================================

ALTER TABLE checkout_sessions
    ADD COLUMN IF NOT EXISTS payment_attempt_id uuid,
    ADD COLUMN IF NOT EXISTS tenant_id         uuid;

ALTER TABLE payment_intents
    ADD COLUMN IF NOT EXISTS payment_attempt_id   uuid,
    ADD COLUMN IF NOT EXISTS tenant_id             uuid,
    ADD COLUMN IF NOT EXISTS provider_idempotency_key varchar(256);

-- payment_intents 状态 CHECK 约束
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'payment_intents' AND constraint_name = 'chk_payment_intents_status'
    ) THEN
        ALTER TABLE payment_intents ADD CONSTRAINT chk_payment_intents_status
            CHECK (status IN ('PENDING', 'REQUIRES_ACTION', 'PROCESSING', 'AUTHORIZED',
                              'CAPTURED', 'SETTLED', 'FAILED', 'CANCELED',
                              'UNKNOWN_EXTERNAL_STATE', 'PARTIALLY_CAPTURED',
                              'REFUNDED', 'PARTIALLY_REFUNDED', 'CHARGED_BACK'));
    END IF;
END
$$;

-- ============================================================================
-- 6. payment_link_access_log — G09 token 不在日志中
--    记录 payment link 访问，但只记录 token_hash 和非敏感信息
-- ============================================================================

CREATE TABLE payment_link_access_log (
    id              uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_link_id uuid          NOT NULL,
    token_hash      varchar(64)   NOT NULL,
    action          varchar(32)   NOT NULL CHECK (action IN ('VIEW', 'CLAIM', 'PAY', 'REVOKE', 'EXPIRE')),
    success         boolean       NOT NULL,
    -- 不记录 token、Referer 中的 token、query string 中的 token
    client_ip       varchar(64),
    user_agent      varchar(256),
    error_code      varchar(64),
    created_at      timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX idx_payment_link_access_log_link ON payment_link_access_log(payment_link_id, created_at DESC);
CREATE INDEX idx_payment_link_access_log_hash ON payment_link_access_log(token_hash, created_at DESC);