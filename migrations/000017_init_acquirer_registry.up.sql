-- payment_provider: 支付提供商配置（非敏感参数）
CREATE TABLE payment_provider (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_code VARCHAR(50) UNIQUE NOT NULL,
    provider_name VARCHAR(255) NOT NULL,
    provider_type VARCHAR(50) NOT NULL CHECK (provider_type IN ('LP', 'BANK', 'MAE', 'CARD')),
    config_json JSONB NOT NULL DEFAULT '{}', -- 非敏感配置参数（base_url, timeout, etc）
    is_active BOOLEAN NOT NULL DEFAULT true,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_provider_code ON payment_provider(provider_code);
CREATE INDEX idx_payment_provider_type ON payment_provider(provider_type);

-- tenant_acquirer_channel: 租户收单通道配置
CREATE TABLE tenant_acquirer_channel (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    merchant_id UUID NOT NULL,
    channel_code VARCHAR(100) NOT NULL,
    provider_id UUID NOT NULL REFERENCES payment_provider(id),
    channel_name TEXT NOT NULL DEFAULT '',
    payment_method TEXT NOT NULL DEFAULT 'card',
    currencies JSONB NOT NULL DEFAULT '["USD"]',
    channel_config JSONB NOT NULL DEFAULT '{}', -- 非敏感通道配置
    is_active BOOLEAN NOT NULL DEFAULT true,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(tenant_id, merchant_id, channel_code)
);

CREATE INDEX idx_tenant_acquirer_channel_tenant ON tenant_acquirer_channel(tenant_id);
CREATE INDEX idx_tenant_acquirer_channel_merchant ON tenant_acquirer_channel(merchant_id);
CREATE INDEX idx_tenant_acquirer_channel_provider ON tenant_acquirer_channel(provider_id);

-- provider_credential_vault: 提供商凭证加密存储
CREATE TABLE provider_credential_vault (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id UUID NOT NULL REFERENCES payment_provider(id),
    channel_id UUID REFERENCES tenant_acquirer_channel(id), -- 可为空（provider 级别凭证）
    credential_type VARCHAR(50) NOT NULL CHECK (credential_type IN ('API_KEY', 'SECRET', 'PRIVATE_KEY', 'WEBHOOK_SECRET', 'CERTIFICATE')),
    key_version INTEGER NOT NULL DEFAULT 1,
    ciphertext BYTEA NOT NULL, -- AES-256-GCM 加密后的密文（含 nonce）
    aad_context TEXT NOT NULL, -- AAD 绑定上下文（tenant+record+field+key_version）
    expires_at TIMESTAMPTZ, -- 可选过期时间
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(provider_id, channel_id, credential_type, key_version)
);

CREATE INDEX idx_provider_credential_vault_provider ON provider_credential_vault(provider_id);
CREATE INDEX idx_provider_credential_vault_channel ON provider_credential_vault(channel_id);
CREATE INDEX idx_provider_credential_vault_type ON provider_credential_vault(credential_type);

COMMENT ON TABLE payment_provider IS '支付提供商配置，非敏感参数存 JSONB';
COMMENT ON TABLE tenant_acquirer_channel IS '租户收单通道配置，非敏感参数存 JSONB';
COMMENT ON TABLE provider_credential_vault IS '提供商凭证加密存储，API key/secret/private key/webhook secret 必须加密';
COMMENT ON COLUMN provider_credential_vault.ciphertext IS 'AES-256-GCM 加密密文（nonce 已打包在内）';
COMMENT ON COLUMN provider_credential_vault.aad_context IS 'AAD 绑定租户/记录/字段/密钥版本';