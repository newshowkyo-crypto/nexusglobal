-- 000070_vcc_bridge_and_provider_ref.down.sql

DROP INDEX IF EXISTS idx_payment_attempts_provider_ref;

CREATE OR REPLACE FUNCTION ledger_account_default_normal_side() RETURNS trigger AS $$
BEGIN
    IF NEW.normal_side IS NULL OR NEW.normal_side = '' THEN
        NEW.normal_side := CASE
            WHEN NEW.account_type IN ('SETTLED', 'PENDING', 'FROZEN_PAYOUT', 'FEE', 'PLATFORM_FEE',
                                      'PARTNER_PAYABLE', 'INTERCOMPANY_PAYABLE', 'LP_PAYABLE',
                                      'RESERVE', 'DISPUTE_HOLD', 'MERCHANT_RESERVE',
                                      'ISSUING_POOL', 'ISSUING_CLEARING')
                THEN 'CREDIT'
            ELSE 'DEBIT'
        END;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
