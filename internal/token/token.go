package token

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrTokenNotFound  = errors.New("token: not found")
	ErrTokenExpired   = errors.New("token: expired")
	ErrTokenUsed      = errors.New("token: already used")
	ErrTokenInvalid   = errors.New("token: invalid")
	ErrOriginMismatch = errors.New("token: origin mismatch")
)

type Token struct {
	ID            string
	Value         string
	OrderID       string
	MerchantID    string
	AllowedOrigin string
	MaxUses       int
	UsedCount     int
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type CreateTokenCmd struct {
	OrderID       string
	MerchantID    string
	AllowedOrigin string
	MaxUses       int
	TTL           time.Duration
}

type RedeemTokenCmd struct {
	Value   string
	Origin  string
	OrderID string
}

type Store interface {
	Save(ctx context.Context, token *Token) error
	Get(ctx context.Context, value string) (*Token, error)
	IncrementUseCount(ctx context.Context, value string) error
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Create(ctx context.Context, cmd CreateTokenCmd) (*Token, error) {
	if cmd.MaxUses <= 0 {
		cmd.MaxUses = 1
	}
	if cmd.TTL <= 0 {
		cmd.TTL = 15 * time.Minute
	}

	value, err := generateTokenValue()
	if err != nil {
		return nil, fmt.Errorf("token: generate: %w", err)
	}

	token := &Token{
		ID:            uuid.New().String(),
		Value:         value,
		OrderID:       cmd.OrderID,
		MerchantID:    cmd.MerchantID,
		AllowedOrigin: cmd.AllowedOrigin,
		MaxUses:       cmd.MaxUses,
		UsedCount:     0,
		ExpiresAt:     time.Now().Add(cmd.TTL),
		CreatedAt:     time.Now(),
	}

	if err := s.store.Save(ctx, token); err != nil {
		return nil, fmt.Errorf("token: save: %w", err)
	}

	return token, nil
}

func (s *Service) Redeem(ctx context.Context, cmd RedeemTokenCmd) (*Token, error) {
	token, err := s.store.Get(ctx, cmd.Value)
	if err != nil {
		return nil, ErrTokenNotFound
	}

	if time.Now().After(token.ExpiresAt) {
		return nil, ErrTokenExpired
	}

	if token.UsedCount >= token.MaxUses {
		return nil, ErrTokenUsed
	}

	if token.AllowedOrigin != "" && cmd.Origin != token.AllowedOrigin {
		return nil, ErrOriginMismatch
	}

	if cmd.OrderID != "" && token.OrderID != "" && cmd.OrderID != token.OrderID {
		return nil, ErrTokenInvalid
	}

	if err := s.store.IncrementUseCount(ctx, cmd.Value); err != nil {
		return nil, fmt.Errorf("token: increment use: %w", err)
	}

	token.UsedCount++
	return token, nil
}

func generateTokenValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
