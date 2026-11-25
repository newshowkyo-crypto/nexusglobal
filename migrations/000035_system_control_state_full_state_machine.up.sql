-- 000035_system_control_state_full_state_machine.up.sql
-- G07: 扩展 system_control_state 状态机以支持完整 Kill Switch 生命周期
--
-- 状态机: OPEN → LOCKDOWN_PENDING → LOCKED → RECOVERY_PENDING → OPEN
--
--   * OPEN              正常运行
--   * LOCKDOWN_PENDING  已触发封锁，事务进行中（瞬态）
--   * LOCKED            全局封锁生效
--   * RECOVERY_PENDING  解除请求已创建，待审批（maker-checker）
--   * DEGRADED          降级（保留，用于未来部分封锁场景）
--
-- 000034 仅允许 ('OPEN', 'LOCKDOWN', 'DEGRADED')，无法表达完整生命周期。
-- 本迁移替换为完整状态枚举，并补加 epoch 单调递增触发器。

-- 1. 替换 state CHECK 约束为完整状态机
ALTER TABLE system_control_state
    DROP CONSTRAINT IF EXISTS system_control_state_state_check;

ALTER TABLE system_control_state
    ADD CONSTRAINT system_control_state_state_check
    CHECK (state IN ('OPEN', 'LOCKDOWN_PENDING', 'LOCKED', 'RECOVERY_PENDING', 'DEGRADED'));

-- 2. epoch 单调递增触发器：禁止 UPDATE 时让 epoch 减小
CREATE OR REPLACE FUNCTION enforce_system_control_epoch_monotonic()
RETURNS trigger AS $$
BEGIN
    IF NEW.epoch < OLD.epoch THEN
        RAISE EXCEPTION 'system_control_state epoch must be monotonically non-decreasing: old=%, new=%', OLD.epoch, NEW.epoch;
    END IF;
    IF NEW.epoch = OLD.epoch AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'system_control_state state change must increment epoch: old_state=%, new_state=%, epoch=%', OLD.state, NEW.state, OLD.epoch;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_system_control_epoch_monotonic ON system_control_state;
CREATE TRIGGER trg_system_control_epoch_monotonic
    BEFORE UPDATE ON system_control_state
    FOR EACH ROW EXECUTE FUNCTION enforce_system_control_epoch_monotonic();

-- 3. 索引以支持管理面板按时间检索封锁事件
CREATE INDEX IF NOT EXISTS idx_system_control_state_updated
    ON system_control_state(updated_at DESC);