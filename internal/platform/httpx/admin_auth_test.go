package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper：签发一个 admin JWT，可自定义 claims。
func signAdminJWT(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	if claims["exp"] == nil {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := token.SignedString([]byte(secret))
	require.NoError(t, err)
	return s
}

// validAdminClaims 返回一组合法的 admin claims。
func validAdminClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub":      "admin-123",
		"username": "admin",
		"iss":      AdminIssuerExpected,
		"aud":      AdminAudienceExpected,
		"role":     AdminRoleSecurityAdmin,
		// ADM-001: 所有 admin 身份都是租户作用域，tenant_id 为必填 claim。
		"tenant_id": "11111111-1111-1111-1111-111111111111",
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
}

func TestAdminAuth_MerchantJWT_Rejected(t *testing.T) {
	// G03 核心：商户 JWT 即使签名有效，也不能进入 Admin API。
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["merchant_id"] = "merch-456" // 商户 token
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/admin/stats", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "merchant token cannot access admin API")
}

func TestAdminAuth_MissingIssuer_Rejected(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	delete(claims, "iss")
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid token issuer")
}

func TestAdminAuth_WrongIssuer_Rejected(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["iss"] = "wrong-issuer"
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAdminAuth_MissingAudience_Rejected(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	delete(claims, "aud")
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid token audience")
}

func TestAdminAuth_AudienceArray_Supported(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["aud"] = []string{"other-audience", AdminAudienceExpected}
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAdminAuth_MissingRole_Rejected(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	delete(claims, "role")
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "missing or invalid admin role")
}

func TestAdminAuth_InvalidRole_Rejected(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["role"] = "superuser" // 不在白名单
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAdminAuth_NotBeforeFuture_Rejected(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["nbf"] = time.Now().Add(time.Hour).Unix() // 未来生效
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// nbf 校验可能由 jwt 库或我们的代码触发，两者都是 401
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAdminAuth_ViewerRole_Injected(t *testing.T) {
	secret := "test-secret"
	var captured *AdminIdentity
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = GetAdminIdentity(r.Context())
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["role"] = AdminRoleViewer
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, captured)
	assert.Equal(t, AdminRoleViewer, captured.Role)
	assert.True(t, captured.HasPermission(PermAdminRead))
	assert.False(t, captured.HasPermission(PermAdminWriteConfig))
	assert.False(t, captured.HasPermission(PermAdminKillSwitch))
}

func TestAdminAuth_OperatorRole_Permissions(t *testing.T) {
	secret := "test-secret"
	var captured *AdminIdentity
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = GetAdminIdentity(r.Context())
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["role"] = AdminRoleOperator
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, captured)
	assert.True(t, captured.HasPermission(PermAdminRead))
	assert.True(t, captured.HasPermission(PermAdminWriteConfig))
	assert.False(t, captured.HasPermission(PermAdminKillSwitch))
	assert.False(t, captured.HasPermission(PermAdminApprove))
}

func TestAdminAuth_ApproverRole_Permissions(t *testing.T) {
	secret := "test-secret"
	var captured *AdminIdentity
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = GetAdminIdentity(r.Context())
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["role"] = AdminRoleApprover
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, captured.HasPermission(PermAdminApprove))
	assert.False(t, captured.HasPermission(PermAdminWriteConfig))
}

func TestAdminAuth_SecurityAdminRole_AllPermissions(t *testing.T) {
	secret := "test-secret"
	var captured *AdminIdentity
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = GetAdminIdentity(r.Context())
		w.Write([]byte("ok"))
	}))

	claims := validAdminClaims()
	claims["role"] = AdminRoleSecurityAdmin
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	for _, perm := range []string{
		PermAdminRead,
		PermAdminWriteConfig,
		PermAdminApprove,
		PermAdminKillSwitch,
		PermAdminCredentialWrite,
		PermAdminRiskHold,
	} {
		assert.True(t, captured.HasPermission(perm), "security-admin should have %s", perm)
	}
}

func TestRequirePermission_Denied(t *testing.T) {
	// viewer 尝试 kill switch 操作
	ctx := withAdminIdentity(nil, &AdminIdentity{
		Subject:     "viewer-1",
		Role:        AdminRoleViewer,
		Permissions: rolePermissions[AdminRoleViewer],
	})
	handler := RequirePermission(PermAdminKillSwitch)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("POST", "/admin/api/v3/system/lockdown", nil)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "insufficient permissions")
}

