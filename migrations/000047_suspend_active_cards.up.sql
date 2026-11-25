-- 000047_suspend_active_cards.up.sql
-- P0-7: 全卡安全离场幂等记录表

CREATE TABLE IF NOT EXISTS admin_suspend_idempotency (
    idempotency_key TEXT PRIMARY KEY,
    operator        TEXT    NOT NULL,
    reason          TEXT    NOT NULL,
    frozen_count    INT     NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 索引：按时间查询
CREATE INDEX IF NOT EXISTS idx_admin_suspend_idempotency_created
    ON admin_suspend_idempotency(created_at DESC);