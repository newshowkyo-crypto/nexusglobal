-- 000030_add_burn_mode_to_strategy_enum.down.sql
-- 回滚：移除 BURN_MODE，恢复原始 enum

-- 创建原始 enum
CREATE TYPE channel_strategy_mode_old AS ENUM ('DEFAULT', 'STRICT', 'MANUAL_REVIEW');

-- 更新 adapter_channel 表使用旧 enum（BURN_MODE 会转为 DEFAULT）
ALTER TABLE adapter_channel ALTER COLUMN strategy_mode TYPE channel_strategy_mode_old
USING CASE WHEN strategy_mode = 'BURN_MODE' THEN 'DEFAULT' ELSE strategy_mode::text END::channel_strategy_mode_old;

-- 更新 acquirer_channels 表使用旧 enum（BURN_MODE 会转为 DEFAULT）
ALTER TABLE acquirer_channels ALTER COLUMN strategy_mode TYPE channel_strategy_mode_old
USING CASE WHEN strategy_mode = 'BURN_MODE' THEN 'DEFAULT' ELSE strategy_mode::text END::channel_strategy_mode_old;

-- 删除新 enum
DROP TYPE channel_strategy_mode;

-- 重命名旧 enum
ALTER TYPE channel_strategy_mode_old RENAME TO channel_strategy_mode;

-- 重新创建索引
CREATE INDEX IF NOT EXISTS idx_adapter_channel_strategy_mode ON adapter_channel(strategy_mode);