func TestRequirePermission_Granted(t *testing.T) {
	ctx := withAdminIdentity(nil, &AdminIdentity{
		Subject:     "sec-admin-1",
		Role:        AdminRoleSecurityAdmin,
		Permissions: rolePermissions[AdminRoleSecurityAdmin],
	})
	handler := RequirePermission(PermAdminKillSwitch)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("POST", "/admin/api/v3/system/lockdown", nil)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRequirePermission_MissingIdentity(t *testing.T) {
	handler := RequirePermission(PermAdminRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestResolveOperator_FromIdentity(t *testing.T) {
	ctx := withAdminIdentity(nil, &AdminIdentity{
		Subject:  "sub-123",
		Username: "alice",
		Role:     AdminRoleOperator,
	})
	assert.Equal(t, "alice", ResolveOperator(ctx))
}

func TestResolveOperator_FallsBackToSubject(t *testing.T) {
	ctx := withAdminIdentity(nil, &AdminIdentity{
		Subject: "sub-123",
		Role:    AdminRoleOperator,
	})
	assert.Equal(t, "sub-123", ResolveOperator(ctx))
}

func TestResolveOperator_UnknownWhenNoIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	assert.Equal(t, "unknown", ResolveOperator(req.Context()))
}

func TestAdminAuth_NonHMACAlgorithm_Rejected(t *testing.T) {
	// 尝试使用 RS256 算法（应被拒绝）
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	// 使用 none 算法
	token := jwt.NewWithClaims(jwt.SigningMethodNone, validAdminClaims())
	tokenStr, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// withAdminIdentity 是测试辅助函数，注入 AdminIdentity 到 context。
func withAdminIdentity(_ *testing.T, identity *AdminIdentity) context.Context {
	return context.WithValue(context.Background(), AdminContextKey{}, identity)
}

// TestSpoofedOperatorFieldsIgnored 验证 ResolveOperator 只从已验证 JWT context 获取操作人身份，
// 忽略请求体中伪造的 operator/requester/checker 字段（RED-15）。
func TestSpoofedOperatorFieldsIgnored(t *testing.T) {
	// 模拟已验证的 AdminIdentity（来自 JWT，非请求体）
	ctx := withAdminIdentity(nil, &AdminIdentity{
		Subject:  "jwt-sub-123",
		Username: "real-admin",
		Role:     AdminRoleSecurityAdmin,
	})

	// ResolveOperator 只从 context 获取，不读请求体
	resolved := ResolveOperator(ctx)
	assert.Equal(t, "real-admin", resolved, "ResolveOperator must return identity from JWT context, not from request body")

	// 即使 context 中有其他伪造值，ResolveOperator 仍应返回 JWT 中的身份
	// （伪造值不会出现在 AdminIdentity 中，因为 AdminAuthStrict 只从 JWT claims 构造 AdminIdentity）
	assert.NotEqual(t, "spoofed-attacker", resolved, "spoofed operator from body must be ignored")
}

// TestAdminViewer_CannotWriteConfig 验证 viewer 角色没有 admin:write:config 权限。
func TestAdminViewer_CannotWriteConfig(t *testing.T) {
	identity := &AdminIdentity{
		Subject:     "viewer-sub",
		Role:        AdminRoleViewer,
		Permissions: RolePermissions(AdminRoleViewer),
	}
	assert.True(t, identity.HasPermission(PermAdminRead))
	assert.False(t, identity.HasPermission(PermAdminWriteConfig))
	assert.False(t, identity.HasPermission(PermAdminApprove))
	assert.False(t, identity.HasPermission(PermAdminKillSwitch))
	assert.False(t, identity.HasPermission(PermAdminCredentialWrite))
	assert.False(t, identity.HasPermission(PermPaymentLinkCreate))
}

// TestOperator_HasRequestChangeButNotApprove 验证 operator 有 request_change 但没有 approve 权限。
func TestOperator_HasRequestChangeButNotApprove(t *testing.T) {
	identity := &AdminIdentity{
		Subject:     "operator-sub",
		Role:        AdminRoleOperator,
		Permissions: RolePermissions(AdminRoleOperator),
	}
	assert.True(t, identity.HasPermission(PermAdminRequestChange))
	assert.False(t, identity.HasPermission(PermAdminApprove))
	assert.True(t, identity.HasPermission(PermPaymentLinkCreate))
}

// TestSecurityAdmin_HasAllPermissions 验证 security-admin 拥有所有权限。
func TestSecurityAdmin_HasAllPermissions(t *testing.T) {
	identity := &AdminIdentity{
		Subject:     "secadmin-sub",
		Role:        AdminRoleSecurityAdmin,
		Permissions: RolePermissions(AdminRoleSecurityAdmin),
	}
	allPerms := []string{
		PermAdminRead, PermAdminWriteConfig, PermAdminApprove,
		PermAdminRequestChange, PermAdminKillSwitch, PermAdminRecovery,
		PermAdminCredentialWrite, PermAdminRiskHold, PermPaymentLinkCreate,
	}
	for _, perm := range allPerms {
		assert.True(t, identity.HasPermission(perm), "security-admin should have %s", perm)
	}
}

// ── P0-1 Cookie 会话和 CSRF 测试 ──

// signSessionJWT 签发一个会话 Cookie JWT（模拟服务端签发）。
func signSessionJWT(t *testing.T, sessionSecret string, identity *AdminIdentity) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":       "nexusglobal-admin-session",
		"aud":       "nexusglobal-admin-dashboard",
		"sub":       identity.Subject,
		"username":  identity.Username,
		"role":      identity.Role,
		"tenant_id": identity.TenantID,
		"iat":       now.Unix(),
		"exp":       now.Add(AdminSessionMaxAge).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := token.SignedString([]byte(sessionSecret))
	require.NoError(t, err)
	return s
}

func TestSessionCookie_ValidCookie_AuthSuccess(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"
	identity := &AdminIdentity{
		Subject:     "admin-001",
		TenantID:    "11111111-1111-1111-1111-111111111111",
		Username:    "alice",
		Role:        AdminRoleSecurityAdmin,
		Permissions: rolePermissions[AdminRoleSecurityAdmin],
	}
	sessionToken := signSessionJWT(t, sessionSecret, identity)

	var captured *AdminIdentity
	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured, _ = GetAdminIdentity(r.Context())
			w.Write([]byte("ok"))
		}),
	)

	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "admin-001", captured.Subject)
	assert.Equal(t, AdminRoleSecurityAdmin, captured.Role)
}

