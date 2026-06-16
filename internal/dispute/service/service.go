package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	disputedomain "github.com/nexusglobal/mamr/internal/dispute/domain"
	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
)

type DisputeRepository interface {
	CreateDispute(ctx context.Context, tx pgx.Tx, dispute *disputedomain.PaymentDispute) error
	GetDisputeByID(ctx context.Context, id uuid.UUID) (*disputedomain.PaymentDispute, error)
	GetDisputeByIDAndIdempotencyKey(ctx context.Context, id uuid.UUID, idempotencyKey string) (*disputedomain.PaymentDispute, error)
	GetDisputeByOrderIDAndType(ctx context.Context, orderID string, disputeType disputedomain.DisputeType) (*disputedomain.PaymentDispute, error)
	UpdateDisputeStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status disputedomain.DisputeStatus) error
	GetOrCreateLedgerAccount(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error)
	GetMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error)
	GetPlatformDisputeReserveBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error)
	CreatePlatformDisputeReserveEntry(ctx context.Context, tx pgx.Tx, entry *disputedomain.PlatformDisputeReserveLedger) error
	CreateMerchantNegativeBalance(ctx context.Context, tx pgx.Tx, neg *disputedomain.MerchantNegativeBalance) error
	GetMerchantNegativeBalance(ctx context.Context, merchantID uuid.UUID, currency string) (decimal.Decimal, error)
}

type LedgerService interface {
	CreateTransaction(ctx context.Context, tx pgx.Tx, txn ledgerdomain.Transaction) error
}

type TxManager interface {
	WithinTx(ctx context.Context, fn func(pgx.Tx) error) error
}

type Service struct {
	repo      DisputeRepository
	ledgerSvc LedgerService
	txm       TxManager
}

func New(repo DisputeRepository, ledgerSvc LedgerService, txm TxManager) *Service {
	return &Service{repo: repo, ledgerSvc: ledgerSvc, txm: txm}
}

func (s *Service) RefundConfirmed(ctx context.Context, cmd disputedomain.CreateDisputeCmd) error {
	idempotencyKey := cmd.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = "refund-" + cmd.OrderID
	}

	existing, _ := s.repo.GetDisputeByOrderIDAndType(ctx, cmd.OrderID, disputedomain.DisputeTypeRefund)
	if existing != nil && existing.IdempotencyKey == idempotencyKey {
		return nil
	}

	return s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		dispute := &disputedomain.PaymentDispute{
			ID:                uuid.New(),
			OrderID:           cmd.OrderID,
			MerchantID:        cmd.MerchantID,
			DisputeType:       disputedomain.DisputeTypeRefund,
			Status:            disputedomain.DisputeStatusOpen,
			Amount:            cmd.Amount,
			Currency:          cmd.Currency,
			Reason:            cmd.Reason,
			ProviderDisputeID: cmd.ProviderDisputeID,
			ChargebackFee:     decimal.Zero,
			IdempotencyKey:    idempotencyKey,
		}

		if err := s.repo.CreateDispute(ctx, tx, dispute); err != nil {
			return err
		}

		// 获取 ledger accounts
		settledID, err := s.repo.GetOrCreateLedgerAccount(ctx, tx, cmd.MerchantID, "SETTLED", cmd.Currency)
		if err != nil {
			return err
		}
		acquirerID, err := s.repo.GetOrCreateLedgerAccount(ctx, tx, cmd.MerchantID, "ACQUIRER_RECEIVABLE", cmd.Currency)
		if err != nil {
			return err
		}

		// 构建 ledger entries
		entries := []ledgerdomain.LedgerEntry{
			{
				ID:          uuid.New(),
				AccountID:   settledID,
				Direction:   ledgerdomain.DirectionDebit,
				Amount:      cmd.Amount,
				Description: "Refund debit from settled",
			},
			{
				ID:          uuid.New(),
				AccountID:   acquirerID,
				Direction:   ledgerdomain.DirectionCredit,
				Amount:      cmd.Amount,
				Description: "Refund credit to acquirer",
			},
		}

		txn := ledgerdomain.Transaction{
			ID:              uuid.New(),
			OrderID:         cmd.OrderID,
			Type:            ledgerdomain.TxnTypeOrderRefund,
			TransactionType: ledgerdomain.TxnTypeOrderRefund,
			AssetCode:       cmd.Currency,
			Amount:          cmd.Amount,
			Status:          ledgerdomain.TransactionStatusConfirmed,
			ExternalRef:     "refund-" + cmd.OrderID,
			IdempotencyKey:  idempotencyKey,
			Entries:         entries,
		}

		return s.ledgerSvc.CreateTransaction(ctx, tx, txn)
	})
}

