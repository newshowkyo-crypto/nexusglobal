-- 000039_payment_dispatch_lease.up.sql
-- B2: 修复 Payment Dispatch 崩溃窗口
-- B6: 支付令牌加密存储
--
-- 问题：
--   B2: 旧代码看到 PROCESSING 就永久跳过，导致 Worker 崩溃后支付永久卡住。
--   B6: payment_method_token 明文存储在 DB 中，违反安全要求。
--
-- 修复：
--   B2: 增加 dispatch lease 字段，允许 lease 过期后重新领取。
--   B6: 增加 payment_method_token_encrypted 列，token 加密后存储。

ALTER TABLE payment_attempts
    ADD COLUMN IF NOT EXISTS dispatch_started_at    timestamptz,
    ADD COLUMN IF NOT EXISTS dispatch_lease_until   timestamptz,
    ADD COLUMN IF NOT EXISTS dispatch_owner         varchar(128),
    ADD COLUMN IF NOT EXISTS dispatch_attempt_count integer NOT NULL DEFAULT 0;

-- B6: payment_method_token 加密存储列（AES-256-GCM ciphertext）
ALTER TABLE payment_attempts
    ADD COLUMN IF NOT EXISTS payment_method_token_encrypted bytea;

-- 索引：支持查找 lease 过期的 PROCESSING 状态记录
CREATE INDEX IF NOT EXISTS idx_payment_attempts_dispatch_lease
    ON payment_attempts(status, dispatch_lease_until)
    WHERE status = 'PROCESSING';
