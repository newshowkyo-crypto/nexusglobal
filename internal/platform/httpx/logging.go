package httpx

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	status int
}

// Preserve streaming support without advertising it for writers that cannot flush.
type flushResponseWriter struct{ *responseWriter }

func (rw *flushResponseWriter) Flush() {
	rw.responseWriter.ResponseWriter.(http.Flusher).Flush()
}

func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// redactPath 对敏感路径做固定占位脱敏。
//
// P1 Payment Link 日志脱敏：
//   - /pay/{token} → /pay/<REDACTED>（token 是 64 hex chars 的敏感凭证）
//   - /checkout/v3/{session_id} → /checkout/v3/<REDACTED>（session_id 是敏感凭证）
//
// 禁止记录原 URL、Referer、Authorization。
// 此函数只处理 path，query string 一律丢弃（敏感参数可能在 query 中）。
func redactPath(path string) string {
	// /pay/{token} — token 是 64 字符 hex，脱敏为固定占位
	if strings.HasPrefix(path, "/pay/") {
		return "/pay/<REDACTED>"
	}
	// /checkout/v3/{session_id} — session_id 是敏感凭证
	if strings.HasPrefix(path, "/checkout/v3/") {
		return "/checkout/v3/<REDACTED>"
	}
	return path
}

// RequestLogger 请求日志中间件。
//
// P1 安全保证：
//   - 对 /pay/{token} 和 /checkout/v3/{session_id} 路径做固定占位脱敏
//   - 禁止记录原 URL（含 token）、Referer、Authorization
//   - 不记录 query string（可能含敏感参数）
//   - 只记录 method、脱敏后 path、status、duration、request_id、remote
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: 200}

			var writer http.ResponseWriter = rw
			if _, ok := w.(http.Flusher); ok {
				writer = &flushResponseWriter{responseWriter: rw}
			}
			next.ServeHTTP(writer, r)

			duration := time.Since(start)
			reqID := r.Header.Get("X-Request-ID")

			// P1: 脱敏 path，禁止记录原 URL（含 token）
			safePath := redactPath(r.URL.Path)

			logger.Info("request",
				"method", r.Method,
				"path", safePath,
				"status", rw.status,
				"duration_ms", duration.Milliseconds(),
				"request_id", reqID,
				"remote", r.RemoteAddr,
			)
		})
	}
}
