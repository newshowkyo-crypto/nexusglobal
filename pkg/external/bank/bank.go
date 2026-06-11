package bank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrExplicitFailure = errors.New("bank: explicit failure")
	ErrTimeout         = errors.New("bank: timeout")
)

type TransferRequest struct {
	SweepTransferID uuid.UUID
	SourceAccountID uuid.UUID
	TargetAccountID uuid.UUID
	Amount          decimal.Decimal
	Currency        string
	Rail            string
	Reference       string
}

type TransferResult struct {
	BankRef string
	Status  string
}

type TransferStatus struct {
	BankRef string
	Status  string
}

type Client interface {
	SubmitTransfer(ctx context.Context, req TransferRequest) (TransferResult, error)
	QueryTransfer(ctx context.Context, bankRef string) (TransferStatus, error)
}

type Scenario string

const (
	ScenarioSuccess Scenario = "success"
	ScenarioFailure Scenario = "failure"
	ScenarioTimeout Scenario = "timeout"
)

type MockBank struct {
	defaultScenario Scenario
	scenarios       map[string]Scenario
}

func NewMockBank() *MockBank {
	return &MockBank{
		defaultScenario: ScenarioSuccess,
		scenarios:       make(map[string]Scenario),
	}
}

func (m *MockBank) SetScenario(key string, s Scenario) {
	m.scenarios[key] = s
}

func (m *MockBank) SetDefaultScenario(s Scenario) {
	m.defaultScenario = s
}

func (m *MockBank) scenarioFor(key string) Scenario {
	if s, ok := m.scenarios[key]; ok {
		return s
	}
	return m.defaultScenario
}

func deterministicID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (m *MockBank) SubmitTransfer(ctx context.Context, req TransferRequest) (TransferResult, error) {
	scenario := m.scenarioFor(req.SweepTransferID.String())

	switch scenario {
	case ScenarioTimeout:
		return TransferResult{}, ErrTimeout
	case ScenarioFailure:
		return TransferResult{}, ErrExplicitFailure
	default:
		ref := deterministicID("bank", req.SweepTransferID.String())
		return TransferResult{
			BankRef: ref,
			Status:  "submitted",
		}, nil
	}
}

func (m *MockBank) QueryTransfer(ctx context.Context, bankRef string) (TransferStatus, error) {
	scenario := m.scenarioFor(bankRef)

	switch scenario {
	case ScenarioTimeout:
		return TransferStatus{}, ErrTimeout
	case ScenarioFailure:
		return TransferStatus{}, ErrExplicitFailure
	default:
		return TransferStatus{
			BankRef: bankRef,
			Status:  "confirmed",
		}, nil
	}
}
