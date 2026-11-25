-- 000048_lockdown_idempotency.up.sql
-- P0-7: 全局封锁幂等记录表（防止并发触发重复封锁）

CREATE TABLE IF NOT EXISTS admin_lockdown_idempotency (
    idempotency_key   TEXT PRIMARY KEY,
    operator          TEXT    NOT NULL,
    reason            TEXT    NOT NULL,
    disabled_channels INT     NOT NULL DEFAULT 0,
    risk_hold_groups  INT     NOT NULL DEFAULT 0,
    epoch             BIGINT  NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 索引：按时间查询
CREATE INDEX IF NOT EXISTS idx_admin_lockdown_idempotency_created
    ON admin_lockdown_idempotency(created_at DESC);