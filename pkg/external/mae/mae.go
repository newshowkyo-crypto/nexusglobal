package mae

import (
	"context"
	"errors"
)

var (
	ErrExplicitFailure = errors.New("mae: explicit failure")
	ErrTimeout         = errors.New("mae: timeout")
)

type Health struct {
	Status  string
	Latency int
}

type Client interface {
	HealthCheck(ctx context.Context, maeID string) (Health, error)
}

type Scenario string

const (
	ScenarioSuccess Scenario = "success"
	ScenarioFailure Scenario = "failure"
	ScenarioTimeout Scenario = "timeout"
)

type MockMAE struct {
	defaultScenario Scenario
	scenarios       map[string]Scenario
}

func NewMockMAE() *MockMAE {
	return &MockMAE{
		defaultScenario: ScenarioSuccess,
		scenarios:       make(map[string]Scenario),
	}
}

func (m *MockMAE) SetScenario(key string, s Scenario) {
	m.scenarios[key] = s
}

func (m *MockMAE) SetDefaultScenario(s Scenario) {
	m.defaultScenario = s
}

func (m *MockMAE) scenarioFor(key string) Scenario {
	if s, ok := m.scenarios[key]; ok {
		return s
	}
	return m.defaultScenario
}

func (m *MockMAE) HealthCheck(ctx context.Context, maeID string) (Health, error) {
	scenario := m.scenarioFor(maeID)

	switch scenario {
	case ScenarioTimeout:
		return Health{}, ErrTimeout
	case ScenarioFailure:
		return Health{}, ErrExplicitFailure
	default:
		return Health{
			Status:  "healthy",
			Latency: 50,
		}, nil
	}
}
