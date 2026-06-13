package token

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_Create(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID:       "order-123",
		MerchantID:    "merchant-1",
		AllowedOrigin: "https://checkout.example.com",
		MaxUses:       1,
		TTL:           15 * time.Minute,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, token.ID)
	assert.NotEmpty(t, token.Value)
	assert.Equal(t, "order-123", token.OrderID)
	assert.Equal(t, 1, token.MaxUses)
	assert.Equal(t, 0, token.UsedCount)
	assert.True(t, token.ExpiresAt.After(time.Now()))
}

func TestService_Redeem_SingleUse(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID:    "order-123",
		MerchantID: "merchant-1",
		MaxUses:    1,
		TTL:        15 * time.Minute,
	})
	require.NoError(t, err)

	redeemed, err := svc.Redeem(ctx, RedeemTokenCmd{
		Value:   token.Value,
		Origin:  "https://checkout.example.com",
		OrderID: "order-123",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, redeemed.UsedCount)

	_, err = svc.Redeem(ctx, RedeemTokenCmd{
		Value:   token.Value,
		Origin:  "https://checkout.example.com",
		OrderID: "order-123",
	})
	assert.ErrorIs(t, err, ErrTokenUsed)
}

func TestService_Redeem_Expired(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID: "order-123",
		MaxUses: 1,
		TTL:     1 * time.Millisecond,
	})
	require.NoError(t, err)

	time.Sleep(5 * time.Millisecond)

	_, err = svc.Redeem(ctx, RedeemTokenCmd{Value: token.Value})
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestService_Redeem_OriginMismatch(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID:       "order-123",
		AllowedOrigin: "https://checkout.example.com",
		MaxUses:       1,
		TTL:           15 * time.Minute,
	})
	require.NoError(t, err)

	_, err = svc.Redeem(ctx, RedeemTokenCmd{
		Value:  token.Value,
		Origin: "https://evil.com",
	})
	assert.ErrorIs(t, err, ErrOriginMismatch)
}

func TestService_Redeem_MultiUse(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID: "order-123",
		MaxUses: 3,
		TTL:     15 * time.Minute,
	})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := svc.Redeem(ctx, RedeemTokenCmd{Value: token.Value})
		require.NoError(t, err)
	}

	_, err = svc.Redeem(ctx, RedeemTokenCmd{Value: token.Value})
	assert.ErrorIs(t, err, ErrTokenUsed)
}

func TestService_Redeem_NotFound(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	_, err := svc.Redeem(ctx, RedeemTokenCmd{Value: "nonexistent"})
	assert.ErrorIs(t, err, ErrTokenNotFound)
}
