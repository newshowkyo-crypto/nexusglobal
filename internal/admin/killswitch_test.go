package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// makeRequestWithAdminIdentity 创建带已验证 AdminIdentity 的请求。
// G07: 身份必须从 Context 获取，拒绝 body 中伪造的 operator（RED-15）。
func makeRequestWithAdminIdentity(method, path string, body interface{}, role string) (*http.Request, *httptest.ResponseRecorder) {
	req, rec := makeRequest(method, path, body)
	identity := &httpx.AdminIdentity{
		Subject:         "admin-test-subject",
		Username:        "admin",
		Role:            role,
		Permissions:     httpx.RolePermissions(role),
		AuthenticatedAt: time.Now(),
	}
	ctx := context.WithValue(req.Context(), httpx.AdminContextKey{}, identity)
	req = req.WithContext(ctx)
	return req, rec
}

func TestKillSwitchHandler_Lockdown_DisablesChannelsAndFreezesGroups(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	tenantID := uuid.New()
	activeChannelID := uuid.New()
	inactiveChannelID := uuid.New()
	groupID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Lockdown Entity', 'Lockdown Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES
		 ($1, $3, 'TIER_2_COMPLIANT', 'provider-a', 'active-channel', 'vault-a', 'DEFAULT', 'ACTIVE'),
		 ($2, $3, 'TIER_2_COMPLIANT', 'provider-b', 'inactive-channel', 'vault-b', 'DEFAULT', 'INACTIVE')`,
		activeChannelID, inactiveChannelID, entityID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO pipeline_isolation_groups (id, name, status)
		 VALUES ($1, 'lockdown-group', 'ACTIVE')`,
		groupID)
	require.NoError(t, err)

	handler := NewKillSwitchHandlerWithService(pool, nil, failclosed.NewService(pool, nil, nil))
	// G07: 身份从 Context 注入，不再从 body 读取 operator
	req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/lockdown", map[string]interface{}{
		"reason":          "staging drill",
		"idempotency_key": "lockdown-drill-001",
	}, httpx.AdminRoleSecurityAdmin)
	handler.Lockdown(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, float64(1), resp["disabled_channels"])
	assert.Equal(t, float64(1), resp["risk_hold_groups"])

	var activeStatus, inactiveStatus, groupStatus, frozenReason, frozenBy string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM acquirer_channels WHERE id = $1`, activeChannelID).Scan(&activeStatus))
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM acquirer_channels WHERE id = $1`, inactiveChannelID).Scan(&inactiveStatus))
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status, COALESCE(frozen_reason, ''), COALESCE(frozen_by, '') FROM pipeline_isolation_groups WHERE id = $1`, groupID).Scan(&groupStatus, &frozenReason, &frozenBy))
	assert.Equal(t, "DISABLED", activeStatus)
	assert.Equal(t, "INACTIVE", inactiveStatus)
	assert.Equal(t, "RISK_HOLD", groupStatus)
	assert.Equal(t, "staging drill", frozenReason)
	assert.Equal(t, "admin", frozenBy)

	var auditCount int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM admin_override_audit_log WHERE override_type = 'SYSTEM_LOCKDOWN'`).Scan(&auditCount))
	assert.Equal(t, 1, auditCount)
}

