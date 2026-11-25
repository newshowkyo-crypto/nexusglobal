package router

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/stretchr/testify/require"
)

func TestOperationsReadinessDoesNotExposeSecretsOrClaimTransactionSuccess(t *testing.T) {
	cfg := &config.Config{VaultToken: "never-expose-me", BankAPIKey: "never-expose-bank", AppEnv: "local"}
	rr := httptest.NewRecorder()
	operationsReadiness(cfg).ServeHTTP(rr, httptest.NewRequest("GET", "/admin/operations/readiness", nil))
	require.Equal(t, 200, rr.Code)
	text := rr.Body.String()
	require.Equal(t, 7, strings.Count(text, `"configured":false`))
	require.NotContains(t, text, cfg.VaultToken)
	require.NotContains(t, text, cfg.BankAPIKey)
	require.Contains(t, text, "仍需完成机构连接")
	require.Equal(t, "no-store", rr.Header().Get("Cache-Control"))
}
