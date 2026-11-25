-- 000044_vcc_funding_idempotency_tables.down.sql
-- 回滚：删除 VCC 资金幂等表

DROP TABLE IF EXISTS vcc_funding_finalizations;
DROP TABLE IF EXISTS vcc_funding_requests;