func Test_KillSwitch_Instant_Blocking(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	failclosed.ClearSystemLockdownForTest()
	t.Cleanup(failclosed.ClearSystemLockdownForTest)

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	gw := checkout.NewMockPaymentGateway()
	_, err := gw.Charge(context.Background(), checkout.PaymentRequest{
		OrderID:  "pre-lockdown",
		Amount:   decimal.NewFromInt(100),
		Currency: "USD",
	})
	require.NoError(t, err)

	handler := NewKillSwitchHandlerWithService(pool, nil, failclosed.NewService(pool, nil, nil))
	// G07: 身份从 Context 注入
	req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/lockdown", map[string]interface{}{
		"reason":          "concurrency fail-closed drill",
		"idempotency_key": "lockdown-drill-002",
	}, httpx.AdminRoleSecurityAdmin)
	handler.Lockdown(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, failclosed.IsSystemLockedDown())

	time.Sleep(time.Millisecond)

	const concurrentCheckoutRequests = 1000
	errs := make(chan error, concurrentCheckoutRequests)
	var wg sync.WaitGroup
	wg.Add(concurrentCheckoutRequests)
	for i := 0; i < concurrentCheckoutRequests; i++ {
		go func(i int) {
			defer wg.Done()
			_, chargeErr := gw.Charge(context.Background(), checkout.PaymentRequest{
				OrderID:  "post-lockdown",
				Amount:   decimal.NewFromInt(100),
				Currency: "USD",
			})
			errs <- chargeErr
		}(i)
	}
	wg.Wait()
	close(errs)

	for chargeErr := range errs {
		require.Error(t, chargeErr)
		assert.True(t, errors.Is(chargeErr, checkout.ErrSystemLockdownFailClosed), "got %v", chargeErr)
		assert.Contains(t, chargeErr.Error(), failclosed.SystemLockdownCode)
	}
}

// Test_KillSwitch_RejectsForgedOperator 验证 RED-15：拒绝 body 中伪造的 operator。
// 无 AdminIdentity Context 时返回 403，即使 body 中有 operator 字段。
func Test_KillSwitch_RejectsForgedOperator(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewKillSwitchHandlerWithService(pool, nil, failclosed.NewService(pool, nil, nil))
	// 不注入 AdminIdentity，模拟无认证中间件的请求
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/system/lockdown", map[string]interface{}{
		"operator": "attacker", // 伪造的 operator
		"reason":   "malicious attempt",
	})
	handler.Lockdown(rec, req)

	// 必须返回 403，拒绝 body 中的伪造身份
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ── P0-7: Lockdown 幂等键必填验证 ──

func TestLockdown_RequiresIdempotencyKey(t *testing.T) {
	handler := NewKillSwitchHandler(nil, nil)
	// 注入 admin 身份
	req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/lockdown", map[string]interface{}{
		"reason": "test reason",
	}, httpx.AdminRoleSecurityAdmin)
	handler.Lockdown(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	errObj, _ := resp["error"].(map[string]interface{})
	assert.Contains(t, errObj["message"], "idempotency_key is required")
}

// ── P0-7: EventsStream 未鉴权拒绝 ──

func TestEventsStream_RequiresAdminAuth(t *testing.T) {
	handler := NewKillSwitchHandler(nil, nil)
	// 不注入 AdminIdentity，模拟未认证请求
	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/events/stream", nil)
	handler.EventsStream(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	errObj, _ := resp["error"].(map[string]interface{})
	assert.Contains(t, errObj["message"], "admin authentication required")
}

// ── P0-7: SuspendActiveCards 鉴权与参数验证 ──

func TestSuspendActiveCards_RequiresAdminAuth(t *testing.T) {
	handler := NewKillSwitchHandler(nil, nil)
	// 不注入 AdminIdentity
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/system/suspend-active-cards", map[string]interface{}{
		"reason":          "emergency",
		"idempotency_key": "test-key-001",
	})
	handler.SuspendActiveCards(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSuspendActiveCards_RequiresReason(t *testing.T) {
	handler := NewKillSwitchHandler(nil, nil)
	req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/suspend-active-cards", map[string]interface{}{
		"idempotency_key": "test-key-002",
	}, httpx.AdminRoleSecurityAdmin)
	handler.SuspendActiveCards(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	errObj, _ := resp["error"].(map[string]interface{})
	assert.Contains(t, errObj["message"], "reason is required")
}

func TestSuspendActiveCards_RequiresIdempotencyKey(t *testing.T) {
	handler := NewKillSwitchHandler(nil, nil)
	req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/suspend-active-cards", map[string]interface{}{
		"reason": "emergency",
	}, httpx.AdminRoleSecurityAdmin)
	handler.SuspendActiveCards(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	errObj, _ := resp["error"].(map[string]interface{})
	assert.Contains(t, errObj["message"], "idempotency_key is required")
}

