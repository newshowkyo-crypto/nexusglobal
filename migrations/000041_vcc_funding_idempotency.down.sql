-- 000041_vcc_funding_idempotency.down.sql
-- P0-A4: 只回滚本迁移新增对象。

DROP TABLE IF EXISTS crypto_pool_ledger_entry;