-- 000035_system_control_state_full_state_machine.down.sql
-- 回滚 000035

DROP TRIGGER IF EXISTS trg_system_control_epoch_monotonic ON system_control_state;
DROP FUNCTION IF EXISTS enforce_system_control_epoch_monotonic();

DROP INDEX IF EXISTS idx_system_control_state_updated;

-- 将任何非 OPEN/LOCKDOWN/DEGRADED 状态归一为 LOCKDOWN，再恢复旧约束
UPDATE system_control_state
    SET state = 'LOCKDOWN'
    WHERE state NOT IN ('OPEN', 'LOCKDOWN', 'DEGRADED');

ALTER TABLE system_control_state
    DROP CONSTRAINT IF EXISTS system_control_state_state_check;

ALTER TABLE system_control_state
    ADD CONSTRAINT system_control_state_state_check
    CHECK (state IN ('OPEN', 'LOCKDOWN', 'DEGRADED'));