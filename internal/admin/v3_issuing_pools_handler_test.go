package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

func TestV3IssuingPoolsHandler_ListPools(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3IssuingPoolsHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/issuing/pools", nil)

	handler.ListPools(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp, "items")
	assert.Contains(t, resp, "total")
}

func TestV3IssuingPoolsHandler_CreatePool_Validation(t *testing.T) {
	handler := NewV3IssuingPoolsHandler(nil, nil)

	testCases := []struct {
		name string
		body map[string]interface{}
	}{
		{
			name: "missing provider_code",
			body: map[string]interface{}{
				"issuing_tier":         "TIER_2_DIRECT",
				"funding_mode":         "FIAT_DIRECT",
				"pool_code":            "test-pool",
				"credential_vault_key": "vault-key-123",
			},
		},
		{
			name: "missing credential_vault_key",
			body: map[string]interface{}{
				"issuing_tier":  "TIER_2_DIRECT",
				"funding_mode":  "FIAT_DIRECT",
				"provider_code": "test-provider",
				"pool_code":     "test-pool",
			},
		},
		{
			name: "invalid issuing_tier",
			body: map[string]interface{}{
				"issuing_tier":         "INVALID_TIER",
				"funding_mode":         "FIAT_DIRECT",
				"provider_code":        "test-provider",
				"pool_code":            "test-pool",
				"credential_vault_key": "vault-key-123",
			},
		},
		{
			name: "invalid funding_mode",
			body: map[string]interface{}{
				"issuing_tier":         "TIER_2_DIRECT",
				"funding_mode":         "INVALID_MODE",
				"provider_code":        "test-provider",
				"pool_code":            "test-pool",
				"credential_vault_key": "vault-key-123",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", tc.body)

			handler.CreatePool(rec, req)

			assert.NotEqual(t, http.StatusOK, rec.Code)
		})
	}
}

func TestV3IssuingPoolsHandler_CreatePool_NoCredentialPlaintext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3IssuingPoolsHandler(pool, nil)
	body := map[string]interface{}{
		"issuing_tier":         "TIER_2_DIRECT",
		"funding_mode":         "FIAT_DIRECT",
		"provider_code":        "test-provider",
		"pool_code":            "test-pool",
		"credential_vault_key": "vault://issuing/test-provider",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", body)

	handler.CreatePool(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.NotContains(t, resp, "credential_vault_key")
	assert.NotContains(t, resp, "credential")
}

func TestV3IssuingPoolsHandler_EnableDisableReload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	poolID := uuid.New()
	entityID := uuid.New()
	tenantID := uuid.New()
	apID := uuid.New()
	vaultRecordID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Issuing Ops Entity', 'Issuing Ops LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, status)
		 VALUES ($1, 'ops-issuer', 'Ops Issuer', 'CARD_ISSUER', 'ACTIVE')`,
		apID)
	require.NoError(t, err)
	// R18: 启用卡池需要完整 ownership 事实链（vault 记录与池/租户/adapter 绑定一致）
	vaultKey := "vault://" + tenantID.String() + "/" + vaultRecordID.String() + "/API_KEY/1"
	_, err = pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, entity_id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, adapter_type, adapter_provider_id, status, provider_config)
		 VALUES ($1, $2, 'TIER_2_DIRECT', 'FIAT_DIRECT', 'test-provider', 'ops-pool', $3, 'NEXUS_ISSUING_HTTP_V1', $4, 'ACTIVE', '{"base_url":"https://issuer.example.com"}'::jsonb)`,
		poolID, entityID, vaultKey, apID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO adapter_credential_vault (id, provider_id, credential_type, key_version, ciphertext, nonce, aad_context, status, tenant_id, issuing_provider_id)
		 VALUES ($1, $2, 'API_KEY', 1, '\x00'::bytea, '\x00'::bytea, '{}', 'ACTIVE', $3, $4)`,
		vaultRecordID, apID, tenantID, poolID)
	require.NoError(t, err)

	handler := NewV3IssuingPoolsHandler(pool, nil)

	// G08: DisablePool/EnablePool 现在创建 PENDING 审批请求（不直接执行）
	// 需要注入 AdminIdentity Context
	req, rec := makeRequestWithChiAndIdentity(http.MethodPost,
		"/admin/api/v3/issuing/pools/"+poolID.String()+"/disable",
		nil, map[string]string{"id": poolID.String()},
		"operator-1", httpx.AdminRoleOperator)
	handler.DisablePool(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var disableResp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&disableResp))
	assert.Equal(t, "PENDING", disableResp["status"], "G08: disable must create PENDING approval")

	// 验证卡池状态不变（PENDING 不改配置）
	var poolStatus string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM issuing_providers WHERE id = $1`, poolID).Scan(&poolStatus))
	assert.Equal(t, "ACTIVE", poolStatus, "G08: PENDING must not change config")

	req, rec = makeRequestWithChiAndIdentity(http.MethodPost,
		"/admin/api/v3/issuing/pools/"+poolID.String()+"/enable",
		nil, map[string]string{"id": poolID.String()},
		"operator-1", httpx.AdminRoleOperator)
	handler.EnablePool(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Reload 仍然直接执行（非高危操作）
	req, rec = makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/issuing/pools/"+poolID.String()+"/reload", nil, map[string]string{"id": poolID.String()})
	handler.ReloadPool(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "reload_triggered", resp["status"])
}

