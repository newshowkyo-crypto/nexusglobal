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

func TestPresentOverrideAuditLogHumanText_Coverage(t *testing.T) {
	tests := []struct {
		name         string
		overrideType string
		operator     string
		reason       string
		expected     string
	}{
		{
			name:         "RISK_OVERRIDE",
			overrideType: "RISK_OVERRIDE",
			operator:     "operator_a",
			reason:       "测试风控",
			expected:     "操作员 operator_a 对通道风控参数执行了人工覆盖，原因：测试风控",
		},
		{
			name:         "FREEZE",
			overrideType: "FREEZE",
			operator:     "admin_user",
			reason:       "可疑交易",
			expected:     "操作员 admin_user 冻结了隔离组，原因：可疑交易",
		},
		{
			name:         "UNFREEZE",
			overrideType: "UNFREEZE",
			operator:     "operator_b",
			reason:       "核实无误",
			expected:     "操作员 operator_b 解除了隔离组冻结，原因：核实无误",
		},
		{
			name:         "SYSTEM_LOCKDOWN",
			overrideType: "SYSTEM_LOCKDOWN",
			operator:     "security_team",
			reason:       "检测到大规模攻击",
			expected:     "操作员 security_team 启动了系统全局 Fail-Closed 锁死大闸，原因：检测到大规模攻击",
		},
		{
			name:         "RFI_APPROVE",
			overrideType: "RFI_APPROVE",
			operator:     "compliance_a",
			reason:       "资料齐全",
			expected:     "操作员 compliance_a 通过了 RFI 资料包，原因：资料齐全",
		},
		{
			name:         "RFI_REJECT",
			overrideType: "RFI_REJECT",
			operator:     "compliance_b",
			reason:       "资料不一致",
			expected:     "操作员 compliance_b 拒绝了 RFI 资料包，原因：资料不一致",
		},
		{
			name:         "unknown fallback",
			overrideType: "CUSTOM_ACTION",
			operator:     "test_op",
			reason:       "测试原因",
			expected:     "操作员 test_op 执行了 CUSTOM_ACTION 操作，原因：测试原因",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := PresentOverrideAuditLogHumanText(tc.overrideType, tc.operator, tc.reason)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestApprovalHandler_Create_MissingFields(t *testing.T) {
	h := &ApprovalHandler{pool: nil, rdb: nil, svc: NewApprovalService(nil, nil)}

	// G08: 需要注入 AdminIdentity Context
	req, rec := makeRequestWithIdentity(http.MethodPost, "/approvals",
		map[string]interface{}{"reason": "test"}, "operator-1", httpx.AdminRoleOperator)
	h.Create(rec, req)

	// action_type 为空 → 400
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestApprovalHandler_Create_InvalidJSON(t *testing.T) {
	h := &ApprovalHandler{pool: nil, rdb: nil, svc: NewApprovalService(nil, nil)}

	// G08: 需要注入 AdminIdentity Context
	req, rec := makeRequestWithIdentity(http.MethodPost, "/approvals",
		`{invalid json}`, "operator-1", httpx.AdminRoleOperator)
	h.Create(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestApprovalHandler_Approve_MissingID(t *testing.T) {
	h := &ApprovalHandler{pool: nil, rdb: nil, svc: NewApprovalService(nil, nil)}

	// G08: 需要注入 AdminIdentity Context
	req, rec := makeRequestWithIdentity(http.MethodPost, "/approvals//approve",
		map[string]interface{}{}, "approver-1", httpx.AdminRoleApprover)
	h.Approve(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestApprovalHandler_Reject_MissingID(t *testing.T) {
	h := &ApprovalHandler{pool: nil, rdb: nil, svc: NewApprovalService(nil, nil)}

	// G08: 需要注入 AdminIdentity Context
	req, rec := makeRequestWithIdentity(http.MethodPost, "/approvals//reject",
		map[string]interface{}{}, "approver-1", httpx.AdminRoleApprover)
	h.Reject(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestApprovalHandler_Reject_MissingChecker(t *testing.T) {
	h := &ApprovalHandler{pool: nil, rdb: nil, svc: NewApprovalService(nil, nil)}

	// G08: 不注入 AdminIdentity → 403，但需要 chi URL param 让 id 非空
	req, rec := makeRequestWithChiCtx(http.MethodPost, "/approvals/test-id/reject",
		map[string]interface{}{}, map[string]string{"id": "test-id"})

	h.Reject(rec, req)

	// G08: 无 AdminIdentity Context → 403
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403 (no identity), got %d", rec.Code)
	}
}

func TestApprovalHandler_ApproveAndReject_PathID(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewApprovalHandler(pool, nil)
	ctx := context.Background()

	// G08: 创建测试通道以支持 ApplyTransaction
	entityID := uuid.New()
	channelID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO corporate_entity (id, tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, 'Test Entity', 'Test Entity LLC', 'US', 'US-DE', 'ACTIVE')`,
		entityID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO acquirer_channels (id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status)
		 VALUES ($1, $2, 'TIER_2_COMPLIANT', 'provider-a', 'test-channel', 'vault-a', 'DEFAULT', 'ACTIVE')`,
		channelID, entityID)
	require.NoError(t, err)
	seedRuntimeChannelBinding(t, pool, channelID, entityID)

	var initialVersion int
	require.NoError(t, pool.QueryRow(ctx, `SELECT version FROM acquirer_channels WHERE id = $1`, channelID).Scan(&initialVersion))

	// 创建审批请求（带 typed payload 和 target_version）
	var approveID string
	err = pool.QueryRow(ctx,
		`INSERT INTO admin_approval_requests (action_type, target_type, target_id, target_version, validated_payload, requester, reason, status)
		 VALUES ('DISABLE_ACQUIRER_CHANNEL', 'acquirer_channel', $1, $2, '{"channel_id":"`+channelID.String()+`"}'::jsonb, 'operator_a', 'disable test', 'PENDING')
		 RETURNING id::text`,
		channelID, initialVersion).Scan(&approveID)
	require.NoError(t, err)

	// G08: 身份从 Context 获取，用不同 checker 批准
	req, rec := makeRequestWithChiAndIdentity(http.MethodPost, "/admin/api/v3/approvals/"+approveID+"/approve",
		map[string]interface{}{"review_reason": "approved"},
		map[string]string{"id": approveID},
		"checker_a", httpx.AdminRoleApprover)
	h.Approve(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var status, checker string
	err = pool.QueryRow(ctx, `SELECT status, checker FROM admin_approval_requests WHERE id = $1`, approveID).Scan(&status, &checker)
	require.NoError(t, err)
	// G08: 状态从 APPROVED 改为 APPLIED（ApplyTransaction 原子应用后）
	assert.Equal(t, "APPLIED", status)
	assert.Equal(t, "checker_a", checker)

	// 验证通道已禁用
	var channelStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM acquirer_channels WHERE id = $1`, channelID).Scan(&channelStatus))
	assert.Equal(t, "INACTIVE", channelStatus)

	// 创建拒绝测试（带 typed payload）
	poolID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO issuing_providers (id, issuing_tier, funding_mode, provider_code, pool_code, credential_vault_key, status)
		 VALUES ($1, 'TIER_2_DIRECT', 'FIAT_DIRECT', 'test-provider', 'test-pool', 'vault-a', 'ACTIVE')`,
		poolID)
	require.NoError(t, err)

	var rejectID string
	err = pool.QueryRow(ctx,
		`INSERT INTO admin_approval_requests (action_type, target_type, target_id, validated_payload, requester, status)
		 VALUES ('DISABLE_ISSUING_POOL', 'issuing_pool', $1, '{"pool_id":"`+poolID.String()+`"}'::jsonb, 'operator_b', 'PENDING')
		 RETURNING id::text`,
		poolID).Scan(&rejectID)
	require.NoError(t, err)

	// G08: 身份从 Context 获取
	req, rec = makeRequestWithChiAndIdentity(http.MethodPost, "/admin/api/v3/approvals/"+rejectID+"/reject",
		map[string]interface{}{"review_reason": "rejected"},
		map[string]string{"id": rejectID},
		"checker_b", httpx.AdminRoleApprover)
	h.Reject(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	err = pool.QueryRow(ctx, `SELECT status, checker FROM admin_approval_requests WHERE id = $1`, rejectID).Scan(&status, &checker)
	require.NoError(t, err)
	assert.Equal(t, "REJECTED", status)
	assert.Equal(t, "checker_b", checker)
}

func TestApprovalHandler_List_AllowsNullCheckerAndReason(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewApprovalHandler(pool, nil)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO admin_approval_requests (action_type, requested_payload, requester, status)
		 VALUES ('UPDATE_MERCHANT_RESERVE', '{"merchant_id":"test"}'::jsonb, 'operator_a', 'PENDING')`)
	require.NoError(t, err)

	req, rec := makeRequest(http.MethodGet, "/admin/api/v3/approvals", nil)
	h.List(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, float64(1), resp["total"])
}
