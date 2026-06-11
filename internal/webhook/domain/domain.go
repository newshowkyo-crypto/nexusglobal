package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/nexusglobal/mamr/internal/platform/idempotency"
)

type SignatureAlgorithm string

const (
	AlgoHMACSHA256 SignatureAlgorithm = "HMAC_SHA256"
	AlgoRSASHA256  SignatureAlgorithm = "RSA_SHA256"
)

func VerifyHMACSHA256(payload []byte, signature string, secret []byte) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(expected)), []byte(strings.ToLower(signature)))
}

func ComputeIdempotencyKey(providerType, providerID, providerEventID, eventType, payloadHash, businessDate string) string {
	if providerEventID != "" {
		return idempotency.Compute(providerType, providerID, providerEventID, eventType)
	}
	return idempotency.Compute(providerType, providerID, payloadHash, businessDate)
}

type IngressEvent struct {
	ID               string
	ProviderType     string
	ProviderID       string
	ProviderEventID  string
	EventType        string
	IdempotencyKey   string
	PayloadHash      string
	RawPayloadCipher []byte
	Status           string
	ErrorMessage     string
}

type IngressStatus string

const (
	StatusReceived   IngressStatus = "Received"
	StatusConsumed   IngressStatus = "Consumed"
	StatusRejected   IngressStatus = "Rejected"
	StatusDeadLetter IngressStatus = "DeadLetter"
)
