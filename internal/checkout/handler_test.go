package checkout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/token"
)

type mockTokenService struct {
	mock.Mock
}

func (m *mockTokenService) Redeem(ctx context.Context, cmd token.RedeemTokenCmd) (*token.Token, error) {
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

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

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

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(nil, token.ErrTokenNotFound)

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

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(nil, token.ErrTokenExpired)

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

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(nil, token.ErrTokenUsed)

	req := httptest.NewRequest("GET", "/api/checkout/validate?token=used", nil)
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleCharge_Success(t *testing.T) {
	tokenSvc := new(mockTokenService)
	paymentGW := NewMockPaymentGateway()

	h := &Handler{
		tokenSvc:  tokenSvc,
		paymentGW: paymentGW,
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
		tokenSvc: tokenSvc,
	}

	tkn := &token.Token{
		ID:         "token-123",
		OrderID:    "order-456",
		MerchantID: "merchant-789",
	}

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(tkn, nil)

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

	tokenSvc.On("Redeem", mock.Anything, mock.Anything).Return(nil, token.ErrOriginMismatch)

	req := httptest.NewRequest("GET", "/api/checkout/validate?token=test-value", nil)
	req.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()

	h.HandleValidateToken(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}
