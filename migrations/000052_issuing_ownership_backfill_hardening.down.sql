-- 000052 down: 回滚 issuing ownership backfill hardening (R24 P0-1)
--
-- R24 P0-1 规则8: down 不得破坏 000050/000051 已建立的列和 ownership，
-- 只撤销 000052 自己新增的数据库对象。
-- 000052 只有数据纠偏（UPDATE/禁用），没有新增列/索引/约束/表。
-- 数据安全变更不可逆：已被 000052 禁用的 Provider（status='DISABLED', adapter_type='UNSUPPORTED'）
-- 无法自动恢复，因为恢复需要重新验证 ownership，而 ownership 事实可能已变化。
-- 已被 000052 回填的 adapter_credential_vault.issuing_provider_id 和 tenant_id
-- 不自动撤销，因为撤销可能破坏其他依赖这些值的流程。
--
-- 此 down migration 为空操作（no-op），仅记录数据安全变更不可逆的声明。
-- 如需恢复特定 Provider，必须通过 Admin 审批流程（Maker-Checker）手动重新激活。

-- 000052 未新增任何数据库对象（列、索引、约束、表），down 为 no-op。
-- 数据纠偏不可逆，不得自动恢复。
SELECT 1;