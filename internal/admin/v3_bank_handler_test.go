package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/stretchr/testify/require"
)

func TestV3BankHandler_ListVAPool(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3BankHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/bank/va-pool", nil)

	handler.ListVAPool(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp, "items")
	assert.Contains(t, resp, "total")
}

func TestV3BankHandler_ListVAPool_MaskAccountNumber(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	tenantID := uuid.New()
	providerID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, provider_config, status)
		 VALUES ($1, $2, $3, $4, '{}', $5)`,
		providerID, "test-bank", "Test Bank Provider", "BANK_COLLECTION", "ACTIVE")
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO adapter_channel (id, tenant_id, provider_id, channel_code, channel_name, adapter_type, status)
		 VALUES ($1, $2, $3, 'test-channel', 'Test Channel', 'BANK_COLLECTION', 'ACTIVE')`,
		channelID, tenantID, providerID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO bank_collection_virtual_account_pool (tenant_id, channel_id, account_number, bank_name, asset_code, status)
		 VALUES ($1, $2, '12345678901234', 'Test Bank', 'USD', 'AVAILABLE')`,
		tenantID, channelID)
	require.NoError(t, err)

	handler := NewV3BankHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/bank/va-pool", nil)
	handler.ListVAPool(rec, req)

	if rec.Code != http.StatusOK {
		t.Logf("Response body: %s", rec.Body.String())
	}
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	itemsVal, ok := resp["items"]
	require.True(t, ok, "response should contain 'items'")
	if itemsVal == nil {
		t.Fatal("items should not be nil")
	}
	items := itemsVal.([]interface{})
	assert.Len(t, items, 1)
	item := items[0].(map[string]interface{})
	assert.Equal(t, "****234", item["account_number"])
	assert.NotContains(t, rec.Body.String(), "12345678901234")
}

func TestV3BankHandler_CreateVAAccount_Success(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	tenantID := uuid.New()
	providerID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, provider_config, status)
		 VALUES ($1, $2, $3, $4, '{}', $5)`,
		providerID, "test-bank", "Test Bank Provider", "BANK_COLLECTION", "ACTIVE")
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO adapter_channel (id, tenant_id, provider_id, channel_code, channel_name, adapter_type, status)
		 VALUES ($1, $2, $3, 'test-channel', 'Test Channel', 'BANK_COLLECTION', 'ACTIVE')`,
		channelID, tenantID, providerID)
	require.NoError(t, err)

	handler := NewV3BankHandler(pool, nil)
	body := map[string]interface{}{
		"tenant_id":      tenantID.String(),
		"channel_id":     channelID.String(),
		"account_number": "1234567890",
		"bank_name":      "Test Bank",
		"asset_code":     "USD",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/va-pool", body)

	handler.CreateVAAccount(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "created", resp["status"])
	assert.NotEmpty(t, resp["id"])
}

func TestV3BankHandler_CreateVAAccount_DuplicateAccountNumber(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	tenantID := uuid.New()
	providerID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, provider_config, status)
		 VALUES ($1, $2, $3, $4, '{}', $5)`,
		providerID, "test-bank", "Test Bank Provider", "BANK_COLLECTION", "ACTIVE")
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO adapter_channel (id, tenant_id, provider_id, channel_code, channel_name, adapter_type, status)
		 VALUES ($1, $2, $3, 'test-channel', 'Test Channel', 'BANK_COLLECTION', 'ACTIVE')`,
		channelID, tenantID, providerID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO bank_collection_virtual_account_pool (tenant_id, channel_id, account_number, bank_name, asset_code, status)
		 VALUES ($1, $2, 'DUPLICATE001', 'Test Bank', 'USD', 'AVAILABLE')`,
		tenantID, channelID)
	require.NoError(t, err)

	handler := NewV3BankHandler(pool, nil)
	body := map[string]interface{}{
		"tenant_id":      tenantID.String(),
		"channel_id":     channelID.String(),
		"account_number": "DUPLICATE001",
		"bank_name":      "Test Bank",
		"asset_code":     "USD",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/va-pool", body)

	handler.CreateVAAccount(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "IDEMPOTENCY_CONFLICT")
	assert.Contains(t, rec.Body.String(), "account_number already exists")
}

