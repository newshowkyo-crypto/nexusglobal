-- 000029_admin_approval_requests.up.sql
-- Maker-Checker 审批流表

CREATE TABLE IF NOT EXISTS admin_approval_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    action_type TEXT NOT NULL,
    requested_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    requester TEXT NOT NULL,
    checker TEXT,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED')),
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_admin_approval_requests_status
ON admin_approval_requests(status, created_at DESC);

COMMENT ON TABLE admin_approval_requests IS '管理员审批请求表，用于 Maker-Checker 审批流';
COMMENT ON COLUMN admin_approval_requests.action_type IS '操作类型，如 DISABLE_ACQUIRER_CHANNEL、DISABLE_ISSUING_POOL';
COMMENT ON COLUMN admin_approval_requests.requested_payload IS '请求的 payload，如 {channel_id: uuid}';
COMMENT ON COLUMN admin_approval_requests.requester IS '申请人';
COMMENT ON COLUMN admin_approval_requests.checker IS '复核人';
COMMENT ON COLUMN admin_approval_requests.status IS '状态：PENDING、APPROVED、REJECTED';
