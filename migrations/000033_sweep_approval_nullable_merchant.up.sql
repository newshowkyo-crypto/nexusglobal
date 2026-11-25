-- 000033_sweep_approval_nullable_merchant.up.sql
-- sweep 是账户级操作，sweep_rule 表没有 merchant_id 字段，移除 merchant_id 非空约束
ALTER TABLE sweep_approval ALTER COLUMN merchant_id DROP NOT NULL;