func TestV3IssuingPoolsHandler_ListPools_NoCredentialEcho(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	poolID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (
			id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, provider_config, velocity_config
		 ) VALUES ($1, 'TIER_2_DIRECT', 'FIAT_DIRECT', 'test-provider', 'secret-pool', 'vault://issuing-secret', $2::jsonb, $3::jsonb)`,
		poolID,
		`{"api_secret":"plain-api-secret","region":"us"}`,
		`{"declined_count_sliding_window":5,"webhook_token":"plain-webhook-token"}`,
	)
	require.NoError(t, err)

	handler := NewV3IssuingPoolsHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/issuing/pools", nil)
	handler.ListPools(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, "credential_vault_key")
	assert.NotContains(t, body, "vault://issuing-secret")
	assert.NotContains(t, body, "plain-api-secret")
	assert.NotContains(t, body, "plain-webhook-token")
	assert.Contains(t, body, `"has_credential":true`)
	// P0-5: SanitizeMap 脱敏标记 <redacted> 在 JSON 序列化后为 \u003c 转义
	assert.Contains(t, body, `api_secret`+"\":\""+`\u003credacted\u003e"`)
	assert.Contains(t, body, `webhook_token`+"\":\""+`\u003credacted\u003e"`)
	assert.Contains(t, body, `"declined_count_sliding_window":5`)
}