func (s *Service) ChargebackReceived(ctx context.Context, cmd disputedomain.CreateDisputeCmd) error {
	idempotencyKey := cmd.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = "chargeback-" + cmd.OrderID
	}

	existing, _ := s.repo.GetDisputeByOrderIDAndType(ctx, cmd.OrderID, disputedomain.DisputeTypeChargeback)
	if existing != nil && existing.IdempotencyKey == idempotencyKey {
		return nil
	}

	settledBalance, err := s.repo.GetMerchantSettledBalance(ctx, cmd.MerchantID, cmd.Currency)
	if err != nil {
		return err
	}

	return s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		dispute := &disputedomain.PaymentDispute{
			ID:                uuid.New(),
			OrderID:           cmd.OrderID,
			MerchantID:        cmd.MerchantID,
			DisputeType:       disputedomain.DisputeTypeChargeback,
			Status:            disputedomain.DisputeStatusOpen,
			Amount:            cmd.Amount,
			Currency:          cmd.Currency,
			Reason:            cmd.Reason,
			ProviderDisputeID: cmd.ProviderDisputeID,
			ChargebackFee:     cmd.ChargebackFee,
			IdempotencyKey:    idempotencyKey,
		}

		if err := s.repo.CreateDispute(ctx, tx, dispute); err != nil {
			return err
		}

		disputeAmount := cmd.Amount
		feeAmount := cmd.ChargebackFee

		// 获取 ledger accounts
		settledID, err := s.repo.GetOrCreateLedgerAccount(ctx, tx, cmd.MerchantID, "SETTLED", cmd.Currency)
		if err != nil {
			return err
		}
		reserveID, err := s.repo.GetOrCreateLedgerAccount(ctx, tx, cmd.MerchantID, "PLATFORM_DISPUTE_RESERVE", cmd.Currency)
		if err != nil {
			return err
		}
		negID, err := s.repo.GetOrCreateLedgerAccount(ctx, tx, cmd.MerchantID, "MERCHANT_NEGATIVE_BALANCE", cmd.Currency)
		if err != nil {
			return err
		}
		feeID, err := s.repo.GetOrCreateLedgerAccount(ctx, tx, cmd.MerchantID, "FEE", cmd.Currency)
		if err != nil {
			return err
		}

		// 计算实际可从 settled 扣除的金额（不能超过可用余额）
		actualSettledDeduction := decimal.Min(settledBalance, disputeAmount)
		// 本金缺口 = disputeAmount - settled实际扣除金额
		principalShortfall := disputeAmount.Sub(actualSettledDeduction)

		// 构建 chargeback 交易 entries
		chargebackEntries := []ledgerdomain.LedgerEntry{}

		// settled debit（如果有）
		if actualSettledDeduction.GreaterThan(decimal.Zero) {
			chargebackEntries = append(chargebackEntries, ledgerdomain.LedgerEntry{
				ID:          uuid.New(),
				AccountID:   settledID,
				Direction:   ledgerdomain.DirectionDebit,
				Amount:      actualSettledDeduction,
				Description: "Chargeback debit from settled",
			})
		}

		// reserve credit
		chargebackEntries = append(chargebackEntries, ledgerdomain.LedgerEntry{
			ID:          uuid.New(),
			AccountID:   reserveID,
			Direction:   ledgerdomain.DirectionCredit,
			Amount:      disputeAmount,
			Description: "Chargeback credit to reserve",
		})

		// negative_balance debit（如果有缺口）
		if principalShortfall.GreaterThan(decimal.Zero) {
			chargebackEntries = append(chargebackEntries, ledgerdomain.LedgerEntry{
				ID:          uuid.New(),
				AccountID:   negID,
				Direction:   ledgerdomain.DirectionDebit,
				Amount:      principalShortfall,
				Description: "Chargeback debit from negative balance",
			})
		}

		// 构建 chargeback 交易
		chargebackTxn := ledgerdomain.Transaction{
			ID:              uuid.New(),
			IdempotencyKey:  idempotencyKey + "-chargeback",
			TransactionType: ledgerdomain.TxnTypeChargebackReceived,
			Type:            ledgerdomain.TxnTypeChargebackReceived,
			AggregateType:   "order",
			AggregateID:     cmd.OrderID,
			Description:     "Chargeback received for order " + cmd.OrderID,
			Amount:          disputeAmount,
			Entries:         chargebackEntries,
		}

		if err := s.ledgerSvc.CreateTransaction(ctx, tx, chargebackTxn); err != nil {
			return err
		}

		// 计算 fee 部分
		// settled 剩余可用于 fee 的金额 = settledBalance - actualSettledDeduction
		settledRemainingForFee := settledBalance.Sub(actualSettledDeduction)
		if settledRemainingForFee.LessThan(decimal.Zero) {
			settledRemainingForFee = decimal.Zero
		}

		// 交易2: CHARGEBACK_FEE - 费用部分（如果有）
		if feeAmount.GreaterThan(decimal.Zero) {
			actualFeeDeduction := decimal.Min(settledRemainingForFee, feeAmount)
			feeShortfall := feeAmount.Sub(actualFeeDeduction)

			feeEntries := []ledgerdomain.LedgerEntry{}

			// settled debit（如果有）
			if actualFeeDeduction.GreaterThan(decimal.Zero) {
				feeEntries = append(feeEntries, ledgerdomain.LedgerEntry{
					ID:          uuid.New(),
					AccountID:   settledID,
					Direction:   ledgerdomain.DirectionDebit,
					Amount:      actualFeeDeduction,
					Description: "Chargeback fee debit from settled",
				})
			}

			// negative_balance debit（如果有缺口）
			if feeShortfall.GreaterThan(decimal.Zero) {
				feeEntries = append(feeEntries, ledgerdomain.LedgerEntry{
					ID:          uuid.New(),
					AccountID:   negID,
					Direction:   ledgerdomain.DirectionDebit,
					Amount:      feeShortfall,
					Description: "Chargeback fee debit from negative balance",
				})
			}

			// fee credit
			feeEntries = append(feeEntries, ledgerdomain.LedgerEntry{
				ID:          uuid.New(),
				AccountID:   feeID,
				Direction:   ledgerdomain.DirectionCredit,
				Amount:      feeAmount,
				Description: "Chargeback fee credit",
			})

			feeTxn := ledgerdomain.Transaction{
				ID:              uuid.New(),
				IdempotencyKey:  idempotencyKey + "-fee",
				TransactionType: ledgerdomain.TxnTypeChargebackFee,
				Type:            ledgerdomain.TxnTypeChargebackFee,
				AggregateType:   "order",
				AggregateID:     cmd.OrderID,
				Description:     "Chargeback fee for order " + cmd.OrderID,
				Amount:          feeAmount,
				Entries:         feeEntries,
			}

			if err := s.ledgerSvc.CreateTransaction(ctx, tx, feeTxn); err != nil {
				return err
			}
		}

		// 记录 reserve 入账
		reserveEntry := &disputedomain.PlatformDisputeReserveLedger{
			ID:               uuid.New(),
			PaymentDisputeID: dispute.ID,
			MerchantID:       cmd.MerchantID,
			Amount:           disputeAmount,
			Currency:         cmd.Currency,
			Direction:        "CREDIT",
			Description:      "Chargeback received for order " + cmd.OrderID,
		}

		if err := s.repo.CreatePlatformDisputeReserveEntry(ctx, tx, reserveEntry); err != nil {
			return err
		}

		// 记录 negative balance（本金缺口 + fee 缺口）
		totalShortfall := principalShortfall
		if feeAmount.GreaterThan(decimal.Zero) {
			settledRemainingForFee := settledBalance.Sub(actualSettledDeduction)
			if settledRemainingForFee.LessThan(decimal.Zero) {
				settledRemainingForFee = decimal.Zero
			}
			feeShortfall := feeAmount.Sub(decimal.Min(settledRemainingForFee, feeAmount))
			totalShortfall = totalShortfall.Add(feeShortfall)
		}

		if totalShortfall.GreaterThan(decimal.Zero) {
			negDisputeID := dispute.ID
			negativeBalance := &disputedomain.MerchantNegativeBalance{
				ID:         uuid.New(),
				MerchantID: cmd.MerchantID,
				Currency:   cmd.Currency,
				Amount:     totalShortfall,
				DisputeID:  &negDisputeID,
				Reason:     "Chargeback received for order " + cmd.OrderID,
				Status:     "ACTIVE",
			}
			if err := s.repo.CreateMerchantNegativeBalance(ctx, tx, negativeBalance); err != nil {
				return err
			}
		}

		return nil
	})
}

