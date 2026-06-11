-- name: LockBalanceForUpdate :one
SELECT * FROM merchant_treasury_ledger
WHERE merchant_id = $1 AND currency = $2
FOR UPDATE;

-- name: UpdateBalance :exec
UPDATE merchant_treasury_ledger
SET pending_balance = $2,
    settled_balance = $3,
    reserve_balance = $4,
    frozen_payout_balance = $5,
    dispute_hold_balance = $6,
    available_payout_balance = $7,
    version = version + 1,
    updated_at = now()
WHERE id = $1 AND version = $8;

-- name: InsertTreasuryLedger :one
INSERT INTO merchant_treasury_ledger (merchant_id, currency)
VALUES ($1, $2)
RETURNING *;

-- name: InsertPayoutAddress :one
INSERT INTO merchant_payout_address (
    merchant_id, target_asset, target_network, address_format_type,
    target_address_enc, target_address_hash, target_address_masked, address_label
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetPayoutAddress :one
SELECT * FROM merchant_payout_address
WHERE id = $1;

-- name: ListPayoutAddresses :many
SELECT * FROM merchant_payout_address
WHERE merchant_id = $1
ORDER BY created_at;

-- name: InsertPayoutRequest :one
INSERT INTO payout_request (
    merchant_id, payout_address_id, amount, fiat_currency,
    target_asset, target_network, status, idempotency_key
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetPayoutRequest :one
SELECT * FROM payout_request
WHERE id = $1;

-- name: GetPayoutRequestByIdempotencyKey :one
SELECT * FROM payout_request
WHERE idempotency_key = $1;

-- name: UpdatePayoutRequestStatus :exec
UPDATE payout_request
SET status = $2, version = version + 1, updated_at = now()
WHERE id = $1 AND version = $3;

-- name: InsertOutboxEvent :one
INSERT INTO transactional_outbox (
    aggregate_type, aggregate_id, event_type, payload, idempotency_key
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ClaimOutboxBatch :many
UPDATE transactional_outbox
SET status = 'Processing', locked_by = $1, locked_at = now(), updated_at = now()
WHERE id IN (
    SELECT id FROM transactional_outbox
    WHERE status IN ('Pending', 'Failed') AND next_retry_at <= now()
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
RETURNING *;

-- name: AckOutboxEvent :exec
UPDATE transactional_outbox
SET status = 'Completed', updated_at = now()
WHERE id = $1;

-- name: FailOutboxEvent :exec
UPDATE transactional_outbox
SET status = 'Failed', retry_count = retry_count + 1, updated_at = now()
WHERE id = $1;
