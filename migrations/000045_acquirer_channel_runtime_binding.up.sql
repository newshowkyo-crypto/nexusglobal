-- 000045_acquirer_channel_runtime_binding.up.sql
-- P0-2: 建立 tenant_acquirer_channel 与 acquirer_channels 的绑定关系
-- 运行时 Registry 通过此列 JOIN acquirer_channels 获取 provider_tier
-- 在 real mode 下，acquirer_channel_id 为空的通道会被 SelectChannel 拒绝（fail-closed）
ALTER TABLE tenant_acquirer_channel ADD COLUMN IF NOT EXISTS acquirer_channel_id UUID REFERENCES acquirer_channels(id);

CREATE INDEX IF NOT EXISTS idx_tenant_acquirer_channel_acquirer_channel_id ON tenant_acquirer_channel(acquirer_channel_id);

-- P0-2: partial UNIQUE：acquirer_channel_id 在非 NULL 时必须唯一（一个控制面通道只能绑定一个 runtime 通道）
CREATE UNIQUE INDEX IF NOT EXISTS uq_tenant_acquirer_channel_acquirer_channel_id
    ON tenant_acquirer_channel(acquirer_channel_id)
    WHERE acquirer_channel_id IS NOT NULL;
