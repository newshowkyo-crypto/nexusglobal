package httpx

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// Admin 会话 Cookie 相关常量。
const (
	AdminSessionCookieName = "admin_session"
	AdminCSRFHeaderName    = "X-CSRF-Token"
	AdminCSRFCookieName    = "admin_csrf"
	AdminSessionMaxAge     = 15 * time.Minute
)

// Admin JWT 收紧 (G03)：
//
// 原先 AdminAuth 只校验签名 + sub，任何持有 JWT_SECRET 的人都能签发
// 无 role/iss/aud 的 token 并访问全部 Admin API。商户 JWT 即使签名有效
// 也能进入 Admin API（因为只看 sub）。
//
// 收紧后：
//  1. 强制 HMAC 签名算法（拒绝 none/RS*/ES*）
//  2. 强制 iss == "nexusglobal-admin"
//  3. 强制 aud == "nexusglobal-admin-api"
//  4. 强制 exp/nbf 有效
//  5. 强制 sub 非空
//  6. 强制 role ∈ {viewer, operator, approver, security-admin}
//  7. 商户 JWT（含 merchant_id claim）一律 403
//  8. 身份只从已验证 Context 获取，拒绝 body 中伪造的 requester/checker/operator
//
// 权限矩阵（最小权限）：
//   - viewer:         所有 GET /admin/*
//   - operator:       viewer + POST/PATCH/DELETE 配置写入（通道、商户、桥接、钱包）
//   - approver:       viewer + POST /admin/api/v3/approvals/{id}/approve|reject
//   - security-admin: 全部，包括 kill switch、凭证覆盖、risk-hold、rotate-secret

const (
	AdminIssuerExpected   = "nexusglobal-admin"
	AdminAudienceExpected = "nexusglobal-admin-api"

	AdminRoleViewer        = "viewer"
	AdminRoleOperator      = "operator"
	AdminRoleApprover      = "approver"
	AdminRoleSecurityAdmin = "security-admin"
)

// AdminContextKey 标识已验证的管理员身份上下文。
// 与 ContextKeySubject/ContextKeyAdminUser 共存，但新增 role/permissions/tenantID。
type AdminContextKey struct{}

// AdminIdentity 是从已验证 JWT 提取的管理员身份。
// 所有 handler 必须通过 GetAdminIdentity(r.Context()) 获取身份，
// 不得从请求体读取 requester/checker/operator 字段（RED-15）。
type AdminIdentity struct {
	Subject     string
	Username    string
	Role        string
	TenantID    string
	Permissions []string
	// AuthenticatedAt 用于审计日志
	AuthenticatedAt time.Time
	// SourceExpiry 是来源 Admin JWT 的 exp 时刻（仅 Bearer 交换场景有值）。
	// 会话 Cookie 有效期不得超过来源 JWT 的剩余有效期。
	SourceExpiry time.Time
}

// Permission 常量。每条管理路由在 RequirePermission 中声明所需权限。
const (
	PermAdminRead            = "admin:read"
	PermAdminWriteConfig     = "admin:write:config"
	PermAdminApprove         = "admin:approve"
	PermAdminRequestChange   = "admin:request_change"
	PermAdminKillSwitch      = "admin:kill_switch"
	PermAdminRecovery        = "admin:recovery"
	PermAdminCredentialWrite = "admin:credential:write"
	PermAdminRiskHold        = "admin:risk:hold"
	PermPaymentLinkCreate    = "payment_link:create"
)

// rolePermissions 是最小权限矩阵。
var rolePermissions = map[string][]string{
	AdminRoleViewer: {
		PermAdminRead,
	},
	AdminRoleOperator: {
		PermAdminRead,
		PermAdminWriteConfig,
		PermPaymentLinkCreate,
		PermAdminRequestChange,
	},
	AdminRoleApprover: {
		PermAdminRead,
		PermAdminApprove,
	},
	AdminRoleSecurityAdmin: {
		PermAdminRead,
		PermAdminWriteConfig,
		PermAdminApprove,
		PermAdminRequestChange,
		PermAdminKillSwitch,
		PermAdminRecovery,
		PermAdminCredentialWrite,
		PermAdminRiskHold,
		PermPaymentLinkCreate,
	},
}

// validAdminRoles 用于快速校验 role claim。
var validAdminRoles = map[string]bool{
	AdminRoleViewer:        true,
	AdminRoleOperator:      true,
	AdminRoleApprover:      true,
	AdminRoleSecurityAdmin: true,
}

// RolePermissions 返回指定角色的权限列表（只读副本）。
// 用于测试注入 AdminIdentity 时构造 permissions 字段。
// 未知角色返回 nil。
func RolePermissions(role string) []string {
	perms := rolePermissions[role]
	cp := make([]string, len(perms))
	copy(cp, perms)
	return cp
}

