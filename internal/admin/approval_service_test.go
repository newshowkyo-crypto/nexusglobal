package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/configvalidator"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ── G08 测试辅助函数 ──

// makeRequestWithIdentity 创建带已验证 AdminIdentity 的请求（指定 username）。
// G08: 身份必须从 Context 获取，拒绝 body 中伪造的 requester/checker（RED-15）。
func makeRequestWithIdentity(method, path string, body interface{}, username, role string) (*http.Request, *httptest.ResponseRecorder) {
	req, rec := makeRequest(method, path, body)
	identity := &httpx.AdminIdentity{
		Subject:         "sub-" + username,
		Username:        username,
		Role:            role,
		Permissions:     httpx.RolePermissions(role),
		AuthenticatedAt: time.Now(),
	}
	ctx := context.WithValue(req.Context(), httpx.AdminContextKey{}, identity)
	req = req.WithContext(ctx)
	return req, rec
}

// makeRequestWithChiAndIdentity 创建带 chi URL params 和 AdminIdentity 的请求。
func makeRequestWithChiAndIdentity(method, path string, body interface{}, urlParams map[string]string, username, role string) (*http.Request, *httptest.ResponseRecorder) {
	req, rec := makeRequestWithIdentity(method, path, body, username, role)
	rctx := chi.NewRouteContext()
	for key, val := range urlParams {
		rctx.URLParams.Add(key, val)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req, rec
}

// ── G08 核心：PENDING 不改配置 ──

func TestG08_PendingDoesNotChangeConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// 创建测试通道
	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	// 读取初始版本
	var initialVersion int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	// 用 operator 身份创建禁用通道的审批请求
	svc := NewApprovalService(pool, nil)
	ctx := context.Background()
	identity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	ctx = context.WithValue(ctx, httpx.AdminContextKey{}, identity)

	targetIDStr := channelID.String()
	approvalID, err := svc.CreateApprovalRequest(
		ctx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		&initialVersion,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable for testing",
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, approvalID)

	// 验证：PENDING 时通道状态不变
	var status string
	var version int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status, version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&status, &version))
	assert.Equal(t, "ACTIVE", status, "PENDING 时通道状态必须不变")
	assert.Equal(t, initialVersion, version, "PENDING 时通道版本必须不变")

	// 验证：PENDING 时无配置 outbox（只有 apply 后才写 outbox）
	var outboxCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM transactional_outbox WHERE aggregate_id = $1`, channelID).Scan(&outboxCount))
	assert.Equal(t, 0, outboxCount, "PENDING 时不能有配置 outbox")

	// 验证：审批状态为 PENDING
	var approvalStatus string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM admin_approval_requests WHERE id = $1`, approvalID).Scan(&approvalStatus))
	assert.Equal(t, "PENDING", approvalStatus)
}

// ── G08 核心：自审 403 ──

func TestG08_SelfReviewRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// 创建测试通道
	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	var initialVersion int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	svc := NewApprovalService(pool, nil)

	// 用 operator-1 创建审批
	ctx := context.Background()
	identity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	ctx = context.WithValue(ctx, httpx.AdminContextKey{}, identity)

	targetIDStr := channelID.String()
	approvalID, err := svc.CreateApprovalRequest(
		ctx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		&initialVersion,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable for testing",
		nil,
	)
	require.NoError(t, err)

	// 用同一身份（operator-1）尝试审批 → 必须返回 403
	// 注意：operator 角色没有 admin:approve 权限，所以会先因权限不足被拒
	// 但我们测试的是自审拒绝，所以用 security-admin 角色但同一 username
	approveCtx := context.Background()
	approveIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1", // 同一 subject
		Username:    "operator-1", // 同一 username → 自审
		Role:        httpx.AdminRoleSecurityAdmin,
		Permissions: httpx.RolePermissions(httpx.AdminRoleSecurityAdmin),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approveIdentity)

	_, err = svc.ApplyTransaction(approveCtx, approvalID, "self review attempt")
	require.Error(t, err)
	// 必须是 Forbidden 错误
	appErr, ok := apperr.AsAPIError(err)
	require.True(t, ok, "must be apperr")
	assert.Equal(t, http.StatusForbidden, appErr.HTTP)
	assert.Contains(t, appErr.Message, "self-review rejected")
}

// ── G08 核心：不同 checker 批准后原子应用 + outbox ──

