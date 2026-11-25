package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

// Repository 回调端点与投递记录的持久化。
type Repository struct {
	pool *pgxpool.Pool
	enc  *crypto.Encryptor
}

func NewRepository(pool *pgxpool.Pool, enc *crypto.Encryptor) *Repository {
	return &Repository{pool: pool, enc: enc}
}

func secretMeta(merchantID, endpointID uuid.UUID) crypto.Meta {
	return crypto.Meta{
		TenantID:   merchantID.String(),
		RecordID:   endpointID.String(),
		FieldName:  "merchant_webhook_secret",
		KeyVersion: "v1",
	}
}

// CreateEndpoint 创建端点，返回明文 secret（仅此一次）。
func (r *Repository) CreateEndpoint(ctx context.Context, tenantID, merchantID uuid.UUID,
	url, secret, description string, eventTypes []string) (*Endpoint, error) {
	if r.enc == nil {
		return nil, fmt.Errorf("merchant webhook: encryptor required")
	}
	if eventTypes == nil {
		eventTypes = []string{}
	}
	endpointID := uuid.New()
	encrypted, err := r.enc.Encrypt(ctx, []byte(secret), secretMeta(merchantID, endpointID))
	if err != nil {
		return nil, fmt.Errorf("merchant webhook: encrypt secret: %w", err)
	}
	var ep Endpoint
	err = r.pool.QueryRow(ctx, `
		INSERT INTO merchant_webhook_endpoint
		    (id, tenant_id, merchant_id, url, secret_enc, secret_masked, event_types, description, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'ACTIVE')
		RETURNING id, tenant_id, merchant_id, url, event_types, description, status, secret_masked, created_at
	`, endpointID, tenantID, merchantID, url, encrypted, MaskSecret(secret), eventTypes, description).Scan(
		&ep.ID, &ep.TenantID, &ep.MerchantID, &ep.URL, &ep.EventTypes, &ep.Description,
		&ep.Status, &ep.SecretMasked, &ep.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("merchant webhook: create endpoint: %w", err)
	}
	return &ep, nil
}

// ListEndpoints 列出商户端点（不含密文）。
func (r *Repository) ListEndpoints(ctx context.Context, merchantID uuid.UUID) ([]Endpoint, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, merchant_id, url, event_types, description, status, secret_masked, created_at
		FROM merchant_webhook_endpoint WHERE merchant_id = $1 ORDER BY created_at
	`, merchantID)
	if err != nil {
		return nil, fmt.Errorf("merchant webhook: list endpoints: %w", err)
	}
	defer rows.Close()

	var out []Endpoint
	for rows.Next() {
		var ep Endpoint
		if err := rows.Scan(&ep.ID, &ep.TenantID, &ep.MerchantID, &ep.URL, &ep.EventTypes,
			&ep.Description, &ep.Status, &ep.SecretMasked, &ep.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// SetEndpointStatus 启用/停用端点。
func (r *Repository) SetEndpointStatus(ctx context.Context, merchantID, endpointID uuid.UUID, status string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE merchant_webhook_endpoint SET status = $3, updated_at = now()
		WHERE merchant_id = $1 AND id = $2
	`, merchantID, endpointID, status)
	return err
}

// DeleteEndpoint 删除端点。
func (r *Repository) DeleteEndpoint(ctx context.Context, merchantID, endpointID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM merchant_webhook_endpoint WHERE merchant_id = $1 AND id = $2
	`, merchantID, endpointID)
	return err
}

// endpointSecret 解密端点签名密钥。
func (r *Repository) endpointSecret(ctx context.Context, epID, merchantID uuid.UUID) (string, string, error) {
	var encrypted []byte
	var url string
	err := r.pool.QueryRow(ctx, `
		SELECT secret_enc, url FROM merchant_webhook_endpoint WHERE id = $1
	`, epID).Scan(&encrypted, &url)
	if err != nil {
		return "", "", err
	}
	if r.enc == nil {
		return "", "", fmt.Errorf("merchant webhook: encryptor required")
	}
	plain, err := r.enc.Decrypt(ctx, encrypted, secretMeta(merchantID, epID))
	if err != nil {
		return "", "", fmt.Errorf("merchant webhook: decrypt secret: %w", err)
	}
	return string(plain), url, nil
}

// ClaimDue 原子领取到期投递（FOR UPDATE SKIP LOCKED）。
func (r *Repository) ClaimDue(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE merchant_webhook_delivery d
		SET status = 'DELIVERING', attempt_count = d.attempt_count + 1, updated_at = now()
		FROM (
			SELECT id FROM merchant_webhook_delivery
			WHERE status IN ('PENDING', 'FAILED') AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		) picked
		WHERE d.id = picked.id
		RETURNING d.id, d.endpoint_id, d.merchant_id, d.event_type, d.event_id,
		          d.payload, d.status, d.attempt_count, d.next_attempt_at
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("merchant webhook: claim due: %w", err)
	}
	defer rows.Close()

	var out []Delivery
	for rows.Next() {
		var d Delivery
		var payload []byte
		if err := rows.Scan(&d.ID, &d.EndpointID, &d.MerchantID, &d.EventType, &d.EventID,
			&payload, &d.Status, &d.AttemptCount, &d.NextAttemptAt); err != nil {
			return nil, err
		}
		d.Payload = payload
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkSucceeded 标记投递成功。
func (r *Repository) MarkSucceeded(ctx context.Context, deliveryID uuid.UUID, statusCode int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE merchant_webhook_delivery
		SET status = 'SUCCEEDED', last_status_code = $2, last_error = '',
		    delivered_at = now(), updated_at = now()
		WHERE id = $1
	`, deliveryID, statusCode)
	return err
}

