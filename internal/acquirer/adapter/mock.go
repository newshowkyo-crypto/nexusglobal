package adapter

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
)

// MockAcquirer Mock 收单客户端
// 支持注入 success/explicit_failure/timeout 场景
type MockAcquirer struct {
	mu            sync.RWMutex
	scenarios     map[string]ScenarioType // order_id -> scenario
	defaultDelay  time.Duration
	webhookSecret string
}

// ScenarioType 场景类型
type ScenarioType string

const (
	ScenarioSuccess ScenarioType = "success"
	ScenarioFailure ScenarioType = "failure"
	ScenarioTimeout ScenarioType = "timeout"
	ScenarioPartial ScenarioType = "partial"
)

// NewMockAcquirer 创建 Mock 收单客户端
func NewMockAcquirer(webhookSecret string) *MockAcquirer {
	return &MockAcquirer{
		scenarios:     make(map[string]ScenarioType),
		defaultDelay:  0,
		webhookSecret: webhookSecret,
	}
}

// SetScenario 设置订单场景
func (m *MockAcquirer) SetScenario(orderID string, scenario ScenarioType) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scenarios[orderID] = scenario
}

// SetDefaultDelay 设置默认延迟
func (m *MockAcquirer) SetDefaultDelay(delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultDelay = delay
}

// getScenario 获取订单场景
func (m *MockAcquirer) getScenario(orderID string) ScenarioType {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.scenarios[orderID]; ok {
		return s
	}
	return ScenarioSuccess // 默认成功
}

// simulateDelay 模拟延迟
func (m *MockAcquirer) simulateDelay() {
	m.mu.RLock()
	delay := m.defaultDelay
	m.mu.RUnlock()
	if delay > 0 {
		time.Sleep(delay)
	}
}

// Authorize 授权
func (m *MockAcquirer) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	m.simulateDelay()

	scenario := m.getScenario(req.OrderID)

	switch scenario {
	case ScenarioSuccess:
		return &domain.AuthorizeResult{
			TransactionID:    uuid.New(),
			ProviderRef:      "mock-auth-" + uuid.New().String()[:8],
			Status:           domain.PaymentStatusAuthorized,
			AuthorizedAmount: req.Amount,
			ExpiresAt:        timePtr(time.Now().Add(24 * time.Hour)),
		}, nil

	case ScenarioFailure:
		return nil, domain.ErrExplicitFailure

	case ScenarioTimeout:
		return nil, domain.ErrProviderTimeout

	case ScenarioPartial:
		// 部分授权
		partialAmount := req.Amount.Mul(decimal.NewFromFloat(0.5))
		return &domain.AuthorizeResult{
			ProviderRef:      "mock-auth-" + uuid.New().String()[:8],
			Status:           domain.PaymentStatusAuthorized,
			AuthorizedAmount: partialAmount,
			ExpiresAt:        timePtr(time.Now().Add(24 * time.Hour)),
		}, nil

	default:
		return nil, errors.New("mock: unknown scenario")
	}
}

// Capture 捕获
func (m *MockAcquirer) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	m.simulateDelay()

	// 根据 provider_ref 判断场景（简化：默认成功）
	return &domain.CaptureResult{
		ProviderRef:    req.ProviderRef,
		Status:         domain.PaymentStatusCaptured,
		CapturedAmount: req.Amount,
	}, nil
}

// Refund 退款
func (m *MockAcquirer) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	m.simulateDelay()

	return &domain.RefundResult{
		ProviderRef:    req.ProviderRef + "-refund",
		Status:         domain.PaymentStatusRefunded,
		RefundedAmount: req.Amount,
	}, nil
}

// VerifyWebhook 验证 webhook 签名
func (m *MockAcquirer) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	if m.webhookSecret == "" {
		return &domain.VerifiedWebhookEvent{
			EventID:     uuid.New().String(),
			EventType:   "mock.event",
			ProviderRef: "mock-ref",
			Payload:     req.Payload,
		}, nil // 无密钥时跳过验签（仅用于测试）
	}

	// HMAC-SHA256 验签
	expectedSig := computeHMAC(req.Payload, m.webhookSecret)
	if req.Signature != expectedSig {
		return nil, errors.New("mock: webhook signature mismatch")
	}
	return &domain.VerifiedWebhookEvent{
		EventID:     uuid.New().String(),
		EventType:   "mock.event",
		ProviderRef: "mock-ref",
		Payload:     req.Payload,
	}, nil
}

// LegacyAuthorize implements AcquirerClient interface for backward compatibility
func (m *MockAcquirer) LegacyAuthorize(ctx context.Context, req domain.LegacyAuthorizeRequest) (*domain.LegacyAuthorizeResponse, error) {
	m.simulateDelay()
	scenario := m.getScenario(req.OrderID)
	switch scenario {
	case ScenarioSuccess:
		return &domain.LegacyAuthorizeResponse{
			TransactionID:    uuid.New(),
			ProviderRef:      "mock-auth-" + uuid.New().String()[:8],
			Status:           domain.PaymentStatusAuthorized,
			AuthorizedAmount: req.Amount,
			ExpiresAt:        timePtr(time.Now().Add(24 * time.Hour)),
		}, nil
	case ScenarioFailure:
		return nil, domain.ErrExplicitFailure
	case ScenarioTimeout:
		return nil, domain.ErrProviderTimeout
	case ScenarioPartial:
		return &domain.LegacyAuthorizeResponse{
			ProviderRef:      "mock-auth-" + uuid.New().String()[:8],
			Status:           domain.PaymentStatusAuthorized,
			AuthorizedAmount: req.Amount.Mul(decimal.NewFromFloat(0.5)),
			ExpiresAt:        timePtr(time.Now().Add(24 * time.Hour)),
		}, nil
	default:
		return nil, errors.New("mock: unknown scenario")
	}
}

func computeHMAC(data []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func timePtr(t time.Time) *time.Time {
	return &t
}
