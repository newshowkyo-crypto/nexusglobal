package idempotency

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompute_Deterministic(t *testing.T) {
	k1 := Compute("a", "b", "c")
	k2 := Compute("a", "b", "c")
	assert.Equal(t, k1, k2)
}

func TestCompute_DifferentInputs(t *testing.T) {
	k1 := Compute("a", "b")
	k2 := Compute("a", "c")
	assert.NotEqual(t, k1, k2)
}

func TestCompute_EmptyParts(t *testing.T) {
	k := Compute()
	assert.NotEmpty(t, k)
	assert.Len(t, k, 64)
}

func TestCompute_SinglePart(t *testing.T) {
	k := Compute("hello")
	assert.Len(t, k, 64)
}

func TestPayloadHash_Deterministic(t *testing.T) {
	body := []byte(`{"amount":"100.00"}`)
	h1 := PayloadHash(body)
	h2 := PayloadHash(body)
	assert.Equal(t, h1, h2)
}

func TestPayloadHash_DifferentBodies(t *testing.T) {
	h1 := PayloadHash([]byte(`{"amount":"100.00"}`))
	h2 := PayloadHash([]byte(`{"amount":"200.00"}`))
	assert.NotEqual(t, h1, h2)
}

func TestPayloadHash_EmptyBody(t *testing.T) {
	h := PayloadHash([]byte{})
	assert.NotEmpty(t, h)
	assert.Len(t, h, 64)
}

func TestWebhookIdempotencyKey_WithEventID(t *testing.T) {
	k := WebhookIdempotencyKey("mae", "prov_01", "evt_123", "captured")
	assert.NotEmpty(t, k)
	assert.Len(t, k, 64)
}

func TestWebhookIdempotencyKey_WithoutEventID(t *testing.T) {
	k := WebhookIdempotencyKey("mae", "prov_01", "", "captured")
	assert.Empty(t, k)
}

func TestWebhookIdempotencyKeyWithHash(t *testing.T) {
	k := WebhookIdempotencyKeyWithHash("mae", "prov_01", "abc123", "2026-06-12")
	assert.NotEmpty(t, k)
	assert.Len(t, k, 64)
}

func TestWebhookIdempotencyKeyWithHash_DifferentDates(t *testing.T) {
	k1 := WebhookIdempotencyKeyWithHash("mae", "prov_01", "abc123", "2026-06-12")
	k2 := WebhookIdempotencyKeyWithHash("mae", "prov_01", "abc123", "2026-06-13")
	assert.NotEqual(t, k1, k2)
}

func TestMatchesPayload_SamePayload(t *testing.T) {
	assert.True(t, MatchesPayload("abc123", "abc123"))
}

func TestMatchesPayload_CaseInsensitive(t *testing.T) {
	assert.True(t, MatchesPayload("ABC123", "abc123"))
}

func TestMatchesPayload_DifferentPayload(t *testing.T) {
	assert.False(t, MatchesPayload("abc123", "def456"))
}
