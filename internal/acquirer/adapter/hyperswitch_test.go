package adapter

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
)

const testHyperswitchSecret = "hyperswitch_webhook_secret"

func newHyperswitchTestClient(t *testing.T, handler http.HandlerFunc) (*HyperswitchClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := NewHyperswitchClient(HyperswitchConfig{
		APIKey:        "test-api-key",
		WebhookSecret: testHyperswitchSecret,
		BaseURL:       srv.URL,
		ProfileID:     "pro_1",
	})
	require.NoError(t, err)
	return client, srv
}

func TestHyperswitch_Authorize_Success(t *testing.T) {
	var gotBody map[string]any
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/payments", r.URL.Path)
		assert.Equal(t, "test-api-key", r.Header.Get("api-key"))
		assert.Equal(t, "pro_1", r.Header.Get("X-Profile-Id"))
		assert.Equal(t, "idem-1", r.Header.Get("Idempotency-Key"))

		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &gotBody))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"payment_id":"pay_123","status":"requires_capture","amount":1050}`))
	})

	res, err := client.Authorize(context.Background(), domain.AuthorizeRequest{
		OrderID:            "order-1",
		Amount:             decimal.NewFromFloat(10.5),
		Currency:           "usd",
		PaymentMethodToken: "pm_card_visa",
		IdempotencyKey:     "idem-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "pay_123", res.ProviderRef)
	assert.Equal(t, domain.PaymentStatusAuthorized, res.Status)

	assert.Equal(t, float64(1050), gotBody["amount"])
	assert.Equal(t, "USD", gotBody["currency"])
	assert.Equal(t, "manual", gotBody["capture_method"])
	assert.Equal(t, true, gotBody["confirm"])
	assert.Equal(t, "pm_card_visa", gotBody["payment_method_id"])
}

func TestHyperswitch_Authorize_RequiresCustomerAction(t *testing.T) {
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"pay_9","status":"requires_customer_action"}`))
	})
	res, err := client.Authorize(context.Background(), domain.AuthorizeRequest{
		Amount: decimal.NewFromInt(10), Currency: "USD", PaymentMethodToken: "pm_1",
	})
	require.ErrorIs(t, err, ErrPaymentRequiresAction)
	require.NotNil(t, res)
	assert.Equal(t, "pay_9", res.ProviderRef)
	assert.Equal(t, domain.PaymentStatusPending, res.Status)
}

func TestHyperswitch_Authorize_Declined(t *testing.T) {
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"card_declined","message":"Card declined by issuer"}}`))
	})
	_, err := client.Authorize(context.Background(), domain.AuthorizeRequest{
		Amount: decimal.NewFromInt(10), Currency: "USD", PaymentMethodToken: "pm_1",
	})
	require.ErrorIs(t, err, ErrPaymentDeclined)
}

func TestHyperswitch_Authorize_TokenRequired(t *testing.T) {
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call provider without token")
	})
	_, err := client.Authorize(context.Background(), domain.AuthorizeRequest{
		Amount: decimal.NewFromInt(10), Currency: "USD",
	})
	require.ErrorIs(t, err, ErrPaymentMethodTokenRequired)
}

func TestHyperswitch_Capture(t *testing.T) {
	var gotBody map[string]any
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/payments/pay_123/capture", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		_, _ = w.Write([]byte(`{"payment_id":"pay_123","status":"succeeded","amount":1000}`))
	})

	res, err := client.Capture(context.Background(), domain.CaptureRequest{
		ProviderRef: "pay_123", Amount: decimal.NewFromInt(10),
	})
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentStatusCaptured, res.Status)
	assert.True(t, res.CapturedAmount.Equal(decimal.NewFromInt(10)))
	assert.Equal(t, float64(1000), gotBody["amount_to_capture"])
}

func TestHyperswitch_Refund_Pending(t *testing.T) {
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/refunds", r.URL.Path)
		_, _ = w.Write([]byte(`{"refund_id":"ref_1","status":"pending","amount":500}`))
	})
	res, err := client.Refund(context.Background(), domain.RefundRequest{
		ProviderRef: "pay_123", Amount: decimal.NewFromInt(5),
	})
	require.NoError(t, err)
	assert.Equal(t, "ref_1", res.ProviderRef)
	assert.Equal(t, domain.PaymentStatusPending, res.Status)
	assert.True(t, res.RefundedAmount.Equal(decimal.NewFromInt(5)))
}

func TestHyperswitch_RateLimited(t *testing.T) {
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := client.Authorize(context.Background(), domain.AuthorizeRequest{
		Amount: decimal.NewFromInt(10), Currency: "USD", PaymentMethodToken: "pm_1",
	})
	require.ErrorIs(t, err, ErrProviderRateLimited)
}

func TestHyperswitch_ServerErrorIsUnknownState(t *testing.T) {
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := client.Authorize(context.Background(), domain.AuthorizeRequest{
		Amount: decimal.NewFromInt(10), Currency: "USD", PaymentMethodToken: "pm_1",
	})
	require.ErrorIs(t, err, ErrUnknownExternalState)
}

func TestHyperswitch_VerifyWebhook(t *testing.T) {
	client, _ := newHyperswitchTestClient(t, func(w http.ResponseWriter, r *http.Request) {})

	payload := []byte(`{"event_id":"evt_1","event_type":"payment_succeeded","content":{"payment_id":"pay_1"}}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha512.New, []byte(testHyperswitchSecret))
	mac.Write([]byte(ts + "." + string(payload)))
	sig := hex.EncodeToString(mac.Sum(nil))

	evt, err := client.VerifyWebhook(context.Background(), domain.VerifyWebhookRequest{
		Payload: payload, Signature: "t=" + ts + ",v1=" + sig,
	})
	require.NoError(t, err)
	assert.Equal(t, "evt_1", evt.EventID)
	assert.Equal(t, "payment_succeeded", evt.EventType)
	assert.Equal(t, "pay_1", evt.ProviderRef)

	_, err = client.VerifyWebhook(context.Background(), domain.VerifyWebhookRequest{
		Payload: payload, Signature: "t=" + ts + ",v1=deadbeef",
	})
	require.ErrorIs(t, err, ErrWebhookSignatureInvalid)
}

func TestHyperswitch_TimeoutIsUnknownState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)

	client, err := NewHyperswitchClient(HyperswitchConfig{
		APIKey:  "k",
		BaseURL: srv.URL,
		Timeout: 20 * time.Millisecond,
	})
	require.NoError(t, err)

	_, err = client.Authorize(context.Background(), domain.AuthorizeRequest{
		Amount: decimal.NewFromInt(10), Currency: "USD", PaymentMethodToken: "pm_1",
	})
	require.True(t, errors.Is(err, ErrProviderTimeout))
}
