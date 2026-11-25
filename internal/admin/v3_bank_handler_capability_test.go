package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestV3BankHandler_CreateVAAccount_LocalRecord_Success 验证 VBA 本地记录 CRUD 成功。
//
// G15 要求：VBA 区分"本地账户记录 CRUD"和"外部银行真实开户/验证/入账"。
// 此测试验证 capability_mode=local_record 时创建本地账户记录成功，
// 响应中包含 capability_mode 和 capability_note。
func TestV3BankHandler_CreateVAAccount_LocalRecord_Success(t *testing.T) {
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
		"tenant_id":       tenantID.String(),
		"channel_id":      channelID.String(),
		"account_number":  "1234567890",
		"bank_name":       "Test Bank",
		"asset_code":      "USD",
		"capability_mode": "local_record",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/va-pool", body)

	handler.CreateVAAccount(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "created", resp["status"])
	assert.NotEmpty(t, resp["id"])
	// G15: 响应中必须包含 capability_mode 和 capability_note。
	assert.Equal(t, "local_record", resp["capability_mode"])
	assert.Contains(t, resp["capability_note"], "local account record created")
	assert.Contains(t, resp["capability_note"], "external bank account opening not performed")
}

// TestV3BankHandler_CreateVAAccount_DefaultCapabilityMode 验证不传 capability_mode 时默认为 local_record。
func TestV3BankHandler_CreateVAAccount_DefaultCapabilityMode(t *testing.T) {
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
	// 不传 capability_mode，应默认为 local_record。
	body := map[string]interface{}{
		"tenant_id":      tenantID.String(),
		"channel_id":     channelID.String(),
		"account_number": "9999999999",
		"bank_name":      "Test Bank",
		"asset_code":     "USD",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/va-pool", body)

	handler.CreateVAAccount(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "local_record", resp["capability_mode"],
		"default capability_mode should be local_record")
}

// TestV3BankHandler_CreateVAAccount_ExternalOpen_NoAdapter_FailClosed 验证
// VBA external_open 模式在没有真实 adapter 时 fail-closed。
//
// G15 红线：没有真实 adapter 时 UI 明确禁用，不可显示"开户成功"。
// 此测试验证 capability_mode=external_open 时返回 FAILED_PRECONDITION 错误，
// 而不是创建账户记录并显示"开户成功"。
func TestV3BankHandler_CreateVAAccount_ExternalOpen_NoAdapter_FailClosed(t *testing.T) {
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
		"tenant_id":       tenantID.String(),
		"channel_id":      channelID.String(),
		"account_number":  "1234567890",
		"bank_name":       "Test Bank",
		"asset_code":      "USD",
		"capability_mode": "external_open",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/va-pool", body)

	handler.CreateVAAccount(rec, req)

	// 必须返回 FAILED_PRECONDITION 错误，不能创建账户记录。
	assert.Equal(t, http.StatusFailedDependency, rec.Code)
	assert.Contains(t, rec.Body.String(), "FAILED_PRECONDITION")
	assert.Contains(t, rec.Body.String(), "BLOCKED_EXTERNAL_CREDENTIAL")
	assert.Contains(t, rec.Body.String(), "EXTERNAL_BANK_OPEN capability not available")

	// 验证没有创建账户记录（fail-closed）。
	var count int
	err = pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM bank_collection_virtual_account_pool WHERE account_number = '1234567890'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "no account record should be created when external_open fails")
}

// TestV3BankHandler_CreateVAAccount_InvalidCapabilityMode 验证无效的 capability_mode 返回错误。
func TestV3BankHandler_CreateVAAccount_InvalidCapabilityMode(t *testing.T) {
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
		"tenant_id":       tenantID.String(),
		"channel_id":      channelID.String(),
		"account_number":  "1234567890",
		"bank_name":       "Test Bank",
		"asset_code":      "USD",
		"capability_mode": "invalid_mode",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/bank/va-pool", body)

	handler.CreateVAAccount(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_ARGUMENT")
	assert.Contains(t, rec.Body.String(), "capability_mode must be one of: local_record, external_open")
}
