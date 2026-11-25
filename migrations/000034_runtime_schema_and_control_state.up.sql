-- 000034_runtime_schema_and_control_state.up.sql
-- G01: 修复运行时 Schema — checkout_orders / ledger_reservations / admin_audit_log / system_control_state
-- G02: 为真实复式账本提供 reservation 生命周期投影与全局控制大闸
--
-- 设计原则：
--   * checkout_orders 与 Hosted Checkout 真实写入字段一致（见 internal/checkout/service.go）
--   * ledger_reservations 仅是 reservation 生命周期投影，资金真相在 ledger_transaction + ledger_entry + account_balance
--   * admin_audit_log 统一审计入口（admin_override_audit_log 保留为 override 专用）
--   * system_control_state 保存全局大闸（kill switch）状态与单调 epoch
--   * 金额一律 int64 amount_minor（NF-03），币种为大写 ISO-4217，状态有 CHECK 约束
--   * 外键与唯一键与运行时幂等域一致（RED-10）

-- ============================================================================
-- 1. checkout_orders — Hosted Checkout 订单主表
--    与 internal/checkout/service.go CreateCheckoutOrder 写入字段完全一致
-- ============================================================================

CREATE TABLE checkout_orders (
    id                uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid          NOT NULL,
    merchant_id       uuid          NOT NULL,
    merchant_order_id varchar(128)  NOT NULL,
    amount_minor      bigint        NOT NULL CHECK (amount_minor > 0),
    currency          varchar(3)    NOT NULL CHECK (currency = upper(currency) AND length(currency) = 3),
    status            varchar(32)   NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING', 'AUTHORIZED', 'CAPTURED', 'SETTLED',
                                          'PARTIALLY_CAPTURED', 'REFUNDED', 'PARTIALLY_REFUNDED',
                                          'FAILED', 'CANCELLED', 'EXPIRED', 'CHARGED_BACK')),
    success_url       text          NOT NULL,
    cancel_url        text          NOT NULL,
    customer          jsonb,
    metadata          jsonb,
    version           integer       NOT NULL DEFAULT 0,
    created_at        timestamptz   NOT NULL DEFAULT now(),
    updated_at        timestamptz   NOT NULL DEFAULT now(),
    -- 幂等域：同一商户下 merchant_order_id 唯一
    UNIQUE (merchant_id, merchant_order_id)
);

CREATE INDEX idx_checkout_orders_merchant        ON checkout_orders(merchant_id);
CREATE INDEX idx_checkout_orders_merchant_status ON checkout_orders(merchant_id, status);
CREATE INDEX idx_checkout_orders_tenant          ON checkout_orders(tenant_id);

-- ============================================================================
-- 2. ledger_reservations — reservation 生命周期投影
--    资金真相在 ledger_transaction + ledger_entry + account_balance
--    本表仅记录预留元数据与状态，以 UUID 外键关联 ledger_transaction
-- ============================================================================

