-- 000036_admin_approval_full_maker_checker.down.sql
-- 回滚 G08 扩展

DROP TRIGGER IF EXISTS trg_admin_approval_no_applied_mutation ON admin_approval_requests;
DROP FUNCTION IF EXISTS prevent_applied_approval_mutation();

DROP INDEX IF EXISTS idx_admin_approval_requests_requester;
DROP INDEX IF EXISTS idx_admin_approval_requests_checker;
DROP INDEX IF EXISTS idx_admin_approval_requests_target;
DROP INDEX IF EXISTS idx_admin_approval_requests_tenant_status;
DROP INDEX IF EXISTS idx_admin_approval_requests_request_id;

ALTER TABLE admin_approval_requests
    DROP COLUMN IF EXISTS request_id,
    DROP COLUMN IF EXISTS applied_at,
    DROP COLUMN IF EXISTS review_reason,
    DROP COLUMN IF EXISTS requester_role,
    DROP COLUMN IF EXISTS before_snapshot,
    DROP COLUMN IF EXISTS validated_payload,
    DROP COLUMN IF EXISTS target_version,
    DROP COLUMN IF EXISTS target_id,
    DROP COLUMN IF EXISTS target_type,
    DROP COLUMN IF EXISTS tenant_id;

ALTER TABLE admin_approval_requests
    DROP CONSTRAINT IF EXISTS admin_approval_requests_status_check;

ALTER TABLE admin_approval_requests
    ADD CONSTRAINT admin_approval_requests_status_check
    CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED'));