package service

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

type PostCmd struct {
	IdempotencyKey  string
	TransactionType domain.TransactionType
	AggregateType   string
	AggregateID     string
	Description     string
	Entries         []domain.Entry
}

type PostResult struct {
	TransactionID string
	Replayed      bool
}

type Repository interface {
	PostTransaction(ctx context.Context, tx pgx.Tx, txn domain.Transaction) (*domain.Transaction, error)
	GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (*domain.Transaction, error)
}

type Service struct {
	repo Repository
	txm  *postgres.TxManager
	pool *pgxpool.Pool
}

func New(repo Repository, txm *postgres.TxManager, pool *pgxpool.Pool) *Service {
	return &Service{repo: repo, txm: txm, pool: pool}
}

func (s *Service) Post(ctx context.Context, cmd PostCmd) (PostResult, error) {
	if err := domain.ValidateBalanced(cmd.Entries); err != nil {
		return PostResult{}, apperr.ErrInvalidArgument.WithMessage(err.Error())
	}

	existing, err := s.repo.GetTransactionByIdempotencyKey(ctx, cmd.IdempotencyKey)
	if err != nil {
		return PostResult{}, err
	}
	if existing != nil {
		return PostResult{TransactionID: existing.ID, Replayed: true}, nil
	}

	var result PostResult
	err = s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		txn := domain.Transaction{
			IdempotencyKey:  cmd.IdempotencyKey,
			TransactionType: cmd.TransactionType,
			AggregateType:   cmd.AggregateType,
			AggregateID:     cmd.AggregateID,
			Description:     cmd.Description,
			Entries:         cmd.Entries,
		}
		inserted, err := s.repo.PostTransaction(ctx, tx, txn)
		if err != nil {
			return err
		}
		result.TransactionID = inserted.ID
		return nil
	})
	if err != nil {
		return PostResult{}, err
	}

	return result, nil
}

func (s *Service) GetTransactionByIdempotencyKey(ctx context.Context, key string) (*domain.Transaction, error) {
	txn, err := s.repo.GetTransactionByIdempotencyKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if txn == nil {
		return nil, apperr.ErrNotFound.WithMessage("transaction not found")
	}
	return txn, nil
}
