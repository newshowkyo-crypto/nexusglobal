-- 000012_check_constraints.up.sql
-- Add CHECK constraints for reserve_balance and dispute_hold_balance

ALTER TABLE merchant_treasury_ledger
    ADD CONSTRAINT check_reserve_balance_non_negative
    CHECK (reserve_balance >= 0);

ALTER TABLE merchant_treasury_ledger
    ADD CONSTRAINT check_dispute_hold_balance_non_negative
    CHECK (dispute_hold_balance >= 0);