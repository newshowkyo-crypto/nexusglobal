-- 000047_suspend_active_cards.down.sql
-- P0-7: 回滚全卡安全离场幂等记录表

DROP TABLE IF EXISTS admin_suspend_idempotency;