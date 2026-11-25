package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/shopspring/decimal"
)

var (
	ErrPaymentFailed            = errors.New("payment: explicit failure")
	ErrPaymentTimeout           = errors.New("payment: timeout")
	ErrSystemLockdownFailClosed = errors.New("SYSTEM_LOCKDOWN_FAIL_CLOSED")
)

type PaymentRequest struct {
	OrderID                string
	MerchantID             string
	AcquirerChannelID      uuid.UUID // P0-3: 唯一权威通道 ID，由 SelectChannel 选择并写入 attempt
	Amount                 decimal.Decimal
	Currency               string
	CardNumber             string // 不落库，仅传给 mock 支付网关（local/mock 模式）
	CardExpiry             string
	CardCVV                string // 不落库，仅传给 mock 支付网关（local/mock 模式）
	CardBIN                string // 卡 BIN 用于风控
	BuyerIP                string // 买家 IP 用于风控
	PaymentMethodToken     string // tokenized payment method（real 模式必须使用）
	ProviderIdempotencyKey string // B3: Provider 幂等键，禁止每次重试重新生成
}

type PaymentResult struct {
	// TransactionID 渠道侧交易单号（Stripe pi_* / Nxtapay nx_tx_* / Mock txn_*），
	// 落库到 payment_attempts.provider_transaction_id；不是本系统内部 UUID，
	// 内部关联一律使用 payment_attempt ID。
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
	timeoutDelay    time.Duration
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
		timeoutDelay:    35 * time.Second,
	}
}

func (m *MockPaymentGateway) SetScenario(key string, s Scenario) {
	m.scenarios[key] = s
}

func (m *MockPaymentGateway) SetDefaultScenario(s Scenario) {
	m.defaultScenario = s
}

func (m *MockPaymentGateway) SetTimeoutDelay(d time.Duration) {
	m.timeoutDelay = d
}

func (m *MockPaymentGateway) scenarioFor(key string) Scenario {
	if s, ok := m.scenarios[key]; ok {
		return s
	}
	return m.defaultScenario
}

func (m *MockPaymentGateway) Charge(ctx context.Context, req PaymentRequest) (*PaymentResult, error) {
	if failclosed.IsSystemLockedDown() {
		return nil, ErrSystemLockdownFailClosed
	}

	scenario := m.scenarioFor(req.OrderID)

	switch scenario {
	case ScenarioTimeout:
		select {
		case <-time.After(m.timeoutDelay):
			return nil, ErrPaymentTimeout
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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