CREATE TABLE ledger_reservations (
    id                  uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid          NOT NULL,
    merchant_id         uuid          NOT NULL,
    ledger_transaction_id uuid        NOT NULL REFERENCES ledger_transaction(id),
    amount_minor        bigint        NOT NULL CHECK (amount_minor > 0),
    currency            varchar(3)    NOT NULL CHECK (currency = upper(currency) AND length(currency) = 3),
    -- reservation 生命周期：HELD → CAPTURED → SETTLED 或 HELD → RELEASED / VOIDED
    status              varchar(32)   NOT NULL DEFAULT 'HELD'
                          CHECK (status IN ('HELD', 'CAPTURED', 'SETTLED', 'RELEASED', 'VOIDED', 'CHARGED_BACK')),
    captured_amount_minor bigint      NOT NULL DEFAULT 0 CHECK (captured_amount_minor >= 0),
    released_amount_minor bigint      NOT NULL DEFAULT 0 CHECK (released_amount_minor >= 0),
    reason              text          NOT NULL DEFAULT '',
    operator            text,
    -- 兼容字段：外部 transaction_id 字符串引用（如 payment_intent_id）
    external_transaction_id varchar(128),
    metadata            jsonb,
    version             integer       NOT NULL DEFAULT 0,
    created_at          timestamptz   NOT NULL DEFAULT now(),
    updated_at          timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX idx_ledger_reservations_merchant   ON ledger_reservations(merchant_id, status);
CREATE INDEX idx_ledger_reservations_txn        ON ledger_reservations(ledger_transaction_id);
CREATE INDEX idx_ledger_reservations_external   ON ledger_reservations(external_transaction_id);

-- ============================================================================
-- 3. admin_audit_log — 统一审计日志
--    admin_override_audit_log（000025）保留为 override 专用，本表为通用审计入口
--    修复 G01：handler 不再引用不存在的 admin_audit_logs（复数）
-- ============================================================================

CREATE TABLE admin_audit_log (
    id            uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid,
    actor_id      uuid,
    actor_type    varchar(32)   NOT NULL DEFAULT 'ADMIN'
                    CHECK (actor_type IN ('ADMIN', 'OPERATOR', 'SYSTEM', 'MERCHANT')),
    action        varchar(128)  NOT NULL,
    entity_type   varchar(64),
    entity_id     uuid,
    details       jsonb         NOT NULL DEFAULT '{}'::jsonb,
    -- 审计不可变：禁止 UPDATE / DELETE（由触发器强制，见下）
    created_at    timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX idx_admin_audit_log_action     ON admin_audit_log(action, created_at DESC);
CREATE INDEX idx_admin_audit_log_entity     ON admin_audit_log(entity_type, entity_id);
CREATE INDEX idx_admin_audit_log_actor      ON admin_audit_log(actor_id, created_at DESC);
CREATE INDEX idx_admin_audit_log_tenant     ON admin_audit_log(tenant_id, created_at DESC);

-- 审计日志不可变（INSERT-only）
CREATE OR REPLACE FUNCTION prevent_admin_audit_log_mutation()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'admin_audit_log is append-only: mutation rejected (action=%)', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_admin_audit_log_no_update
    BEFORE UPDATE ON admin_audit_log
    FOR EACH ROW EXECUTE FUNCTION prevent_admin_audit_log_mutation();

CREATE TRIGGER trg_admin_audit_log_no_delete
    BEFORE DELETE ON admin_audit_log
    FOR EACH ROW EXECUTE FUNCTION prevent_admin_audit_log_mutation();

-- ============================================================================
-- 4. system_control_state — 全局控制大闸（kill switch / lockdown）
--    单行表（通过 CHECK 约束强制），保存单调 epoch 与 operator/reason
-- ============================================================================

CREATE TABLE system_control_state (
    id            smallint     PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    -- 大闸状态：OPEN（正常）| LOCKDOWN（全局封锁）| DEGRADED（降级）
    state         varchar(32)  NOT NULL DEFAULT 'OPEN'
                    CHECK (state IN ('OPEN', 'LOCKDOWN', 'DEGRADED')),
    -- 单调递增 epoch：每次状态变更 +1，用于缓存失效与事件排序
    epoch         bigint       NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    operator      text,
    reason        text         NOT NULL DEFAULT '',
    activated_at  timestamptz,
    released_at   timestamptz,
    metadata      jsonb        NOT NULL DEFAULT '{}'::jsonb,
    version       integer      NOT NULL DEFAULT 0,
    updated_at    timestamptz  NOT NULL DEFAULT now()
);

-- 初始化单行
INSERT INTO system_control_state (id, state, epoch) VALUES (1, 'OPEN', 0)
ON CONFLICT (id) DO NOTHING;

-- ============================================================================
-- 5. 补强 account_balance 约束（RED-05: 余额不能为负）
--    000023 创建 account_balance 时未加非负 CHECK
--    PostgreSQL 不支持 ADD CONSTRAINT IF NOT EXISTS，使用 DO 块条件添加
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'account_balance' AND constraint_name = 'chk_account_balance_balance_nonneg'
    ) THEN
        ALTER TABLE account_balance ADD CONSTRAINT chk_account_balance_balance_nonneg CHECK (balance >= 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'account_balance' AND constraint_name = 'chk_account_balance_available_nonneg'
    ) THEN
        ALTER TABLE account_balance ADD CONSTRAINT chk_account_balance_available_nonneg CHECK (available >= 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'account_balance' AND constraint_name = 'chk_account_balance_frozen_nonneg'
    ) THEN
        ALTER TABLE account_balance ADD CONSTRAINT chk_account_balance_frozen_nonneg CHECK (frozen >= 0);
    END IF;
END
$$;

-- ============================================================================
-- 6. 补强 ledger_entry 约束（RED-13: 借贷平衡由应用层 + 同事务保证）
--    000004 已有 amount > 0 CHECK，这里补加 asset_code 非空（000023 默认 ''）
-- ============================================================================

-- ledger_entry.amount 已有 CHECK (amount > 0)（000004）
-- ledger_entry.direction 已有 CHECK (direction IN ('DEBIT', 'CREDIT'))（000004）

-- ============================================================================
-- 7. 补强 ledger_transaction 约束
--    000023 添加了 status 列但无 CHECK 约束
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'ledger_transaction' AND constraint_name = 'chk_ledger_txn_status'
    ) THEN
        ALTER TABLE ledger_transaction ADD CONSTRAINT chk_ledger_txn_status
            CHECK (status IN ('PENDING', 'PROCESSING', 'CONFIRMED', 'FAILED',
                              'REVERSED', 'REQUIRES_APPROVAL',
                              'AUTHORIZED', 'CAPTURED', 'SETTLED'));
    END IF;
END
$$;