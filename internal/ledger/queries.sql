-- name: InsertLedgerAccount :one
INSERT INTO ledger_account (merchant_id, account_type, currency, description)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetLedgerAccount :one
SELECT * FROM ledger_account
WHERE id = $1;

-- name: GetLedgerAccountByMerchant :one
SELECT * FROM ledger_account
WHERE merchant_id = $1 AND account_type = $2 AND currency = $3;

-- name: InsertLedgerTransaction :one
INSERT INTO ledger_transaction (idempotency_key, transaction_type, aggregate_type, aggregate_id, description)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING *;

-- name: GetLedgerTransactionByIdempotencyKey :one
SELECT * FROM ledger_transaction
WHERE idempotency_key = $1;

-- name: InsertLedgerEntry :one
INSERT INTO ledger_entry (transaction_id, account_id, direction, amount)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListEntriesByTransaction :many
SELECT * FROM ledger_entry
WHERE transaction_id = $1
ORDER BY created_at;

-- name: ListTransactionsByAggregate :many
SELECT * FROM ledger_transaction
WHERE aggregate_type = $1 AND aggregate_id = $2
ORDER BY created_at;

-- name: ListEntriesByAccount :many
SELECT * FROM ledger_entry
WHERE account_id = $1
ORDER BY created_at;

-- name: GetMerchantTreasuryLedger :one
SELECT * FROM merchant_treasury_ledger
WHERE merchant_id = $1 AND currency = $2;
