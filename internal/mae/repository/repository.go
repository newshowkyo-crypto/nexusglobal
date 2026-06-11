package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/mae/repository/db"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) InsertConfig(ctx context.Context, params db.InsertMAEConfigParams) (*db.MaeConfiguration, error) {
	q := db.New(r.pool)
	row, err := q.InsertMAEConfig(ctx, params)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetConfig(ctx context.Context, id uuid.UUID) (*db.MaeConfiguration, error) {
	q := db.New(r.pool)
	row, err := q.GetMAEConfig(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) GetConfigByMAEID(ctx context.Context, maeID string) (*db.MaeConfiguration, error) {
	q := db.New(r.pool)
	row, err := q.GetMAEConfigByMAEID(ctx, maeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ListConfigs(ctx context.Context) ([]db.MaeConfiguration, error) {
	q := db.New(r.pool)
	return q.ListMAEConfigs(ctx)
}

func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, status, reason string, version int32) error {
	q := db.New(r.pool)
	return q.UpdateMAEStatus(ctx, db.UpdateMAEStatusParams{
		ID:           id,
		Status:       status,
		StatusReason: reason,
		Version:      version,
	})
}

func (r *Repository) RotateSecret(ctx context.Context, id uuid.UUID, apiKeyCiphertext, webhookSecretCiphertext []byte, version int32) error {
	q := db.New(r.pool)
	return q.RotateMAESecret(ctx, db.RotateMAESecretParams{
		ID:                      id,
		ApiKeyCiphertext:        apiKeyCiphertext,
		WebhookSecretCiphertext: webhookSecretCiphertext,
		Version:                 version,
	})
}