func TestV3BankHandler_CreateVAAccount_ChannelNotFound(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	tenantID := uuid.New()
	invalidChannelID := uuid.New()

	handler := NewV3BankHandler(pool, nil)
	body := map[string]interface{}{
		"tenant_id":      tenantID.String(),
		"channel_id":     invalidChannelID.String(),
		"account_number": "1234567890",
		"bank_name":      "Test Bank",
		"asset_code":     "USD",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/va-pool", body)

	handler.CreateVAAccount(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "CONFIG_INCONSISTENT")
	assert.Contains(t, rec.Body.String(), "channel_id not found")
}

func TestV3BankHandler_CreateVAAccount_Validation(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)

	testCases := []struct {
		name string
		body map[string]interface{}
	}{
		{
			name: "missing tenant_id",
			body: map[string]interface{}{
				"channel_id":     uuid.NewString(),
				"account_number": "1234567890",
				"bank_name":      "Test Bank",
				"asset_code":     "USD",
			},
		},
		{
			name: "missing channel_id",
			body: map[string]interface{}{
				"tenant_id":      uuid.NewString(),
				"account_number": "1234567890",
				"bank_name":      "Test Bank",
				"asset_code":     "USD",
			},
		},
		{
			name: "missing account_number",
			body: map[string]interface{}{
				"tenant_id":  uuid.NewString(),
				"channel_id": uuid.NewString(),
				"bank_name":  "Test Bank",
				"asset_code": "USD",
			},
		},
		{
			name: "missing bank_name",
			body: map[string]interface{}{
				"tenant_id":      uuid.NewString(),
				"channel_id":     uuid.NewString(),
				"account_number": "1234567890",
				"asset_code":     "USD",
			},
		},
		{
			name: "missing asset_code",
			body: map[string]interface{}{
				"tenant_id":      uuid.NewString(),
				"channel_id":     uuid.NewString(),
				"account_number": "1234567890",
				"bank_name":      "Test Bank",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/va-pool", tc.body)
			handler.CreateVAAccount(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestV3BankHandler_ReleaseVA(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	tenantID := uuid.New()
	providerID := uuid.New()
	channelID := uuid.New()
	vaID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, provider_config, status)
		 VALUES ($1, $2, $3, $4, '{}', $5)`,
		providerID, "test-bank", "Test Bank Provider", "BANK_COLLECTION", "ACTIVE")
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO adapter_channel (id, tenant_id, provider_id, channel_code, channel_name, adapter_type, status)
		 VALUES ($1, $2, $3, 'test-channel', 'Test Channel', 'BANK_COLLECTION', 'ACTIVE')`,
		channelID, tenantID, providerID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO bank_collection_virtual_account_pool (id, tenant_id, channel_id, account_number, bank_name, asset_code, status)
		 VALUES ($1, $2, $3, '1234567890', 'Test Bank', 'USD', 'ALLOCATED')`,
		vaID, tenantID, channelID)
	require.NoError(t, err)

	handler := NewV3BankHandler(pool, nil)
	// P0-9 契约：release 不再直接改状态，而是创建 PENDING 审批（需 reason）
	req, rec := makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/bank/va-pool/"+vaID.String()+"/release",
		map[string]string{"reason": "release drill"}, map[string]string{"id": vaID.String()})
	req = withAdminIdentity(req)

	handler.ReleaseVA(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "PENDING", resp["status"])
	assert.NotEmpty(t, resp["approval_id"], "release must create an approval request")

	// 数据库中 VA 状态不变（仍 ALLOCATED），直到审批被应用
	var vaStatus string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM bank_collection_virtual_account_pool WHERE id = $1`, vaID).Scan(&vaStatus))
	assert.Equal(t, "ALLOCATED", vaStatus, "PENDING 审批不得改变 VA 状态")
}

func TestV3BankHandler_ReleaseVA_NotFound(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	vaID := uuid.New()

	handler := NewV3BankHandler(pool, nil)
	req, rec := makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/bank/va-pool/"+vaID.String()+"/release",
		map[string]string{"reason": "release drill"}, map[string]string{"id": vaID.String()})
	req = withAdminIdentity(req)

	handler.ReleaseVA(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "va not found")
}

func TestV3BankHandler_DisableVA_ApprovalRequired(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	tenantID := uuid.New()
	providerID := uuid.New()
	channelID := uuid.New()
	vaID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, provider_config, status)
		 VALUES ($1, $2, $3, $4, '{}', $5)`,
		providerID, "test-bank", "Test Bank Provider", "BANK_COLLECTION", "ACTIVE")
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO adapter_channel (id, tenant_id, provider_id, channel_code, channel_name, adapter_type, status)
		 VALUES ($1, $2, $3, 'test-channel', 'Test Channel', 'BANK_COLLECTION', 'ACTIVE')`,
		channelID, tenantID, providerID)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(),
		`INSERT INTO bank_collection_virtual_account_pool (id, tenant_id, channel_id, account_number, bank_name, asset_code, status)
		 VALUES ($1, $2, $3, '1234567890', 'Test Bank', 'USD', 'AVAILABLE')`,
		vaID, tenantID, channelID)
	require.NoError(t, err)

	handler := NewV3BankHandler(pool, nil)
	body := map[string]interface{}{
		"requester": "admin_user",
		"reason":    "test disable reason",
	}
	req, rec := makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/bank/va-pool/"+vaID.String()+"/disable", body, map[string]string{"id": vaID.String()})
	req = withAdminIdentity(req)

	handler.DisableVA(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "DISABLE_VA_ACCOUNT", resp["action_type"])
	assert.Equal(t, "PENDING", resp["status"])
	assert.Equal(t, vaID.String(), resp["va_id"])
	assert.NotEmpty(t, resp["approval_id"])
	assert.Contains(t, resp["message"], "禁用 VA 审批单已创建，待复核后生效")

	approvalID := resp["approval_id"].(string)

	var foundActionType, foundStatus, foundVAID string
	err = pool.QueryRow(context.Background(),
		`SELECT action_type, status, requested_payload->>'va_id' FROM admin_approval_requests WHERE id = $1`,
		approvalID).Scan(&foundActionType, &foundStatus, &foundVAID)
	require.NoError(t, err, "approval record should exist")
	assert.Equal(t, "DISABLE_VA_ACCOUNT", foundActionType)
	assert.Equal(t, "PENDING", foundStatus)
	assert.Equal(t, vaID.String(), foundVAID)
}