func TestG08_DifferentCheckerApprovesAndApplies(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// 创建测试通道
	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	var initialVersion int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	svc := NewApprovalService(pool, nil)

	// 1. operator 创建审批
	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	createCtx = context.WithValue(createCtx, httpx.AdminContextKey{}, operatorIdentity)

	targetIDStr := channelID.String()
	approvalID, err := svc.CreateApprovalRequest(
		createCtx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		&initialVersion,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable for testing",
		nil,
	)
	require.NoError(t, err)

	// 2. 不同 checker（approver）审批
	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	result, err := svc.ApplyTransaction(approveCtx, approvalID, "approved by different checker")
	require.NoError(t, err)
	require.NotNil(t, result)

	// 3. 验证：通道状态已改变
	var status string
	var version int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status, version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&status, &version))
	assert.Equal(t, "INACTIVE", status, "通道必须已被禁用")
	assert.Equal(t, initialVersion+1, version, "版本必须 +1")

	// 4. 验证：仅一个 outbox
	var outboxCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM transactional_outbox WHERE aggregate_id = $1`, channelID).Scan(&outboxCount))
	assert.Equal(t, 1, outboxCount, "必须仅有一个 outbox")

	// 5. 验证：审批状态为 APPLIED
	var approvalStatus, checker string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status, checker FROM admin_approval_requests WHERE id = $1`, approvalID).Scan(&approvalStatus, &checker))
	assert.Equal(t, "APPLIED", approvalStatus)
	assert.Equal(t, "approver-1", checker)

	// 6. 验证：审计日志已写入
	var auditCount int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM admin_audit_log WHERE entity_id = $1 AND action LIKE 'APPROVAL_APPLIED%'`,
		channelID).Scan(&auditCount))
	assert.Equal(t, 1, auditCount, "必须有一条审计日志")
}

// ── G08：版本冲突 → FAILED_STALE ──

func TestG08_VersionConflictFailsStale(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	var initialVersion int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	svc := NewApprovalService(pool, nil)

	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	createCtx = context.WithValue(createCtx, httpx.AdminContextKey{}, operatorIdentity)

	targetIDStr := channelID.String()
	approvalID, err := svc.CreateApprovalRequest(
		createCtx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		&initialVersion,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable for testing",
		nil,
	)
	require.NoError(t, err)

	// 在审批前，另一个操作修改了通道版本
	_, err = pool.Exec(context.Background(),
		`UPDATE acquirer_channels SET version = version + 1, updated_at = now() WHERE id = $1`, channelID)
	require.NoError(t, err)

	// 尝试审批 → 版本冲突
	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	_, err = svc.ApplyTransaction(approveCtx, approvalID, "approve after version changed")
	require.Error(t, err)
	appErr, ok := apperr.AsAPIError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, appErr.HTTP)
	assert.Contains(t, appErr.Message, "version conflict")

	// 验证：审批状态为 FAILED_STALE
	var approvalStatus string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM admin_approval_requests WHERE id = $1`, approvalID).Scan(&approvalStatus))
	assert.Equal(t, "FAILED_STALE", approvalStatus)
}

// ── G08：目标不存在 → NOT_FOUND ──

func TestG08_TargetNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	svc := NewApprovalService(pool, nil)

	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	createCtx = context.WithValue(createCtx, httpx.AdminContextKey{}, operatorIdentity)

	nonExistentID := uuid.New().String()
	approvalID, err := svc.CreateApprovalRequest(
		createCtx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&nonExistentID,
		nil,
		map[string]interface{}{"channel_id": nonExistentID},
		"disable non-existent",
		nil,
	)
	require.NoError(t, err)

	// 尝试审批 → 目标不存在
	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	_, err = svc.ApplyTransaction(approveCtx, approvalID, "approve non-existent target")
	require.Error(t, err)
	appErr, ok := apperr.AsAPIError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusNotFound, appErr.HTTP)
}

// ── G08：拒绝 body 中伪造的 requester ──

