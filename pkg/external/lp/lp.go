package lp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrExplicitFailure = errors.New("lp: explicit failure")
	ErrTimeout         = errors.New("lp: timeout")
)

type FXLockRequest struct {
	PayoutRequestID uuid.UUID
	Amount          decimal.Decimal
	FiatCurrency    string
	TargetAsset     string
	TargetNetwork   string
}

type FXLockResult struct {
	LockID    string
	Rate      decimal.Decimal
	ExpiresAt time.Time
}

type PayoutRequest struct {
	PayoutRequestID uuid.UUID
	LockID          string
	Amount          decimal.Decimal
	FiatCurrency    string
	TargetAsset     string
	TargetAddress   string
	TargetNetwork   string
}

type PayoutResult struct {
	ProviderRef string
	Status      string
}

type StatusResult struct {
	ProviderRef string
	Status      string
	Amount      decimal.Decimal
}

type Client interface {
	FXLock(ctx context.Context, req FXLockRequest) (FXLockResult, error)
	Payout(ctx context.Context, req PayoutRequest) (PayoutResult, error)
	QueryStatus(ctx context.Context, providerRef string) (StatusResult, error)
}

type Scenario string

const (
	ScenarioSuccess Scenario = "success"
	ScenarioFailure Scenario = "failure"
	ScenarioTimeout Scenario = "timeout"
	ScenarioDelay   Scenario = "delay"
)

type MockLP struct {
	defaultScenario Scenario
	scenarios       map[string]Scenario
	delay           time.Duration
}

func NewMockLP() *MockLP {
	return &MockLP{
		defaultScenario: ScenarioSuccess,
		scenarios:       make(map[string]Scenario),
	}
}

func (m *MockLP) SetScenario(key string, s Scenario) {
	m.scenarios[key] = s
}

func (m *MockLP) SetDefaultScenario(s Scenario) {
	m.defaultScenario = s
}

func (m *MockLP) SetDelay(d time.Duration) {
	m.delay = d
}

func (m *MockLP) scenarioFor(key string) Scenario {
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

func (m *MockLP) FXLock(ctx context.Context, req FXLockRequest) (FXLockResult, error) {
	scenario := m.scenarioFor(req.PayoutRequestID.String())

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return FXLockResult{}, ErrTimeout
		}
	}

	switch scenario {
	case ScenarioTimeout:
		return FXLockResult{}, ErrTimeout
	case ScenarioFailure:
		return FXLockResult{}, ErrExplicitFailure
	default:
		lockID := deterministicID("fxlock", req.PayoutRequestID.String())
		return FXLockResult{
			LockID:    lockID,
			Rate:      decimal.RequireFromString("1.05"),
			ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		}, nil
	}
}

func (m *MockLP) Payout(ctx context.Context, req PayoutRequest) (PayoutResult, error) {
	scenario := m.scenarioFor(req.PayoutRequestID.String())

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return PayoutResult{}, ErrTimeout
		}
	}

	switch scenario {
	case ScenarioTimeout:
		return PayoutResult{}, ErrTimeout
	case ScenarioFailure:
		return PayoutResult{}, ErrExplicitFailure
	default:
		ref := deterministicID("payout", req.PayoutRequestID.String())
		return PayoutResult{
			ProviderRef: ref,
			Status:      "submitted",
		}, nil
	}
}

func (m *MockLP) QueryStatus(ctx context.Context, providerRef string) (StatusResult, error) {
	scenario := m.scenarioFor(providerRef)

	switch scenario {
	case ScenarioTimeout:
		return StatusResult{}, ErrTimeout
	case ScenarioFailure:
		return StatusResult{}, ErrExplicitFailure
	default:
		return StatusResult{
			ProviderRef: providerRef,
			Status:      "confirmed",
		}, nil
	}
}
