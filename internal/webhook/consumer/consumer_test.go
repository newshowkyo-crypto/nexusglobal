package consumer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadHash(t *testing.T) {
	data := []byte(`{"event":"captured","amount":"100.00"}`)
	hash := PayloadHash(data)

	require.NotEmpty(t, hash)
	assert.Len(t, hash, 64) // SHA256 hex string
}

func TestPayloadHash_Deterministic(t *testing.T) {
	data := []byte(`{"event":"captured","amount":"100.00"}`)
	hash1 := PayloadHash(data)
	hash2 := PayloadHash(data)

	assert.Equal(t, hash1, hash2)
}

func TestPayloadHash_Different(t *testing.T) {
	data1 := []byte(`{"event":"captured","amount":"100.00"}`)
	data2 := []byte(`{"event":"captured","amount":"200.00"}`)
	hash1 := PayloadHash(data1)
	hash2 := PayloadHash(data2)

	assert.NotEqual(t, hash1, hash2)
}

func TestWebhookIdempotencyKey(t *testing.T) {
	key := WebhookIdempotencyKey("mae", "prov_001", "evt_001", "captured")

	require.NotEmpty(t, key)
	assert.Len(t, key, 64) // SHA256 hex string
}

func TestWebhookIdempotencyKey_Deterministic(t *testing.T) {
	key1 := WebhookIdempotencyKey("mae", "prov_001", "evt_001", "captured")
	key2 := WebhookIdempotencyKey("mae", "prov_001", "evt_001", "captured")

	assert.Equal(t, key1, key2)
}

func TestWebhookIdempotencyKey_Different(t *testing.T) {
	key1 := WebhookIdempotencyKey("mae", "prov_001", "evt_001", "captured")
	key2 := WebhookIdempotencyKey("mae", "prov_001", "evt_002", "captured")

	assert.NotEqual(t, key1, key2)
}
