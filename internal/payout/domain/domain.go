package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/pkg/validator"
)

type PayoutStatus string

const (
	StatusDraft                PayoutStatus = "Draft"
	StatusBalanceFrozen        PayoutStatus = "BalanceFrozen"
	StatusFxLocked             PayoutStatus = "FxLocked"
	StatusLPProcessing         PayoutStatus = "LPProcessing"
	StatusConfirmed            PayoutStatus = "Confirmed"
	StatusReconciled           PayoutStatus = "Reconciled"
	StatusFailed               PayoutStatus = "Failed"
	StatusBalanceReleased      PayoutStatus = "BalanceReleased"
	StatusUnknownExternalState PayoutStatus = "UnknownExternalState"
)

var legalTransitions = map[PayoutStatus][]PayoutStatus{
	StatusDraft:                {StatusBalanceFrozen},
	StatusBalanceFrozen:        {StatusFxLocked, StatusFailed},
	StatusFxLocked:             {StatusLPProcessing, StatusFailed, StatusUnknownExternalState},
	StatusLPProcessing:         {StatusConfirmed, StatusFailed, StatusUnknownExternalState},
	StatusConfirmed:            {StatusReconciled},
	StatusReconciled:           {},
	StatusFailed:               {StatusBalanceReleased},
	StatusBalanceReleased:      {},
	StatusUnknownExternalState: {StatusConfirmed, StatusFailed},
}

func CanTransition(from, to PayoutStatus) bool {
	allowed, ok := legalTransitions[from]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}

func Transition(from, to PayoutStatus) (PayoutStatus, error) {
	if !CanTransition(from, to) {
		return from, apperr.ErrIllegalTransition.WithMessage(
			"cannot transition from " + string(from) + " to " + string(to))
	}
	return to, nil
}

type CleanedAddress struct {
	TargetAddress       string
	AddressHash         string
	TargetAddressMasked string
	IsValid             bool
	ValidationError     string
}

func CleanAddress(rawAddress string, network validator.Network) CleanedAddress {
	addr := strings.TrimSpace(rawAddress)
	addr = removeInvisible(addr)

	hash := sha256.Sum256([]byte(addr))
	addrHash := hex.EncodeToString(hash[:])

	masked := maskAddress(addr)

	valid, msg := validator.ValidateAddress(network, addr)

	return CleanedAddress{
		TargetAddress:       addr,
		AddressHash:         addrHash,
		TargetAddressMasked: masked,
		IsValid:             valid,
		ValidationError:     msg,
	}
}

func removeInvisible(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsControl(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func maskAddress(addr string) string {
	if len(addr) <= 8 {
		return strings.Repeat("*", len(addr))
	}
	return addr[:4] + strings.Repeat("*", len(addr)-8) + addr[len(addr)-4:]
}