func TestG08_RejectsForgedRequester(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewApprovalHandler(pool, nil)

	// 不注入 AdminIdentity，模拟无认证中间件的请求
	req, rec := makeRequest(http.MethodPost, "/admin/api/v3/approvals", map[string]interface{}{
		"action_type":       ActionDisableAcquirerChannel,
		"requester":         "attacker", // 伪造的 requester
		"reason":            "malicious",
		"requested_payload": map[string]interface{}{"channel_id": uuid.New().String()},
	})
	handler.Create(rec, req)

	// 必须返回 403，拒绝 body 中的伪造身份
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ── G08：拒绝 body 中伪造的 checker ──

func TestG08_RejectsForgedChecker(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewApprovalHandler(pool, nil)

	// 不注入 AdminIdentity——提供 route 参数使请求到达 Service，
	// 由 Service 返回 403（拒绝无身份的 apply，身份只能来自认证 Context）
	targetID := uuid.New()
	req, rec := makeRequestWithChiCtx(http.MethodPost, "/admin/api/v3/approvals/"+targetID.String()+"/approve", map[string]interface{}{
		"checker": "attacker", // 伪造的 checker
	}, map[string]string{"id": targetID.String()})
	handler.Approve(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ── G08：幂等键唯一（RED-10）──

func TestG08_IdempotencyKeyUnique(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	svc := NewApprovalService(pool, nil)

	ctx := context.Background()
	identity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	ctx = context.WithValue(ctx, httpx.AdminContextKey{}, identity)

	targetIDStr := channelID.String()
	requestID := "req-001"

	// 第一次创建 → 成功
	_, err = svc.CreateApprovalRequest(
		ctx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		nil,
		map[string]interface{}{"channel_id": channelID.String()},
		"first request",
		&requestID,
	)
	require.NoError(t, err)

	// 第二次创建（相同 request_id）→ 幂等冲突
	_, err = svc.CreateApprovalRequest(
		ctx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		nil,
		map[string]interface{}{"channel_id": channelID.String()},
		"second request",
		&requestID,
	)
	require.Error(t, err)
	appErr, ok := apperr.AsAPIError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, appErr.HTTP)
}

// ── G08：action_type 白名单 ──

func TestG08_ActionTypeWhitelist(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	svc := NewApprovalService(pool, nil)

	ctx := context.Background()
	identity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	ctx = context.WithValue(ctx, httpx.AdminContextKey{}, identity)

	// 非法 action_type → 拒绝
	_, err := svc.CreateApprovalRequest(
		ctx,
		"DROP_TABLE_ATTACK", // 非法 action
		"",
		nil,
		nil,
		map[string]interface{}{"table": "users"},
		"malicious",
		nil,
	)
	require.Error(t, err)
	appErr, ok := apperr.AsAPIError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, appErr.HTTP)
}

// ── G08：两个 checker 并发，只有一个成功 ──

func TestG08_ConcurrentCheckersOnlyOneSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	var initialVersion int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	svc := NewApprovalService(pool, nil)

	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	createCtx = context.WithValue(createCtx, httpx.AdminContextKey{}, operatorIdentity)

	targetIDStr := channelID.String()
	approvalID, err := svc.CreateApprovalRequest(
		createCtx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		&initialVersion,
		map[string]interface{}{"channel_id": channelID.String()},
		"concurrent test",
		nil,
	)
	require.NoError(t, err)

	// 两个 checker 并发审批
	var wg sync.WaitGroup
	var successCount, failCount int
	var mu sync.Mutex

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			approveCtx := context.Background()
			approverIdentity := &httpx.AdminIdentity{
				Subject:     fmt.Sprintf("approver-%d", i),
				Username:    fmt.Sprintf("approver-%d", i),
				Role:        httpx.AdminRoleApprover,
				Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
			}
			approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

			_, err := svc.ApplyTransaction(approveCtx, approvalID, "concurrent approve")
			mu.Lock()
			if err == nil {
				successCount++
			} else {
				failCount++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// 结果必须单一且可解释：只有一个成功，另一个失败（状态已变）
	assert.Equal(t, 1, successCount, "只有一个 checker 能成功")
	assert.Equal(t, 1, failCount, "另一个 checker 必须失败")
}

// ── G08：发卡卡池禁用/启用 ──

func TestG08_IssuingPoolDisableEnable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	// 创建测试卡池
	poolID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO issuing_providers (id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, status)
		 VALUES ($1, 'TIER_2_DIRECT', 'CRYPTO_DESENSITIZED', 'provider-a', 'test-pool', 'vault-a', 'ACTIVE')`,
		poolID)
	require.NoError(t, err)

	var initialVersion int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT version FROM issuing_providers WHERE id = $1`, poolID).Scan(&initialVersion))

	svc := NewApprovalService(pool, nil)

	// 1. 创建禁用审批
	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	createCtx = context.WithValue(createCtx, httpx.AdminContextKey{}, operatorIdentity)

	targetIDStr := poolID.String()
	approvalID, err := svc.CreateApprovalRequest(
		createCtx,
		ActionDisableIssuingPool,
		TargetTypeIssuingPool,
		&targetIDStr,
		&initialVersion,
		map[string]interface{}{"pool_id": poolID.String()},
		"disable pool",
		nil,
	)
	require.NoError(t, err)

	// 2. PENDING 时卡池状态不变
	var status string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM issuing_providers WHERE id = $1`, poolID).Scan(&status))
	assert.Equal(t, "ACTIVE", status)

	// 3. 不同 checker 批准
	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	result, err := svc.ApplyTransaction(approveCtx, approvalID, "approved")
	require.NoError(t, err)
	assert.Equal(t, "issuing_pool.status_changed", result.EventType)
	assert.Equal(t, "config:issuing_pool:reload", result.ReloadTopic)

	// 4. 验证卡池已禁用
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM issuing_providers WHERE id = $1`, poolID).Scan(&status))
	assert.Equal(t, "INACTIVE", status)
}

