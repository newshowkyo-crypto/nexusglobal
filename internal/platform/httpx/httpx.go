package httpx

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type contextKey string

const (
	ContextKeyMerchantID contextKey = "merchant_id"
	ContextKeyAdminUser  contextKey = "admin_user"
	ContextKeySubject    contextKey = "subject"
)

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", id)
		r.Header.Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					logger.Error("panic recovered",
						"panic", p,
						"stack", string(debug.Stack()),
						"method", r.Method,
						"path", redactPath(r.URL.Path))
					reqID := r.Header.Get("X-Request-ID")
					writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "internal server error", reqID)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func AdminAuth(jwtSecret string) func(http.Handler) http.Handler {
	// G03 收紧：委托给 AdminAuthStrict，强制 iss/aud/role 校验。
	// 旧调用方仍只传 jwtSecret，使用默认 iss/aud，不支持 Cookie 会话。
	return AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, "")
}

func MerchantAuth(jwtSecret string) func(http.Handler) http.Handler {
	if jwtSecret == "" {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "JWT_SECRET not configured", r.Header.Get("X-Request-ID"))
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			tokenStr := extractBearerToken(r)
			if tokenStr == "" {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid Authorization header", reqID)
				return
			}

			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return []byte(jwtSecret), nil
			})
			if err != nil || !token.Valid {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token", reqID)
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token claims", reqID)
				return
			}

			subject, _ := claims["sub"].(string)
			if subject == "" {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing subject in token", reqID)
				return
			}

			merchantID, ok := claims["merchant_id"].(string)
			if !ok || merchantID == "" {
				writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", "token missing merchant_id claim", reqID)
				return
			}

			ctx := context.WithValue(r.Context(), ContextKeyMerchantID, merchantID)
			ctx = context.WithValue(ctx, ContextKeySubject, subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func InternalAuth(sharedSecret string) func(http.Handler) http.Handler {
	if sharedSecret == "" {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "INTERNAL_TOKEN not configured", r.Header.Get("X-Request-ID"))
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			token := r.Header.Get("X-Internal-Token")
			if token == "" {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing X-Internal-Token header", reqID)
				return
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(sharedSecret)) != 1 {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid internal token", reqID)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

func GetMerchantID(ctx context.Context) string {
	if v, ok := ctx.Value(ContextKeyMerchantID).(string); ok {
		return v
	}
	return ""
}

func GetSubject(ctx context.Context) string {
	if v, ok := ctx.Value(ContextKeySubject).(string); ok {
		return v
	}
	return ""
}

func HealthCheck(dbPing func() error, redisPing func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}
}

func ReadinessCheck(dbPing func() error, redisPing func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var failures []string
		if err := dbPing(); err != nil {
			failures = append(failures, "database: "+err.Error())
		}
		if err := redisPing(); err != nil {
			failures = append(failures, "redis: "+err.Error())
		}
		if len(failures) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			msg := strings.Join(failures, "; ")
			reqID := r.Header.Get("X-Request-ID")
			w.Write([]byte(`{"error":{"code":"NOT_READY","message":"` + msg + `","request_id":"` + reqID + `"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	}
}

func writeErrorJSON(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + message + `","request_id":"` + requestID + `"}}`))
}

func WriteError(w http.ResponseWriter, err *apperr.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.HTTP)
	w.Write([]byte(`{"code":"` + err.Code + `","message":"` + err.Message + `"}`))
}

// readRandBytes 从 crypto/rand 读取 n 字节随机数据。
func readRandBytes(b []byte) (int, error) {
	return rand.Read(b)
}

// encodeBase64 返回 base64 标准编码字符串。
func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// AdminLoginPageHandler 渲染管理员登录页面（只渲染 HTML，不做鉴权）。
func AdminLoginPageHandler(appEnv string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nonce := generateNonce()
		writeSecurityHeaders(w, nonce)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(adminLoginHTML(nonce, appEnv)))
	}
}

// adminLoginHTML 生成管理员登录页 HTML。
func adminLoginHTML(nonce, env string) string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>NexusGlobal 管理后台登录</title>
<style nonce="` + nonce + `">
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#0f172a;color:#e2e8f0;min-height:100vh;display:flex;align-items:center;justify-content:center}
.login-card{background:#1e293b;border:1px solid #334155;border-radius:12px;padding:32px;max-width:400px;width:90%}
.login-card h1{color:#f1f5f9;font-size:20px;margin-bottom:24px;text-align:center}
.login-card .form-group{display:flex;flex-direction:column;gap:6px;margin-bottom:16px}
.login-card label{color:#94a3b8;font-size:13px;font-weight:600}
.login-card input{background:#0f172a;color:#e2e8f0;border:1px solid #334155;padding:10px 14px;border-radius:6px;font-size:14px}
.login-card input:focus{outline:none;border-color:#38bdf8}
.login-card .submit-btn{background:#38bdf8;color:#0f172a;border:none;padding:10px 20px;border-radius:6px;cursor:pointer;font-size:14px;font-weight:600;width:100%;margin-top:8px}
.login-card .submit-btn:hover{opacity:.9}
.login-card .error{color:#f87171;font-size:13px;margin-top:8px;text-align:center}
.login-card .hint{color:#64748b;font-size:12px;margin-top:12px;text-align:center}
.hidden{display:none}
</style>
</head>
<body>
<div class="login-card">
<h1>NexusGlobal 管理后台</h1>
<form id="login-form">
<div class="form-group">
<label for="admin-jwt">管理员登录凭证</label>
<input type="password" id="admin-jwt" placeholder="粘贴管理员提供的短期登录凭证" autocomplete="off">
</div>
<button type="submit" class="submit-btn">登录</button>
<div id="error-msg" class="error hidden"></div>
</form>
<div class="hint">请使用管理员提供的短期登录凭证。<br>登录后建立安全会话，凭证不会保存在浏览器本地存储中。</div>
</div>
<script nonce="` + nonce + `">
document.getElementById('login-form').addEventListener('submit',function(e){
e.preventDefault();
var jwt=document.getElementById('admin-jwt').value;
if(!jwt){showError('请输入管理员登录凭证');return;}
fetch('/admin/session/exchange',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({admin_jwt:jwt}),credentials:'same-origin'}).then(function(r){return r.json().then(function(d){if(r.ok){window.location.href='/admin/dashboard';}else{showError(d.error?d.error.message:'登录失败');}});}).catch(function(){showError('网络错误，请重试');});
});
function showError(msg){var el=document.getElementById('error-msg');el.textContent=msg;el.classList.remove('hidden');}
</script>
</body>
</html>`
}

// AdminSessionExchangeHandler 接收预签发的短期 Admin JWT，
// 验证成功后用 ADMIN_SESSION_SECRET 签发 HttpOnly 会话 Cookie + CSRF Cookie。
// 可选参数 sessionTTL 覆盖默认会话时长（来自 ADMIN_SESSION_TTL），超过 AdminSessionMaxTTL 会被收敛。
func AdminSessionExchangeHandler(jwtSecret, sessionSecret, appEnv string, sessionTTL ...time.Duration) http.HandlerFunc {
	ttl := time.Duration(0)
	if len(sessionTTL) > 0 {
		ttl = sessionTTL[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if r.Method != http.MethodPost {
			writeErrorJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only POST is accepted", reqID)
			return
		}
		if sessionSecret == "" {
			writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "ADMIN_SESSION_SECRET not configured", reqID)
			return
		}

		// ADM-020: 请求体大小限制（防超大 payload 放大解析压力）
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB

		// 解析请求体
		var body struct {
			AdminJWT string `json:"admin_jwt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body", reqID)
			return
		}
		if body.AdminJWT == "" {
			writeErrorJSON(w, http.StatusBadRequest, "BAD_REQUEST", "admin_jwt is required", reqID)
			return
		}

		// 使用统一的 VerifyAdminJWT 函数验证（与 AdminAuthStrict 共用同一验证逻辑）
		identity, err := VerifyAdminJWT(body.AdminJWT, jwtSecret, AdminIssuerExpected, AdminAudienceExpected, time.Now())
		if err != nil {
			// ADM-020: 失败审计（不记录 token 内容，只记录失败事实与原因类别）
			slog.Warn("admin session exchange rejected",
				"req_id", reqID,
				"client_ip", r.RemoteAddr,
				"reason_class", "invalid_admin_jwt",
			)
			if strings.Contains(err.Error(), "merchant token") || strings.Contains(err.Error(), "invalid admin role") {
				writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", err.Error(), reqID)
			} else {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", err.Error(), reqID)
			}
			return
		}

		// 签发会话 Cookie
		sessionCookie, err := signSessionCookie(identity, sessionSecret, appEnv, ttl)
		if err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "failed to create session", reqID)
			return
		}
		http.SetCookie(w, sessionCookie)

		// 签发 CSRF Cookie（双提交模式）
		csrfCookie := signCSRFCookie(appEnv, ttl)
		if csrfCookie != nil {
			http.SetCookie(w, csrfCookie)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","message":"session established"}`))
	}
}

// AdminSessionLogoutHandler 清除会话 Cookie 和 CSRF Cookie。
func AdminSessionLogoutHandler(appEnv string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			reqID := r.Header.Get("X-Request-ID")
			writeErrorJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only POST is accepted", reqID)
			return
		}
		// 清除会话 Cookie
		http.SetCookie(w, &http.Cookie{
			Name:     AdminSessionCookieName,
			Value:    "",
			Path:     "/admin",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		// 清除 CSRF Cookie
		http.SetCookie(w, &http.Cookie{
			Name:     AdminCSRFCookieName,
			Value:    "",
			Path:     "/admin",
			MaxAge:   -1,
			SameSite: http.SameSiteStrictMode,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","message":"session cleared"}`))
	}
}
