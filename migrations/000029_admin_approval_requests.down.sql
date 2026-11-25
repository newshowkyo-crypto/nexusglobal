-- 000029_admin_approval_requests.down.sql
-- 回滚 Maker-Checker 审批流表

DROP INDEX IF EXISTS idx_admin_approval_requests_status;
DROP TABLE IF EXISTS admin_approval_requests;
