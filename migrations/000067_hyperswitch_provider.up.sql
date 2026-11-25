-- 000067_hyperswitch_provider.up.sql
-- P3: 注册 Hyperswitch（收单编排层）为 CARD_ACQUIRER provider。
--
-- 说明：本项目是跨境电商，不做国内扫码（微信/支付宝），因此不引入 QR_PAY。

INSERT INTO adapter_provider (provider_code, provider_name, adapter_type, provider_config) VALUES
    ('hyperswitch', 'Hyperswitch Orchestrator', 'CARD_ACQUIRER', '{}')
ON CONFLICT (provider_code) DO NOTHING;