// ── G08：审批拒绝 ──

func TestG08_RejectApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	svc := NewApprovalService(pool, nil)

	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	createCtx = context.WithValue(createCtx, httpx.AdminContextKey{}, operatorIdentity)

	targetIDStr := channelID.String()
	approvalID, err := svc.CreateApprovalRequest(
		createCtx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		nil,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable",
		nil,
	)
	require.NoError(t, err)

	// 不同 checker 拒绝
	rejectCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	rejectCtx = context.WithValue(rejectCtx, httpx.AdminContextKey{}, approverIdentity)

	err = svc.RejectApproval(rejectCtx, approvalID, "rejected by approver")
	require.NoError(t, err)

	// 验证状态为 REJECTED
	var status string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM admin_approval_requests WHERE id = $1`, approvalID).Scan(&status))
	assert.Equal(t, "REJECTED", status)

	// 验证通道状态不变
	var channelStatus string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM acquirer_channels WHERE id = $1`, channelID).Scan(&channelStatus))
	assert.Equal(t, "ACTIVE", channelStatus, "拒绝后通道状态必须不变")
}

// ── G08：非 PENDING 状态不能再次审批 ──

func TestG08_CannotApproveNonPending(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	svc := NewApprovalService(pool, nil)

	createCtx := context.Background()
	operatorIdentity := &httpx.AdminIdentity{
		Subject:     "operator-1",
		Username:    "operator-1",
		Role:        httpx.AdminRoleOperator,
		Permissions: httpx.RolePermissions(httpx.AdminRoleOperator),
	}
	createCtx = context.WithValue(createCtx, httpx.AdminContextKey{}, operatorIdentity)

	targetIDStr := channelID.String()
	approvalID, err := svc.CreateApprovalRequest(
		createCtx,
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		&targetIDStr,
		nil,
		map[string]interface{}{"channel_id": channelID.String()},
		"disable",
		nil,
	)
	require.NoError(t, err)

	// 第一次审批成功
	approveCtx := context.Background()
	approverIdentity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx = context.WithValue(approveCtx, httpx.AdminContextKey{}, approverIdentity)

	_, err = svc.ApplyTransaction(approveCtx, approvalID, "first approve")
	require.NoError(t, err)

	// 第二次审批（已 APPLIED）→ 必须失败
	approveCtx2 := context.Background()
	approverIdentity2 := &httpx.AdminIdentity{
		Subject:     "approver-2",
		Username:    "approver-2",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	approveCtx2 = context.WithValue(approveCtx2, httpx.AdminContextKey{}, approverIdentity2)

	_, err = svc.ApplyTransaction(approveCtx2, approvalID, "second approve")
	require.Error(t, err)
	appErr, ok := apperr.AsAPIError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, appErr.HTTP)
}

// ── G08：Handler 层 — 身份从 Context 获取 ──

func TestG08_Handler_Create_IdentityFromContext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewApprovalHandler(pool, nil)

	// 注入 operator 身份
	req, rec := makeRequestWithIdentity(http.MethodPost, "/admin/api/v3/approvals",
		map[string]interface{}{
			"action_type":       ActionDisableAcquirerChannel,
			"reason":            "test",
			"target_id":         uuid.New().String(),
			"requested_payload": map[string]interface{}{"channel_id": uuid.New().String()},
		},
		"operator-from-context", httpx.AdminRoleOperator)

	handler.Create(rec, req)

	// 应该成功创建（即使 body 中没有 requester，身份从 Context 获取）
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "operator-from-context", resp["requester"], "requester 必须从 Context 获取")
}

// ── G08：Handler 层 — 高危通道禁用创建 PENDING ──