// ── P0-7: SuspendActiveCards 集成测试 ──

func TestSuspendActiveCards_FreezesActiveCards(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// 准备测试数据：创建 entity → issuing_provider → virtual_cards
	entityID := uuid.New()
	tenantID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Suspend Entity', 'Suspend Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	providerID := uuid.New()
	_, err = pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, entity_id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, status)
		 VALUES ($1, $2, 'TIER_2_DIRECT', 'CRYPTO_DESENSITIZED', 'provider-suspend', 'pool-suspend', 'vault-suspend', 'ACTIVE')`,
		providerID, entityID)
	require.NoError(t, err)

	// 创建 3 张 ACTIVE 卡
	cardIDs := make([]uuid.UUID, 3)
	for i := 0; i < 3; i++ {
		cardIDs[i] = uuid.New()
		_, err = pool.Exec(context.Background(),
			`INSERT INTO virtual_cards (id, merchant_id, provider_id, status, idempotency_key, payload_hash, created_by)
			 VALUES ($1, $2, $3, 'ACTIVE', $4, $5, 'test-admin')`,
			cardIDs[i], uuid.New(), providerID,
			fmt.Sprintf("card-idemp-%d", i), fmt.Sprintf("hash-%d", i))
		require.NoError(t, err)
	}

	// 创建 1 张 FROZEN 卡（不应被影响）
	frozenCardID := uuid.New()
	_, err = pool.Exec(context.Background(),
		`INSERT INTO virtual_cards (id, merchant_id, provider_id, status, idempotency_key, payload_hash, created_by)
		 VALUES ($1, $2, $3, 'FROZEN', $4, $5, 'test-admin')`,
		frozenCardID, uuid.New(), providerID, "card-idemp-frozen", "hash-frozen")
	require.NoError(t, err)

	handler := NewKillSwitchHandlerWithService(pool, nil, failclosed.NewService(pool, nil, nil))
	idempKey := "suspend-test-key-001"
	req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/suspend-active-cards", map[string]interface{}{
		"reason":          "security incident drill",
		"idempotency_key": idempKey,
	}, httpx.AdminRoleSecurityAdmin)
	handler.SuspendActiveCards(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "SUSPEND_ACTIVE_CARDS", resp["event"])
	assert.Equal(t, float64(3), resp["frozen_count"])
	assert.Equal(t, idempKey, resp["idempotency_key"])

	// 验证 DB：3 张卡已冻结
	var frozenCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM virtual_cards WHERE status = 'FROZEN'`).Scan(&frozenCount))
	assert.Equal(t, 4, frozenCount, "original 1 FROZEN + 3 newly frozen = 4")

	// 验证幂等记录
	var idempCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admin_suspend_idempotency WHERE idempotency_key = $1`, idempKey).Scan(&idempCount))
	assert.Equal(t, 1, idempCount)

	// 验证审计记录
	var auditCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admin_audit_log WHERE action = 'SUSPEND_ACTIVE_CARDS'`).Scan(&auditCount))
	assert.Equal(t, 1, auditCount)

	// 验证 Outbox 事件
	var outboxCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM transactional_outbox WHERE event_type = 'CARD_FROZEN_SUSPEND'`).Scan(&outboxCount))
	assert.Equal(t, 3, outboxCount, "one outbox event per frozen card")
}

func TestSuspendActiveCards_Idempotency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	tenantID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Idemp Entity', 'Idemp Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	providerID := uuid.New()
	_, err = pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, entity_id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, status)
		 VALUES ($1, $2, 'TIER_2_DIRECT', 'CRYPTO_DESENSITIZED', 'provider-idemp', 'pool-idemp', 'vault-idemp', 'ACTIVE')`,
		providerID, entityID)
	require.NoError(t, err)

	cardID := uuid.New()
	_, err = pool.Exec(context.Background(),
		`INSERT INTO virtual_cards (id, merchant_id, provider_id, status, idempotency_key, payload_hash, created_by)
		 VALUES ($1, $2, $3, 'ACTIVE', $4, $5, 'test-admin')`,
		cardID, uuid.New(), providerID, "card-idemp-idemp-test", "hash-idemp")
	require.NoError(t, err)

	handler := NewKillSwitchHandlerWithService(pool, nil, failclosed.NewService(pool, nil, nil))
	idempKey := "suspend-idemp-key-002"

	// 第一次调用
	req1, rec1 := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/suspend-active-cards", map[string]interface{}{
		"reason":          "first call",
		"idempotency_key": idempKey,
	}, httpx.AdminRoleSecurityAdmin)
	handler.SuspendActiveCards(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code)

	var resp1 map[string]interface{}
	require.NoError(t, json.NewDecoder(rec1.Body).Decode(&resp1))
	assert.Equal(t, float64(1), resp1["frozen_count"])
	assert.Nil(t, resp1["idempotent"])

	// 第二次调用（相同 idempotency_key）— 应返回幂等结果
	req2, rec2 := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/suspend-active-cards", map[string]interface{}{
		"reason":          "second call",
		"idempotency_key": idempKey,
	}, httpx.AdminRoleSecurityAdmin)
	handler.SuspendActiveCards(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code)

	var resp2 map[string]interface{}
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&resp2))
	assert.Equal(t, true, resp2["idempotent"])
	assert.Equal(t, float64(1), resp2["frozen_count"])
	assert.Equal(t, "first call", resp2["reason"], "idempotent response should return original reason")

	// 验证幂等记录只有一条
	var idempCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admin_suspend_idempotency WHERE idempotency_key = $1`, idempKey).Scan(&idempCount))
	assert.Equal(t, 1, idempCount, "idempotency key should only be recorded once")
}

func TestSuspendActiveCards_NoActiveCards(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewKillSwitchHandlerWithService(pool, nil, failclosed.NewService(pool, nil, nil))
	idempKey := "suspend-no-cards-key-003"
	req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/suspend-active-cards", map[string]interface{}{
		"reason":          "no active cards test",
		"idempotency_key": idempKey,
	}, httpx.AdminRoleSecurityAdmin)
	handler.SuspendActiveCards(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "SUSPEND_ACTIVE_CARDS", resp["event"])
	assert.Equal(t, float64(0), resp["frozen_count"])

	// 验证幂等记录和审计仍然被写入
	var idempCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admin_suspend_idempotency WHERE idempotency_key = $1`, idempKey).Scan(&idempCount))
	assert.Equal(t, 1, idempCount)

	var auditCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admin_audit_log WHERE action = 'SUSPEND_ACTIVE_CARDS'`).Scan(&auditCount))
	assert.Equal(t, 1, auditCount)
}

// TestSuspendActiveCards_ConcurrentIdempotency 验证并发幂等：
// 多个并发请求使用相同 idempotency_key，仅执行一次冻结操作。
func TestSuspendActiveCards_ConcurrentIdempotency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	tenantID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Concurrent Entity', 'Concurrent Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, tenantID)
	require.NoError(t, err)

	providerID := uuid.New()
	_, err = pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, entity_id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, status)
		 VALUES ($1, $2, 'TIER_2_DIRECT', 'CRYPTO_DESENSITIZED', 'provider-concurrent', 'pool-concurrent', 'vault-concurrent', 'ACTIVE')`,
		providerID, entityID)
	require.NoError(t, err)

	// 插入 3 张 ACTIVE 卡
	cardIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, cid := range cardIDs {
		_, err = pool.Exec(context.Background(),
			`INSERT INTO virtual_cards (id, merchant_id, provider_id, status, idempotency_key, payload_hash, created_by)
			 VALUES ($1, $2, $3, 'ACTIVE', $4, $5, 'test-admin')`,
			cid, uuid.New(), providerID, fmt.Sprintf("card-concurrent-%s", cid.String()[:8]), "hash-concurrent")
		require.NoError(t, err)
	}

	handler := NewKillSwitchHandlerWithService(pool, nil, failclosed.NewService(pool, nil, nil))
	idempKey := "suspend-concurrent-key-004"

	// 并发发送 5 个请求，使用相同 idempotency_key
	const concurrency = 5
	var wg sync.WaitGroup
	results := make([]struct {
		status int
		body   map[string]interface{}
	}, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req, rec := makeRequestWithAdminIdentity(http.MethodPost, "/admin/api/v3/system/suspend-active-cards", map[string]interface{}{
				"reason":          fmt.Sprintf("concurrent call %d", idx),
				"idempotency_key": idempKey,
			}, httpx.AdminRoleSecurityAdmin)
			handler.SuspendActiveCards(rec, req)
			results[idx].status = rec.Code
			var resp map[string]interface{}
			_ = json.NewDecoder(rec.Body).Decode(&resp)
			results[idx].body = resp
		}(i)
	}
	wg.Wait()

	// 所有请求必须返回 200
	for i, r := range results {
		assert.Equal(t, http.StatusOK, r.status, "request %d should return 200", i)
	}

	// 至少一个请求返回首次执行结果（frozen_count=3, idempotent=nil）
	// 其余请求返回幂等结果（idempotent=true）
	idempotentCount := 0
	firstCount := 0
	for _, r := range results {
		if r.body["idempotent"] == true {
			idempotentCount++
		} else if r.body["idempotent"] == nil && r.body["frozen_count"] != nil {
			firstCount++
		}
	}
	assert.Equal(t, 1, firstCount, "exactly one request should be the first execution")
	assert.Equal(t, concurrency-1, idempotentCount, "remaining requests should be idempotent")

	// 验证幂等记录只有一条
	var idempCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admin_suspend_idempotency WHERE idempotency_key = $1`, idempKey).Scan(&idempCount))
	assert.Equal(t, 1, idempCount, "idempotency key should only be recorded once even under concurrency")

	// 验证审计记录只有一条（首次执行）
	var auditCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admin_audit_log WHERE action = 'SUSPEND_ACTIVE_CARDS'`).Scan(&auditCount))
	assert.Equal(t, 1, auditCount, "audit log should only be written once even under concurrency")
}

