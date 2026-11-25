CREATE TABLE IF NOT EXISTS merchant_site (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id UUID NOT NULL,
 merchant_id UUID NOT NULL REFERENCES corporate_entity(id), site_code VARCHAR(100) NOT NULL,
 display_name TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL,
 status VARCHAR(20) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','ACTIVE','SUSPENDED','REVOKED')),
 version INTEGER NOT NULL DEFAULT 1, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (merchant_id, site_code)
);
CREATE INDEX IF NOT EXISTS idx_merchant_site_tenant ON merchant_site(tenant_id);
CREATE TABLE IF NOT EXISTS site_payment_policy (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id UUID NOT NULL,
 merchant_id UUID NOT NULL REFERENCES corporate_entity(id), site_id UUID NOT NULL REFERENCES merchant_site(id),
 channel_id UUID NOT NULL REFERENCES tenant_acquirer_channel(id), business_scope TEXT NOT NULL DEFAULT '',
 countries JSONB NOT NULL DEFAULT '[]', currencies JSONB NOT NULL DEFAULT '[]', payment_methods JSONB NOT NULL DEFAULT '[]',
 status VARCHAR(20) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','ACTIVE','EXPIRED','REVOKED')),
 effective_at TIMESTAMPTZ, expires_at TIMESTAMPTZ, version INTEGER NOT NULL DEFAULT 1,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_site_payment_policy_site ON site_payment_policy(site_id,status);
CREATE INDEX IF NOT EXISTS idx_site_payment_policy_channel ON site_payment_policy(channel_id,status);