// HasPermission 检查身份是否拥有给定权限。
func (a *AdminIdentity) HasPermission(perm string) bool {
	if a == nil {
		return false
	}
	for _, p := range a.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// GetAdminIdentity 从 context 提取已验证的管理员身份。
// 返回 (identity, true) 表示已验证；否则 (nil, false)。
func GetAdminIdentity(ctx context.Context) (*AdminIdentity, bool) {
	v, ok := ctx.Value(AdminContextKey{}).(*AdminIdentity)
	return v, ok
}

// signSessionCookie 使用 ADMIN_SESSION_SECRET 签发一个短期会话 Cookie JWT。
// Cookie 属性：HttpOnly、SameSite=Strict；production 模式下 Secure=true。
// 会话 JWT 的 claims 复用 AdminIdentity 的 sub/role/username/tenant_id，
// 但 iss 改为 "nexusglobal-admin-session"，aud 改为 "nexusglobal-admin-dashboard"，
// exp 不超过 AdminSessionMaxAge（15 分钟）。
func signSessionCookie(identity *AdminIdentity, sessionSecret, appEnv string) (*http.Cookie, error) {
	if sessionSecret == "" {
		return nil, fmt.Errorf("ADMIN_SESSION_SECRET not configured")
	}
	now := time.Now()
	// 会话有效期 = min(AdminSessionMaxAge, 来源 Admin JWT 剩余有效期)。
	ttl := AdminSessionMaxAge
	if !identity.SourceExpiry.IsZero() {
		remaining := time.Until(identity.SourceExpiry)
		if remaining < ttl {
			ttl = remaining
		}
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("source admin JWT already expired; cannot establish session")
	}
	claims := jwt.MapClaims{
		"iss":       "nexusglobal-admin-session",
		"aud":       "nexusglobal-admin-dashboard",
		"sub":       identity.Subject,
		"username":  identity.Username,
		"role":      identity.Role,
		"tenant_id": identity.TenantID,
		"iat":       now.Unix(),
		"exp":       now.Add(ttl).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(sessionSecret))
	if err != nil {
		return nil, fmt.Errorf("failed to sign session cookie: %w", err)
	}
	cookie := &http.Cookie{
		Name:     AdminSessionCookieName,
		Value:    signed,
		Path:     "/admin",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	if appEnv == "production" {
		cookie.Secure = true
	}
	return cookie, nil
}

// verifySessionCookie 验证请求中的 admin_session Cookie JWT。
// 返回验证后的 AdminIdentity；验证失败返回 nil。
func verifySessionCookie(r *http.Request, sessionSecret string) *AdminIdentity {
	if sessionSecret == "" {
		return nil
	}
	cookie, err := r.Cookie(AdminSessionCookieName)
	if err != nil || cookie == nil {
		return nil
	}
	token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
		// ADM-019: 与 Bearer 路径一致，严格锁定 HS256（拒绝 HS384/HS512 等）
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v (only HS256 is accepted)", t.Header["alg"])
		}
		return []byte(sessionSecret), nil
	})
	if err != nil || !token.Valid {
		return nil
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil
	}
	// 校验 issuer
	iss, _ := claims["iss"].(string)
	if subtle.ConstantTimeCompare([]byte(iss), []byte("nexusglobal-admin-session")) != 1 {
		return nil
	}
	// 校验 audience
	if !validateAudience(claims["aud"], "nexusglobal-admin-dashboard") {
		return nil
	}
	// 校验 subject
	subject, _ := claims["sub"].(string)
	if subject == "" {
		return nil
	}
	// 校验 role
	role, _ := claims["role"].(string)
	if !validAdminRoles[role] {
		return nil
	}
	tenantID, _ := claims["tenant_id"].(string)
	// ADM-001: 当前角色矩阵没有全局（跨租户）角色——所有 admin 身份都是
	// 租户作用域。tenant_id 为空意味着对象级租户校验无法执行，必须 fail closed。
	if tenantID == "" {
		return nil
	}
	username, _ := claims["username"].(string)
	return &AdminIdentity{
		Subject:         subject,
		Username:        username,
		Role:            role,
		TenantID:        tenantID,
		Permissions:     rolePermissions[role],
		AuthenticatedAt: time.Now(),
	}
}

// signCSRFCookie 签发一个 CSRF 双提交 Cookie。
// Cookie 不带 HttpOnly（前端 JS 需要读取），SameSite=Strict。
func signCSRFCookie(appEnv string) *http.Cookie {
	b := make([]byte, 32)
	if _, err := readRandBytes(b); err != nil {
		return nil
	}
	token := encodeBase64(b)
	cookie := &http.Cookie{
		Name:     AdminCSRFCookieName,
		Value:    token,
		Path:     "/admin",
		MaxAge:   int(AdminSessionMaxAge.Seconds()),
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
	}
	if appEnv == "production" {
		cookie.Secure = true
	}
	return cookie
}

