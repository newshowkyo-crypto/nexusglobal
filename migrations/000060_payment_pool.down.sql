-- 000060_payment_pool.down.sql
ALTER TABLE payment_attempts DROP COLUMN IF EXISTS pool_channel_id;
ALTER TABLE checkout_orders DROP COLUMN IF EXISTS site_id;
DROP TABLE IF EXISTS pool_lease;
DROP TABLE IF EXISTS pool_watermark;
DROP TABLE IF EXISTS pool_policy;
DROP TABLE IF EXISTS pool_channel;
