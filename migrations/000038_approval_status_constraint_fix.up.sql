-- 000038_approval_status_constraint_fix.up.sql
-- P0-1: 修复 Kill Switch 恢复状态机 — 统一审批状态
--
-- 背景：
--   迁移 000029 创建 admin_approval_requests 时使用 CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED'))
--   迁移 000036 已将 CHECK 扩展为 ('PENDING','APPLYING','APPLIED','REJECTED','FAILED_STALE')
--   但运行时代码 (failclosed/service.go ExecuteRecovery) 仍写入 'APPROVED'，
--   且存量数据中可能存在 status='APPROVED' 的记录，违反统一状态机。
--
-- 本迁移：
--   1. 将存量 status='APPROVED' 的记录更新为 'APPLIED'（语义等价：已应用）
--   2. 重建 CHECK 约束，明确禁止 'APPROVED'，仅允许统一状态机五态
--      （PENDING / APPLYING / APPLIED / REJECTED / FAILED_STALE）
--   3. 添加防护触发器：未来任何写入 'APPROVED' 的语句直接抛错
--
-- 红线：
--   - 不修改 000001-000037 已部署迁移
--   - 不删除任何审批记录，仅做语义等价的状态迁移
--   - CHECK 约束与 approval_service.go 常量完全一致

BEGIN;

-- 1. 将存量 APPROVED 记录更新为 APPLIED（语义等价：审批通过且已应用）
UPDATE admin_approval_requests
SET status = 'APPLIED',
    applied_at = COALESCE(applied_at, reviewed_at, now())
WHERE status = 'APPROVED';

-- 2. 重建 CHECK 约束：仅允许统一状态机五态，明确排除 APPROVED
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

-- 3. 防护触发器：拒绝任何未来写入 'APPROVED' 的语句
--    一旦运行时代码意外回退，DB 层立即抛错而非静默接受
CREATE OR REPLACE FUNCTION reject_approved_status()
RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'APPROVED' THEN
        RAISE EXCEPTION 'APPROVED status is prohibited: use APPLIED instead (approval_id=%)', NEW.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_admin_approval_reject_approved
    ON admin_approval_requests;

CREATE TRIGGER trg_admin_approval_reject_approved
    BEFORE INSERT OR UPDATE OF status ON admin_approval_requests
    FOR EACH ROW EXECUTE FUNCTION reject_approved_status();

-- 4. 注释更新
COMMENT ON CONSTRAINT admin_approval_requests_status_check ON admin_approval_requests
    IS 'P0-1: 统一审批状态机：PENDING/APPLYING/APPLIED/REJECTED/FAILED_STALE，禁止 APPROVED';

COMMIT;