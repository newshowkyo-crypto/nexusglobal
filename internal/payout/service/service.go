package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/pkg/validator"
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
	GetPayoutRequest(ctx context.Context, id uuid.UUID) (*db.PayoutRequest, error)
	GetPayoutRequestByIdempotencyKey(ctx context.Context, key string) (*db.PayoutRequest, error)
	GetPayoutAddress(ctx context.Context, id uuid.UUID) (*db.MerchantPayoutAddress, error)
	InsertOutboxEvent(ctx context.Context, tx pgx.Tx, params db.InsertOutboxEventParams) (*db.TransactionalOutbox, error)
	InsertPayoutAddress(ctx context.Context, params db.InsertPayoutAddressParams) (*db.MerchantPayoutAddress, error)
	InsertReserveBatch(ctx context.Context, params db.InsertReserveBatchParams) (*db.ReserveBatch, error)
	ListDueReserveBatches(ctx context.Context, limit int32) ([]db.ReserveBatch, error)
	UpdateReserveBatchStatus(ctx context.Context, id uuid.UUID, status string) error
}

type LedgerRepository interface {
	PostTransaction(ctx context.Context, tx pgx.Tx, txn ledgerdomain.Transaction) (*ledgerdomain.Transaction, error)
	GetOrCreateAccount(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error)
	GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (*ledgerdomain.Transaction, error)
}

type Service struct {
	repo       PayoutRepository
	ledgerRepo LedgerRepository
	encryptor  *crypto.Encryptor
	txm        *postgres.TxManager
	pool       *pgxpool.Pool
}

