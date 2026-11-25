-- 商户滚动准备金比例（basis points，[0, 10000]），挂在投影行上。
-- 结算时按该比例从净额中计提准备金（pending → reserve）。
ALTER TABLE merchant_treasury_ledger
    ADD COLUMN reserve_ratio_bps integer NOT NULL DEFAULT 0
    CHECK (reserve_ratio_bps >= 0 AND reserve_ratio_bps <= 10000);
