-- 000048_lockdown_idempotency.down.sql
-- P0-7: 回滚全局封锁幂等记录表

DROP INDEX IF EXISTS idx_admin_lockdown_idempotency_created;
DROP TABLE IF EXISTS admin_lockdown_idempotency;