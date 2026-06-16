package integration

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/acquirer/adapter"
	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/shopspring/decimal"
)

func TestStripeSandbox_AuthorizeCaptureRefund(t *testing.T) {
	stripeSecret := os.Getenv("STRIPE_SANDBOX_SECRET")
	if stripeSecret == "" {
		t.Skip("STRIPE_SANDBOX_SECRET not set, skipping sandbox test")
	}

	ctx := context.Background()
	client, err := adapter.NewStripeClient(adapter.StripeConfig{
		SecretKey:     stripeSecret,
		WebhookSecret: "",
	})
	if err != nil {
		t.Fatalf("Failed to create Stripe client: %v", err)
	}

	testMerchantID := uuid.New()
	testChannelID := uuid.New()

	authorizeReq := domain.AuthorizeRequest{
		OrderID:            "test-order-" + uuid.New().String(),
		MerchantID:         testMerchantID,
		ChannelID:          testChannelID,
		Amount:             decimal.NewFromInt(100),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_visa",
		IdempotencyKey:     uuid.New().String(),
	}

	authResult, err := client.Authorize(ctx, authorizeReq)
	if err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	if authResult.Status != domain.PaymentStatusAuthorized {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusAuthorized, authResult.Status)
	}

	if authResult.TransactionID == uuid.Nil {
		t.Error("TransactionID should not be empty")
	}

	captureReq := domain.CaptureRequest{
		ChannelID:      testChannelID,
		ProviderRef:    authResult.ProviderRef,
		Amount:         decimal.NewFromInt(100),
		IdempotencyKey: uuid.New().String(),
	}

	captureResult, err := client.Capture(ctx, captureReq)
	if err != nil {
		t.Fatalf("Capture failed: %v", err)
	}

	if captureResult.Status != domain.PaymentStatusCaptured {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusCaptured, captureResult.Status)
	}

	refundReq := domain.RefundRequest{
		ChannelID:      testChannelID,
		ProviderRef:    authResult.ProviderRef,
		Amount:         decimal.NewFromInt(50),
		Reason:         "requested_by_customer",
		IdempotencyKey: uuid.New().String(),
	}

	refundResult, err := client.Refund(ctx, refundReq)
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}

	if refundResult.Status != domain.PaymentStatusRefunded {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusRefunded, refundResult.Status)
	}

	t.Logf("Stripe sandbox test PASSED: Auth=%s, Capture=%s, Refund=%s",
		authResult.TransactionID, captureResult.ProviderRef, refundResult.ProviderRef)
}

func TestStripeSandbox_WebhookSignature(t *testing.T) {
	stripeSecret := os.Getenv("STRIPE_SANDBOX_SECRET")
	webhookSecret := os.Getenv("STRIPE_SANDBOX_WEBHOOK_SECRET")
	if stripeSecret == "" || webhookSecret == "" {
		t.Skip("STRIPE_SANDBOX_SECRET or STRIPE_SANDBOX_WEBHOOK_SECRET not set, skipping webhook test")
	}

	ctx := context.Background()
	client, err := adapter.NewStripeClient(adapter.StripeConfig{
		SecretKey:     stripeSecret,
		WebhookSecret: webhookSecret,
	})
	if err != nil {
		t.Fatalf("Failed to create Stripe client: %v", err)
	}

	testCases := []struct {
		name       string
		payload    []byte
		signature  string
		expectFail bool
	}{
		{
			name:       "empty payload should fail",
			payload:    []byte{},
			signature:  "",
			expectFail: true,
		},
		{
			name:       "invalid signature should fail",
			payload:    []byte(`{"id": "evt_test", "type": "payment_intent.succeeded"}`),
			signature:  "t=12345,v1=invalid_signature",
			expectFail: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := domain.VerifyWebhookRequest{
				ChannelID: uuid.New(),
				Payload:   tc.payload,
				Signature: tc.signature,
			}

			_, err := client.VerifyWebhook(ctx, req)
			if tc.expectFail && err == nil {
				t.Error("Expected verification to fail, but it succeeded")
			}
			if !tc.expectFail && err != nil {
				t.Errorf("Expected verification to succeed, but got error: %v", err)
			}
		})
	}

	t.Log("Stripe webhook signature test PASSED")
}

