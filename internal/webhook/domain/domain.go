package domain

import (
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
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

func VerifyRSA256(payload []byte, signature string, publicKeyPEM []byte) (bool, error) {
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return false, errors.New("webhook: invalid PEM public key")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return false, err
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return false, errors.New("webhook: not an RSA public key")
	}

	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		sigBytes, err = hex.DecodeString(signature)
		if err != nil {
			return false, errors.New("webhook: invalid signature encoding")
		}
	}

	hash := sha256.Sum256(payload)
	err = rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, hash[:], sigBytes)
	return err == nil, nil
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
