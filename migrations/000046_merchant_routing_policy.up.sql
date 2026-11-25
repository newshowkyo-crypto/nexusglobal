-- 000046_merchant_routing_policy.up.sql
-- P0-1: 商户权威路由策略表
-- SelectChannel 必须从此表读取商户的 provider_tier，不得接受 body/header/payload 指定 tier
CREATE TABLE IF NOT EXISTS merchant_routing_policy (
    merchant_id UUID PRIMARY KEY REFERENCES corporate_entity(id),
    provider_tier provider_tier NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE')),
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);