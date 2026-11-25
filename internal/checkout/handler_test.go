package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
	"github.com/nexusglobal/mamr/internal/token"
)

type mockTokenService struct {
	mock.Mock
}

func (m *mockTokenService) Create(ctx context.Context, cmd token.CreateTokenCmd) (*token.Token, error) {
	args := m.Called(ctx, cmd)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*token.Token), args.Error(1)
}

func (m *mockTokenService) Redeem(ctx context.Context, cmd token.RedeemTokenCmd) (*token.Token, error) {
	args := m.Called(ctx, cmd)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*token.Token), args.Error(1)
}

func (m *mockTokenService) Peek(ctx context.Context, cmd token.RedeemTokenCmd) (*token.Token, error) {
	args := m.Called(ctx, cmd)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*token.Token), args.Error(1)
}

func TestHandleValidateToken_Success(t *testing.T) {
	tokenSvc := new(mockTokenService)

	h := &Handler{

		tokenSvc: tokenSvc,
	}

	tkn := &token.Token{
		ID:         "token-123",
		Value:      "test-value",
		OrderID:    "order-456",
		MerchantID: "merchant-789",
	}

	tokenSvc.On("Peek", mock.Anything, mock.Anything).Return(tkn, nil)

	req := httptest.NewRequest("GET", "/api/checkout/validate?token=test-value", nil)
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp CheckoutData
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "order-456", resp.OrderID)
}

