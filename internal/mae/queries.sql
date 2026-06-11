-- name: InsertMAEConfig :one
INSERT INTO mae_configuration (
    mae_id, display_name, mae_type, tenant_scope, jurisdiction,
    supported_currencies, api_base_url, api_key_ciphertext,
    webhook_secret_ciphertext, webhook_proxy_url, risk_label,
    settlement_bank_account_id, daily_vmax_minor, monthly_vmax_minor,
    cmax_bps, card_testing_limit, routing_weight, cooldown_minutes,
    testing_traffic_bps, testing_min_txn_count, testing_min_success_rate_bps,
    testing_max_p95_latency_ms
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22
)
RETURNING *;

-- name: GetMAEConfig :one
SELECT * FROM mae_configuration
WHERE id = $1;

-- name: GetMAEConfigByMAEID :one
SELECT * FROM mae_configuration
WHERE mae_id = $1;

-- name: ListMAEConfigs :many
SELECT * FROM mae_configuration
ORDER BY created_at;

-- name: UpdateMAEConfig :exec
UPDATE mae_configuration
SET display_name = $2, mae_type = $3, tenant_scope = $4, jurisdiction = $5,
    supported_currencies = $6, api_base_url = $7, webhook_proxy_url = $8,
    risk_label = $9, daily_vmax_minor = $10, monthly_vmax_minor = $11,
    cmax_bps = $12, card_testing_limit = $13, routing_weight = $14,
    cooldown_minutes = $15, testing_traffic_bps = $16,
    testing_min_txn_count = $17, testing_min_success_rate_bps = $18,
    testing_max_p95_latency_ms = $19, version = version + 1, updated_at = now()
WHERE id = $1 AND version = $20;

-- name: UpdateMAEStatus :exec
UPDATE mae_configuration
SET status = $2, status_reason = $3, business_day_lock_until = $4,
    version = version + 1, updated_at = now()
WHERE id = $1 AND version = $5;

-- name: RotateMAESecret :exec
UPDATE mae_configuration
SET api_key_ciphertext = $2, webhook_secret_ciphertext = $3,
    version = version + 1, updated_at = now()
WHERE id = $1 AND version = $4;

-- name: InsertMAESnapshot :one
INSERT INTO mae_runtime_metric_snapshot (mae_id, business_date, metric_key, metric_value)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListMAESnapshots :many
SELECT * FROM mae_runtime_metric_snapshot
WHERE mae_id = $1 AND business_date = $2
ORDER BY created_at;
