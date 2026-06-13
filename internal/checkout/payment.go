package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrPaymentFailed  = errors.New("payment: explicit failure")
	ErrPaymentTimeout = errors.New("payment: timeout")
)

type PaymentRequest struct {
	OrderID    string
	MerchantID string
	Amount     decimal.Decimal
	Currency   string
	CardNumber string // 不落库，仅传给支付网关
	CardExpiry string
	CardCVV    string
}

type PaymentResult struct {
	TransactionID string
	Status        string
	ProviderRef   string
}

type PaymentGateway interface {
	Charge(ctx context.Context, req PaymentRequest) (*PaymentResult, error)
}

// MockPaymentGateway 模拟支付网关，支持成功/失败/超时三态
type MockPaymentGateway struct {
	defaultScenario Scenario
	scenarios       map[string]Scenario
}

type Scenario string

const (
	ScenarioSuccess Scenario = "success"
	ScenarioFailure Scenario = "failure"
	ScenarioTimeout Scenario = "timeout"
)

func NewMockPaymentGateway() *MockPaymentGateway {
	return &MockPaymentGateway{
		defaultScenario: ScenarioSuccess,
		scenarios:       make(map[string]Scenario),
	}
}

func (m *MockPaymentGateway) SetScenario(key string, s Scenario) {
	m.scenarios[key] = s
}

func (m *MockPaymentGateway) SetDefaultScenario(s Scenario) {
	m.defaultScenario = s
}

func (m *MockPaymentGateway) scenarioFor(key string) Scenario {
	if s, ok := m.scenarios[key]; ok {
		return s
	}
	return m.defaultScenario
}

func (m *MockPaymentGateway) Charge(ctx context.Context, req PaymentRequest) (*PaymentResult, error) {
	scenario := m.scenarioFor(req.OrderID)

	switch scenario {
	case ScenarioTimeout:
		time.Sleep(35 * time.Second) // 超过 30s 超时
		return nil, ErrPaymentTimeout
	case ScenarioFailure:
		return nil, ErrPaymentFailed
	default:
		return &PaymentResult{
			TransactionID: "txn_" + req.OrderID,
			Status:        "captured",
			ProviderRef:   "ref_" + req.OrderID,
		}, nil
	}
}