// verifyCSRFToken 验证 CSRF 双提交 token。
// Cookie 中的 token 和 Header 中的 token 必须一致。
func verifyCSRFToken(r *http.Request) bool {
	cookie, err := r.Cookie(AdminCSRFCookieName)
	if err != nil || cookie == nil {
		return false
	}
	headerToken := r.Header.Get(AdminCSRFHeaderName)
	if headerToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(headerToken)) == 1
}

// isWriteMethod 判断是否为写方法（需要 CSRF 验证）。
func isWriteMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// VerifyAdminJWT 是唯一的 Admin JWT 验证函数。
// AdminAuthStrict 和 AdminSessionExchangeHandler 必须调用此函数，不得各自维护验证逻辑。
// secret 为空时返回配置错误，不能拿空字节作为 HMAC key。
// 严格只接受 HS256 签名算法，拒绝 HS384/HS512 和其他算法。
func VerifyAdminJWT(tokenString, secret, expectedIssuer, expectedAudience string, now time.Time) (*AdminIdentity, error) {
	if secret == "" {
		return nil, fmt.Errorf("JWT_SECRET not configured")
	}
	if expectedIssuer == "" {
		expectedIssuer = AdminIssuerExpected
	}
	if expectedAudience == "" {
		expectedAudience = AdminAudienceExpected
	}
	// 1. 解析并校验签名算法（严格只接受 HS256）
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v (only HS256 is accepted)", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid or expired token: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}
	// 2. 拒绝商户 JWT（G03 核心：merchant token 不能进入 Admin API）
	if merchantID, exists := claims["merchant_id"]; exists {
		if mid, _ := merchantID.(string); mid != "" {
			return nil, fmt.Errorf("merchant token cannot access admin API")
		}
	}
	// 3. 校验 issuer
	iss, _ := claims["iss"].(string)
	if subtle.ConstantTimeCompare([]byte(iss), []byte(expectedIssuer)) != 1 {
		return nil, fmt.Errorf("invalid token issuer")
	}
	// 4. 校验 audience
	if !validateAudience(claims["aud"], expectedAudience) {
		return nil, fmt.Errorf("invalid token audience")
	}
	// 5. 校验 subject
	subject, _ := claims["sub"].(string)
	if subject == "" {
		return nil, fmt.Errorf("missing subject in token")
	}
	// 6. 校验 role
	role, _ := claims["role"].(string)
	if !validAdminRoles[role] {
		return nil, fmt.Errorf("token missing or invalid admin role")
	}
	// 7. 校验 nbf（not-before）
	if nbfVal, exists := claims["nbf"]; exists {
		nbfFloat, ok := nbfVal.(float64)
		if !ok {
			return nil, fmt.Errorf("invalid nbf claim")
		}
		if now.Unix() < int64(nbfFloat) {
			return nil, fmt.Errorf("token not yet valid")
		}
	}
	// 8. 构造 AdminIdentity
	tenantID, _ := claims["tenant_id"].(string)
	// ADM-001: 无全局角色——tenant_id 为空的 admin 身份拒绝（fail closed，
	// 与 Bearer 交换路径语义一致）。
	if tenantID == "" {
		return nil, fmt.Errorf("admin identity missing tenant_id claim")
	}
	username, _ := claims["username"].(string)
	identity := &AdminIdentity{
		Subject:         subject,
		Username:        username,
		Role:            role,
		TenantID:        tenantID,
		Permissions:     rolePermissions[role],
		AuthenticatedAt: now,
	}
	// 记录来源 Admin JWT 的剩余有效期，供 Session Cookie TTL 取 min(15min, 剩余)。
	if expVal, exists := claims["exp"]; exists {
		if expFloat, ok := expVal.(float64); ok {
			identity.SourceExpiry = time.Unix(int64(expFloat), 0)
		}
	}
	return identity, nil
}

