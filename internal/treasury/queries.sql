-- name: InsertBankingCalendar :one
INSERT INTO banking_calendar (
    calendar_name, jurisdiction, timezone, business_days,
    holidays, emergency_closure_dates, cut_off_matrix
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetBankingCalendar :one
SELECT * FROM banking_calendar
WHERE id = $1;

-- name: GetBankingCalendarByName :one
SELECT * FROM banking_calendar
WHERE calendar_name = $1;

-- name: InsertSweepRule :one
INSERT INTO sweep_rule (
    source_bank_account_id, target_bank_account_id, currency, rail,
    beneficiary_type, same_name_verified, trigger_threshold_minor,
    sweep_ratio_bps, max_single_sweep_minor, min_residual_minor,
    time_window_matrix, scheduled_sweep_times, sweep_mode,
    max_daily_sweep_count, delay_queue_enabled, require_four_eyes,
    approval_threshold_minor
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING *;

-- name: GetSweepRule :one
SELECT * FROM sweep_rule
WHERE id = $1;

-- name: InsertSweepTransfer :one
INSERT INTO sweep_transfer (
    sweep_rule_id, source_account_id, target_account_id,
    amount_minor, currency, rail, status, idempotency_key
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetSweepTransfer :one
SELECT * FROM sweep_transfer
WHERE id = $1;

-- name: UpdateSweepTransferStatus :exec
UPDATE sweep_transfer
SET status = $2, version = version + 1, updated_at = now()
WHERE id = $1 AND version = $3;

-- name: CountDailySweeps :one
SELECT COUNT(*) FROM sweep_transfer
WHERE sweep_rule_id = $1 AND created_at >= $2 AND status != 'Failed';