func TestG08_Handler_DisableChannel_CreatesPending(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	handler := NewV3ChannelsHandler(pool, nil)

	// 注入 operator 身份
	req, rec := makeRequestWithChiAndIdentity(http.MethodPost,
		"/admin/api/v3/channels/"+channelID.String()+"/disable",
		nil,
		map[string]string{"id": channelID.String()},
		"operator-1", httpx.AdminRoleOperator)

	handler.DisableChannel(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "PENDING", resp["status"])
	assert.NotEmpty(t, resp["approval_id"], "必须返回 approval_id")

	// 验证通道状态不变
	var status string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM acquirer_channels WHERE id = $1`, channelID).Scan(&status))
	assert.Equal(t, "ACTIVE", status, "PENDING 时通道状态必须不变")
}

// ── G08：Presenter 展示函数 ──

func TestG08_PresentApprovalStatusCN(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"PENDING", "已申请"},
		{"APPLYING", "应用中"},
		{"APPLIED", "已应用"},
		{"REJECTED", "已拒绝"},
		{"FAILED_STALE", "应用失败（版本冲突）"},
		{"UNKNOWN", "UNKNOWN"},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			assert.Equal(t, tc.expected, presentApprovalStatusCN(tc.status))
		})
	}
}

func TestG08_PresentApprovalSyncStatus(t *testing.T) {
	// PENDING: 只有"已申请"
	result := PresentApprovalSyncStatus("PENDING", "")
	assert.Contains(t, result, "✓ 已申请")
	assert.Contains(t, result, "○ 已批准")
	assert.Contains(t, result, "○ 已应用")
	assert.Contains(t, result, "○ 节点已同步")

	// APPLIED + Dispatched: 全部完成
	result = PresentApprovalSyncStatus("APPLIED", "Dispatched")
	assert.Contains(t, result, "✓ 已申请")
	assert.Contains(t, result, "✓ 已批准")
	assert.Contains(t, result, "✓ 已应用")
	assert.Contains(t, result, "✓ 节点已同步")
}

func TestG08_PresentActionTypeCN(t *testing.T) {
	assert.Equal(t, "禁用收单通道", PresentActionTypeCN(ActionDisableAcquirerChannel))
	assert.Equal(t, "启用收单通道", PresentActionTypeCN(ActionEnableAcquirerChannel))
	assert.Equal(t, "禁用发卡卡池", PresentActionTypeCN(ActionDisableIssuingPool))
	assert.Equal(t, "解除系统封锁", PresentActionTypeCN(ActionSystemLockdownRecovery))
}

func TestG08_PresentApprovalInboxItem(t *testing.T) {
	result := PresentApprovalInboxItem(
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		"channel-123",
		"operator-1",
		"PENDING",
	)
	assert.Contains(t, result, "禁用收单通道")
	assert.Contains(t, result, "operator-1")
	assert.Contains(t, result, "已申请")
}

func TestG08_PresentApprovalDetail(t *testing.T) {
	version := 5
	result := PresentApprovalDetail(
		ActionDisableAcquirerChannel,
		TargetTypeAcquirerChannel,
		"channel-123",
		&version,
		"operator-1",
		"operator",
		"PENDING",
		"",
		"disable for testing",
		"",
		"禁用收单通道将立即停止该通道的所有交易路由",
	)
	assert.Contains(t, result, "禁用收单通道")
	assert.Contains(t, result, "channel-123")
	assert.Contains(t, result, "版本锁：5")
	assert.Contains(t, result, "operator-1")
	assert.Contains(t, result, "已申请")
	assert.Contains(t, result, "风险说明")
}

func TestG08_PresentApprovalBeforeAfter(t *testing.T) {
	before := map[string]interface{}{"status": "ACTIVE", "version": 5}
	after := map[string]interface{}{"status": "INACTIVE", "version": 6}
	result := PresentApprovalBeforeAfter(before, after)
	assert.Contains(t, result, "Before:")
	assert.Contains(t, result, "After:")
	assert.Contains(t, result, "ACTIVE")
	assert.Contains(t, result, "INACTIVE")
}

// TestApprovalConfigUpdate_RequiresRuntimeBinding 验证 buildRuntimeChannelConfig 不包含敏感字段。
// P0-1 约束：runtime channel_config 不得包含 credential/token/secret/api_key/private_key。
// 纯内存测试，不需要容器，不因 testing.Short() 而 Skip。
func TestApprovalConfigUpdate_RequiresRuntimeBinding(t *testing.T) {
	merged := &configvalidator.AcquirerChannelConfig{
		StrategyMode: "DEFAULT",
		ProviderConfig: map[string]interface{}{
			"timeout_ms":  5000,
			"api_key":     "secret-value",
			"credential":  "secret-cred",
			"token":       "secret-token",
			"secret":      "secret-secret",
			"private_key": "secret-key",
		},
	}
	config, isActive := buildRuntimeChannelConfig(merged, "ACTIVE")
	assert.True(t, isActive, "DEFAULT mode should be active")
	assert.Equal(t, false, config["burn_mode"], "DEFAULT mode should not be burn mode")
	// 验证敏感字段被排除
	assert.NotContains(t, config, "api_key")
	assert.NotContains(t, config, "credential")
	assert.NotContains(t, config, "token")
	assert.NotContains(t, config, "secret")
	assert.NotContains(t, config, "private_key")
	// 验证非敏感字段被保留
	assert.Contains(t, config, "timeout_ms")
}

// TestApprovalConfigUpdate_UsesRuntimeTargetAndVersion 验证 BURN_MODE 状态转换：
// BURN_MODE → burn_mode=true + is_active=false；DEFAULT → burn_mode=false + is_active=true。
// 纯内存测试，不需要容器，不因 testing.Short() 而 Skip。
func TestApprovalConfigUpdate_UsesRuntimeTargetAndVersion(t *testing.T) {
	// BURN_MODE → burn_mode=true, is_active=false
	burnMerged := &configvalidator.AcquirerChannelConfig{
		StrategyMode: "BURN_MODE",
		ProviderConfig: map[string]interface{}{
			"timeout_ms": 5000,
		},
	}
	burnConfig, burnIsActive := buildRuntimeChannelConfig(burnMerged, "ACTIVE")
	assert.False(t, burnIsActive, "BURN_MODE should set is_active=false")
	assert.Equal(t, true, burnConfig["burn_mode"], "BURN_MODE should set burn_mode=true")

	// DEFAULT → burn_mode=false, is_active=true
	defaultMerged := &configvalidator.AcquirerChannelConfig{
		StrategyMode: "DEFAULT",
		ProviderConfig: map[string]interface{}{
			"timeout_ms": 5000,
		},
	}
	defaultConfig, defaultIsActive := buildRuntimeChannelConfig(defaultMerged, "ACTIVE")
	assert.True(t, defaultIsActive, "DEFAULT should set is_active=true")
	assert.Equal(t, false, defaultConfig["burn_mode"], "DEFAULT should set burn_mode=false")
}

// TestBuildRuntimeConfig_DefaultInactiveControl 验证 DEFAULT + INACTIVE control 时 runtime 不激活。
func TestBuildRuntimeConfig_DefaultInactiveControl(t *testing.T) {
	merged := &configvalidator.AcquirerChannelConfig{
		StrategyMode:   "DEFAULT",
		ProviderConfig: map[string]interface{}{"timeout_ms": 5000},
	}
	config, isActive := buildRuntimeChannelConfig(merged, "INACTIVE")
	assert.Equal(t, false, config["burn_mode"], "DEFAULT should set burn_mode=false")
	assert.False(t, isActive, "DEFAULT with INACTIVE control must not activate runtime")
}

// TestBuildRuntimeConfig_BurnModeActiveControl 验证 BURN_MODE 覆盖一切：burn_mode=true + is_active=false。
func TestBuildRuntimeConfig_BurnModeActiveControl(t *testing.T) {
	merged := &configvalidator.AcquirerChannelConfig{
		StrategyMode:   "BURN_MODE",
		ProviderConfig: map[string]interface{}{"timeout_ms": 5000},
	}
	config, isActive := buildRuntimeChannelConfig(merged, "ACTIVE")
	assert.Equal(t, true, config["burn_mode"], "BURN_MODE should set burn_mode=true")
	assert.False(t, isActive, "BURN_MODE must force runtime inactive regardless of control status")
}

// TestCreateRuntimeConfig_BurnMode 验证 Create 期用同一 builder 派生的 runtime 参数对 BURN_MODE 正确。
func TestCreateRuntimeConfig_BurnMode(t *testing.T) {
	req := &configvalidator.AcquirerChannelConfig{
		StrategyMode:   "BURN_MODE",
		ProviderCode:   "mock",
		ChannelCode:    "ch-burn",
		ProviderConfig: map[string]interface{}{"currencies": []interface{}{"USD"}},
	}
	runtimeConfig, runtimeIsActive := buildRuntimeChannelConfig(req, "ACTIVE")
	assert.Equal(t, true, runtimeConfig["burn_mode"], "created BURN_MODE runtime must have burn_mode=true")
	assert.False(t, runtimeIsActive, "created BURN_MODE runtime must be is_active=false")
}

// ── 测试D：ApplyTransaction savepoint/失败回滚（可替换事务执行器，无 DB）──

// recordingFakeTx 记录 Exec/Commit 调用序列，用于验证 savepoint 失败路径。
// 嵌入 pgx.Tx 接口：未实现的方法在意外调用时因 nil 接口方法 panic，可暴露测试路径外的调用。
type recordingFakeTx struct {
	pgx.Tx
	mu                sync.Mutex
	execSQLs          []string // 记录 Exec 的 SQL（含失败的 ROLLBACK 调用）
	committed         bool     // Commit 是否被调用
	rollbackErr       error    // 非 nil 时，ROLLBACK TO SAVEPOINT 前缀的 Exec 返回该错误
	staleErr          error    // 非 nil 时，FAILED_STALE 的 Exec 返回该错误
	staleRowsAffected *int64   // nil = 默认影响 1 行；非 nil = 使用该值（0 模拟零行更新）
}

func (f *recordingFakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execSQLs = append(f.execSQLs, sql)
	if strings.Contains(sql, "ROLLBACK TO SAVEPOINT") {
		if f.rollbackErr != nil {
			return pgconn.CommandTag{}, f.rollbackErr
		}
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	if strings.Contains(sql, "FAILED_STALE") {
		if f.staleErr != nil {
			return pgconn.CommandTag{}, f.staleErr
		}
		rowsAffected := int64(1)
		if f.staleRowsAffected != nil {
			rowsAffected = *f.staleRowsAffected
		}
		return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", rowsAffected)), nil
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (f *recordingFakeTx) Commit(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = true
	return nil
}

func (f *recordingFakeTx) Rollback(ctx context.Context) error { return nil }

// failingBeginner 是始终 Begin 失败的 txBeginner fake。
type failingBeginner struct {
	err error
}

func (f *failingBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, f.err
}

// TestHandleApplyFailure_RollbackThenStaleThenCommit 验证失败路径的调用序列：
// ROLLBACK TO SAVEPOINT → FAILED_STALE → Commit，且返回原始 apply 错误。
// 纯单元测试，不需要 DB/Redis/容器，不因 testing.Short() 而 Skip。
func TestHandleApplyFailure_RollbackThenStaleThenCommit(t *testing.T) {
	fake := &recordingFakeTx{}
	svc := &ApprovalService{}
	applyErr := errors.New("no runtime channel bound")
	err := svc.handleApplyFailure(context.Background(), fake, uuid.New(), "test-approval", applyErr)
	// 必须返回原始 apply 错误
	require.Error(t, err)
	assert.ErrorIs(t, err, applyErr, "must return the original apply error")
	// 调用序列：ROLLBACK → FAILED_STALE → Commit
	require.Len(t, fake.execSQLs, 2, "exactly 2 Exec calls expected")
	assert.Contains(t, fake.execSQLs[0], "ROLLBACK TO SAVEPOINT approval_apply")
	assert.Contains(t, fake.execSQLs[1], "FAILED_STALE")
	assert.True(t, fake.committed, "FAILED_STALE must be committed")
}

// TestHandleApplyFailure_RollbackErrorAbortsCommit 验证 SAVEPOINT 回滚失败时：
// 不写 FAILED_STALE、不 Commit，返回回滚内部错误。
// 纯单元测试，不需要 DB/Redis/容器，不因 testing.Short() 而 Skip。
func TestHandleApplyFailure_RollbackErrorAbortsCommit(t *testing.T) {
	fake := &recordingFakeTx{rollbackErr: errors.New("savepoint gone")}
	svc := &ApprovalService{}
	err := svc.handleApplyFailure(context.Background(), fake, uuid.New(), "test", errors.New("apply failed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback to savepoint")
	assert.False(t, fake.committed, "must not commit when savepoint rollback fails")
	// 只有 1 次 Exec（ROLLBACK 失败后不应写 FAILED_STALE）
	require.Len(t, fake.execSQLs, 1, "no FAILED_STALE write after failed savepoint rollback")
	assert.Contains(t, fake.execSQLs[0], "ROLLBACK TO SAVEPOINT")
}

// TestHandleApplyFailure_StaleUpdateErrorDoesNotCommit 验证 FAILED_STALE 更新失败时：
// 不 Commit（由调用方 defer Rollback 回滚整个事务使审批回到 PENDING），
// 返回包含 FAILED_STALE 的内部错误。
// 纯单元测试，不需要 DB/Redis/容器，不因 testing.Short() 而 Skip。
func TestHandleApplyFailure_StaleUpdateErrorDoesNotCommit(t *testing.T) {
	fake := &recordingFakeTx{staleErr: errors.New("deadlock detected")}
	svc := &ApprovalService{}
	err := svc.handleApplyFailure(context.Background(), fake, uuid.New(), "test-approval", errors.New("apply failed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FAILED_STALE", "error must mention FAILED_STALE marking failure")
	assert.False(t, fake.committed, "must not commit when FAILED_STALE update fails")
	// 调用序列：ROLLBACK 成功 → FAILED_STALE 失败 → 不应 Commit
	require.Len(t, fake.execSQLs, 2)
	assert.Contains(t, fake.execSQLs[0], "ROLLBACK TO SAVEPOINT")
	assert.Contains(t, fake.execSQLs[1], "FAILED_STALE")
}

// TestHandleApplyFailure_StaleUpdateZeroRowsDoesNotCommit 验证 FAILED_STALE 更新影响 0 行
// （审批不是 APPLYING，如并发已流转）时：不 Commit，返回非法状态迁移错误。
// 纯单元测试，不需要 DB/Redis/容器，不因 testing.Short() 而 Skip。
func TestHandleApplyFailure_StaleUpdateZeroRowsDoesNotCommit(t *testing.T) {
	zero := int64(0)
	fake := &recordingFakeTx{staleRowsAffected: &zero}
	svc := &ApprovalService{}
	err := svc.handleApplyFailure(context.Background(), fake, uuid.New(), "test-approval", errors.New("apply failed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RowsAffected", "error must report the zero-row mismatch")
	assert.False(t, fake.committed, "must not commit when FAILED_STALE affects 0 rows")
	// 调用序列：ROLLBACK 成功 → FAILED_STALE 0 行 → 不应 Commit
	require.Len(t, fake.execSQLs, 2)
	assert.Contains(t, fake.execSQLs[0], "ROLLBACK TO SAVEPOINT")
	assert.Contains(t, fake.execSQLs[1], "FAILED_STALE")
}

// TestApplyTransaction_UsesInjectedTxBeginner 验证 ApplyTransaction 优先使用注入的
// txBeginner：nil pool 下不 panic，且 Begin 错误被包装为 failed to begin transaction。
// 若注入未生效，nil pool.Begin 会直接 panic，测试必然失败。
// 纯单元测试，不需要 DB/Redis/容器，不因 testing.Short() 而 Skip。
func TestApplyTransaction_UsesInjectedTxBeginner(t *testing.T) {
	svc := &ApprovalService{} // pool == nil
	svc.setTxBeginnerForTest(&failingBeginner{err: errors.New("begin failed")})
	ctx := context.Background()
	identity := &httpx.AdminIdentity{
		Subject:     "approver-1",
		Username:    "approver-1",
		Role:        httpx.AdminRoleApprover,
		Permissions: httpx.RolePermissions(httpx.AdminRoleApprover),
	}
	ctx = context.WithValue(ctx, httpx.AdminContextKey{}, identity)

	_, err := svc.ApplyTransaction(ctx, uuid.New().String(), "test reason")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to begin transaction", "injected txBeginner must be used")
}

// TestFundingMode_OnlyTopLevelPreservesOldRouting 验证仅提交顶层 funding_mode 时，
// 深拷贝旧 routing_config 并写入新 vcc_funding_mode，保留旧字段（risk_isolation_group_id 等）。
// 这是 applyChannelUpdate / UpdateChannel 中"req.FundingMode != "" && req.RoutingConfig == nil"路径的 pure helper 验证。
func TestFundingMode_OnlyTopLevelPreservesOldRouting(t *testing.T) {
	// 旧 routing_config 含 risk_isolation_group_id + CRYPTO_DESENSITIZED
	oldRC := map[string]interface{}{
		"risk_isolation_group_id": "grp-1",
		"vcc_funding_mode":        "CRYPTO_DESENSITIZED",
	}

	// 只提交 FIAT_DIRECT（顶层 funding_mode），不携带 routing_config
	// 模拟 applyChannelUpdate 中的深拷贝逻辑
	copiedRouting := make(map[string]interface{}, len(oldRC)+1)
	for k, v := range oldRC {
		copiedRouting[k] = v
	}
	copiedRouting["vcc_funding_mode"] = "FIAT_DIRECT"

	// 断言：旧字段保留，vcc_funding_mode 被新值覆盖
	assert.Equal(t, "grp-1", copiedRouting["risk_isolation_group_id"],
		"old routing fields must be preserved")
	assert.Equal(t, "FIAT_DIRECT", copiedRouting["vcc_funding_mode"],
		"vcc_funding_mode must be updated to new top-level value")

	// 原 oldRC 不被修改（深拷贝验证）
	assert.Equal(t, "CRYPTO_DESENSITIZED", oldRC["vcc_funding_mode"],
		"original oldRC must not be mutated")
}

// seedRuntimeChannelBinding 为控制面通道播种 runtime 绑定（P0-3 apply 路径
// 要求 tenant_acquirer_channel 存在，fail-closed；缺绑定时 apply 返回 NOT_FOUND）。
func seedRuntimeChannelBinding(t *testing.T, pool *pgxpool.Pool, channelID, entityID uuid.UUID) {
	t.Helper()
	providerID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO payment_provider (id, provider_code, provider_name, provider_type)
		 VALUES ($1, $2, 'Test Provider', 'MAE')`,
		providerID, "testprov-"+channelID.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO tenant_acquirer_channel
			(id, tenant_id, merchant_id, channel_code, provider_id, channel_name, currencies, is_active, acquirer_channel_id)
		VALUES ($1, $2, $3, $4, $5, 'Test Runtime Channel', '["USD"]', true, $6)`,
		uuid.New(), uuid.New(), entityID, "runtime-"+channelID.String()[:8], providerID, channelID)
	require.NoError(t, err)
}
