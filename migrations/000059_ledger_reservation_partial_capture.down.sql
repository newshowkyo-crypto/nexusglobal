-- 000059_ledger_reservation_partial_capture.down.sql
ALTER TABLE ledger_reservations DROP CONSTRAINT IF EXISTS ledger_reservations_status_check;
ALTER TABLE ledger_reservations
    ADD CONSTRAINT ledger_reservations_status_check
    CHECK (status IN ('HELD', 'CAPTURED', 'SETTLED', 'RELEASED', 'VOIDED', 'CHARGED_BACK'));
