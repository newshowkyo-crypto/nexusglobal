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

// SEC-003: Peek 校验但不消费 single-use token——页面查看/validate 只 peek，
// 多次 Peek 后 token 仍可被 Redeem 一次性消费。
func TestService_Peek_DoesNotConsume(t *testing.T) {
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

	// 多次 Peek（模拟页面刷新/validate）
	for i := 0; i < 3; i++ {
		p, err := svc.Peek(ctx, RedeemTokenCmd{
			Value:   token.Value,
			Origin:  "https://checkout.example.com",
			OrderID: "order-123",
		})
		require.NoError(t, err)
		assert.Equal(t, 0, p.UsedCount, "Peek must not increment used count")
	}

	// Peek 后 Redeem 仍然成功（旧实现会直接 ErrTokenUsed）
	redeemed, err := svc.Redeem(ctx, RedeemTokenCmd{
		Value:   token.Value,
		Origin:  "https://checkout.example.com",
		OrderID: "order-123",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, redeemed.UsedCount)

	// 消费后 Peek 也应报告已用
	_, err = svc.Peek(ctx, RedeemTokenCmd{
		Value:   token.Value,
		Origin:  "https://checkout.example.com",
		OrderID: "order-123",
	})
	assert.ErrorIs(t, err, ErrTokenUsed)
}

// SEC-003: Peek 与 Redeem 的校验规则一致（过期拒绝）。
func TestService_Peek_Expired(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	token, err := svc.Create(ctx, CreateTokenCmd{
		OrderID: "order-123",
		MaxUses: 1,
		TTL:     1 * time.Millisecond,
	})
	require.NoError(t, err)

	time.Sleep(5 * time.Millisecond) // Create 对非正 TTL 回退 15min，用短 TTL + sleep 制造过期

	_, err = svc.Peek(ctx, RedeemTokenCmd{Value: token.Value})
	assert.ErrorIs(t, err, ErrTokenExpired)
}
