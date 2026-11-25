package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/stretchr/testify/require"
)

func TestConnectionSetupRejectsCredentialsBeforeSaving(t *testing.T) {
	r := httptest.NewRequest("PUT", "/", strings.NewReader(`{"api_key":"private-credential"}`))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "11111111-1111-1111-1111-111111111111")
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, httpx.AdminContextKey{}, &httpx.AdminIdentity{TenantID: "11111111-1111-1111-1111-111111111111"})
	w := httptest.NewRecorder()
	(&V3MerchantHandler{}).ConnectionSetup(w, r.WithContext(ctx))
	require.Equal(t, 400, w.Code)
	require.NotContains(t, w.Body.String(), "private-credential")
}

func TestConnectionSetupRequiresAccountScope(t *testing.T) {
	w := httptest.NewRecorder()
	(&V3MerchantHandler{}).ConnectionSetup(w, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, 403, w.Code)
}

func TestConnectionSetupDisallowsSecretsInWebAddresses(t *testing.T) {
	for _, value := range []string{"http://shop.example.com", "https://user:secret@shop.example.com", "https://shop.example.com?api_key=secret", "not-a-url"} {
		require.False(t, (merchantConnectionSetup{ServiceURL: value}).validate())
	}
	require.True(t, (merchantConnectionSetup{ServiceURL: "https://pay.example.com", SalesURL: "https://shop.example.com"}).validate())
}
