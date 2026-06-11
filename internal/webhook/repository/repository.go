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
