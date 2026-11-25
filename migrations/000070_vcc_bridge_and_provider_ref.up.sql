-- 000070_vcc_bridge_and_provider_ref.up.sql
-- P7: VCC 归集桥接科目 + P3: provider_ref 反查索引
--
-- VCC_BRIDGE_PENDING：桥接在途资金，商户资金子科目（负债，CREDIT）。

UPDATE ledger_account SET normal_side = 'CREDIT'
WHERE account_type = 'VCC_BRIDGE_PENDING';

CREATE OR REPLACE FUNCTION ledger_account_default_normal_side() RETURNS trigger AS $$
BEGIN
    IF NEW.normal_side IS NULL OR NEW.normal_side = '' THEN
        NEW.normal_side := CASE
            WHEN NEW.account_type IN ('SETTLED', 'PENDING', 'FROZEN_PAYOUT', 'FEE', 'PLATFORM_FEE',
                                      'PARTNER_PAYABLE', 'INTERCOMPANY_PAYABLE', 'LP_PAYABLE',
                                      'RESERVE', 'DISPUTE_HOLD', 'MERCHANT_RESERVE',
                                      'ISSUING_POOL', 'ISSUING_CLEARING', 'VCC_BRIDGE_PENDING')
                THEN 'CREDIT'
            ELSE 'DEBIT'
        END;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- P3: Hyperswitch 回调按 payment_id（provider_ref）反查 attempt。
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_attempts_provider_ref
    ON payment_attempts(provider_ref)
    WHERE provider_ref IS NOT NULL AND provider_ref <> '';
