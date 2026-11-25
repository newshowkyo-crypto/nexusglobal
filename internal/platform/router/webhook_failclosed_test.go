package router

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/config"
)

// TestBuildRouter_ProductionMissingWebhookSigningKey_Panics 验证 production 模式
// 缺少 WEBHOOK_SIGNING_KEY 时 BuildRouter 必须 panic（fail-closed）。
func TestBuildRouter_ProductionMissingWebhookSigningKey_Panics(t *testing.T) {
	deps := Deps{
		Cfg: &config.Config{
			AppEnv:             "production",
			ExternalMode:       "real",
			JWTSecret:          "a_very_long_secret_key_that_is_at_least_32_bytes",
			InternalToken:      "a_very_long_internal_token_that_is_at_least_32_bytes",
			AdminSessionSecret: "a_very_long_admin_secret_that_is_at_least_32_bytes",
			MasterKeyBase64:    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			WebhookSigningKey:  "", // 空 — production 必须 fail-closed
		},
		Log: slog.Default(),
	}

	require.Panics(t, func() {
		BuildRouter(deps)
	}, "production mode with empty WEBHOOK_SIGNING_KEY must panic")
}

// TestBuildRouter_ProductionShortWebhookSigningKey_Panics 验证 production 模式
// WEBHOOK_SIGNING_KEY 过短时 BuildRouter 必须 panic。
func TestBuildRouter_ProductionShortWebhookSigningKey_Panics(t *testing.T) {
	deps := Deps{
		Cfg: &config.Config{
			AppEnv:             "production",
			ExternalMode:       "real",
			JWTSecret:          "a_very_long_secret_key_that_is_at_least_32_bytes",
			InternalToken:      "a_very_long_internal_token_that_is_at_least_32_bytes",
			AdminSessionSecret: "a_very_long_admin_secret_that_is_at_least_32_bytes",
			MasterKeyBase64:    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			WebhookSigningKey:  "short_key_only_15_bytes", // < 32 bytes
		},
		Log: slog.Default(),
	}

	require.Panics(t, func() {
		BuildRouter(deps)
	}, "production mode with short WEBHOOK_SIGNING_KEY must panic")
}

// TestBuildRouter_LocalMockNoWebhookKey_NoUnifiedRoute 验证 local/mock 模式
// 未配置 WebhookSigningKey 时不注册 unified webhook 路由（不 panic，不伪装可用）。
func TestBuildRouter_LocalMockNoWebhookKey_NoUnifiedRoute(t *testing.T) {
	deps := Deps{
		Cfg: &config.Config{
			AppEnv:            "local",
			ExternalMode:      "mock",
			JWTSecret:         "test-jwt-secret-at-least-32-characters-long!!",
			InternalToken:     "test-internal-token-at-least-32-chars!!",
			MasterKeyBase64:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			WebhookSigningKey: "", // local/mock 空 — 允许不注册
		},
		Log: slog.Default(),
	}

	r := BuildRouter(deps)
	require.NotNil(t, r)

	// 验证 unified webhook 路由不存在
	routes := r.Routes()
	for _, route := range routes {
		assert.NotContains(t, route.Pattern, "/webhook/unified/",
			"local/mock without WebhookSigningKey must NOT register unified webhook route")
	}
}

// TestBuildRouter_ProductionValidWebhookConfig_RegistersUnifiedRoute 验证合法配置下
// Unified Webhook 路由确实注册。
// R26 Task 1: 使用 registerUnifiedWebhookRoute helper + chi.Walk 验证
// POST /webhook/unified/{provider_type}/{provider_id} 路由存在。
// 同时验证 validateUnifiedWebhookConfig 在合法配置下返回 nil。
func TestBuildRouter_ProductionValidWebhookConfig_RegistersUnifiedRoute(t *testing.T) {
	// 1. 验证 validateUnifiedWebhookConfig 对合法 production 配置返回 nil
	cfg := &config.Config{
		AppEnv:            "production",
		ExternalMode:      "real",
		WebhookSigningKey: "a_valid_signing_key_that_is_at_least_32_bytes_long!!",
	}
	err := validateUnifiedWebhookConfig(cfg, slog.Default())
	assert.NoError(t, err, "valid production config should pass validation")

	// 2. 验证 registerUnifiedWebhookRoute 注册了正确的 POST 路由
	r := chi.NewRouter()
	fakeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	registerUnifiedWebhookRoute(r, fakeHandler)

	foundUnified := false
	chi.Walk(r, func(method string, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == "POST" && route == "/webhook/unified/{provider_type}/{provider_id}" {
			foundUnified = true
		}
		return nil
	})

	assert.True(t, foundUnified,
		"POST /webhook/unified/{provider_type}/{provider_id} route must be registered with valid config")
}
