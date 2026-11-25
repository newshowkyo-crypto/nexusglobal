-- 000067_hyperswitch_provider.down.sql

DELETE FROM adapter_provider WHERE provider_code = 'hyperswitch';

-- 兼容旧版本（若曾写入国内扫码 provider）
DELETE FROM adapter_provider WHERE provider_code IN ('wechatpay', 'alipay');
