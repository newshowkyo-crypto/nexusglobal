package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

type FreezeCmd struct {
	PayoutRequestID uuid.UUID
	MerchantID      uuid.UUID
	Amount          decimal.Decimal
	FiatCurrency    string
}

type FreezeResult struct {
	PayoutRequestID string
	Status          string
}

type PayoutRepository interface {
	LockBalanceForUpdate(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string) (*repository.Balance, error)
	UpdateBalance(ctx context.Context, tx pgx.Tx, b *repository.Balance) error
	InsertPayoutRequest(ctx context.Context, tx pgx.Tx, params db.InsertPayoutRequestParams) (*db.PayoutRequest, error)
	GetPayoutRequestByIdempotencyKey(ctx context.Context, key string) (*db.PayoutRequest, error)
	InsertOutboxEvent(ctx context.Context, tx pgx.Tx, params db.InsertOutboxEventParams) (*db.TransactionalOutbox, error)
}

type Service struct {
	repo PayoutRepository
	txm  *postgres.TxManager
	pool *pgxpool.Pool
}

func New(repo PayoutRepository, txm *postgres.TxManager, pool *pgxpool.Pool) *Service {
	return &Service{repo: repo, txm: txm, pool: pool}
}

func (s *Service) Freeze(ctx context.Context, cmd FreezeCmd) (FreezeResult, error) {
	var result FreezeResult

	err := s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		bal, err := s.repo.LockBalanceForUpdate(ctx, tx, cmd.MerchantID, cmd.FiatCurrency)
		if err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}
		if bal == nil {
			return apperr.ErrNotFound.WithMessage("no treasury ledger for merchant+currency")
		}

		available := bal.SettledBalance.
			Sub(bal.ReserveBalance).
			Sub(bal.FrozenPayoutBalance).
			Sub(bal.DisputeHoldBalance)

		if available.LessThan(cmd.Amount) {
			return apperr.ErrInsufficientBalance.WithMessage(
				fmt.Sprintf("available=%s, requested=%s", available.String(), cmd.Amount.String()))
		}

		bal.SettledBalance = bal.SettledBalance.Sub(cmd.Amount)
		bal.FrozenPayoutBalance = bal.FrozenPayoutBalance.Add(cmd.Amount)

		if err := s.repo.UpdateBalance(ctx, tx, bal); err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		outboxPayload, _ := json.Marshal(map[string]string{
			"payout_request_id": cmd.PayoutRequestID.String(),
		})
		outboxKey := "freeze:" + cmd.PayoutRequestID.String()
		_, err = s.repo.InsertOutboxEvent(ctx, tx, db.InsertOutboxEventParams{
			AggregateType:  "payout_request",
			AggregateID:    cmd.PayoutRequestID,
			EventType:      "FX_LOCK_REQUESTED",
			Payload:        outboxPayload,
			IdempotencyKey: outboxKey,
		})
		if err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}

		result = FreezeResult{
			PayoutRequestID: cmd.PayoutRequestID.String(),
			Status:          string(domain.StatusBalanceFrozen),
		}
		return nil
	})

	if err != nil {
		return FreezeResult{}, err
	}
	return result, nil
}
