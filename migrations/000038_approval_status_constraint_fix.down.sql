-- 000038_approval_status_constraint_fix.down.sql
-- 回滚 P0-1：恢复 APPROVED 状态允许（不推荐，仅用于回滚路径完整性）

BEGIN;

-- 1. 删除防护触发器
DROP TRIGGER IF EXISTS trg_admin_approval_reject_approved
    ON admin_approval_requests;

DROP FUNCTION IF EXISTS reject_approved_status();

-- 2. 恢复 000036 的 CHECK 约束（仍包含五态，但不再强制拒绝 APPROVED）
ALTER TABLE admin_approval_requests
    DROP CONSTRAINT IF EXISTS admin_approval_requests_status_check;

ALTER TABLE admin_approval_requests
    ADD CONSTRAINT admin_approval_requests_status_check
    CHECK (status IN (
        'PENDING',
        'APPLYING',
        'APPLIED',
        'REJECTED',
        'FAILED_STALE'
    ));

COMMIT;