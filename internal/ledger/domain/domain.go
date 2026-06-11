package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

type AccountType string

const (
	AccountTypeSettled        AccountType = "SETTLED"
	AccountTypePending        AccountType = "PENDING"
	AccountTypeReserve        AccountType = "RESERVE"
	AccountTypeFrozenPayout   AccountType = "FROZEN_PAYOUT"
	AccountTypeDisputeHold    AccountType = "DISPUTE_HOLD"
	AccountTypeExternalPayout AccountType = "EXTERNAL_PAYOUT_SETTLEMENT"
)

type TransactionType string

const (
	TxnTypePayoutFreeze  TransactionType = "PAYOUT_FREEZE"
	TxnTypePayoutConfirm TransactionType = "PAYOUT_CONFIRM"
	TxnTypePayoutRelease TransactionType = "PAYOUT_RELEASE"
	TxnTypeReversal      TransactionType = "REVERSAL"
)

type Entry struct {
	AccountID string
	Direction Direction
	Amount    decimal.Decimal
}

type Transaction struct {
	ID              string
	IdempotencyKey  string
	TransactionType TransactionType
	AggregateType   string
	AggregateID     string
	Description     string
	Entries         []Entry
}

var (
	ErrImbalanced       = errors.New("ledger: debit and credit totals must be equal")
	ErrZeroAmount       = errors.New("ledger: entry amount must be positive")
	ErrInvalidDirection = errors.New("ledger: direction must be DEBIT or CREDIT")
	ErrNoEntries        = errors.New("ledger: transaction must have at least one entry")
	ErrOddEntries       = errors.New("ledger: transaction must have even number of entries (paired debit+credit)")
)

func ValidateBalanced(entries []Entry) error {
	if len(entries) == 0 {
		return ErrNoEntries
	}
	if len(entries)%2 != 0 {
		return ErrOddEntries
	}

	debitTotal := decimal.Zero
	creditTotal := decimal.Zero

	for i, e := range entries {
		if e.Amount.LessThanOrEqual(decimal.Zero) {
			return fmt.Errorf("%w: entry[%d]", ErrZeroAmount, i)
		}
		switch e.Direction {
		case DirectionDebit:
			debitTotal = debitTotal.Add(e.Amount)
		case DirectionCredit:
			creditTotal = creditTotal.Add(e.Amount)
		default:
			return fmt.Errorf("%w: entry[%d] got %q", ErrInvalidDirection, i, e.Direction)
		}
	}

	if !debitTotal.Equal(creditTotal) {
		return fmt.Errorf("%w: debit=%s credit=%s", ErrImbalanced, debitTotal.String(), creditTotal.String())
	}

	return nil
}