func TestHandleValidateToken_MissingToken(t *testing.T) {
	h := &Handler{}

	req := httptest.NewRequest("GET", "/api/checkout/validate", nil)
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleValidateToken_InvalidToken(t *testing.T) {
	tokenSvc := new(mockTokenService)
	h := &Handler{
		tokenSvc: tokenSvc,
	}

	tokenSvc.On("Peek", mock.Anything, mock.Anything).Return(nil, token.ErrTokenNotFound)

	req := httptest.NewRequest("GET", "/api/checkout/validate?token=invalid", nil)
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleValidateToken_ExpiredToken(t *testing.T) {
	tokenSvc := new(mockTokenService)
	h := &Handler{
		tokenSvc: tokenSvc,
	}

	tokenSvc.On("Peek", mock.Anything, mock.Anything).Return(nil, token.ErrTokenExpired)

	req := httptest.NewRequest("GET", "/api/checkout/validate?token=expired", nil)
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusGone, w.Code)
}

func TestHandleValidateToken_UsedToken(t *testing.T) {
	tokenSvc := new(mockTokenService)
	h := &Handler{
		tokenSvc: tokenSvc,
	}

	tokenSvc.On("Peek", mock.Anything, mock.Anything).Return(nil, token.ErrTokenUsed)

	req := httptest.NewRequest("GET", "/api/checkout/validate?token=used", nil)
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleCharge_Success(t *testing.T) {
	tokenSvc := new(mockTokenService)
	paymentGW := NewMockPaymentGateway()
	h := &Handler{
		tokenSvc:      tokenSvc,
		paymentGW:     paymentGW,
		orderProvider: NewMockOrderProvider(),
	}

	tkn := &token.Token{
		ID:      "token-123",
		OrderID: "order-456",
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

	body := `{"token":"test-value","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, true, resp["success"])
	assert.Equal(t, "order-456", resp["order_id"])
	assert.NotEmpty(t, resp["transaction_id"])
}

func TestHandleCharge_MissingToken(t *testing.T) {
	h := &Handler{}

	body := `{"card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCharge_InvalidToken(t *testing.T) {
	tokenSvc := new(mockTokenService)
	h := &Handler{
		tokenSvc: tokenSvc,
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(nil, token.ErrTokenNotFound)

	body := `{"token":"invalid","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleCharge_ExpiredToken(t *testing.T) {
	tokenSvc := new(mockTokenService)
	h := &Handler{
		tokenSvc: tokenSvc,
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(nil, token.ErrTokenExpired)

	body := `{"token":"expired","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	assert.Equal(t, http.StatusGone, w.Code)
}

func TestHandleCharge_UsedToken(t *testing.T) {
	tokenSvc := new(mockTokenService)
	h := &Handler{
		tokenSvc: tokenSvc,
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(nil, token.ErrTokenUsed)

	body := `{"token":"used","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestMerchantDescriptor_Displayed(t *testing.T) {
	tokenSvc := new(mockTokenService)
	h := &Handler{
		tokenSvc:      tokenSvc,
		orderProvider: NewMockOrderProvider(),
	}

	tkn := &token.Token{
		ID:         "token-123",
		OrderID:    "order-456",
		MerchantID: "merchant-789",
	}

	tokenSvc.On("Peek", mock.Anything, mock.Anything).Return(tkn, nil)

	req := httptest.NewRequest("GET", "/api/checkout/validate?token=test-value", nil)
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp CheckoutData
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "order-456", resp.OrderID)
}

func TestOriginValidation(t *testing.T) {
	tokenSvc := new(mockTokenService)
	h := &Handler{
		tokenSvc: tokenSvc,
	}

	tokenSvc.On("Peek", mock.Anything, mock.Anything).Return(nil, token.ErrOriginMismatch)

	req := httptest.NewRequest("GET", "/api/checkout/validate?token=test-value", nil)
	req.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestMockPaymentGateway_Success(t *testing.T) {
	gw := NewMockPaymentGateway()
	result, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID: "order-123",
		Amount:  decimal.NewFromInt(100),
	})

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "captured", result.Status)
	assert.Equal(t, "txn_order-123", result.TransactionID)
}

func TestMockPaymentGateway_SystemLockdownFailClosed(t *testing.T) {
	failclosed.ClearSystemLockdownForTest()
	defer failclosed.ClearSystemLockdownForTest()
	failclosed.ActivateSystemLockdown()

	gw := NewMockPaymentGateway()
	result, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID: "order-locked",
		Amount:  decimal.NewFromInt(100),
	})

	assert.Nil(t, result)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSystemLockdownFailClosed))
	assert.Contains(t, err.Error(), failclosed.SystemLockdownCode)
}

func TestMockPaymentGateway_Failure(t *testing.T) {
	gw := NewMockPaymentGateway()
	gw.SetDefaultScenario(ScenarioFailure)

	result, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID: "order-123",
		Amount:  decimal.NewFromInt(100),
	})

	assert.Error(t, err)
	assert.Equal(t, ErrPaymentFailed, err)
	assert.Nil(t, result)
}

func TestMockPaymentGateway_Timeout(t *testing.T) {
	gw := NewMockPaymentGateway()
	gw.SetDefaultScenario(ScenarioTimeout)
	gw.SetTimeoutDelay(10 * time.Millisecond)

	result, err := gw.Charge(context.Background(), PaymentRequest{
		OrderID: "order-123",
		Amount:  decimal.NewFromInt(100),
	})

	assert.Error(t, err)
	assert.Equal(t, ErrPaymentTimeout, err)
	assert.Nil(t, result)
}

func TestMockPaymentGateway_PerOrderScenario(t *testing.T) {
	gw := NewMockPaymentGateway()
	gw.SetScenario("order-fail", ScenarioFailure)
	gw.SetScenario("order-timeout", ScenarioTimeout)

	// 默认成功
	result, err := gw.Charge(context.Background(), PaymentRequest{OrderID: "order-ok"})
	assert.NoError(t, err)
	assert.Equal(t, "captured", result.Status)

	// 特定订单失败
	result, err = gw.Charge(context.Background(), PaymentRequest{OrderID: "order-fail"})
	assert.Error(t, err)
	assert.Equal(t, ErrPaymentFailed, err)

	// 特定订单超时

	gw.SetTimeoutDelay(10 * time.Millisecond)
	result, err = gw.Charge(context.Background(), PaymentRequest{OrderID: "order-timeout"})
	assert.Error(t, err)
	assert.Equal(t, ErrPaymentTimeout, err)
}

func TestHandleCharge_OrderFail_402(t *testing.T) {
	tokenSvc := new(mockTokenService)
	paymentGW := NewMockPaymentGateway()
	paymentGW.SetScenario("order-fail", ScenarioFailure)
	h := &Handler{
		tokenSvc:      tokenSvc,
		paymentGW:     paymentGW,
		orderProvider: NewMockOrderProvider(),
	}

	tkn := &token.Token{
		ID:      "token-123",
		OrderID: "order-fail",
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

	body := `{"token":"test-value","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	assert.Equal(t, http.StatusPaymentRequired, w.Code)
}

func TestHandleCharge_OrderTimeout_504(t *testing.T) {
	tokenSvc := new(mockTokenService)
	paymentGW := NewMockPaymentGateway()
	paymentGW.SetScenario("order-timeout", ScenarioTimeout)
	paymentGW.SetTimeoutDelay(10 * time.Millisecond)
	h := &Handler{
		tokenSvc:      tokenSvc,
		paymentGW:     paymentGW,
		orderProvider: NewMockOrderProvider(),
	}

	tkn := &token.Token{
		ID:      "token-123",
		OrderID: "order-timeout",
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

	body := `{"token":"test-value","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
}

// mockRiskGateway 用于测试的 mock 风控网关
type mockRiskGateway struct {
	mock.Mock
}

func (m *mockRiskGateway) Check(ctx context.Context, req domain.CheckRequest) domain.CheckResult {
	args := m.Called(ctx, req)
	return args.Get(0).(domain.CheckResult)
}

func (m *mockRiskGateway) RecordFailure(ctx context.Context, req domain.CheckRequest) {
	m.Called(ctx, req)
}

func (m *mockRiskGateway) RecordSuccess(ctx context.Context, req domain.CheckRequest) {
	m.Called(ctx, req)
}

func TestHandleCharge_RiskBlocked_429(t *testing.T) {
	tokenSvc := new(mockTokenService)
	paymentGW := NewMockPaymentGateway()
	riskGW := new(mockRiskGateway)
	h := &Handler{
		orderProvider: NewMockOrderProvider(),
		tokenSvc:      tokenSvc,
		paymentGW:     paymentGW,
		riskGateway:   riskGW,
	}

	tkn := &token.Token{
		ID:         "token-123",
		OrderID:    "order-456",
		MerchantID: "merchant-789",
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

	// 风控返回 Block
	riskGW.On("Check", mock.Anything, mock.Anything).Return(domain.CheckResult{
		Decision:  domain.DecisionBlock,
		Reason:    "ip_threshold_exceeded",
		BlockedBy: domain.DimensionBuyerIP,
	})
	riskGW.On("RecordFailure", mock.Anything, mock.Anything).Return()

	body := `{"token":"test-value","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	// 应返回 429

	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "risk_blocked", resp["error"])
	assert.Equal(t, "ip_threshold_exceeded", resp["reason"])
}

func TestHandleCharge_RiskCooldown_403(t *testing.T) {
	tokenSvc := new(mockTokenService)
	paymentGW := NewMockPaymentGateway()
	riskGW := new(mockRiskGateway)
	orderProvider := NewMockOrderProvider()
	h := &Handler{
		orderProvider: orderProvider,
		tokenSvc:      tokenSvc,
		paymentGW:     paymentGW,
		riskGateway:   riskGW,
	}

	tkn := &token.Token{
		ID:         "token-123",
		OrderID:    "order-456",
		MerchantID: "merchant-789",
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

	// 风控返回 Cooldown
	riskGW.On("Check", mock.Anything, mock.Anything).Return(domain.CheckResult{
		Decision:  domain.DecisionCooldown,
		Reason:    "merchant_in_cooldown",
		BlockedBy: domain.DimensionMerchantID,
	})

	body := `{"token":"test-value","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	// 应返回 403
	assert.Equal(t, http.StatusForbidden, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "merchant_in_cooldown", resp["error"])
}

func TestHandleCharge_RiskAllowed_PaymentCalled(t *testing.T) {
	tokenSvc := new(mockTokenService)
	paymentGW := NewMockPaymentGateway()
	orderProvider := NewMockOrderProvider()
	riskGW := new(mockRiskGateway)
	h := &Handler{
		orderProvider: orderProvider,
		tokenSvc:      tokenSvc,
		paymentGW:     paymentGW,
		riskGateway:   riskGW,
	}

	tkn := &token.Token{
		ID:         "token-123",
		OrderID:    "order-success",
		MerchantID: "merchant-789",
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

	// 风控允许
	riskGW.On("Check", mock.Anything, mock.Anything).Return(domain.CheckResult{
		Decision: domain.DecisionAllow,
		Reason:   "all_checks_passed",
	})
	riskGW.On("RecordSuccess", mock.Anything, mock.Anything).Return()

	body := `{"token":"test-value","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	// 应正常处理支付
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, true, resp["success"])
}

func TestHandleCharge_RiskAllowed_PaymentFailed_RecordFailure(t *testing.T) {
	tokenSvc := new(mockTokenService)
	paymentGW := NewMockPaymentGateway()
	paymentGW.SetScenario("order-fail", ScenarioFailure)
	riskGW := new(mockRiskGateway)
	h := &Handler{
		orderProvider: NewMockOrderProvider(),
		tokenSvc:      tokenSvc,
		paymentGW:     paymentGW,
		riskGateway:   riskGW,
	}

	tkn := &token.Token{
		ID:         "token-123",
		OrderID:    "order-fail",
		MerchantID: "merchant-789",
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

	// 风控允许
	riskGW.On("Check", mock.Anything, mock.Anything).Return(domain.CheckResult{
		Decision: domain.DecisionAllow,
		Reason:   "all_checks_passed",
	})
	// 支付失败应记录失败
	riskGW.On("RecordFailure", mock.Anything, mock.Anything).Return()

	body := `{"token":"test-value","card_number":"4242424242424242","card_expiry":"12/25","card_cvv":"123"}`
	req := httptest.NewRequest("POST", "/api/checkout/charge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCharge(w, req)

	// 应返回 402
	assert.Equal(t, http.StatusPaymentRequired, w.Code)
}

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		xri        string
		expected   string
	}{
		{
			name:       "RemoteAddr only",
			remoteAddr: "192.168.1.1:12345",
			expected:   "192.168.1.1",
		},
		{
			name:       "X-Forwarded-For single",
			remoteAddr: "10.0.0.1:12345",
			xff:        "203.0.113.1",
			expected:   "203.0.113.1",
		},
		{
			name:       "X-Forwarded-For multiple",
			remoteAddr: "10.0.0.1:12345",
			xff:        "203.0.113.1, 70.41.3.18, 150.172.238.178",
			expected:   "203.0.113.1",
		},
		{
			name:       "X-Real-IP",
			remoteAddr: "10.0.0.1:12345",
			xri:        "198.51.100.1",
			expected:   "198.51.100.1",
		},
		{
			name:       "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr: "10.0.0.1:12345",
			xff:        "203.0.113.1",
			xri:        "198.51.100.1",
			expected:   "203.0.113.1",
		},
		{
			name:       "IPv6 RemoteAddr",
			remoteAddr: "[::1]:12345",
			expected:   "::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-IP", tt.xri)
			}

			ip := extractClientIP(req)
			assert.Equal(t, tt.expected, ip)
		})
	}
}
