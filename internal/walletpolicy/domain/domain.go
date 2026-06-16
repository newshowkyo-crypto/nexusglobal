package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

var (
	ErrAddressNotAllowlisted     = apperr.New("ADDRESS_NOT_ALLOWLISTED", 403)
	ErrWrongNetwork              = apperr.New("WRONG_NETWORK", 403)
	ErrExceedsSingleLimit        = apperr.New("EXCEEDS_SINGLE_LIMIT", 403)
	ErrExceedsDailyLimit         = apperr.New("EXCEEDS_DAILY_LIMIT", 403)
	ErrDuplicateTxHash           = apperr.New("DUPLICATE_TX_HASH", 409)
	ErrInsufficientConfirmations = apperr.New("INSUFFICIENT_CONFIRMATIONS", 403)
)

type Network string

const (
	NetworkEthereum Network = "ETHEREUM"
	NetworkPolygon  Network = "POLYGON"
	NetworkArbitrum Network = "ARBITRUM"
	NetworkSolana   Network = "SOLANA"
)

type AllowlistedAddress struct {
	ID          uuid.UUID
	Address     string
	Network     Network
	AssetCode   string
	Description string
	CreatedAt   time.Time
}

type TransitWalletPolicy struct {
	ID               uuid.UUID
	MerchantID       uuid.UUID
	Network          Network
	AssetCode        string
	SingleLimitMinor int64
	DailyLimitMinor  int64
	MinConfirmations int
	AllowListEnabled bool
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type WithdrawalRequest struct {
	ID            uuid.UUID
	MerchantID    uuid.UUID
	Network       Network
	AssetCode     string
	Address       string
	AmountMinor   int64
	TxHash        string
	Confirmations int
	Status        string
	SubmittedAt   time.Time
}

func (p *TransitWalletPolicy) ValidateAddress(address string) error {
	if !p.AllowListEnabled {
		return nil
	}
	return ErrAddressNotAllowlisted
}

func (p *TransitWalletPolicy) ValidateNetwork(network Network) error {
	if p.Network != network {
		return ErrWrongNetwork
	}
	return nil
}

func (p *TransitWalletPolicy) ValidateSingleLimit(amountMinor int64) error {
	if amountMinor > p.SingleLimitMinor {
		return ErrExceedsSingleLimit
	}
	return nil
}

func (p *TransitWalletPolicy) ValidateDailyLimit(dailyTotalMinor int64, amountMinor int64) error {
	if dailyTotalMinor+amountMinor > p.DailyLimitMinor {
		return ErrExceedsDailyLimit
	}
	return nil
}

func (p *TransitWalletPolicy) ValidateConfirmations(confirmations int) error {
	if confirmations < p.MinConfirmations {
		return ErrInsufficientConfirmations
	}
	return nil
}
