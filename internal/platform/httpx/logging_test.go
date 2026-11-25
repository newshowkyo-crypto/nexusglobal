package httpx

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRedactPath_PaymentLinkTokenRedacted 验证 /pay/{token} 路径被脱敏为固定占位。
// P1: 禁止在请求日志中记录原 URL（含 token）。
func TestRedactPath_PaymentLinkTokenRedacted(t *testing.T) {
	token := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	path := "/pay/" + token

	got := redactPath(path)
	if got != "/pay/<REDACTED>" {
		t.Errorf("redactPath(/pay/{token}) = %q, want /pay/<REDACTED>", got)
	}
	if strings.Contains(got, token) {
		t.Errorf("redactPath leaked token: %q contains token", got)
	}
}

// TestRedactPath_CheckoutSessionRedacted 验证 /checkout/v3/{session_id} 路径被脱敏。
func TestRedactPath_CheckoutSessionRedacted(t *testing.T) {
	sessionID := "sess_abc123def456"
	path := "/checkout/v3/" + sessionID

	got := redactPath(path)
	if got != "/checkout/v3/<REDACTED>" {
		t.Errorf("redactPath(/checkout/v3/{session_id}) = %q, want /checkout/v3/<REDACTED>", got)
	}
	if strings.Contains(got, sessionID) {
		t.Errorf("redactPath leaked session_id: %q contains session_id", got)
	}
}

// TestRedactPath_NonSensitivePathPreserved 验证非敏感路径不被脱敏。
func TestRedactPath_NonSensitivePathPreserved(t *testing.T) {
	cases := []string{
		"/healthz",
		"/readyz",
		"/api/v3/channels",
		"/admin/dashboard",
		"/pay", // 不带 token 的 /pay 本身不脱敏（只有 /pay/{token} 才脱敏）
	}
	for _, path := range cases {
		got := redactPath(path)
		if got != path {
			t.Errorf("redactPath(%q) = %q, want %q (non-sensitive path should be preserved)", path, got, path)
		}
	}
}

// TestRequestLogger_PaymentLinkTokenNotLogged 验证请求日志不记录 /pay/{token} 中的 token。
// P1: 请求日志必须对 /pay/{token} 做固定占位脱敏，禁止记录原 URL。
func TestRequestLogger_PaymentLinkTokenNotLogged(t *testing.T) {
	token := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	handler := RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/pay/"+token, nil)
	req.Header.Set("X-Request-ID", "req-123")
	req.Header.Set("Referer", "https://evil.example.com/pay/"+token)
	req.Header.Set("Authorization", "Bearer secret-jwt-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	logOutput := logBuf.String()

	// token 不得出现在日志中
	if strings.Contains(logOutput, token) {
		t.Errorf("token leaked into request log:\n%s", logOutput)
	}

	// 必须出现脱敏后的路径
	if !strings.Contains(logOutput, "/pay/<REDACTED>") {
		t.Errorf("log does not contain redacted path /pay/<REDACTED>:\n%s", logOutput)
	}

	// Authorization 不得出现在日志中
	if strings.Contains(logOutput, "secret-jwt-token") {
		t.Errorf("Authorization header value leaked into log:\n%s", logOutput)
	}

	// Referer 中的 token 不得出现在日志中
	if strings.Contains(logOutput, "Referer") && strings.Contains(logOutput, token) {
		t.Errorf("Referer with token leaked into log:\n%s", logOutput)
	}
}

// TestRequestLogger_QueryStringDropped 验证请求日志不记录 query string。
// P1: query string 可能含敏感参数，一律丢弃。
func TestRequestLogger_QueryStringDropped(t *testing.T) {
	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	handler := RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v3/channels?secret=abc123&token=xyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	logOutput := logBuf.String()

	if strings.Contains(logOutput, "secret=abc123") {
		t.Errorf("query string leaked into log:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "token=xyz") {
		t.Errorf("query string leaked into log:\n%s", logOutput)
	}
}

// TestRedactPath 验证 /pay/{token} 和 /checkout/v3/{session} 被脱敏。
// P1: 综合验证 redactPath 对所有敏感路径的脱敏行为。
func TestRedactPath(t *testing.T) {
	token := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	sessionID := "sess_abc123def456"

	cases := []struct {
		name     string
		path     string
		expected string
	}{
		{"payment_link_token", "/pay/" + token, "/pay/<REDACTED>"},
		{"payment_link_confirm", "/pay/" + token + "/confirm", "/pay/<REDACTED>"},
		{"checkout_v3_session", "/checkout/v3/" + sessionID, "/checkout/v3/<REDACTED>"},
		{"healthz", "/healthz", "/healthz"},
		{"readyz", "/readyz", "/readyz"},
		{"pay_no_token", "/pay", "/pay"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactPath(tc.path)
			if got != tc.expected {
				t.Errorf("redactPath(%q) = %q, want %q", tc.path, got, tc.expected)
			}
			// 敏感路径中不得包含原始 token/sessionID
			if strings.HasPrefix(tc.path, "/pay/") && tc.path != "/pay" {
				if strings.Contains(got, token) {
					t.Errorf("redactPath leaked token in %q: %q", tc.name, got)
				}
			}
			if strings.HasPrefix(tc.path, "/checkout/v3/") {
				if strings.Contains(got, sessionID) {
					t.Errorf("redactPath leaked session_id in %q: %q", tc.name, got)
				}
			}
		})
	}
}

// TestRecoverRedactsPath 验证 panic 时日志中 path 被脱敏。
// P1: Recover 中间件必须调用 redactPath，禁止在 panic 日志中泄露敏感路径。
func TestRecoverRedactsPath(t *testing.T) {
	token := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	// 构造一个会 panic 的 handler
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	recoverMW := Recover(logger)
	handler := recoverMW(panicHandler)

	req := httptest.NewRequest(http.MethodPost, "/pay/"+token+"/confirm", nil)
	req.Header.Set("X-Request-ID", "req-panic-123")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	logOutput := logBuf.String()

	// token 不得出现在 panic 日志中
	if strings.Contains(logOutput, token) {
		t.Errorf("token leaked into panic recovery log:\n%s", logOutput)
	}

	// 必须出现脱敏后的路径
	if !strings.Contains(logOutput, "/pay/<REDACTED>") {
		t.Errorf("panic recovery log does not contain redacted path /pay/<REDACTED>:\n%s", logOutput)
	}

	// 必须记录 panic 信息
	if !strings.Contains(logOutput, "panic recovered") {
		t.Errorf("panic recovery log does not contain 'panic recovered':\n%s", logOutput)
	}

	// 响应应为 500
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}
}
