-- 000059_ledger_reservation_partial_capture.up.sql
-- P0 4.6: 部分捕获需要独立状态，否则差额释放后无法区分"全额捕获"与"部分捕获"。
-- 000034 的 CHECK 只允许 HELD/CAPTURED/SETTLED/RELEASED/VOIDED/CHARGED_BACK。

ALTER TABLE ledger_reservations DROP CONSTRAINT IF EXISTS ledger_reservations_status_check;
ALTER TABLE ledger_reservations
    ADD CONSTRAINT ledger_reservations_status_check
    CHECK (status IN ('HELD', 'CAPTURED', 'PARTIALLY_CAPTURED', 'SETTLED', 'RELEASED', 'VOIDED', 'CHARGED_BACK'));
