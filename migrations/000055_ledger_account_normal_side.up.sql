-- 000055_ledger_account_normal_side.up.sql
-- P0 4.2: 科目正常余额方向（asset / liability）
--
-- 背景：updateAccountBalanceWithLock 过去把 DEBIT 一律视为增加余额。
-- 但 SETTLED 是平台对商户的负债（normal = CREDIT），收单捕获时应为增加，
-- 退款/出金时应为减少。缺 normal_side 导致同一科目在不同入口方向相反。
--
-- 说明：normal_side 先允许 NULL，由触发器按 account_type 自动填充，
-- 最后再置 NOT NULL。这样任何历史写入路径（含测试夹具）都不会漏填。

ALTER TABLE ledger_account ADD COLUMN IF NOT EXISTS normal_side varchar(8);

-- 负债类（平台对商户/合作方的应付）：余额随 CREDIT 增加
UPDATE ledger_account SET normal_side = 'CREDIT'
WHERE account_type IN ('SETTLED', 'PENDING', 'FROZEN_PAYOUT', 'FEE', 'PLATFORM_FEE',
                       'PARTNER_PAYABLE', 'INTERCOMPANY_PAYABLE', 'LP_PAYABLE',
                       'RESERVE', 'DISPUTE_HOLD', 'MERCHANT_RESERVE');

-- 资产类：余额随 DEBIT 增加
UPDATE ledger_account SET normal_side = 'DEBIT'
WHERE account_type IN (
    'PROVIDER_RECEIVABLE', 'ACQUIRER_RECEIVABLE',
    'PLATFORM_DISPUTE_RESERVE', 'MERCHANT_NEGATIVE_BALANCE', 'ISSUING_POOL',
    'INTERCOMPANY_RECEIVABLE'
);

UPDATE ledger_account SET normal_side = 'DEBIT' WHERE normal_side IS NULL;

CREATE OR REPLACE FUNCTION ledger_account_default_normal_side() RETURNS trigger AS $$
BEGIN
    IF NEW.normal_side IS NULL OR NEW.normal_side = '' THEN
        NEW.normal_side := CASE
            WHEN NEW.account_type IN ('SETTLED', 'PENDING', 'FROZEN_PAYOUT', 'FEE', 'PLATFORM_FEE',
                                      'PARTNER_PAYABLE', 'INTERCOMPANY_PAYABLE', 'LP_PAYABLE',
                                      'RESERVE', 'DISPUTE_HOLD', 'MERCHANT_RESERVE')
                THEN 'CREDIT'
            ELSE 'DEBIT'
        END;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_ledger_account_normal_side ON ledger_account;
CREATE TRIGGER trg_ledger_account_normal_side
    BEFORE INSERT OR UPDATE OF account_type, normal_side ON ledger_account
    FOR EACH ROW EXECUTE FUNCTION ledger_account_default_normal_side();

ALTER TABLE ledger_account ALTER COLUMN normal_side SET NOT NULL;

ALTER TABLE ledger_account DROP CONSTRAINT IF EXISTS chk_ledger_account_normal_side;
ALTER TABLE ledger_account
    ADD CONSTRAINT chk_ledger_account_normal_side CHECK (normal_side IN ('DEBIT', 'CREDIT'));