func New(repo PayoutRepository, ledgerRepo LedgerRepository, encryptor *crypto.Encryptor, txm *postgres.TxManager, pool *pgxpool.Pool) *Service {
	return &Service{repo: repo, ledgerRepo: ledgerRepo, encryptor: encryptor, txm: txm, pool: pool}
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

		payoutReq, err := s.repo.InsertPayoutRequest(ctx, tx, db.InsertPayoutRequestParams{
			MerchantID:      cmd.MerchantID,
			PayoutAddressID: cmd.PayoutAddressID,
			Amount:          decimalToNumeric(cmd.Amount),
			FiatCurrency:    cmd.FiatCurrency,
			TargetAsset:     cmd.TargetAsset,
			TargetNetwork:   cmd.TargetNetwork,
			Status:          string(domain.StatusBalanceFrozen),
			IdempotencyKey:  cmd.IdempotencyKey,
		})
		if err != nil {
			return fmt.Errorf("insert payout request: %w", err)
		}

		// 获取或创建 ledger accounts
		settledAccID, err := s.ledgerRepo.GetOrCreateAccount(ctx, tx, cmd.MerchantID, "SETTLED", cmd.FiatCurrency)
		if err != nil {
			return fmt.Errorf("get settled account: %w", err)
		}
		frozenAccID, err := s.ledgerRepo.GetOrCreateAccount(ctx, tx, cmd.MerchantID, "FROZEN_PAYOUT", cmd.FiatCurrency)
		if err != nil {
			return fmt.Errorf("get frozen account: %w", err)
		}

		_, err = s.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  "payout_freeze:" + payoutReq.ID.String(),
			TransactionType: ledgerdomain.TransactionType("PAYOUT_FREEZE"),
			AggregateType:   "payout_request",
			AggregateID:     payoutReq.ID.String(),
			Description:     fmt.Sprintf("Payout freeze %s %s", cmd.Amount.String(), cmd.FiatCurrency),
			Amount:          cmd.Amount,
			Entries: []ledgerdomain.Entry{
				{AccountID: settledAccID, Direction: ledgerdomain.DirectionDebit, Amount: cmd.Amount},
				{AccountID: frozenAccID, Direction: ledgerdomain.DirectionCredit, Amount: cmd.Amount},
			},
		})
		if err != nil {
			return fmt.Errorf("ledger post: %w", err)
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

type CreateAddressCmd struct {
	MerchantID        uuid.UUID
	TargetAsset       string
	TargetNetwork     string
	AddressFormatType string
	TargetAddress     string
	AddressLabel      string
}

type CreateAddressResult struct {
	ID                  string
	TargetAddressMasked string
	ScreeningStatus     string
	WhitelistStatus     string
}

func (s *Service) CreateAddress(ctx context.Context, cmd CreateAddressCmd) (CreateAddressResult, error) {
	network := validator.Network(cmd.TargetNetwork)
	cleaned := domain.CleanAddress(cmd.TargetAddress, network)
	if !cleaned.IsValid {
		return CreateAddressResult{}, apperr.ErrInvalidArgument.WithMessage(cleaned.ValidationError)
	}

	var encBytes []byte
	if s.encryptor != nil {
		enc, err := s.encryptor.Encrypt(ctx, []byte(cleaned.TargetAddress), crypto.Meta{
			TenantID:  cmd.MerchantID.String(),
			RecordID:  "payout_address",
			FieldName: "target_address",
		})
		if err != nil {
			return CreateAddressResult{}, fmt.Errorf("encrypt address: %w", err)
		}
		encBytes = enc
	} else {
		encBytes = []byte(cleaned.TargetAddress)
	}

	addr, err := s.repo.InsertPayoutAddress(ctx, db.InsertPayoutAddressParams{
		MerchantID:          cmd.MerchantID,
		TargetAsset:         cmd.TargetAsset,
		TargetNetwork:       cmd.TargetNetwork,
		AddressFormatType:   cmd.AddressFormatType,
		TargetAddressEnc:    encBytes,
		TargetAddressHash:   cleaned.AddressHash,
		TargetAddressMasked: cleaned.TargetAddressMasked,
		AddressLabel:        cmd.AddressLabel,
	})
	if err != nil {
		return CreateAddressResult{}, fmt.Errorf("insert address: %w", err)
	}

	return CreateAddressResult{
		ID:                  addr.ID.String(),
		TargetAddressMasked: addr.TargetAddressMasked,
		ScreeningStatus:     addr.ScreeningStatus,
		WhitelistStatus:     addr.WhitelistStatus,
	}, nil
}

type CreateRequestCmd struct {
	MerchantID      uuid.UUID
	PayoutAddressID uuid.UUID
	Amount          decimal.Decimal
	FiatCurrency    string
	TargetAsset     string
	TargetNetwork   string
	IdempotencyKey  string
	PayloadHash     string // R2-1: 用于幂等冲突检测
}

func (s *Service) CreatePayoutRequest(ctx context.Context, cmd CreateRequestCmd) (uuid.UUID, error) {
	existing, err := s.repo.GetPayoutRequestByIdempotencyKey(ctx, cmd.IdempotencyKey)
	if err != nil {
		return uuid.Nil, err
	}
	if existing != nil {
		// R2-1: 同 key 不同 payload 返回 409
		if existing.PayloadHash != "" && cmd.PayloadHash != "" && existing.PayloadHash != cmd.PayloadHash {
			return uuid.Nil, apperr.ErrIdempotencyConflict.WithMessage("idempotency key conflict: different payload")
		}
		return existing.ID, nil
	}

	// G1: 校验地址归属
	if cmd.PayoutAddressID != uuid.Nil {
		addr, err := s.repo.GetPayoutAddress(ctx, cmd.PayoutAddressID)
		if err != nil {
			return uuid.Nil, err
		}
		if addr == nil {
			return uuid.Nil, apperr.ErrNotFound.WithMessage("payout address not found")
		}
		if addr.MerchantID != cmd.MerchantID {
			return uuid.Nil, apperr.ErrForbidden.WithMessage("payout address does not belong to merchant")
		}

		// G7: 校验地址 screening/whitelist 状态
		if addr.ScreeningStatus != "Passed" {
			return uuid.Nil, apperr.New("ADDRESS_NOT_SCREENED", 422).WithMessage("address screening not passed")
		}
		if addr.WhitelistStatus != "Active" {
			return uuid.Nil, apperr.New("ADDRESS_NOT_WHITELISTED", 422).WithMessage("address not whitelisted")
		}
	}

	payoutReq, err := s.repo.InsertPayoutRequest(ctx, nil, db.InsertPayoutRequestParams{
		MerchantID:      cmd.MerchantID,
		PayoutAddressID: cmd.PayoutAddressID,
		Amount:          decimalToNumeric(cmd.Amount),
		FiatCurrency:    cmd.FiatCurrency,
		TargetAsset:     cmd.TargetAsset,
		TargetNetwork:   cmd.TargetNetwork,
		Status:          string(domain.StatusDraft),
		IdempotencyKey:  cmd.IdempotencyKey,
		PayloadHash:     cmd.PayloadHash, // R2-1: 写入 payload hash
	})
	if err != nil {
		return uuid.Nil, err
	}
	return payoutReq.ID, nil
}

func (s *Service) GetPayoutRequest(ctx context.Context, id uuid.UUID) (*db.PayoutRequest, error) {
	return s.repo.GetPayoutRequest(ctx, id)
}

// decimalToNumeric converts a decimal to pgtype.Numeric for DB writes.
// Delegates to money.DecimalToNumeric (StringFixed(4)); a previous hand-rolled
// version negated the exponent and corrupted every fractional amount.
func decimalToNumeric(d decimal.Decimal) pgtype.Numeric {
	return money.DecimalToNumeric(d)
}

type ReserveHoldCmd struct {
	MerchantID     uuid.UUID
	Amount         decimal.Decimal
	FiatCurrency   string
	IdempotencyKey string
}

type ReserveHoldResult struct {
	Replayed bool
}

func (s *Service) ReserveHold(ctx context.Context, cmd ReserveHoldCmd) (ReserveHoldResult, error) {
	existing, err := s.ledgerRepo.GetTransactionByIdempotencyKey(ctx, cmd.IdempotencyKey)
	if err != nil {
		return ReserveHoldResult{}, err
	}
	if existing != nil {
		return ReserveHoldResult{Replayed: true}, nil
	}

	err = s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		bal, err := s.repo.LockBalanceForUpdate(ctx, tx, cmd.MerchantID, cmd.FiatCurrency)
		if err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}
		if bal == nil {
			return apperr.ErrNotFound.WithMessage("no treasury ledger")
		}

		available := bal.SettledBalance.Sub(bal.ReserveBalance).Sub(bal.FrozenPayoutBalance).Sub(bal.DisputeHoldBalance)
		if available.LessThan(cmd.Amount) {
			return apperr.ErrInsufficientBalance.WithMessage("insufficient available for reserve hold")
		}

		_, err = s.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  cmd.IdempotencyKey,
			TransactionType: ledgerdomain.TransactionType("RESERVE_HOLD"),
			AggregateType:   "merchant",
			AggregateID:     cmd.MerchantID.String(),
			Description:     fmt.Sprintf("Reserve hold %s %s", cmd.Amount.String(), cmd.FiatCurrency),
			Amount:          cmd.Amount,
		})
		if err != nil {
			return err
		}

		bal.ReserveBalance = bal.ReserveBalance.Add(cmd.Amount)
		bal.AvailablePayoutBalance = bal.SettledBalance.Sub(bal.ReserveBalance).Sub(bal.FrozenPayoutBalance).Sub(bal.DisputeHoldBalance)
		return s.repo.UpdateBalance(ctx, tx, bal)
	})

	return ReserveHoldResult{}, err
}