func TestV3BankHandler_ListSettlementAccounts(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3BankHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/bank/settlement-accounts", nil)

	handler.ListSettlementAccounts(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp, "items")
	assert.Contains(t, resp, "total")
}

func TestV3BankHandler_CreateSettlementAccount_UPSERT(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	bankAccountID := uuid.New()

	handler := NewV3BankHandler(pool, nil)

	body := map[string]interface{}{
		"bank_account_id":   bankAccountID.String(),
		"currency":          "USD",
		"accumulated_minor": 1000000,
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/settlement-accounts", body)
	handler.CreateSettlementAccount(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	body2 := map[string]interface{}{
		"bank_account_id":   bankAccountID.String(),
		"currency":          "USD",
		"accumulated_minor": 2000000,
	}
	req, rec = makeRequest(http.MethodPost, "/admin/api/v3/bank/settlement-accounts", body2)
	handler.CreateSettlementAccount(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	req, rec = makeRequest(http.MethodGet, "/admin/api/v3/bank/settlement-accounts?currency=USD", nil)
	handler.ListSettlementAccounts(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	items := resp["items"].([]interface{})
	assert.Len(t, items, 1)
	item := items[0].(map[string]interface{})
	assert.Equal(t, float64(2000000), item["accumulated_minor"])
}

func TestV3BankHandler_ListWebhookLogs_RawPayloadMasked(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3BankHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/bank/webhook-logs", nil)

	handler.ListWebhookLogs(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp, "items")

	itemsVal, ok := resp["items"]
	if ok && itemsVal != nil {
		items := itemsVal.([]interface{})
		for _, item := range items {
			i := item.(map[string]interface{})
			assert.Equal(t, "[BINARY]", i["raw_payload"])
		}
	}
}

func TestV3BankHandler_ListIntents(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3BankHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/bank/intents", nil)

	handler.ListIntents(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp, "items")
	assert.Contains(t, resp, "total")
}

func TestV3BankHandler_ListReconciliationLogs(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3BankHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/bank/reconciliation", nil)

	handler.ListReconciliationLogs(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp, "items")
	assert.Contains(t, resp, "total")
}

// withAdminIdentity 附加已认证的 operator 身份到请求（RED-15：requester 只从
// 已验证 Context 获取——VA release/disable handler 强制要求身份）。
func withAdminIdentity(req *http.Request) *http.Request {
	identity := &httpx.AdminIdentity{
		Subject:     "operator-test",
		Username:    "operator-test",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	return req.WithContext(context.WithValue(req.Context(), httpx.AdminContextKey{}, identity))
}