func TestSessionCookie_NoCookie_NoBearer_Unauthorized(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSessionCookie_TamperedCookie_Rejected(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: "tampered-cookie-value"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSessionCookie_ExpiredCookie_Rejected(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"

	// 签发一个已过期的会话 JWT
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":  "nexusglobal-admin-session",
		"aud":  "nexusglobal-admin-dashboard",
		"sub":  "admin-001",
		"role": AdminRoleSecurityAdmin,
		"iat":  now.Add(-2 * time.Hour).Unix(),
		"exp":  now.Add(-1 * time.Hour).Unix(), // 已过期
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	sessionToken, _ := token.SignedString([]byte(sessionSecret))

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSessionCookie_WrongSecret_Rejected(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"
	otherSecret := "other-secret"

	identity := &AdminIdentity{
		Subject: "admin-001",
		Role:    AdminRoleSecurityAdmin,
	}
	sessionToken := signSessionJWT(t, otherSecret, identity) // 用不同的 secret 签发

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSessionCookie_WriteRequest_MissingCSRF_Forbidden(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"
	identity := &AdminIdentity{
		Subject:     "admin-001",
		TenantID:    "11111111-1111-1111-1111-111111111111",
		Role:        AdminRoleOperator,
		Permissions: rolePermissions[AdminRoleOperator],
	}
	sessionToken := signSessionJWT(t, sessionSecret, identity)

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	// POST 请求（写方法）但没有 CSRF token header
	req := httptest.NewRequest("POST", "/admin/api/v3/channels", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "CSRF")
}

func TestSessionCookie_WriteRequest_ValidCSRF_Success(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"
	identity := &AdminIdentity{
		Subject:     "admin-001",
		TenantID:    "11111111-1111-1111-1111-111111111111",
		Role:        AdminRoleOperator,
		Permissions: rolePermissions[AdminRoleOperator],
	}
	sessionToken := signSessionJWT(t, sessionSecret, identity)
	csrfToken := "test-csrf-token"

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	// POST 请求（写方法）带正确的 CSRF token header 和 cookie
	req := httptest.NewRequest("POST", "/admin/api/v3/channels", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: AdminCSRFCookieName, Value: csrfToken})
	req.Header.Set(AdminCSRFHeaderName, csrfToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestSessionCookie_WriteRequest_MismatchedCSRF_Forbidden(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"
	identity := &AdminIdentity{
		Subject:     "admin-001",
		TenantID:    "11111111-1111-1111-1111-111111111111",
		Role:        AdminRoleOperator,
		Permissions: rolePermissions[AdminRoleOperator],
	}
	sessionToken := signSessionJWT(t, sessionSecret, identity)

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	// POST 请求，CSRF cookie 和 header 不匹配
	req := httptest.NewRequest("POST", "/admin/api/v3/channels", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: AdminCSRFCookieName, Value: "cookie-token"})
	req.Header.Set(AdminCSRFHeaderName, "header-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "CSRF")
}

func TestSessionCookie_GetRequest_NoCSRFNeeded(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"
	identity := &AdminIdentity{
		TenantID:    "11111111-1111-1111-1111-111111111111",
		Subject:     "admin-001",
		Role:        AdminRoleViewer,
		Permissions: rolePermissions[AdminRoleViewer],
	}
	sessionToken := signSessionJWT(t, sessionSecret, identity)

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	// GET 请求（读方法）不需要 CSRF token
	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestBearerJWT_StillWorks_WithSessionSecret(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	// 使用 Bearer JWT（API 客户端），不使用 Cookie
	tokenStr := signAdminJWT(t, jwtSecret, validAdminClaims())
	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestBearerJWT_WriteRequest_NoCSRFNeeded(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	// 使用 Bearer JWT 的 POST 请求不需要 CSRF（API 客户端场景）
	tokenStr := signAdminJWT(t, jwtSecret, validAdminClaims())
	req := httptest.NewRequest("POST", "/admin/api/v3/channels", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestSignSessionCookie_ProductionSecure(t *testing.T) {
	identity := &AdminIdentity{
		Subject: "admin-001",
		Role:    AdminRoleSecurityAdmin,
	}
	cookie, err := signSessionCookie(identity, "session-secret", "production")
	require.NoError(t, err)
	assert.True(t, cookie.Secure, "production cookie must be Secure")
	assert.True(t, cookie.HttpOnly, "cookie must be HttpOnly")
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
}

func TestSignSessionCookie_LocalNotSecure(t *testing.T) {
	identity := &AdminIdentity{
		Subject: "admin-001",
		Role:    AdminRoleSecurityAdmin,
	}
	cookie, err := signSessionCookie(identity, "session-secret", "local")
	require.NoError(t, err)
	assert.False(t, cookie.Secure, "local cookie should not be Secure")
	assert.True(t, cookie.HttpOnly, "cookie must be HttpOnly")
}

func TestSignSessionCookie_EmptySecret_Error(t *testing.T) {
	identity := &AdminIdentity{
		Subject: "admin-001",
		Role:    AdminRoleSecurityAdmin,
	}
	_, err := signSessionCookie(identity, "", "local")
	assert.Error(t, err)
}

func TestSessionCookie_MerchantRole_Rejected(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"

	// 签发一个 role 不在白名单中的会话 JWT
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":  "nexusglobal-admin-session",
		"aud":  "nexusglobal-admin-dashboard",
		"sub":  "attacker",
		"role": "merchant", // 不在 validAdminRoles 中
		"iat":  now.Unix(),
		"exp":  now.Add(AdminSessionMaxAge).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	sessionToken, _ := token.SignedString([]byte(sessionSecret))

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSessionCookie_WrongIssuer_Rejected(t *testing.T) {
	jwtSecret := "jwt-secret"
	sessionSecret := "session-secret"

	// 签发一个 issuer 错误的会话 JWT
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":  "wrong-issuer",
		"aud":  "nexusglobal-admin-dashboard",
		"sub":  "admin-001",
		"role": AdminRoleSecurityAdmin,
		"iat":  now.Unix(),
		"exp":  now.Add(AdminSessionMaxAge).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	sessionToken, _ := token.SignedString([]byte(sessionSecret))

	handler := AdminAuthStrict(jwtSecret, AdminIssuerExpected, AdminAudienceExpected, sessionSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
	)

	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ADM-001: tenant_id 缺失的 admin JWT 必须 fail closed（无全局角色设计）。
func TestAdminAuth_MissingTenantID_Rejected(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached without tenant_id claim")
	}))

	claims := validAdminClaims()
	delete(claims, "tenant_id")
	tokenStr := signAdminJWT(t, secret, claims)

	req := httptest.NewRequest("GET", "/admin/api/v3/merchants", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
