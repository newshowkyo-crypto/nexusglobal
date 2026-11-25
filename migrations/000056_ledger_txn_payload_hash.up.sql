-- 000056_ledger_txn_payload_hash.up.sql
-- P0 4.3: 账本幂等必须校验 payload。
-- 旧实现先 SELECT 再 INSERT：并发同 key 会唯一键冲突返回 500；
-- 同 key 不同金额会静默 return nil，造成"重放吞掉新账"。

ALTER TABLE ledger_transaction
    ADD COLUMN IF NOT EXISTS payload_hash varchar(64) NOT NULL DEFAULT '';