// MarkFailed 记录失败；超过上限进入 DEAD。
func (r *Repository) MarkFailed(ctx context.Context, deliveryID uuid.UUID, attempt int,
	statusCode int, errMsg string) error {
	if attempt >= MaxAttempts {
		_, err := r.pool.Exec(ctx, `
			UPDATE merchant_webhook_delivery
			SET status = 'DEAD', last_status_code = $2, last_error = $3, updated_at = now()
			WHERE id = $1
		`, deliveryID, statusCode, errMsg)
		return err
	}
	next := time.Now().Add(Backoff(attempt))
	_, err := r.pool.Exec(ctx, `
		UPDATE merchant_webhook_delivery
		SET status = 'FAILED', last_status_code = $2, last_error = $3,
		    next_attempt_at = $4, updated_at = now()
		WHERE id = $1
	`, deliveryID, statusCode, errMsg, next)
	return err
}

// Replay 把死信/失败投递重新排队。
func (r *Repository) Replay(ctx context.Context, merchantID, deliveryID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE merchant_webhook_delivery
		SET status = 'PENDING', attempt_count = 0, next_attempt_at = now(),
		    last_error = '', updated_at = now()
		WHERE id = $1 AND merchant_id = $2 AND status IN ('DEAD', 'FAILED')
	`, deliveryID, merchantID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ListDeliveries 查询商户投递记录。
func (r *Repository) ListDeliveries(ctx context.Context, merchantID uuid.UUID, limit int) ([]map[string]any, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, endpoint_id::text, event_type, event_id, status, attempt_count,
		       last_status_code, last_error, created_at::text, delivered_at::text
		FROM merchant_webhook_delivery
		WHERE merchant_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, merchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var id, epID, eventType, eventID, status, lastError, createdAt string
		var deliveredAt *string
		var attempts, statusCode int
		if err := rows.Scan(&id, &epID, &eventType, &eventID, &status, &attempts,
			&statusCode, &lastError, &createdAt, &deliveredAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "endpoint_id": epID, "event_type": eventType, "event_id": eventID,
			"status": status, "attempt_count": attempts, "last_status_code": statusCode,
			"last_error": lastError, "created_at": createdAt, "delivered_at": deliveredAt,
		})
	}
	return out, rows.Err()
}

// EnqueueTx 在业务事务内把事件 fan-out 到该商户所有匹配端点。
// 没有端点时插入 0 行，不报错。
func EnqueueTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID,
	eventType, eventID string, payload map[string]any) error {
	body, err := json.Marshal(map[string]any{
		"id":         eventID,
		"type":       eventType,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"data":       payload,
	})
	if err != nil {
		return fmt.Errorf("merchant webhook: marshal payload: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO merchant_webhook_delivery
		    (endpoint_id, merchant_id, event_type, event_id, payload)
		SELECT e.id, e.merchant_id, $2::varchar, $3::varchar, $4::jsonb
		FROM merchant_webhook_endpoint e
		WHERE e.merchant_id = $1
		  AND e.status = 'ACTIVE'
		  AND (cardinality(e.event_types) = 0 OR $2::text = ANY(e.event_types))
		ON CONFLICT (endpoint_id, event_type, event_id) DO NOTHING
	`, merchantID, eventType, eventID, body)
	if err != nil {
		return fmt.Errorf("merchant webhook: enqueue delivery: %w", err)
	}
	return nil
}
