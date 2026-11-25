-- 000040_payment_token_encryption.up.sql
-- P0-3 (R4) / P0-A1 (R6): 禁止 payment_method_token 明文落库
--
-- 问题：
--   payment_attempts.payment_method_token 和 payment_intents.payment_method_token
--   仍可存储明文 token，违反安全要求。
--
-- 修复：
--   1. 先检查历史数据：存在非空明文时 RAISE EXCEPTION，禁止静默删除、伪加密
--      或使用固定密钥。必须先将历史明文迁移到加密列并清空后才能应用本迁移。
--   2. PostgreSQL 不支持 ADD CONSTRAINT IF NOT EXISTS，改用 DO $$ 块检查
--      pg_constraint 后条件创建。
--   3. 约束只允许 NULL 或空字符串；加密 token 只允许存入
--      payment_method_token_encrypted 列。
--
-- down：
--   只删除本迁移新增的两个约束，不触碰任何业务数据。

-- 步骤 1：历史非空明文检查（fail-closed，存在则拒绝应用迁移）
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM payment_attempts
        WHERE payment_method_token IS NOT NULL AND payment_method_token <> ''
    ) THEN
        RAISE EXCEPTION
            'migration 000040: payment_attempts.payment_method_token contains non-empty plaintext tokens; migrate them to encrypted storage and clear the column before applying this migration';
    END IF;

    IF EXISTS (
        SELECT 1 FROM payment_intents
        WHERE payment_method_token IS NOT NULL AND payment_method_token <> ''
    ) THEN
        RAISE EXCEPTION
            'migration 000040: payment_intents.payment_method_token contains non-empty plaintext tokens; migrate them to encrypted storage and clear the column before applying this migration';
    END IF;
END
$$;

-- 步骤 2：条件创建约束（PostgreSQL 不支持 ADD CONSTRAINT IF NOT EXISTS）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_payment_attempts_no_plaintext_token'
          AND conrelid = 'payment_attempts'::regclass
    ) THEN
        ALTER TABLE payment_attempts
            ADD CONSTRAINT chk_payment_attempts_no_plaintext_token
            CHECK (payment_method_token = '' OR payment_method_token IS NULL);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_payment_intents_no_plaintext_token'
          AND conrelid = 'payment_intents'::regclass
    ) THEN
        ALTER TABLE payment_intents
            ADD CONSTRAINT chk_payment_intents_no_plaintext_token
            CHECK (payment_method_token = '' OR payment_method_token IS NULL);
    END IF;
END
$$;
