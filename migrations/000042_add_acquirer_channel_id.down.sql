-- 000042_add_acquirer_channel_id.down.sql
-- P0-B2: 只回滚本迁移新增列，不触碰业务数据。

DROP INDEX IF EXISTS idx_payment_attempts_acquirer_channel;

ALTER TABLE payment_attempts
    DROP COLUMN IF EXISTS acquirer_channel_id;