-- 000042_add_acquirer_channel_id.up.sql
-- P0-B2: 保存选中的 acquirer_channel_id 到 payment_attempts
--
-- 问题：
--   AcquirerGateway 使用 channelIDFromOrder 启发式猜测通道（按 OrderID 前缀匹配），
--   并回退到"第一个 active channel"，导致跨商户误路由。
--
-- 修复：
--   1. 新增 acquirer_channel_id 列，保存选中的通道 ID。
--   2. 创建时由 SelectChannel（基于 tenant/merchant/currency/amount/provider_tier）选定。
--   3. Worker 重试必须使用此列保存的同一 Channel ID，不得重新选择。
--   4. 无匹配通道时 Fail-Closed，不调用 Provider。
--
-- down：只回滚本迁移新增列，不触碰业务数据。

ALTER TABLE payment_attempts
    ADD COLUMN IF NOT EXISTS acquirer_channel_id uuid;

-- 索引：按通道查询 dispatch 事件
CREATE INDEX IF NOT EXISTS idx_payment_attempts_acquirer_channel
    ON payment_attempts(acquirer_channel_id) WHERE acquirer_channel_id IS NOT NULL;