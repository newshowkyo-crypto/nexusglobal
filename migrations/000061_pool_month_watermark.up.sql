-- 000061_pool_month_watermark.up.sql
-- P1: 月限额需要独立水位表，避免每次选路都做 SUM（也避免日表被清理后失去月累计）。

CREATE TABLE IF NOT EXISTS pool_month_watermark (
    channel_id            uuid        NOT NULL REFERENCES pool_channel(id) ON DELETE CASCADE,
    month_start           date        NOT NULL,
    used_amount_minor     bigint      NOT NULL DEFAULT 0 CHECK (used_amount_minor >= 0),
    reserved_amount_minor bigint      NOT NULL DEFAULT 0 CHECK (reserved_amount_minor >= 0),
    used_count            bigint      NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, month_start)
);
