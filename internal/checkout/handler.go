package checkout

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/token"
)

type MerchantDescriptor struct {
	Name         string `json:"name"`
	LogoURL      string `json:"logo_url"`
	TermsURL     string `json:"terms_url"`
	RefundPolicy string `json:"refund_policy"`
	SupportEmail string `json:"support_email"`
	SupportPhone string `json:"support_phone"`
}

type CheckoutData struct {
	OrderID      string             `json:"order_id"`
	MerchantName string             `json:"merchant_name"`
	Amount       string             `json:"amount"`
	Currency     string             `json:"currency"`
	Descriptor   MerchantDescriptor `json:"descriptor"`
	SuccessURL   string             `json:"success_url"`
	CancelURL    string             `json:"cancel_url"`
}

type TokenValidator interface {
	Redeem(ctx context.Context, cmd token.RedeemTokenCmd) (*token.Token, error)
}

type OrderProvider interface {
	GetOrderDetails(ctx context.Context, orderID string) (*CheckoutData, error)
}

type Handler struct {
	tokenSvc      TokenValidator
	orderProvider OrderProvider
	paymentGW     PaymentGateway
	tmpl          *template.Template
}

func NewHandler(tokenSvc TokenValidator, orderProvider OrderProvider, paymentGW PaymentGateway, tmpl *template.Template) *Handler {
	return &Handler{
		tokenSvc:      tokenSvc,
		orderProvider: orderProvider,
		paymentGW:     paymentGW,
		tmpl:          tmpl,
	}
}

func (h *Handler) ServeCheckoutPage(w http.ResponseWriter, r *http.Request) {
	tokenValue := r.URL.Query().Get("token")
	if tokenValue == "" {
		http.Error(w, "Missing token", http.StatusBadRequest)
		return
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}

	tkn, err := h.tokenSvc.Redeem(r.Context(), token.RedeemTokenCmd{
		Value:  tokenValue,
		Origin: origin,
	})
	if err != nil {
		switch err {
		case token.ErrTokenNotFound:
			http.Error(w, "Invalid token", http.StatusNotFound)
		case token.ErrTokenExpired:
			http.Error(w, "Token expired", http.StatusGone)
		case token.ErrTokenUsed:
			http.Error(w, "Token already used", http.StatusConflict)
		case token.ErrOriginMismatch:
			http.Error(w, "Origin not allowed", http.StatusForbidden)
		default:
			http.Error(w, "Internal error", http.StatusInternalServerError)
		}
		return
	}

	checkoutData := &CheckoutData{
		OrderID:      tkn.OrderID,
		MerchantName: "Merchant",
		Amount:       "0.00",
		Currency:     "USD",
		Descriptor: MerchantDescriptor{
			Name:         "NexusGlobal Merchant",
			TermsURL:     "/terms",
			RefundPolicy: "Refunds available within 30 days",
			SupportEmail: "support@nexusglobal.com",
		},
	}

	if h.orderProvider != nil {
		details, err := h.orderProvider.GetOrderDetails(r.Context(), tkn.OrderID)
		if err == nil && details != nil {
			checkoutData = details
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if err := h.tmpl.Execute(w, checkoutData); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func (h *Handler) HandleValidateToken(w http.ResponseWriter, r *http.Request) {
	tokenValue := r.URL.Query().Get("token")
	if tokenValue == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing token"})
		return
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}

	tkn, err := h.tokenSvc.Redeem(r.Context(), token.RedeemTokenCmd{
		Value:  tokenValue,
		Origin: origin,
	})
	if err != nil {
		switch err {
		case token.ErrTokenNotFound:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Invalid token"})
		case token.ErrTokenExpired:
			writeJSON(w, http.StatusGone, map[string]string{"error": "Token expired"})
		case token.ErrTokenUsed:
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Token already used"})
		case token.ErrOriginMismatch:
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Origin not allowed"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal error"})
		}
		return
	}

	checkoutData := &CheckoutData{
		OrderID:      tkn.OrderID,
		MerchantName: "Merchant",
		Amount:       "0.00",
		Currency:     "USD",
	}

	if h.orderProvider != nil {
		details, err := h.orderProvider.GetOrderDetails(r.Context(), tkn.OrderID)
		if err == nil && details != nil {
			checkoutData = details
		}
	}

	writeJSON(w, http.StatusOK, checkoutData)
}

func (h *Handler) HandleCharge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token      string `json:"token"`
		CardNumber string `json:"card_number"`
		CardExpiry string `json:"card_expiry"`
		CardCVV    string `json:"card_cvv"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing token"})
		return
	}

	// R3-1: token 校验
	tkn, err := h.tokenSvc.Redeem(r.Context(), token.RedeemTokenCmd{
		Value: req.Token,
	})
	if err != nil {
		switch err {
		case token.ErrTokenNotFound:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Invalid token"})
		case token.ErrTokenExpired:
			writeJSON(w, http.StatusGone, map[string]string{"error": "Token expired"})
		case token.ErrTokenUsed:
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Token already used"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal error"})
		}
		return
	}

	// R3-1: 获取订单详情
	amount := decimal.NewFromInt(0)
	currency := "USD"
	merchantID := ""

	if h.orderProvider != nil {
		details, err := h.orderProvider.GetOrderDetails(r.Context(), tkn.OrderID)
		if err == nil && details != nil {
			// 解析金额
			if parsedAmount, parseErr := decimal.NewFromString(details.Amount); parseErr == nil {
				amount = parsedAmount
			}
			currency = details.Currency
		}
	}

	// R3-1: 调用支付网关扣款（不持久化卡号）
	if h.paymentGW == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Payment gateway not configured"})
		return
	}

	paymentResult, err := h.paymentGW.Charge(r.Context(), PaymentRequest{
		OrderID:    tkn.OrderID,
		MerchantID: merchantID,
		Amount:     amount,
		Currency:   currency,
		CardNumber: req.CardNumber, // 仅传给网关，不落库
		CardExpiry: req.CardExpiry,
		CardCVV:    req.CardCVV,
	})
	if err != nil {
		switch err {
		case ErrPaymentFailed:
			writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "Payment failed"})
		case ErrPaymentTimeout:
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "Payment timeout"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Payment error"})
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":        true,
		"order_id":       tkn.OrderID,
		"transaction_id": paymentResult.TransactionID,
		"status":         paymentResult.Status,
		"redirect_url":   "/checkout/success",
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type ValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	return e.Message
}

func NewValidationError(code, message string) *ValidationError {
	return &ValidationError{Code: code, Message: message}
}

func IsValidationError(err error) bool {
	_, ok := err.(*ValidationError)
	return ok
}

func ToAPIError(err error) *apperr.Error {
	if ve, ok := err.(*ValidationError); ok {
		return apperr.New(ve.Code, http.StatusBadRequest)
	}
	return apperr.ErrInternal
}
