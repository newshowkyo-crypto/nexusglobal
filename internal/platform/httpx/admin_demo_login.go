package httpx

import (
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func isAdminHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func AdminDemoLoginHandler(username, passwordHash, tenantID, sessionSecret string, ttl time.Duration) http.HandlerFunc {
	origin := http.NewCrossOriginProtection()
	capacity := make(chan struct{}, 2)
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			writeErrorJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only POST is accepted", reqID)
			return
		}
		if !isAdminHTTPS(r) || origin.Check(r) != nil {
			writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", "必须通过本站 HTTPS 页面登录", reqID)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeErrorJSON(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "application/json is required", reqID)
			return
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Username) == 0 || len(body.Username) > 64 || len(body.Password) == 0 || len(body.Password) > 72 {
			writeErrorJSON(w, http.StatusBadRequest, "BAD_REQUEST", "请输入账号和密码", reqID)
			return
		}
		select {
		case capacity <- struct{}{}:
			defer func() { <-capacity }()
		default:
			w.Header().Set("Retry-After", "2")
			writeErrorJSON(w, http.StatusTooManyRequests, "RATE_LIMITED", "请求过多，请稍后重试", reqID)
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(body.Password)) != nil || body.Username != username {
			slog.Warn("admin demo login rejected", "req_id", reqID, "client_ip", r.RemoteAddr)
			writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "账号或密码不正确", reqID)
			return
		}
		identity := &AdminIdentity{
			Subject:  username,
			Username: username,
			Role:     AdminRoleViewer,
			TenantID: tenantID,
		}
		writeAdminSession(w, identity, sessionSecret, true, ttl, reqID)
	}
}
