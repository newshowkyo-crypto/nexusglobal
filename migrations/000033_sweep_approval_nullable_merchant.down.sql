-- 000033_sweep_approval_nullable_merchant.down.sql
UPDATE sweep_approval SET merchant_id = '00000000-0000-0000-0000-000000000000' WHERE merchant_id IS NULL;
ALTER TABLE sweep_approval ALTER COLUMN merchant_id SET NOT NULL;
