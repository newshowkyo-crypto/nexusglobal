-- 000069_issuing_pool_ledger.up.sql
-- P7: 发卡池科目（ISSUING_POOL / ISSUING_CLEARING）纳入负债类科目方向。
--
-- 语义：
--   ISSUING_POOL     商户资金子科目（授权时从 SETTLED 划入）
--   ISSUING_CLEARING 应付卡组织/发卡行的款项（清算时从 ISSUING_POOL 划入）
-- 两者均为负债，normal_side = CREDIT。

UPDATE ledger_account SET normal_side = 'CREDIT'
WHERE account_type IN ('ISSUING_POOL', 'ISSUING_CLEARING');

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
