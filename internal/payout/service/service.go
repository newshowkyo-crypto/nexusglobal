package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
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
	PayoutAddressID uuid.UUID
	TargetAsset     string
	TargetNetwork   string
	IdempotencyKey  string
}

type FreezeResult struct {
	PayoutRequestID string
	Status          string
	Replayed        bool
}

type PayoutRepository interface {
	LockBalanceForUpdate(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string) (*repository.Balance, error)
	UpdateBalance(ctx context.Context, tx pgx.Tx, b *repository.Balance) error
	InsertPayoutRequest(ctx context.Context, tx pgx.Tx, params db.InsertPayoutRequestParams) (*db.PayoutRequest, error)
	GetPayoutRequestByIdempotencyKey(ctx context.Context, key string) (*db.PayoutRequest, error)
	InsertOutboxEvent(ctx context.Context, tx pgx.Tx, params db.InsertOutboxEventParams) (*db.TransactionalOutbox, error)
}

type LedgerRepository interface {
	PostTransaction(ctx context.Context, tx pgx.Tx, txn ledgerdomain.Transaction) (*ledgerdomain.Transaction, error)
}

type Service struct {
	repo       PayoutRepository
	ledgerRepo LedgerRepository
	txm        *postgres.TxManager
	pool       *pgxpool.Pool
}

func New(repo PayoutRepository, ledgerRepo LedgerRepository, txm *postgres.TxManager, pool *pgxpool.Pool) *Service {
	return &Service{repo: repo, ledgerRepo: ledgerRepo, txm: txm, pool: pool}
}

func (s *Service) Freeze(ctx context.Context, cmd FreezeCmd) (FreezeResult, error) {
	existing, err := s.repo.GetPayoutRequestByIdempotencyKey(ctx, cmd.IdempotencyKey)
	if err != nil {
		return FreezeResult{}, err
	}
	if existing != nil {
		return FreezeResult{
			PayoutRequestID: existing.ID.String(),
			Status:          existing.Status,
			Replayed:        true,
		}, nil
	}

	var result FreezeResult

	err = s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
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

		_, err = s.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  "payout_freeze:" + cmd.PayoutRequestID.String(),
			TransactionType: ledgerdomain.TxnTypePayoutFreeze,
			AggregateType:   "payout_request",
			AggregateID:     cmd.PayoutRequestID.String(),
			Description:     fmt.Sprintf("Payout freeze %s %s", cmd.Amount.String(), cmd.FiatCurrency),
			Entries: []ledgerdomain.Entry{
				{AccountID: "settled", Direction: ledgerdomain.DirectionDebit, Amount: cmd.Amount},
				{AccountID: "frozen_payout", Direction: ledgerdomain.DirectionCredit, Amount: cmd.Amount},
			},
		})
		if err != nil {
			return fmt.Errorf("ledger post: %w", err)
		}

		payoutReq, err := s.repo.InsertPayoutRequest(ctx, tx, db.InsertPayoutRequestParams{
			MerchantID:      cmd.MerchantID,
			PayoutAddressID: cmd.PayoutAddressID,
			Amount:          pgtype.Numeric{Int: cmd.Amount.Coefficient(), Exp: -int32(cmd.Amount.Exponent()), Valid: true},
			FiatCurrency:    cmd.FiatCurrency,
			TargetAsset:     cmd.TargetAsset,
			TargetNetwork:   cmd.TargetNetwork,
			Status:          string(domain.StatusBalanceFrozen),
			IdempotencyKey:  cmd.IdempotencyKey,
		})
		if err != nil {
			return fmt.Errorf("insert payout request: %w", err)
		}

		outboxPayload, _ := json.Marshal(map[string]string{
			"payout_request_id": payoutReq.ID.String(),
		})
		outboxKey := "freeze:" + payoutReq.ID.String()
		_, err = s.repo.InsertOutboxEvent(ctx, tx, db.InsertOutboxEventParams{
			AggregateType:  "payout_request",
			AggregateID:    payoutReq.ID,
			EventType:      "FX_LOCK_REQUESTED",
			Payload:        outboxPayload,
			IdempotencyKey: outboxKey,
		})
		if err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}

		result = FreezeResult{
			PayoutRequestID: payoutReq.ID.String(),
			Status:          string(domain.StatusBalanceFrozen),
		}
		return nil
	})

	if err != nil {
		return FreezeResult{}, err
	}
	return result, nil
}
