package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

type credentialScopeDB struct {
	t              *testing.T
	tenantID       uuid.UUID
	requestTenant  uuid.UUID
	channelID      uuid.UUID
	readStatus     bool
	queryPerformed bool
}

func (database *credentialScopeDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	database.queryPerformed = true
	require.Contains(database.t, query, "ce.tenant_id = $2")
	require.Equal(database.t, []any{database.channelID, database.requestTenant}, args)
	if database.tenantID != database.requestTenant {
		return &fakeVaultRow{scanErr: pgx.ErrNoRows}
	}
	require.True(database.t, database.readStatus)
	require.NotContains(database.t, query, "ciphertext")
	require.NotContains(database.t, query, "credential_vault_key")
	return &fakeVaultRow{scanFn: func(dest ...any) error {
		*dest[0].(*string) = "stripe"
		*dest[1].(*string) = "test-channel"
		*dest[2].(*string) = "INACTIVE"
		*dest[3].(*string) = "test-merchant"
		*dest[4].(*bool) = true
		*dest[5].(*json.RawMessage) = json.RawMessage(`{"SECRET":{"version":2,"expired":false}}`)
		return nil
	}}
}

func (database *credentialScopeDB) Begin(context.Context) (pgx.Tx, error) {
	database.t.Fatal("unexpected credential write transaction")
	return nil, nil
}

func TestAcquirerCredentialStatusAndWriteScope(t *testing.T) {
	owner := uuid.New()
	for _, testCase := range []struct {
		name     string
		tenant   uuid.UUID
		role     string
		method   string
		expected int
		editable bool
	}{
		{"owner viewer", owner, httpx.AdminRoleViewer, http.MethodGet, http.StatusOK, false},
		{"owner security administrator", owner, httpx.AdminRoleSecurityAdmin, http.MethodGet, http.StatusOK, true},
		{"other tenant cannot read", uuid.New(), httpx.AdminRoleSecurityAdmin, http.MethodGet, http.StatusNotFound, false},
		{"other tenant cannot write", uuid.New(), httpx.AdminRoleSecurityAdmin, http.MethodPost, http.StatusNotFound, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			channelID := uuid.New()
			database := &credentialScopeDB{t: t, tenantID: owner, requestTenant: testCase.tenant, channelID: channelID, readStatus: testCase.method == http.MethodGet}
			handler := &AcquirerCredentialHandler{db: database, enc: &fakeVaultEncryptor{}}
			router := chi.NewRouter()
			router.Get("/channels/{id}/credentials", handler.CredentialStatus)
			router.Post("/channels/{id}/credentials", handler.WriteCredential)
			request := httptest.NewRequest(testCase.method, "/channels/"+channelID.String()+"/credentials",
				strings.NewReader(`{"credential_type":"SECRET","credential_material":"synthetic-test-value"}`))
			request = request.WithContext(context.WithValue(request.Context(), httpx.AdminContextKey{}, &httpx.AdminIdentity{
				TenantID: testCase.tenant.String(), Permissions: httpx.RolePermissions(testCase.role),
			}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.True(t, database.queryPerformed)
			require.Equal(t, testCase.expected, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "synthetic-test-value")
			if testCase.expected != http.StatusOK {
				return
			}
			var status struct {
				Required []string `json:"required"`
				CanWrite bool     `json:"can_write"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &status))
			require.Equal(t, []string{"SECRET", "WEBHOOK_SECRET"}, status.Required)
			require.Equal(t, testCase.editable, status.CanWrite)
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		})
	}
}