type ReserveReleaseCmd struct {
	MerchantID     uuid.UUID
	Amount         decimal.Decimal
	FiatCurrency   string
	IdempotencyKey string
}

type ReserveReleaseResult struct {
	Replayed bool
}

func (s *Service) ReserveRelease(ctx context.Context, cmd ReserveReleaseCmd) (ReserveReleaseResult, error) {
	existing, err := s.ledgerRepo.GetTransactionByIdempotencyKey(ctx, cmd.IdempotencyKey)
	if err != nil {
		return ReserveReleaseResult{}, err
	}
	if existing != nil {
		return ReserveReleaseResult{Replayed: true}, nil
	}

	err = s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		bal, err := s.repo.LockBalanceForUpdate(ctx, tx, cmd.MerchantID, cmd.FiatCurrency)
		if err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}
		if bal == nil {
			return apperr.ErrNotFound.WithMessage("no treasury ledger")
		}

		if bal.ReserveBalance.LessThan(cmd.Amount) {
			return apperr.ErrInsufficientBalance.WithMessage("reserve balance insufficient for release")
		}

		_, err = s.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  cmd.IdempotencyKey,
			TransactionType: ledgerdomain.TransactionType("RESERVE_RELEASE"),
			AggregateType:   "merchant",
			AggregateID:     cmd.MerchantID.String(),
			Description:     fmt.Sprintf("Reserve release %s %s", cmd.Amount.String(), cmd.FiatCurrency),
			Amount:          cmd.Amount.Neg(), // 释放是负数
		})
		if err != nil {
			return err
		}

		bal.ReserveBalance = bal.ReserveBalance.Sub(cmd.Amount)
		bal.SettledBalance = bal.SettledBalance.Add(cmd.Amount)
		bal.AvailablePayoutBalance = bal.SettledBalance.Sub(bal.ReserveBalance).Sub(bal.FrozenPayoutBalance).Sub(bal.DisputeHoldBalance)
		return s.repo.UpdateBalance(ctx, tx, bal)
	})

	return ReserveReleaseResult{}, err
}

type CreateReserveBatchCmd struct {
	MerchantID     uuid.UUID
	Amount         decimal.Decimal
	FiatCurrency   string
	HoldDays       int
	IdempotencyKey string
}

func (s *Service) CreateReserveBatch(ctx context.Context, cmd CreateReserveBatchCmd) (*db.ReserveBatch, error) {
	now := time.Now()
	releaseDate := now.AddDate(0, 0, cmd.HoldDays)
	return s.repo.InsertReserveBatch(ctx, db.InsertReserveBatchParams{
		MerchantID:     cmd.MerchantID,
		Currency:       cmd.FiatCurrency,
		AmountMinor:    cmd.Amount.IntPart(),
		HoldDate:       pgtype.Date{Time: now, Valid: true},
		ReleaseDate:    pgtype.Date{Time: releaseDate, Valid: true},
		IdempotencyKey: cmd.IdempotencyKey,
	})
}

func (s *Service) ProcessDueReserveBatches(ctx context.Context) (int, error) {
	batches, err := s.repo.ListDueReserveBatches(ctx, 100)
	if err != nil {
		return 0, err
	}

	processed := 0
	for _, batch := range batches {
		amount := decimal.NewFromInt(batch.AmountMinor)
		releaseKey := "reserve-auto-release:" + batch.ID.String()

		_, err := s.ReserveRelease(ctx, ReserveReleaseCmd{
			MerchantID:     batch.MerchantID,
			Amount:         amount,
			FiatCurrency:   batch.Currency,
			IdempotencyKey: releaseKey,
		})
		if err != nil {
			continue
		}

		if err := s.repo.UpdateReserveBatchStatus(ctx, batch.ID, "Released"); err != nil {
			continue
		}
		processed++
	}
	return processed, nil
}
