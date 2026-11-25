-- 000040_payment_token_encryption.down.sql
-- P0-A1: 只回滚本迁移新增的约束，不触碰任何业务数据。

ALTER TABLE payment_attempts
    DROP CONSTRAINT IF EXISTS chk_payment_attempts_no_plaintext_token;

ALTER TABLE payment_intents
    DROP CONSTRAINT IF EXISTS chk_payment_intents_no_plaintext_token;
