package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/webhook/repository/db"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) InsertIngressEvent(ctx context.Context, params db.InsertIngressEventParams) (*db.WebhookIngressEvent, error) {
	q := db.New(r.pool)
	row, err := q.InsertIngressEvent(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetIngressEvent(ctx context.Context, id uuid.UUID) (*db.WebhookIngressEvent, error) {
	q := db.New(r.pool)
	row, err := q.GetIngressEvent(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetIngressEventByIdempotencyKey(ctx context.Context, key string) (*db.WebhookIngressEvent, error) {
	q := db.New(r.pool)
	row, err := q.GetIngressEventByIdempotencyKey(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, status, errorMessage string) error {
	q := db.New(r.pool)
	return q.UpdateIngressEventStatus(ctx, db.UpdateIngressEventStatusParams{
		ID:           id,
		Status:       status,
		ErrorMessage: errorMessage,
	})
}

func (r *Repository) GetReceiverProxy(ctx context.Context, providerType, providerID string) (*db.WebhookReceiverProxy, error) {
	q := db.New(r.pool)
	row, err := q.GetReceiverProxy(ctx, db.GetReceiverProxyParams{
		ProviderType: providerType,
		ProviderID:   providerID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ListIngressEvents(ctx context.Context, providerType string, limit int) ([]db.WebhookIngressEvent, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, provider_type, provider_id, provider_event_id, event_type, 
		        idempotency_key, payload_hash, raw_payload_ciphertext, status, 
		        error_message, created_at, updated_at
		 FROM webhook_ingress_event 
		 WHERE provider_type = $1 
		 ORDER BY created_at DESC 
		 LIMIT $2`,
		providerType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []db.WebhookIngressEvent
	for rows.Next() {
		var e db.WebhookIngressEvent
		if err := rows.Scan(
			&e.ID, &e.ProviderType, &e.ProviderID, &e.ProviderEventID,
			&e.EventType, &e.IdempotencyKey, &e.PayloadHash,
			&e.RawPayloadCiphertext, &e.Status, &e.ErrorMessage,
			&e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
