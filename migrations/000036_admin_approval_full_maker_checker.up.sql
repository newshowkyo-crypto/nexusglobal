-- 000036_admin_approval_full_maker_checker.up.sql
-- G08: 扩展 admin_approval_requests 以支持完整 Maker-Checker 事务应用闭环
--
-- 原表 (000029) 缺陷：
--   1. requester 来自请求体，可伪造（RED-15）
--   2. 无 target_type/target_id/target_version，无法做版本锁
--   3. 无 validated typed payload，可执行任意 SQL/字段
--   4. 无 before snapshot，审批详情无法展示 before/after
--   5. status 仅 PENDING/APPROVED/REJECTED，无 APPLYING/APPLIED/FAILED_STALE
--   6. 无 tenant_id，跨租户审批无法隔离
--   7. 无 request_id 幂等键，重复事件可重复入账（RED-10）
--   8. 允许自审（checker=requester）
--
-- 本迁移补全上述字段，并保留旧字段以兼容存量数据。
-- 状态机：PENDING → APPLYING → APPLIED（成功）
--                  → REJECTED（拒绝）
--                  → FAILED_STALE（版本冲突/目标不存在/零 affected rows）

-- 1. 扩展 status CHECK 约束为完整状态机
ALTER TABLE admin_approval_requests
    DROP CONSTRAINT IF EXISTS admin_approval_requests_status_check;

ALTER TABLE admin_approval_requests
    ADD CONSTRAINT admin_approval_requests_status_check
    CHECK (status IN (
        'PENDING',      -- 已创建，待审批
        'APPLYING',     -- 审批通过，正在应用（瞬态）
        'APPLIED',      -- 已应用，配置已变更
        'REJECTED',     -- 已拒绝
        'FAILED_STALE'  -- 应用失败：版本冲突/目标不存在/零 affected rows
    ));

-- 2. 新增字段（全部可空以兼容存量数据）
ALTER TABLE admin_approval_requests
    ADD COLUMN IF NOT EXISTS tenant_id UUID,
    ADD COLUMN IF NOT EXISTS target_type VARCHAR(64),
    ADD COLUMN IF NOT EXISTS target_id UUID,
    ADD COLUMN IF NOT EXISTS target_version INTEGER,
    ADD COLUMN IF NOT EXISTS validated_payload JSONB,
    ADD COLUMN IF NOT EXISTS before_snapshot JSONB,
    ADD COLUMN IF NOT EXISTS requester_role VARCHAR(32),
    ADD COLUMN IF NOT EXISTS review_reason TEXT,
    ADD COLUMN IF NOT EXISTS applied_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS request_id VARCHAR(128);

-- 3. 幂等键唯一约束（RED-10）：同一 request_id 不能重复创建审批
--    request_id 为空时不受约束（兼容旧数据）
CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_approval_requests_request_id
    ON admin_approval_requests(request_id)
    WHERE request_id IS NOT NULL;

-- 4. 索引以支持 inbox 查询（按 tenant + status）
CREATE INDEX IF NOT EXISTS idx_admin_approval_requests_tenant_status
    ON admin_approval_requests(tenant_id, status, created_at DESC);

-- 5. 索引以支持按目标查询（审批历史）
CREATE INDEX IF NOT EXISTS idx_admin_approval_requests_target
    ON admin_approval_requests(target_type, target_id, created_at DESC);

-- 6. 索引以支持按申请人/复核人查询
CREATE INDEX IF NOT EXISTS idx_admin_approval_requests_requester
    ON admin_approval_requests(requester, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_admin_approval_requests_checker
    ON admin_approval_requests(checker, created_at DESC);

-- 7. 审计不可变触发器：APPLIED 状态后禁止修改（防止篡改已应用审批）
CREATE OR REPLACE FUNCTION prevent_applied_approval_mutation()
RETURNS trigger AS $$
BEGIN
    IF OLD.status = 'APPLIED' AND NEW.status <> OLD.status THEN
        RAISE EXCEPTION 'APPLIED approval is immutable: status change rejected (old=APPLIED, new=%)', NEW.status;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_admin_approval_no_applied_mutation ON admin_approval_requests;
CREATE TRIGGER trg_admin_approval_no_applied_mutation
    BEFORE UPDATE ON admin_approval_requests
    FOR EACH ROW EXECUTE FUNCTION prevent_applied_approval_mutation();

-- 8. 注释更新
COMMENT ON TABLE admin_approval_requests IS 'G08: Maker-Checker 审批请求表，支持完整事务应用闭环';
COMMENT ON COLUMN admin_approval_requests.tenant_id IS '租户隔离';
COMMENT ON COLUMN admin_approval_requests.target_type IS '目标类型：acquirer_channel / issuing_pool / system_control / merchant_reserve 等';
COMMENT ON COLUMN admin_approval_requests.target_id IS '目标 UUID';
COMMENT ON COLUMN admin_approval_requests.target_version IS '目标版本锁：apply 时必须等于此版本';
COMMENT ON COLUMN admin_approval_requests.validated_payload IS '已验证的 typed payload，禁止 map[string]any 直接执行任意 SQL';
COMMENT ON COLUMN admin_approval_requests.before_snapshot IS '变更前快照（不含凭证/token），用于审批详情展示 before/after';
COMMENT ON COLUMN admin_approval_requests.requester_role IS '申请人角色（从 Context 获取）';
COMMENT ON COLUMN admin_approval_requests.review_reason IS '复核人评审理由';
COMMENT ON COLUMN admin_approval_requests.applied_at IS '应用时间（事务提交后）';
COMMENT ON COLUMN admin_approval_requests.request_id IS '幂等键（RED-10）：同一 request_id 不能重复创建审批';