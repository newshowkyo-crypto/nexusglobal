package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAdminBrowserEntryRequiresLogin(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, accept, cookie string
		status                             int
	}{
		{"browser navigation", "GET", "/admin/dashboard", "text/html,application/xhtml+xml", "", http.StatusSeeOther},
		{"expired browser session", "GET", "/admin/dashboard", "text/html", "expired-or-invalid", http.StatusSeeOther},
		{"JSON client", "GET", "/admin/dashboard", "application/json", "", http.StatusUnauthorized},
		{"API must not redirect", "GET", "/admin/dashboard/data", "text/html", "", http.StatusUnauthorized},
		{"write must not redirect", "POST", "/admin/dashboard", "text/html", "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := AdminAuthStrict("jwt-secret", "", "", "session-secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Accept", tc.accept)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: tc.cookie})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code)
			assert.False(t, called, "unauthenticated requests must never reach the dashboard")
			if tc.status == http.StatusSeeOther {
				assert.Equal(t, "/admin/login", rec.Header().Get("Location"))
				assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			}
		})
	}
}