func TestV3IssuingPoolsHandler_CreatePool_DeclineSliderMapsToVelocityConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3IssuingPoolsHandler(pool, nil)
	body := map[string]interface{}{
		"issuing_tier":         "TIER_1_AGGREGATOR",
		"funding_mode":         "CRYPTO_DESENSITIZED",
		"provider_code":        "test-provider",
		"pool_code":            "slider-pool",
		"credential_vault_key": "vault://issuing/test-provider",
		"velocity_config": map[string]interface{}{
			"declined_count_sliding_window": 5,
		},
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", body)
	handler.CreatePool(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var declineLimit int
	err := pool.QueryRow(context.Background(),
		`SELECT (velocity_config->>'declined_count_sliding_window')::int FROM issuing_providers WHERE pool_code = 'slider-pool'`,
	).Scan(&declineLimit)
	require.NoError(t, err)
	assert.Equal(t, 5, declineLimit)
}

// ── Section 5: adapter_type / adapter_provider_id handler 测试 ──

// TestCreatePool_InvalidAdapterTypeRejected 无效 adapter_type 在校验阶段被拒绝（不需要 DB）。
func TestCreatePool_InvalidAdapterTypeRejected(t *testing.T) {
	handler := NewV3IssuingPoolsHandler(nil, nil)
	body := map[string]interface{}{
		"issuing_tier":         "TIER_2_DIRECT",
		"funding_mode":         "CRYPTO_DESENSITIZED",
		"provider_code":        "test-provider",
		"pool_code":            "test-pool",
		"credential_vault_key": "vault://key/1",
		"adapter_type":         "BOGUS_ADAPTER",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", body)
	handler.CreatePool(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "无效 adapter_type 应返回 400")
}

// TestCreatePool_NexusHTTPV1WithoutProviderIDRejected
// NEXUS_ISSUING_HTTP_V1 缺少 adapter_provider_id 在校验阶段被拒绝（不需要 DB）。
func TestCreatePool_NexusHTTPV1WithoutProviderIDRejected(t *testing.T) {
	handler := NewV3IssuingPoolsHandler(nil, nil)
	body := map[string]interface{}{
		"issuing_tier":         "TIER_2_DIRECT",
		"funding_mode":         "CRYPTO_DESENSITIZED",
		"provider_code":        "test-provider",
		"pool_code":            "test-pool",
		"credential_vault_key": "vault://key/1",
		"adapter_type":         "NEXUS_ISSUING_HTTP_V1",
		"provider_config": map[string]interface{}{
			"base_url": "https://api.example.com",
		},
		// 故意不带 adapter_provider_id
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", body)
	handler.CreatePool(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "NEXUS_ISSUING_HTTP_V1 缺少 adapter_provider_id 应返回 400")
}

// TestCreatePool_NexusHTTPV1WithoutBaseURLRejected
// NEXUS_ISSUING_HTTP_V1 缺少 base_url 在校验阶段被拒绝（不需要 DB）。
func TestCreatePool_NexusHTTPV1WithoutBaseURLRejected(t *testing.T) {
	handler := NewV3IssuingPoolsHandler(nil, nil)
	body := map[string]interface{}{
		"issuing_tier":         "TIER_2_DIRECT",
		"funding_mode":         "CRYPTO_DESENSITIZED",
		"provider_code":        "test-provider",
		"pool_code":            "test-pool",
		"credential_vault_key": "vault://key/1",
		"adapter_type":         "NEXUS_ISSUING_HTTP_V1",
		"adapter_provider_id":  "550e8400-e29b-41d4-a716-446655440099",
		// 故意不带 provider_config.base_url
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", body)
	handler.CreatePool(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "NEXUS_ISSUING_HTTP_V1 缺少 base_url 应返回 400")
}

// TestCreatePool_DefaultsToDisabled 新创建的 provider 状态为 DISABLED。
// Section 5: 禁止创建即 ACTIVE，必须显式 enable 审批才能激活。
func TestCreatePool_DefaultsToDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewV3IssuingPoolsHandler(pool, nil)
	body := map[string]interface{}{
		"issuing_tier":         "TIER_2_DIRECT",
		"funding_mode":         "CRYPTO_DESENSITIZED",
		"provider_code":        "disabled-on-create-provider",
		"pool_code":            "disabled-on-create-pool",
		"credential_vault_key": "vault://key/1",
		"adapter_type":         "MOCK_LOCAL",
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", body)
	handler.CreatePool(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	poolID, ok := resp["id"].(string)
	require.True(t, ok, "响应必须包含 id")

	var status string
	err := pool.QueryRow(context.Background(),
		`SELECT status FROM issuing_providers WHERE id = $1`, poolID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "DISABLED", status, "新创建的 provider 状态必须为 DISABLED")
}

// TestEnablePool_RejectsUnsupported UNSUPPORTED adapter_type 的 provider 不能被激活。
// Section 5: 启用审批前查询 adapter_type，UNSUPPORTED 返回 422。
func TestEnablePool_RejectsUnsupported(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	poolID := uuid.New()
	// 创建一个 adapter_type=UNSUPPORTED 的卡池
	_, err := pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, adapter_type, status)
		 VALUES ($1, 'TIER_2_DIRECT', 'CRYPTO_DESENSITIZED', 'unsupported-provider', 'unsupported-pool', 'vault://key/1', 'UNSUPPORTED', 'DISABLED')`,
		poolID)
	require.NoError(t, err)

	handler := NewV3IssuingPoolsHandler(pool, nil)
	req, rec := makeRequestWithChiAndIdentity(http.MethodPost,
		"/admin/api/v3/issuing/pools/"+poolID.String()+"/enable",
		nil, map[string]string{"id": poolID.String()},
		"operator-1", httpx.AdminRoleOperator)
	handler.EnablePool(rec, req)

	// Section 5: UNSUPPORTED 适配器禁止启用，返回 422 CONFIG_INCONSISTENT
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "UNSUPPORTED adapter_type 启用应返回 422")

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	errObj, ok := resp["error"].(map[string]interface{})
	require.True(t, ok, "响应必须包含 error 对象")
	assert.Contains(t, errObj["message"], "UNSUPPORTED", "错误消息应提及 UNSUPPORTED")
}

// TestEnablePool_AllowsMockLocal MOCK_LOCAL adapter_type 的 provider 可以创建启用审批。
// Section 5: 非 UNSUPPORTED 的 adapter_type 允许创建启用审批。
func TestEnablePool_AllowsMockLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	poolID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, adapter_type, status)
		 VALUES ($1, 'TIER_2_DIRECT', 'CRYPTO_DESENSITIZED', 'mock-provider', 'mock-pool', 'vault://key/1', 'MOCK_LOCAL', 'DISABLED')`,
		poolID)
	require.NoError(t, err)

	handler := NewV3IssuingPoolsHandler(pool, nil)
	req, rec := makeRequestWithChiAndIdentity(http.MethodPost,
		"/admin/api/v3/issuing/pools/"+poolID.String()+"/enable",
		nil, map[string]string{"id": poolID.String()},
		"operator-1", httpx.AdminRoleOperator)
	handler.EnablePool(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "MOCK_LOCAL adapter_type 启用应创建 PENDING 审批")
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "PENDING", resp["status"])
}

// TestCreatePool_WritesAdapterTypeAndProviderID
// CreatePool 应将 adapter_type 和 adapter_provider_id 写入 DB。
func TestCreatePool_WritesAdapterTypeAndProviderID(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// 先创建一个 adapter_provider 供引用
	adapterProviderID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO adapter_provider (id, provider_code, provider_name, adapter_type, status)
		 VALUES ($1, 'nexus-issuing', 'Nexus Issuing HTTP v1', 'CARD_ISSUER', 'ACTIVE')`,
		adapterProviderID)
	require.NoError(t, err)

	handler := NewV3IssuingPoolsHandler(pool, nil)
	body := map[string]interface{}{
		"issuing_tier":         "TIER_2_DIRECT",
		"funding_mode":         "CRYPTO_DESENSITIZED",
		"provider_code":        "adapter-writer-provider",
		"pool_code":            "adapter-writer-pool",
		"credential_vault_key": "vault://key/1",
		"adapter_type":         "NEXUS_ISSUING_HTTP_V1",
		"adapter_provider_id":  adapterProviderID.String(),
		"provider_config": map[string]interface{}{
			"base_url": "https://api.example.com",
		},
	}
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/issuing/pools", body)
	handler.CreatePool(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	poolID, ok := resp["id"].(string)
	require.True(t, ok, "响应必须包含 id")

	var adapterType string
	var writtenAdapterProviderID *string
	err = pool.QueryRow(context.Background(),
		`SELECT adapter_type::text, adapter_provider_id::text FROM issuing_providers WHERE id = $1`, poolID).Scan(&adapterType, &writtenAdapterProviderID)
	require.NoError(t, err)
	assert.Equal(t, "NEXUS_ISSUING_HTTP_V1", adapterType)
	require.NotNil(t, writtenAdapterProviderID)
	assert.Equal(t, adapterProviderID.String(), *writtenAdapterProviderID)
}

// TestListPools_IncludesAdapterFields ListPools 响应应包含 adapter_type 和 adapter_provider_id 字段。
func TestListPools_IncludesAdapterFields(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	poolID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, adapter_type, status)
		 VALUES ($1, 'TIER_2_DIRECT', 'CRYPTO_DESENSITIZED', 'list-adapter-provider', 'list-adapter-pool', 'vault://key/1', 'MOCK_LOCAL', 'DISABLED')`,
		poolID)
	require.NoError(t, err)

	handler := NewV3IssuingPoolsHandler(pool, nil)
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/issuing/pools", nil)
	handler.ListPools(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	items, ok := resp["items"].([]interface{})
	require.True(t, ok, "响应必须包含 items 数组")
	require.NotEmpty(t, items, "items 不能为空")

	// 找到我们创建的卡池
	var found map[string]interface{}
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if m["id"] == poolID.String() {
			found = m
			break
		}
	}
	require.NotNil(t, found, "必须能找到刚创建的卡池")
	assert.Equal(t, "MOCK_LOCAL", found["adapter_type"], "响应应包含 adapter_type")
	assert.Contains(t, found, "adapter_provider_id", "响应应包含 adapter_provider_id 字段")
}
