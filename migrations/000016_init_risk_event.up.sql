-- risk_event 审计表
CREATE TABLE risk_event (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    merchant_id UUID NOT NULL,
    app_id VARCHAR(255) NOT NULL,
    decision VARCHAR(50) NOT NULL CHECK (decision IN ('ALLOW', 'BLOCK', 'CHALLENGE', 'COOLDOWN')),
    reason TEXT NOT NULL,
    buyer_ip_hash VARCHAR(64) NOT NULL,
    card_bin_hash VARCHAR(64),
    device_hash VARCHAR(64),
    window_type VARCHAR(20) NOT NULL CHECK (window_type IN ('60s', '5m', '1h')),
    threshold_hit INTEGER NOT NULL DEFAULT 0,
    current_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_risk_event_tenant ON risk_event(tenant_id);
CREATE INDEX idx_risk_event_merchant ON risk_event(merchant_id);
CREATE INDEX idx_risk_event_app ON risk_event(app_id);
CREATE INDEX idx_risk_event_decision ON risk_event(decision);
CREATE INDEX idx_risk_event_created ON risk_event(created_at);

COMMENT ON TABLE risk_event IS '测卡/异常流量拦截审计日志';
COMMENT ON COLUMN risk_event.buyer_ip_hash IS 'IP地址SHA-256哈希，不存原始IP';
COMMENT ON COLUMN risk_event.card_bin_hash IS '卡BIN SHA-256哈希，不存完整卡号或CVV';
COMMENT ON COLUMN risk_event.device_hash IS '设备指纹哈希';