-- 000057_ledger_entry_amount_precision.up.sql
-- P0 4.4: 多资产精度。
-- 000004 建的 ledger_entry.amount 是 numeric(20,4)，000023 只扩宽了
-- balance_before/balance_after，entry 本身仍是 4 位，BTC/USDT 会被截断。

ALTER TABLE ledger_entry
    ALTER COLUMN amount TYPE numeric(36, 18) USING amount::numeric(36, 18);

-- 资产精度登记表：用于入账前校验小数位，防止把主单位小数误当 minor 单位。
CREATE TABLE IF NOT EXISTS asset_precision (
    asset_code  varchar(32) PRIMARY KEY,
    decimals    integer     NOT NULL CHECK (decimals >= 0 AND decimals <= 18),
    description varchar(128) NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO asset_precision (asset_code, decimals, description) VALUES
    ('USD', 2, 'US Dollar'),
    ('EUR', 2, 'Euro'),
    ('GBP', 2, 'Pound Sterling'),
    ('HKD', 2, 'Hong Kong Dollar'),
    ('JPY', 0, 'Japanese Yen'),
    ('CNY', 2, 'Chinese Yuan'),
    ('BTC', 8, 'Bitcoin (satoshi)'),
    ('ETH', 18, 'Ether (wei)'),
    ('USDT', 6, 'Tether USD'),
    ('USDC', 6, 'USD Coin')
ON CONFLICT (asset_code) DO NOTHING;
