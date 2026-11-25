package provider

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testHyperswitchWebhookSecret = "hs_whsec_test"

func TestHyperswitchVerifier_Verify(t *testing.T) {
	verifier := NewHyperswitchWebhookVerifier(testHyperswitchWebhookSecret)
	payload := []byte(`{"event_id":"evt_1","event_type":"payment_succeeded"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	mac := hmac.New(sha512.New, []byte(testHyperswitchWebhookSecret))
	mac.Write([]byte(ts + "." + string(payload)))
	sig := hex.EncodeToString(mac.Sum(nil))

	require.NoError(t, verifier.Verify(payload, "t="+ts+",v1="+sig))
	require.Error(t, verifier.Verify(payload, "t="+ts+",v1=deadbeef"))
	require.Error(t, verifier.Verify(payload, ""))
}

func TestMapHyperswitchEventToInternal(t *testing.T) {
	merchantID := uuid.New()

	cases := []struct {
		name      string
		eventType string
		wantType  InternalEventType
		wantRef   string
	}{
		{"payment succeeded", HyperswitchEventPaymentSucceeded, InternalEventPaymentCaptured, "pay_1"},
		{"payment failed", HyperswitchEventPaymentFailed, InternalEventPaymentFailed, "pay_1"},
		{"refund succeeded", HyperswitchEventRefundSucceeded, InternalEventRefundConfirmed, "ref_1"},
		{"refund failed", HyperswitchEventRefundFailed, InternalEventRefundConfirmed, "ref_1"},
		{"dispute opened", HyperswitchEventDisputeOpened, InternalEventChargebackReceived, "pay_1"},
		{"dispute won", HyperswitchEventDisputeWon, InternalEventChargebackReversed, "pay_1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := &HyperswitchEvent{
				EventID:   "evt_" + tc.eventType,
				EventType: tc.eventType,
			}
			event.Content.PaymentID = "pay_1"
			event.Content.RefundID = "ref_1"
			event.Content.Amount = 1000
			event.Content.Currency = "usd"
			event.Content.Metadata = map[string]string{
				"order_id":    "order-1",
				"merchant_id": merchantID.String(),
			}

			got, err := MapHyperswitchEventToInternal(event, "hash")
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, got.InternalType)
			assert.Equal(t, tc.wantRef, got.ProviderRef)
			assert.Equal(t, int64(1000), got.Amount)
			assert.Equal(t, "USD", got.Currency)
			assert.Equal(t, "order-1", got.OrderID)
			assert.Equal(t, merchantID, got.MerchantID)
			assert.Equal(t, "hash", got.RawPayloadHash)
		})
	}
}

func TestMapHyperswitchEventToInternal_Unknown(t *testing.T) {
	_, err := MapHyperswitchEventToInternal(&HyperswitchEvent{
		EventID: "evt_1", EventType: "action_required",
	}, "hash")
	require.Error(t, err)
}