// AdminAuthStrict 是收紧后的 Admin JWT 中间件。
//
// 与旧 AdminAuth 的区别：
//   - 校验 iss/aud/role
//   - 拒绝商户 JWT（含 merchant_id claim）
//   - 注入 AdminIdentity 到 context
//   - 支持 Bearer JWT（API 客户端）和已签名会话 Cookie（浏览器 Dashboard）
//   - Cookie 鉴权的写请求必须验证 CSRF token（双提交 Cookie 模式）
//
// jwtSecret 为空时返回 500（配置错误）。
// expectedIssuer/expectedAudience 为空时使用默认值。
// sessionSecret 为空时禁用 Cookie 会话支持（只允许 Bearer JWT）。
func AdminAuthStrict(jwtSecret, expectedIssuer, expectedAudience, sessionSecret string) func(http.Handler) http.Handler {
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

			// 优先尝试会话 Cookie 鉴权（浏览器 Dashboard 场景）
			if sessionSecret != "" {
				identity := verifySessionCookie(r, sessionSecret)
				if identity != nil {
					// Cookie 鉴权成功：写请求必须验证 CSRF token
					if isWriteMethod(r.Method) && !verifyCSRFToken(r) {
						writeErrorJSON(w, http.StatusForbidden, "CSRF_REQUIRED", "CSRF token missing or invalid for cookie-authenticated write request", reqID)
						return
					}
					ctx := context.WithValue(r.Context(), AdminContextKey{}, identity)
					ctx = context.WithValue(ctx, ContextKeySubject, identity.Subject)
					ctx = context.WithValue(ctx, ContextKeyAdminUser, identity.Username)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// 回退到 Bearer JWT 鉴权（API 客户端场景）
			tokenStr := extractBearerToken(r)
			if tokenStr == "" {
				// A normal browser navigation should reach the login form. API
				// requests keep their JSON 401 response and all auth checks.
				if sessionSecret != "" && r.Method == http.MethodGet && r.URL.Path == "/admin/dashboard" && strings.Contains(r.Header.Get("Accept"), "text/html") {
					w.Header().Set("Cache-Control", "no-store")
					http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
					return
				}
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid Authorization header", reqID)
				return
			}

			// 使用统一的 VerifyAdminJWT 函数验证
			identity, err := VerifyAdminJWT(tokenStr, jwtSecret, expectedIssuer, expectedAudience, time.Now())
			if err != nil {
				// 区分 FORBIDDEN（商户 token）和 UNAUTHORIZED
				if strings.Contains(err.Error(), "merchant token") {
					writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", err.Error(), reqID)
				} else if strings.Contains(err.Error(), "invalid admin role") {
					writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", err.Error(), reqID)
				} else {
					writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", err.Error(), reqID)
				}
				return
			}

			ctx := context.WithValue(r.Context(), AdminContextKey{}, identity)
			ctx = context.WithValue(ctx, ContextKeySubject, identity.Subject)
			ctx = context.WithValue(ctx, ContextKeyAdminUser, identity.Username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// validateAudience 校验 aud claim 是否包含 expected。
// aud 可以是 string 或 []string（RFC 7519）。
func validateAudience(audClaim interface{}, expected string) bool {
	switch aud := audClaim.(type) {
	case string:
		return subtle.ConstantTimeCompare([]byte(aud), []byte(expected)) == 1
	case []interface{}:
		for _, a := range aud {
			if s, ok := a.(string); ok {
				if subtle.ConstantTimeCompare([]byte(s), []byte(expected)) == 1 {
					return true
				}
			}
		}
	case []string:
		for _, s := range aud {
			if subtle.ConstantTimeCompare([]byte(s), []byte(expected)) == 1 {
				return true
			}
		}
	}
	return false
}

// RequirePermission 返回一个中间件，要求当前 AdminIdentity 拥有给定权限。
// 若身份缺失或权限不足，返回 403。
//
// 用法：
//
//	r.With(httpx.RequirePermission(httpx.PermAdminKillSwitch)).Post("/system/lockdown", h.Lockdown)
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			identity, ok := GetAdminIdentity(r.Context())
			if !ok || identity == nil {
				writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", "admin identity not found", reqID)
				return
			}
			if !identity.HasPermission(perm) {
				writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions", reqID)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ResolveOperator 从已验证 context 获取操作人身份。
// 所有高危管理操作（kill switch、credential write、approval）必须使用此函数，
// 禁止从请求体读取 operator/requester/checker 字符串（RED-15）。
//
// 若 context 中无 AdminIdentity，返回 "unknown" 并记录警告（调用方负责）。
func ResolveOperator(ctx context.Context) string {
	if identity, ok := GetAdminIdentity(ctx); ok && identity != nil {
		if identity.Username != "" {
			return identity.Username
		}
		return identity.Subject
	}
	return "unknown"
}

// IsAdminReadMethod 判断是否为只读方法（用于 viewer 权限快速放行）。
func IsAdminReadMethod(method string) bool {
	return strings.EqualFold(method, http.MethodGet) || strings.EqualFold(method, http.MethodHead)
}

// RequireRoleForMethod 是一个便捷中间件，对写操作要求 operator 权限，
// 对读操作要求 viewer 权限。用于通用 admin 路由。
func RequireRoleForMethod() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			identity, ok := GetAdminIdentity(r.Context())
			if !ok || identity == nil {
				writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", "admin identity not found", reqID)
				return
			}
			if IsAdminReadMethod(r.Method) {
				if !identity.HasPermission(PermAdminRead) {
					writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions", reqID)
					return
				}
			} else {
				if !identity.HasPermission(PermAdminWriteConfig) {
					writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", "write requires operator role", reqID)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
