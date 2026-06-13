-- 000012_check_constraints.down.sql
-- Remove CHECK constraints for reserve_balance and dispute_hold_balance

ALTER TABLE merchant_treasury_ledger
    DROP CONSTRAINT IF EXISTS chk_reserve_balance_non_negative;

ALTER TABLE merchant_treasury_ledger
    DROP CONSTRAINT IF EXISTS chk_dispute_hold_balance_non_negative;