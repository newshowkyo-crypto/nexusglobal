package vcc_funding

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
)

// TestVCCAdapterFactory_LocalMock_RequiresRealPool 验证 VCC local mock 要求真实 DB pool。
//
// G15 要求：VCC local mock 使用真实 issuing repository、ledger、fraud guard、outbox。
// 此测试验证 "mock" kind 在 pool 为 nil 时返回错误，
// 证明它使用 LocalLedgerService（真实 DB），不是 NoopLedgerService（账本断流）。
//
// NoopLedgerService 不要求 pool，会静默返回 nil（账本断流）。
// LocalLedgerService 要求 pool，nil 时返回错误（fail-closed）。
func TestVCCAdapterFactory_LocalMock_RequiresRealPool(t *testing.T) {
	factory := NewDefaultVCCAdapterFactory()
	logger := slog.Default()

	// "mock" kind 在 pool 为 nil 时应返回错误，证明它使用 LocalLedgerService。
	bundle, err := factory.Create("mock", nil, logger)

	assert.Error(t, err, "mock kind should require non-nil pool for real ledger")
	assert.Nil(t, bundle, "bundle must be nil on error")
	assert.Contains(t, err.Error(), "requires non-nil pool for real ledger",
		"error should indicate LocalLedgerService requires real DB pool")
}

// TestVCCAdapterFactory_LocalKind_RequiresRealPool 验证 "local" kind 同 "mock"。
func TestVCCAdapterFactory_LocalKind_RequiresRealPool(t *testing.T) {
	factory := NewDefaultVCCAdapterFactory()
	logger := slog.Default()

	bundle, err := factory.Create("local", nil, logger)

	assert.Error(t, err, "local kind should require non-nil pool for real ledger")
	assert.Nil(t, bundle)
	assert.Contains(t, err.Error(), "requires non-nil pool for real ledger")
}

// TestVCCAdapterFactory_UnsupportedKind_FailClosed 验证未知 provider kind 启动失败。
//
// G15 红线：未知 provider 或未实现 capability 必须明确拒绝启动或标记
// UNSUPPORTED_CAPABILITY，严禁自动回退 mock。
//
// 此测试验证 "stripe"、"checkout_com"、"" 等未注册的 kind 返回
// ErrUnsupportedCapability，而不是静默回退到 mock。
func TestVCCAdapterFactory_UnsupportedKind_FailClosed(t *testing.T) {
	factory := NewDefaultVCCAdapterFactory()
	logger := slog.Default()

	unsupportedKinds := []string{
		"stripe",
		"checkout_com",
		"marqeta",
		"unknown_provider",
		"", // empty string
	}

	for _, kind := range unsupportedKinds {
		t.Run(kind, func(t *testing.T) {
			bundle, err := factory.Create(kind, nil, logger)

			// 必须返回错误，不能静默回退到 mock。
			assert.Error(t, err, "unsupported kind %q must return error, not fallback to mock", kind)
			assert.Nil(t, bundle, "bundle must be nil on error")
			assert.True(t, errors.Is(err, ErrUnsupportedCapability),
				"error must be ErrUnsupportedCapability, got: %v", err)
		})
	}
}

// TestVCCAdapterFactory_BlockedCredential 验证凭证缺失时返回 BLOCKED_EXTERNAL_CREDENTIAL。
//
// 此测试注册一个需要凭证的 kind，验证 constructor 返回 ErrBlockedExternalCredential
// 时 factory 正确透传错误。
func TestVCCAdapterFactory_BlockedCredential(t *testing.T) {
	factory := NewVCCAdapterFactory()
	logger := slog.Default()

	// 注册一个需要凭证的 kind，constructor 返回 ErrBlockedExternalCredential。
	factory.RegisterLocal("real_provider", func(pool *pgxpool.Pool, logger *slog.Logger) (*VCCAdapterBundle, error) {
		return nil, ErrBlockedExternalCredential
	})

	bundle, err := factory.Create("real_provider", nil, logger)

	// factory 应透传 ErrBlockedExternalCredential，不静默回退到 mock。
	assert.Error(t, err)
	assert.Nil(t, bundle)
	assert.True(t, errors.Is(err, ErrBlockedExternalCredential),
		"error must be ErrBlockedExternalCredential, got: %v", err)
}

// TestSupportedVCCKinds 验证 SupportedVCCKinds 白名单。
func TestSupportedVCCKinds(t *testing.T) {
	assert.True(t, SupportedVCCKinds["mock"], "mock should be in supported kinds")
	assert.True(t, SupportedVCCKinds["local"], "local should be in supported kinds")
	assert.False(t, SupportedVCCKinds["stripe"], "stripe should not be in supported kinds")
	assert.False(t, SupportedVCCKinds[""], "empty string should not be in supported kinds")
}

// TestLocalLedgerService_NilPool_ReturnsError 验证 LocalLedgerService 在 nil pool 时返回错误。
//
// 这保证 LocalLedgerService 不会静默跳过账本写入（NoopLedgerService 的行为）。
func TestLocalLedgerService_NilPool_ReturnsError(t *testing.T) {
	svc := NewLocalLedgerService(nil, slog.Default())
	ctx := context.Background()

	err := svc.CreateCryptoPoolLedgerEntry(ctx, LedgerEntry{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires non-nil pool")

	err = svc.TriggerVCCFundingEvent(ctx, VCCFundingEvent{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires non-nil pool")
}

// TestNoopLedgerService_AlwaysReturnsNil 验证 NoopLedgerService 永远返回 nil（账本断流）。
//
// 此测试文档化 NoopLedgerService 的行为：它不写入任何记录。
// 这正是 G15 要修复的问题——生产装配禁止使用 NoopLedgerService。
func TestNoopLedgerService_AlwaysReturnsNil(t *testing.T) {
	svc := NewNoopLedgerService()
	ctx := context.Background()

	// NoopLedgerService 永远返回 nil，不写入任何记录。
	// 这是账本断流的根本原因。
	err := svc.CreateCryptoPoolLedgerEntry(ctx, LedgerEntry{})
	assert.NoError(t, err, "NoopLedgerService always returns nil (ledger break - this is the bug G15 fixes)")

	err = svc.TriggerVCCFundingEvent(ctx, VCCFundingEvent{})
	assert.NoError(t, err, "NoopLedgerService always returns nil (ledger break - this is the bug G15 fixes)")
}