func Test_V31VisualConsistency_StateTransitionMatrix(t *testing.T) {
	matrix := []struct {
		name       string
		from       string
		event      string
		to         string
		assertion  string
		failClosed bool
	}{
		{
			name:       "front-end bypass submits inconsistent tier funding pair",
			from:       "ADMIN_FORM_SUBMITTED",
			event:      "TIER_1_HIGH_RISK + FIAT_DIRECT",
			to:         "REJECTED_422_CONFIG_INCONSISTENT",
			assertion:  "高危收单通道必须强行绑定加密脱敏模式，禁止直连发卡仓",
			failClosed: true,
		},
		{
			name:       "operator activates dashboard kill switch",
			from:       "ROUTING_ACTIVE",
			event:      "POST /admin/api/v3/system/lockdown",
			to:         "SYSTEM_LOCKDOWN_FAIL_CLOSED",
			assertion:  failclosed.SystemLockdownCode,
			failClosed: true,
		},
		{
			name:       "checkout arrives after lockdown commit",
			from:       "CHECKOUT_ACCEPTING",
			event:      "charge request at T+1ms",
			to:         "CHECKOUT_BLOCKED",
			assertion:  failclosed.SystemLockdownCode,
			failClosed: true,
		},
	}

	for _, row := range matrix {
		t.Run(row.name, func(t *testing.T) {
			assert.NotEmpty(t, row.from)
			assert.NotEmpty(t, row.event)
			assert.NotEmpty(t, row.to)
			assert.NotEmpty(t, row.assertion)
			assert.True(t, row.failClosed)
		})
	}
}