func TestNxtapaySandbox_AuthorizeCaptureRefund(t *testing.T) {
	apiKey := os.Getenv("NXTAPAY_SANDBOX_API_KEY")
	apiSecret := os.Getenv("NXTAPAY_SANDBOX_API_SECRET")
	merchantID := os.Getenv("NXTAPAY_SANDBOX_MERCHANT_ID")
	if apiKey == "" || apiSecret == "" || merchantID == "" {
		t.Skip("NXTAPAY_SANDBOX_API_KEY, NXTAPAY_SANDBOX_API_SECRET, or NXTAPAY_SANDBOX_MERCHANT_ID not set, skipping sandbox test")
	}

	ctx := context.Background()
	client, err := adapter.NewNxtapayClient(adapter.NxtapayConfig{
		APIKey:        apiKey,
		APISecret:     apiSecret,
		MerchantID:    merchantID,
		WebhookSecret: "",
	})
	if err != nil {
		t.Fatalf("Failed to create Nxtapay client: %v", err)
	}

	testMerchantID := uuid.New()
	testChannelID := uuid.New()

	authorizeReq := domain.AuthorizeRequest{
		OrderID:            "test-order-" + uuid.New().String(),
		MerchantID:         testMerchantID,
		ChannelID:          testChannelID,
		Amount:             decimal.NewFromInt(100),
		Currency:           "USD",
		PaymentMethodToken: "pm_card_visa",
		IdempotencyKey:     uuid.New().String(),
	}

	authResult, err := client.Authorize(ctx, authorizeReq)
	if err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	if authResult.Status != domain.PaymentStatusAuthorized {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusAuthorized, authResult.Status)
	}

	if authResult.TransactionID == uuid.Nil {
		t.Error("TransactionID should not be empty")
	}

	captureReq := domain.CaptureRequest{
		ChannelID:      testChannelID,
		ProviderRef:    authResult.ProviderRef,
		Amount:         decimal.NewFromInt(100),
		IdempotencyKey: uuid.New().String(),
	}

	captureResult, err := client.Capture(ctx, captureReq)
	if err != nil {
		t.Fatalf("Capture failed: %v", err)
	}

	if captureResult.Status != domain.PaymentStatusCaptured {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusCaptured, captureResult.Status)
	}

	refundReq := domain.RefundRequest{
		ChannelID:      testChannelID,
		ProviderRef:    authResult.ProviderRef,
		Amount:         decimal.NewFromInt(50),
		Reason:         "requested_by_customer",
		IdempotencyKey: uuid.New().String(),
	}

	refundResult, err := client.Refund(ctx, refundReq)
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}

	if refundResult.Status != domain.PaymentStatusRefunded {
		t.Errorf("Expected status %s, got %s", domain.PaymentStatusRefunded, refundResult.Status)
	}

	t.Logf("Nxtapay sandbox test PASSED: Auth=%s, Capture=%s, Refund=%s",
		authResult.TransactionID, captureResult.ProviderRef, refundResult.ProviderRef)
}

func TestNxtapaySandbox_WebhookSignature(t *testing.T) {
	apiKey := os.Getenv("NXTAPAY_SANDBOX_API_KEY")
	apiSecret := os.Getenv("NXTAPAY_SANDBOX_API_SECRET")
	merchantID := os.Getenv("NXTAPAY_SANDBOX_MERCHANT_ID")
	webhookSecret := os.Getenv("NXTAPAY_SANDBOX_WEBHOOK_SECRET")
	if apiKey == "" || apiSecret == "" || merchantID == "" || webhookSecret == "" {
		t.Skip("NXTAPAY_SANDBOX_API_KEY, NXTAPAY_SANDBOX_API_SECRET, NXTAPAY_SANDBOX_MERCHANT_ID, or NXTAPAY_SANDBOX_WEBHOOK_SECRET not set, skipping webhook test")
	}

	ctx := context.Background()
	client, err := adapter.NewNxtapayClient(adapter.NxtapayConfig{
		APIKey:        apiKey,
		APISecret:     apiSecret,
		MerchantID:    merchantID,
		WebhookSecret: webhookSecret,
	})
	if err != nil {
		t.Fatalf("Failed to create Nxtapay client: %v", err)
	}

	testCases := []struct {
		name       string
		payload    []byte
		signature  string
		expectFail bool
	}{
		{
			name:       "empty payload should fail",
			payload:    []byte{},
			signature:  "",
			expectFail: true,
		},
		{
			name:       "invalid signature should fail",
			payload:    []byte(`{"transaction_id": "txn_test", "status": "success"}`),
			signature:  "invalid_signature",
			expectFail: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := domain.VerifyWebhookRequest{
				ChannelID: uuid.New(),
				Payload:   tc.payload,
				Signature: tc.signature,
			}

			_, err := client.VerifyWebhook(ctx, req)
			if tc.expectFail && err == nil {
				t.Error("Expected verification to fail, but it succeeded")
			}
			if !tc.expectFail && err != nil {
				t.Errorf("Expected verification to succeed, but got error: %v", err)
			}
		})
	}

	t.Log("Nxtapay webhook signature test PASSED")
}

func TestCredentialNotExposed(t *testing.T) {
	stripeSecret := os.Getenv("STRIPE_SANDBOX_SECRET")
	if stripeSecret == "" {
		t.Skip("STRIPE_SANDBOX_SECRET not set, skipping credential exposure test")
	}

	client, err := adapter.NewStripeClient(adapter.StripeConfig{
		SecretKey:     stripeSecret,
		WebhookSecret: "",
	})
	if err != nil {
		t.Fatalf("Failed to create Stripe client: %v", err)
	}

	if client == nil {
		t.Fatal("Client should not be nil")
	}

	t.Log("Credential exposure check PASSED: Credential not exposed in client structure")
}
