package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestAdminDemoLogin(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"
	const secret = "test-demo-session-secret-not-a-real-key"
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	require.NoError(t, err)
	handler := AdminDemoLoginHandler("atlas-demo", string(hash), tenant, secret, 12*time.Hour)
	for _, tc := range []struct {
		name, payload, proto, origin string
		status                       int
	}{
		{"valid", `{"username":"atlas-demo","password":"test-password"}`, "https", "https://demo.example", 200},
		{"wrong password", `{"username":"atlas-demo","password":"wrong"}`, "https", "", 401},
		{"wrong username", `{"username":"other","password":"test-password"}`, "https", "", 401},
		{"legacy token", `{"admin_jwt":"some-token"}`, "https", "", 400},
		{"plaintext", `{"username":"atlas-demo","password":"test-password"}`, "http", "", 403},
		{"cross origin", `{"username":"atlas-demo","password":"test-password"}`, "https", "https://other.example", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://demo.example/admin/session/password", strings.NewReader(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Forwarded-Proto", tc.proto)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			cookies := rec.Result().Cookies()
			if tc.status != 200 {
				assert.Empty(t, cookies)
				return
			}
			require.Len(t, cookies, 2)
			dashboard := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
			for _, cookie := range cookies {
				assert.True(t, cookie.Secure)
				assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
				assert.Equal(t, 43200, cookie.MaxAge)
				assert.Equal(t, cookie.Name == AdminSessionCookieName, cookie.HttpOnly)
				dashboard.AddCookie(cookie)
			}
			identity := verifySessionCookie(dashboard, secret)
			require.NotNil(t, identity)
			assert.Equal(t, tenant, identity.TenantID)
			assert.Equal(t, AdminRoleViewer, identity.Role)
			assert.False(t, identity.HasPermission(PermAdminApprove))
			assert.False(t, identity.HasPermission(PermAdminWriteConfig))
		})
	}
}
