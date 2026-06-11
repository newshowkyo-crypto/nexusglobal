package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifyHMACSHA256_Valid(t *testing.T) {
	payload := []byte(`{"event":"captured"}`)
	secret := []byte("test-secret")
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))
	assert.True(t, VerifyHMACSHA256(payload, sig, secret))
}

func TestVerifyHMACSHA256_Invalid(t *testing.T) {
	payload := []byte(`{"event":"captured"}`)
	secret := []byte("test-secret")
	assert.False(t, VerifyHMACSHA256(payload, "invalid-signature", secret))
}

func TestVerifyHMACSHA256_WrongSecret(t *testing.T) {
	payload := []byte(`{"event":"captured"}`)
	secret := []byte("test-secret")
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))
	assert.False(t, VerifyHMACSHA256(payload, sig, []byte("wrong-secret")))
}

func TestComputeIdempotencyKey_WithEventID(t *testing.T) {
	k := ComputeIdempotencyKey("mae", "prov_01", "evt_123", "captured", "", "")
	assert.NotEmpty(t, k)
	assert.Len(t, k, 64)
}

func TestComputeIdempotencyKey_WithoutEventID(t *testing.T) {
	k := ComputeIdempotencyKey("mae", "prov_01", "", "captured", "abc123", "2026-06-12")
	assert.NotEmpty(t, k)
	assert.Len(t, k, 64)
}

func TestComputeIdempotencyKey_Deterministic(t *testing.T) {
	k1 := ComputeIdempotencyKey("mae", "prov_01", "evt_123", "captured", "", "")
	k2 := ComputeIdempotencyKey("mae", "prov_01", "evt_123", "captured", "", "")
	assert.Equal(t, k1, k2)
}

func TestComputeIdempotencyKey_DifferentInputs(t *testing.T) {
	k1 := ComputeIdempotencyKey("mae", "prov_01", "evt_123", "captured", "", "")
	k2 := ComputeIdempotencyKey("mae", "prov_01", "evt_456", "captured", "", "")
	assert.NotEqual(t, k1, k2)
}

func TestIngressStatus_Constants(t *testing.T) {
	assert.Equal(t, IngressStatus("Received"), StatusReceived)
	assert.Equal(t, IngressStatus("Consumed"), StatusConsumed)
	assert.Equal(t, IngressStatus("Rejected"), StatusRejected)
	assert.Equal(t, IngressStatus("DeadLetter"), StatusDeadLetter)
}
