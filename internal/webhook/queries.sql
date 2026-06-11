-- name: InsertIngressEvent :one
INSERT INTO webhook_ingress_event (
    provider_type, provider_id, provider_event_id, event_type,
    idempotency_key, payload_hash, raw_payload_ciphertext, status
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetIngressEvent :one
SELECT * FROM webhook_ingress_event
WHERE id = $1;

-- name: GetIngressEventByIdempotencyKey :one
SELECT * FROM webhook_ingress_event
WHERE idempotency_key = $1;

-- name: UpdateIngressEventStatus :exec
UPDATE webhook_ingress_event
SET status = $2, error_message = $3, updated_at = now()
WHERE id = $1;

-- name: ListIngressEvents :many
SELECT * FROM webhook_ingress_event
WHERE provider_type = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: GetReceiverProxy :one
SELECT * FROM webhook_receiver_proxy
WHERE provider_type = $1 AND provider_id = $2;
