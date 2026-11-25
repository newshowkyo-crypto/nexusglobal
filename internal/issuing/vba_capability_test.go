package issuing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVBACapability_NoExternalAdapter_FailClosed 验证没有真实 adapter 时
// VBA EXTERNAL_BANK_OPEN capability 不可用。
//
// G15 要求：没有真实 adapter 时 UI 明确禁用，不可显示"开户成功"。
// 此测试验证 NewVBACapabilityResolver 在 factory 为 nil 或 providerCode 未注册时
// 返回 ErrVBANoExternalAdapter。
func TestVBACapability_NoExternalAdapter_FailClosed(t *testing.T) {
	ctx := context.Background()

	// 场景 1：factory 为 nil。
	resolver := NewVBACapabilityResolver(nil, nil)
	cap, err := resolver.ResolveVBACapabilityExplicit(ctx, "checkout_com", "vault-key-123")
	assert.Error(t, err, "nil factory should return error")
	assert.True(t, errors.Is(err, ErrVBANoExternalAdapter),
		"error must be ErrVBANoExternalAdapter, got: %v", err)
	assert.Equal(t, VBACapabilityLocalRecordCRUD, cap,
		"capability must be LOCAL_RECORD_CRUD when no external adapter")

	// 场景 2：factory 不为 nil 但 providerCode 未注册。
	factory := NewHTTPAdapterFactory()
	resolver2 := NewVBACapabilityResolver(factory, nil)
	cap2, err2 := resolver2.ResolveVBACapabilityExplicit(ctx, "unregistered_provider", "vault-key-123")
	assert.Error(t, err2, "unregistered provider should return error")
	assert.True(t, errors.Is(err2, ErrVBANoExternalAdapter),
		"error must be ErrVBANoExternalAdapter, got: %v", err2)
	assert.Equal(t, VBACapabilityLocalRecordCRUD, cap2)
}

// TestVBACapability_BlockedCredential 验证 adapter 可用但凭证缺失时
// 返回 ErrVBABlockedCredential。
//
// G15 要求：没有凭证报 BLOCKED_EXTERNAL_CREDENTIAL，不得伪造凭证。
func TestVBACapability_BlockedCredential(t *testing.T) {
	ctx := context.Background()

	factory := NewHTTPAdapterFactory()
	// 注册一个需要凭证的 provider：传入空凭证时返回错误（模拟真实 adapter 行为）。
	factory.Register("real_bank", func(ctx context.Context, cred map[string]string) (IssuingAdapter, error) {
		if cred == nil || cred["api_key"] == "" {
			return nil, errors.New("api_key required")
		}
		return &httpAdapter{}, nil
	})

	// 场景 1：adapter 存在但 vaultKey 为空。
	resolver := NewVBACapabilityResolver(factory, nil)
	cap, err := resolver.ResolveVBACapabilityExplicit(ctx, "real_bank", "")
	assert.Error(t, err, "empty vault key should return error")
	assert.True(t, errors.Is(err, ErrVBABlockedCredential),
		"error must be ErrVBABlockedCredential, got: %v", err)
	assert.Equal(t, VBACapabilityLocalRecordCRUD, cap)
}

// TestVBACapability_ExternalBankOpen_Available 验证 adapter 和凭证都可用时
// 返回 VBACapabilityExternalBankOpen。
//
// 此测试模拟一个已注册的 adapter + 可用的凭证，验证 capability 解析为 EXTERNAL_BANK_OPEN。
func TestVBACapability_ExternalBankOpen_Available(t *testing.T) {
	ctx := context.Background()

	factory := NewHTTPAdapterFactory()
	// 注册一个不需要凭证的 mock adapter（模拟 adapter 可用）。
	factory.Register("mock_bank", func(ctx context.Context, cred map[string]string) (IssuingAdapter, error) {
		return &httpAdapter{}, nil
	})

	resolver := NewVBACapabilityResolver(factory, nil)
	cap, err := resolver.ResolveVBACapabilityExplicit(ctx, "mock_bank", "vault-key-123")

	// adapter 可用且不返回错误时，capability 应为 EXTERNAL_BANK_OPEN。
	assert.NoError(t, err)
	assert.Equal(t, VBACapabilityExternalBankOpen, cap)
}

// TestVBACapability_Constants 验证 VBA capability 常量。
func TestVBACapability_Constants(t *testing.T) {
	assert.Equal(t, VBACapability("LOCAL_RECORD_CRUD"), VBACapabilityLocalRecordCRUD)
	assert.Equal(t, VBACapability("EXTERNAL_BANK_OPEN"), VBACapabilityExternalBankOpen)
	assert.NotEqual(t, VBACapabilityLocalRecordCRUD, VBACapabilityExternalBankOpen,
		"LOCAL_RECORD_CRUD and EXTERNAL_BANK_OPEN must be distinct")
}

// TestVBAErrors 验证 VBA capability 错误。
func TestVBAErrors(t *testing.T) {
	assert.True(t, errors.Is(ErrVBANoExternalAdapter, ErrVBANoExternalAdapter))
	assert.True(t, errors.Is(ErrVBABlockedCredential, ErrVBABlockedCredential))
	assert.NotEqual(t, ErrVBANoExternalAdapter, ErrVBABlockedCredential,
		"ErrVBANoExternalAdapter and ErrVBABlockedCredential must be distinct")
}