func (s *Service) ChargebackReversed(ctx context.Context, cmd disputedomain.ReverseDisputeCmd) error {
	dispute, err := s.repo.GetDisputeByID(ctx, cmd.DisputeID)
	if err != nil {
		return err
	}

	if dispute.Status != disputedomain.DisputeStatusOpen && dispute.Status != disputedomain.DisputeStatusUnderReview {
		return disputedomain.ErrInvalidDisputeStatus
	}

	reserveBalance, err := s.repo.GetPlatformDisputeReserveBalance(ctx, dispute.MerchantID, dispute.Currency)
	if err != nil {
		return err
	}

	if reserveBalance.LessThan(dispute.Amount) {
		return disputedomain.ErrInsufficientSettled
	}

	return s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.repo.UpdateDisputeStatus(ctx, tx, dispute.ID, disputedomain.DisputeStatusReversed); err != nil {
			return err
		}

		// 获取 ledger accounts
		reserveID, err := s.repo.GetOrCreateLedgerAccount(ctx, tx, dispute.MerchantID, "PLATFORM_DISPUTE_RESERVE", dispute.Currency)
		if err != nil {
			return err
		}
		settledID, err := s.repo.GetOrCreateLedgerAccount(ctx, tx, dispute.MerchantID, "SETTLED", dispute.Currency)
		if err != nil {
			return err
		}

		// 构建 ledger entries（反转 chargeback：reserve debit, settled credit）
		entries := []ledgerdomain.LedgerEntry{
			{
				ID:          uuid.New(),
				AccountID:   reserveID,
				Direction:   ledgerdomain.DirectionDebit,
				Amount:      dispute.Amount,
				Description: "Chargeback reversed debit from reserve",
			},
			{
				ID:          uuid.New(),
				AccountID:   settledID,
				Direction:   ledgerdomain.DirectionCredit,
				Amount:      dispute.Amount,
				Description: "Chargeback reversed credit to settled",
			},
		}

		txn := ledgerdomain.Transaction{
			ID:              uuid.New(),
			IdempotencyKey:  cmd.IdempotencyKey,
			TransactionType: ledgerdomain.TxnTypeChargebackReversed,
			Type:            ledgerdomain.TxnTypeChargebackReversed,
			AggregateType:   "order",
			AggregateID:     dispute.OrderID,
			Description:     "Chargeback reversed for order " + dispute.OrderID,
			Amount:          dispute.Amount,
			Entries:         entries,
		}

		if err := s.ledgerSvc.CreateTransaction(ctx, tx, txn); err != nil {
			return err
		}

		reserveEntry := &disputedomain.PlatformDisputeReserveLedger{
			ID:               uuid.New(),
			PaymentDisputeID: dispute.ID,
			MerchantID:       dispute.MerchantID,
			Amount:           dispute.Amount,
			Currency:         dispute.Currency,
			Direction:        "DEBIT",
			Description:      "Chargeback reversed for order " + dispute.OrderID,
		}

		return s.repo.CreatePlatformDisputeReserveEntry(ctx, tx, reserveEntry)
	})
}
