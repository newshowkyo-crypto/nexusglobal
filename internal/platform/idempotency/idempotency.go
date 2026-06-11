package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func Compute(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%s\x00", p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func PayloadHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

type Record struct {
	ID             string
	PayloadHash    string
	ResponseStatus int
	ResponseBody   []byte
}

type Store interface {
	Get(id string) (*Record, bool, error)
	Set(id string, rec Record) error
}

func WebhookIdempotencyKey(providerType, providerID, providerEventID, eventType string) string {
	if providerEventID != "" {
		return Compute(providerType, providerID, providerEventID, eventType)
	}
	return ""
}

func WebhookIdempotencyKeyWithHash(providerType, providerID, payloadHash, businessDate string) string {
	return Compute(providerType, providerID, payloadHash, businessDate)
}

func MatchesPayload(stored, incoming string) bool {
	return strings.EqualFold(stored, incoming)
}
