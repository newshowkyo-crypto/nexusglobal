-- 000030_add_burn_mode_to_strategy_enum.up.sql
-- 扩展 channel_strategy_mode enum 支持 BURN_MODE

-- 创建包含 BURN_MODE 的新 enum
CREATE TYPE channel_strategy_mode_new AS ENUM ('DEFAULT', 'STRICT', 'MANUAL_REVIEW', 'BURN_MODE');

-- 先删除 adapter_channel 表的默认值约束
ALTER TABLE adapter_channel ALTER COLUMN strategy_mode DROP DEFAULT;

-- 更新 adapter_channel 表使用新 enum
ALTER TABLE adapter_channel ALTER COLUMN strategy_mode TYPE channel_strategy_mode_new
USING strategy_mode::text::channel_strategy_mode_new;

-- 恢复默认值
ALTER TABLE adapter_channel ALTER COLUMN strategy_mode SET DEFAULT 'DEFAULT';

-- 先删除 acquirer_channels 表的默认值约束（如果存在）
ALTER TABLE acquirer_channels ALTER COLUMN strategy_mode DROP DEFAULT;

-- 更新 acquirer_channels 表使用新 enum
ALTER TABLE acquirer_channels ALTER COLUMN strategy_mode TYPE channel_strategy_mode_new
USING strategy_mode::text::channel_strategy_mode_new;

-- 恢复默认值
ALTER TABLE acquirer_channels ALTER COLUMN strategy_mode SET DEFAULT 'DEFAULT';

-- 删除旧 enum
DROP TYPE channel_strategy_mode;

-- 重命名新 enum
ALTER TYPE channel_strategy_mode_new RENAME TO channel_strategy_mode;

-- 重新创建索引
CREATE INDEX IF NOT EXISTS idx_adapter_channel_strategy_mode ON adapter_channel(strategy_mode);
