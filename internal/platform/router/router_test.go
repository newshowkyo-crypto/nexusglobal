package router

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/issuing"
	"github.com/nexusglobal/mamr/internal/pipeline"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// helper: 生成一个 admin JWT token
func makeAdminToken(secret, sub, role string, expired bool) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":  sub,
		"iss":  httpx.AdminIssuerExpected,
		"aud":  httpx.AdminAudienceExpected,
		"role": role,
		// ADM-001: admin 身份必须携带 tenant_id（fail closed）
		"tenant_id": "11111111-1111-1111-1111-111111111111",
		"nbf":       now.Add(-1 * time.Minute).Unix(),
		"exp":       now.Add(1 * time.Hour).Unix(),
		"iat":       now.Unix(),
	}
	if expired {
		claims["exp"] = now.Add(-1 * time.Hour).Unix()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := tok.SignedString([]byte(secret))
	return s
}

// helper: 生成一个 merchant JWT token（含 merchant_id claim）
func makeMerchantToken(secret, merchantID string) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":         merchantID,
		"merchant_id": merchantID,
		"iss":         "nexusglobal-merchant",
		"aud":         "nexusglobal-merchant-api",
		"nbf":         now.Add(-1 * time.Minute).Unix(),
		"exp":         now.Add(1 * time.Hour).Unix(),
		"iat":         now.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := tok.SignedString([]byte(secret))
	return s
}

// helper: 生成一个缺少 role 的 admin JWT
func makeAdminTokenNoRole(secret, sub string) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": sub,
		"iss": httpx.AdminIssuerExpected,
		"aud": httpx.AdminAudienceExpected,
		"nbf": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(1 * time.Hour).Unix(),
		"iat": now.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := tok.SignedString([]byte(secret))
	return s
}

func makeDeps() Deps {
	return Deps{
		Cfg: &config.Config{
			AppEnv:          "test",
			ExternalMode:    "mock",
			JWTSecret:       "test-jwt-secret-at-least-32-characters-long!!",
			InternalToken:   "test-internal-token-at-least-32-chars!!",
			MasterKeyBase64: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", // 32-byte zero key for test
		},
		Log: slog.Default(),
	}
}

// TestBuildRouter_AdminAPIRequiresAuth 验证 /admin/api/v3/* 路由需要鉴权。
func TestBuildRouter_AdminAPIRequiresAuth(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	// 无 Authorization header → 401
	resp, err := http.Get(ts.URL + "/admin/api/v3/merchants")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for no auth, got %d", resp.StatusCode)
	}
}

// TestBuildRouter_MerchantJWTRejectedOnAdminAPI 验证商户 JWT 不能访问 Admin API。
func TestBuildRouter_MerchantJWTRejectedOnAdminAPI(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	merchantTok := makeMerchantToken(deps.Cfg.JWTSecret, "merchant-123")
	req, _ := http.NewRequest("GET", ts.URL+"/admin/api/v3/merchants", nil)
	req.Header.Set("Authorization", "Bearer "+merchantTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for merchant JWT on admin API, got %d", resp.StatusCode)
	}
}

// TestBuildRouter_AdminTokenWithoutRoleRejected 验证缺少 role 的 admin JWT 被拒绝。
func TestBuildRouter_AdminTokenWithoutRoleRejected(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	tok := makeAdminTokenNoRole(deps.Cfg.JWTSecret, "admin-user")
	req, _ := http.NewRequest("GET", ts.URL+"/admin/api/v3/merchants", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for admin JWT without role, got %d", resp.StatusCode)
	}
}

// TestBuildRouter_ExpiredAdminTokenRejected 验证过期的 admin JWT 被拒绝。
func TestBuildRouter_ExpiredAdminTokenRejected(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	tok := makeAdminToken(deps.Cfg.JWTSecret, "admin-user", "viewer", true)
	req, _ := http.NewRequest("GET", ts.URL+"/admin/api/v3/merchants", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", resp.StatusCode)
	}
}

// TestBuildRouter_ValidAdminTokenPassesAuth 验证有效 admin JWT 通过鉴权层。
// 由于 pool=nil，handler 会返回 503/500，但不应是 401/403。
func TestBuildRouter_ValidAdminTokenPassesAuth(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	tok := makeAdminToken(deps.Cfg.JWTSecret, "admin-user", "viewer", false)
	req, _ := http.NewRequest("GET", ts.URL+"/admin/api/v3/merchants", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	// 不应是 401 或 403（鉴权通过）
	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("valid admin token should not return 401")
	}
	if resp.StatusCode == http.StatusForbidden {
		t.Error("valid admin token with valid role should not return 403")
	}
}

// TestBuildRouter_HealthzAccessible 验证健康检查不需要鉴权。
func TestBuildRouter_HealthzAccessible(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for healthz, got %d", resp.StatusCode)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// P0-3: Pipeline Isolation 注入测试
// ──────────────────────────────────────────────────────────────────────────────

// TestBuildRouter_InjectsPipelineService 验证 BuildRouter 将 PipelineService 注入 AcquirerGateway。
// 当 deps.PipelineService 为 nil 且 deps.Pool != nil 时，BuildRouter 应自动构造
// 基于 DBRepository 的真实 pipeline.Service 并注入 AcquirerGateway（P0-3）。
func TestBuildRouter_InjectsPipelineService(t *testing.T) {
	deps := makeDeps()
	// 注入一个 AcquirerGateway（pipelineSvc 为 nil）
	// 由于 Pool=nil，BuildRouter 无法自动构造 PipelineService，
	// 但我们手动注入一个 PipelineService 来验证 SetPipelineService 被调用。
	mockPipelineSvc := pipeline.NewService(
		&mockPipelineRepo{},
		time.Now,
	)
	deps.PipelineService = mockPipelineSvc

	// 创建一个 AcquirerGateway（pipelineSvc 为 nil）
	acqGW := checkout.NewAcquirerGateway(nil, nil, false)
	deps.AcquirerGateway = acqGW

	BuildRouter(deps)

	// 验证 PipelineService 已注入
	if acqGW.PipelineService() == nil {
		t.Error("P0-3: PipelineService should be injected into AcquirerGateway")
	}
	if acqGW.PipelineService() != mockPipelineSvc {
		t.Error("P0-3: injected PipelineService should match the one from deps")
	}
}

// TestProductionStartup_RejectsMissingPipelineService 验证 production/real 模式下
// PipelineService 缺失（且 Pool=nil 无法自动构造）时 BuildRouter 必须 panic（fail-closed）。
func TestProductionStartup_RejectsMissingPipelineService(t *testing.T) {
	deps := makeDeps()
	deps.Cfg.AppEnv = "production"
	deps.Cfg.AcquirerMode = "real"
	deps.PipelineService = nil
	// Pool 保持 nil，无法自动构造 PipelineService

	defer func() {
		if r := recover(); r == nil {
			t.Error("P0-3: BuildRouter should panic in production mode with nil PipelineService and nil Pool")
		}
	}()

	BuildRouter(deps)
}

// mockPipelineRepo 是 pipeline.Repository 的测试 mock。
type mockPipelineRepo struct{}

func (m *mockPipelineRepo) GetGroupByID(ctx context.Context, id uuid.UUID) (*pipeline.IsolationGroup, error) {
	return nil, pipeline.ErrGroupNotFound
}
func (m *mockPipelineRepo) GetGroupByName(ctx context.Context, name string) (*pipeline.IsolationGroup, error) {
	return nil, pipeline.ErrGroupNotFound
}
func (m *mockPipelineRepo) CreateGroup(ctx context.Context, group *pipeline.IsolationGroup) error {
	return nil
}
func (m *mockPipelineRepo) UpdateGroup(ctx context.Context, group *pipeline.IsolationGroup) error {
	return nil
}
func (m *mockPipelineRepo) FreezeGroup(ctx context.Context, id uuid.UUID, reason, operator string) error {
	return nil
}
func (m *mockPipelineRepo) UnfreezeGroup(ctx context.Context, id uuid.UUID, operator string) error {
	return nil
}
func (m *mockPipelineRepo) LogOverride(ctx context.Context, log *pipeline.AdminOverrideAuditLog) error {
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// P0-8: RBAC 权限挂载测试
// ──────────────────────────────────────────────────────────────────────────────

// TestAdminViewer_AllWriteRoutesForbidden 验证 viewer 角色无法访问任何写路由。
// viewer 只有 admin:read 权限，所有 POST/PATCH/DELETE 应返回 403。
func TestAdminViewer_AllWriteRoutesForbidden(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	viewerTok := makeAdminToken(deps.Cfg.JWTSecret, "viewer-user", "viewer", false)

	writeRoutes := []struct {
		method string
		path   string
	}{
		{"POST", "/admin/api/v3/channels"},
		{"POST", "/admin/api/v3/issuing/pools"},
		{"POST", "/admin/api/v3/approvals"},
		{"POST", "/admin/api/v3/system/lockdown"},
		{"POST", "/admin/api/v3/system/recovery/request"},
		{"POST", "/admin/api/v3/system/recovery/execute"},
		{"POST", "/admin/api/v3/payment-links"},
		{"POST", "/admin/adapter/providers"},
		{"POST", "/admin/entities/"},
		{"POST", "/admin/bank-account"},
		{"POST", "/admin/sweep-rule-create"},
		{"POST", "/admin/mae"},
		{"PATCH", "/admin/mae/test-mae-id"},
		{"POST", "/admin/mae/test-mae-id/rotate-secret"},
		{"POST", "/admin/v3/isolation-groups"},
		{"POST", "/admin/v3/isolation-groups/test-id/freeze"},
		{"POST", "/admin/v3/rfi-documents/test-id/approve"},
		{"POST", "/admin/v3/rfi-documents/test-id/reject"},
		{"POST", "/admin/entities/test-id/risk-hold"},
		{"POST", "/admin/webhook/events/test-id/replay"},
	}

	for _, route := range writeRoutes {
		req, _ := http.NewRequest(route.method, ts.URL+route.path, nil)
		req.Header.Set("Authorization", "Bearer "+viewerTok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request to %s %s failed: %v", route.method, route.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("P0-8: viewer should get 403 on %s %s, got %d", route.method, route.path, resp.StatusCode)
		}
	}
}

// TestOperator_CannotApprove 验证 operator 角色无法批准审批。
// operator 有 admin:request_change 但没有 admin:approve 权限。
func TestOperator_CannotApprove(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	operatorTok := makeAdminToken(deps.Cfg.JWTSecret, "operator-user", "operator", false)

	// operator 可以创建审批（admin:request_change）
	req, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/approvals", nil)
	req.Header.Set("Authorization", "Bearer "+operatorTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	// 不应是 403（operator 有 request_change 权限）
	if resp.StatusCode == http.StatusForbidden {
		t.Error("P0-8: operator should be able to create approval (has request_change permission)")
	}

	// operator 不能批准（没有 admin:approve 权限）
	req2, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/approvals/test-id/approve", nil)
	req2.Header.Set("Authorization", "Bearer "+operatorTok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("P0-8: operator should get 403 on approve, got %d", resp2.StatusCode)
	}
}

// TestApprover_CannotCreateApproval 验证 approver 角色无法创建审批（只有 approve 权限，没有 request_change）。
func TestApprover_CannotCreateApproval(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	approverTok := makeAdminToken(deps.Cfg.JWTSecret, "approver-user", "approver", false)

	// approver 不能创建审批（没有 admin:request_change 权限）
	req, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/approvals", nil)
	req.Header.Set("Authorization", "Bearer "+approverTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("P0-8: approver should get 403 on create approval, got %d", resp.StatusCode)
	}

	// approver 可以批准（有 admin:approve 权限）
	req2, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/approvals/test-id/approve", nil)
	req2.Header.Set("Authorization", "Bearer "+approverTok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp2.Body.Close()
	// 不应是 403
	if resp2.StatusCode == http.StatusForbidden {
		t.Error("P0-8: approver should be able to approve (has approve permission)")
	}
}

// TestKillSwitch_RequiresKillSwitchPermission 验证 Kill Switch 路由要求 admin:kill_switch 权限。
// operator 没有 kill_switch 权限，应返回 403。
func TestKillSwitch_RequiresKillSwitchPermission(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	operatorTok := makeAdminToken(deps.Cfg.JWTSecret, "operator-user", "operator", false)

	// operator 不能触发 lockdown
	req, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/system/lockdown", nil)
	req.Header.Set("Authorization", "Bearer "+operatorTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("P0-8: operator should get 403 on lockdown, got %d", resp.StatusCode)
	}

	// security-admin 可以触发 lockdown
	adminTok := makeAdminToken(deps.Cfg.JWTSecret, "sec-admin", "security-admin", false)
	req2, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/system/lockdown", nil)
	req2.Header.Set("Authorization", "Bearer "+adminTok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp2.Body.Close()
	// 不应是 403
	if resp2.StatusCode == http.StatusForbidden {
		t.Error("P0-8: security-admin should be able to trigger lockdown (has kill_switch permission)")
	}
}

// TestPaymentLinkCreate_RequiresPermission 验证创建 Payment Link 要求 payment_link:create 权限。
// viewer 没有此权限，应返回 403；operator 有此权限。
func TestPaymentLinkCreate_RequiresPermission(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	// viewer 不能创建 payment link
	viewerTok := makeAdminToken(deps.Cfg.JWTSecret, "viewer-user", "viewer", false)
	req, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/payment-links", nil)
	req.Header.Set("Authorization", "Bearer "+viewerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("P0-8: viewer should get 403 on payment-link create, got %d", resp.StatusCode)
	}

	// operator 可以创建 payment link
	operatorTok := makeAdminToken(deps.Cfg.JWTSecret, "operator-user", "operator", false)
	req2, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/payment-links", nil)
	req2.Header.Set("Authorization", "Bearer "+operatorTok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp2.Body.Close()
	// 不应是 403
	if resp2.StatusCode == http.StatusForbidden {
		t.Error("P0-8: operator should be able to create payment link (has payment_link:create permission)")
	}
}

// TestCredentialWrite_RequiresPermission 验证 rotate-secret 要求 credential:write 权限。
// operator 没有 credential:write 权限，应返回 403。
func TestCredentialWrite_RequiresPermission(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	operatorTok := makeAdminToken(deps.Cfg.JWTSecret, "operator-user", "operator", false)

	// operator 不能 rotate-secret
	req, _ := http.NewRequest("POST", ts.URL+"/admin/mae/test-mae-id/rotate-secret", nil)
	req.Header.Set("Authorization", "Bearer "+operatorTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("P0-8: operator should get 403 on rotate-secret, got %d", resp.StatusCode)
	}

	// security-admin 可以 rotate-secret
	adminTok := makeAdminToken(deps.Cfg.JWTSecret, "sec-admin", "security-admin", false)
	req2, _ := http.NewRequest("POST", ts.URL+"/admin/mae/test-mae-id/rotate-secret", nil)
	req2.Header.Set("Authorization", "Bearer "+adminTok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp2.Body.Close()
	// 不应是 403
	if resp2.StatusCode == http.StatusForbidden {
		t.Error("P0-8: security-admin should be able to rotate-secret (has credential:write permission)")
	}
}

// TestRiskHold_RequiresPermission 验证 risk-hold 和 freeze 要求 admin:risk:hold 权限。
// operator 没有 risk:hold 权限，应返回 403。
func TestRiskHold_RequiresPermission(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	operatorTok := makeAdminToken(deps.Cfg.JWTSecret, "operator-user", "operator", false)

	// operator 不能设置 risk-hold
	req, _ := http.NewRequest("POST", ts.URL+"/admin/entities/test-id/risk-hold", nil)
	req.Header.Set("Authorization", "Bearer "+operatorTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("P0-8: operator should get 403 on risk-hold, got %d", resp.StatusCode)
	}

	// operator 不能 freeze isolation group
	req2, _ := http.NewRequest("POST", ts.URL+"/admin/v3/isolation-groups/test-id/freeze", nil)
	req2.Header.Set("Authorization", "Bearer "+operatorTok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("P0-8: operator should get 403 on freeze, got %d", resp2.StatusCode)
	}
}

// TestRecovery_RequiresPermission 验证 recovery 路由要求 admin:recovery 权限。
// operator 没有 recovery 权限，应返回 403。
func TestRecovery_RequiresPermission(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	operatorTok := makeAdminToken(deps.Cfg.JWTSecret, "operator-user", "operator", false)

	// operator 不能请求 recovery
	req, _ := http.NewRequest("POST", ts.URL+"/admin/api/v3/system/recovery/request", nil)
	req.Header.Set("Authorization", "Bearer "+operatorTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("P0-8: operator should get 403 on recovery, got %d", resp.StatusCode)
	}
}

// TestBuildRouter_DashboardHasSecurityHeaders 验证 Dashboard HTML 包含安全头 (G04)。
func TestBuildRouter_DashboardHasSecurityHeaders(t *testing.T) {
	deps := makeDeps()
	r := BuildRouter(deps)
	ts := httptest.NewServer(r)
	defer ts.Close()

	tok := makeAdminToken(deps.Cfg.JWTSecret, "admin-user", "viewer", false)
	req, _ := http.NewRequest("GET", ts.URL+"/admin/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// G04: CSP header should be present
	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Error("Dashboard should have Content-Security-Policy header")
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("Dashboard should have X-Content-Type-Options: nosniff")
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("Dashboard should have X-Frame-Options: DENY")
	}
}

// ============================================================================
// R23 Item 7: Dashboard 组合契约测试
// 测试必须先调用 issuing.ResolveRuntimeMode：
//   - local+real 得到 Production 并渲染无 MOCK_LOCAL
//   - local+mock 得到 LocalMock 并渲染有 MOCK_LOCAL
// ============================================================================

// TestDashboardComposition_LocalRealProducesProduction 验证 local+real 解析为 Production，
// Dashboard HTML 不包含 MOCK_LOCAL option。
func TestDashboardComposition_LocalRealProducesProduction(t *testing.T) {
	// 先调用 issuing.ResolveRuntimeMode 验证组合参数
	mode, err := issuing.ResolveRuntimeMode("local", "real")
	require.NoError(t, err)
	assert.True(t, mode.IsProduction(), "local+real must resolve to Production")

	// allowMockLocal = !IsProduction() = false
	allowMockLocal := !mode.IsProduction()
	assert.False(t, allowMockLocal, "Production mode must not allow MOCK_LOCAL")

	// 验证 dashboardHTML 在 allowMockLocal=false 时不渲染 MOCK_LOCAL
	html := httpx.DashboardHTMLForTest("test-nonce", "local", allowMockLocal)
	assert.NotContains(t, html, `<option value="MOCK_LOCAL">`,
		"Production mode dashboard must NOT render MOCK_LOCAL option")
}

// TestDashboardComposition_LocalMockProducesLocalMock 验证 local+mock 解析为 LocalMock，
// Dashboard HTML 包含 MOCK_LOCAL option。
func TestDashboardComposition_LocalMockProducesLocalMock(t *testing.T) {
	// 先调用 issuing.ResolveRuntimeMode 验证组合参数
	mode, err := issuing.ResolveRuntimeMode("local", "mock")
	require.NoError(t, err)
	assert.False(t, mode.IsProduction(), "local+mock must resolve to LocalMock")

	// allowMockLocal = !IsProduction() = true
	allowMockLocal := !mode.IsProduction()
	assert.True(t, allowMockLocal, "LocalMock mode must allow MOCK_LOCAL")

	// 验证 dashboardHTML 在 allowMockLocal=true 时渲染 MOCK_LOCAL
	html := httpx.DashboardHTMLForTest("test-nonce", "local", allowMockLocal)
	assert.Contains(t, html, "MOCK_LOCAL",
		"LocalMock mode dashboard must render MOCK_LOCAL option")
}

// TestDashboardComposition_ProductionRealProducesProduction 验证 production+real 解析为 Production。
func TestDashboardComposition_ProductionRealProducesProduction(t *testing.T) {
	mode, err := issuing.ResolveRuntimeMode("production", "real")
	require.NoError(t, err)
	assert.True(t, mode.IsProduction(), "production+real must resolve to Production")

	allowMockLocal := !mode.IsProduction()
	assert.False(t, allowMockLocal)

	html := httpx.DashboardHTMLForTest("test-nonce", "production", allowMockLocal)
	assert.NotContains(t, html, `<option value="MOCK_LOCAL">`,
		"Production mode dashboard must NOT render MOCK_LOCAL option")
}

// TestDashboardComposition_StagingMockProducesLocalMock 验证 staging+mock 解析为 LocalMock。
func TestDashboardComposition_StagingMockProducesLocalMock(t *testing.T) {
	mode, err := issuing.ResolveRuntimeMode("staging", "mock")
	require.NoError(t, err)
	assert.False(t, mode.IsProduction(), "staging+mock must resolve to LocalMock")

	allowMockLocal := !mode.IsProduction()
	assert.True(t, allowMockLocal)

	html := httpx.DashboardHTMLForTest("test-nonce", "staging", allowMockLocal)
	assert.Contains(t, html, "MOCK_LOCAL",
		"LocalMock mode dashboard must render MOCK_LOCAL option")
}

// TestDashboardComposition_RealMockProducesProduction 验证 real+mock 解析为 Production（fail-closed）。
// EXTERNAL_MODE=real 优先于 APP_ENV 非 production。
func TestDashboardComposition_RealMockProducesProduction(t *testing.T) {
	mode, err := issuing.ResolveRuntimeMode("test", "real")
	require.NoError(t, err)
	assert.True(t, mode.IsProduction(), "test+real must resolve to Production (EXTERNAL_MODE=real takes priority)")

	allowMockLocal := !mode.IsProduction()
	assert.False(t, allowMockLocal)

	html := httpx.DashboardHTMLForTest("test-nonce", "test", allowMockLocal)
	assert.NotContains(t, html, `<option value="MOCK_LOCAL">`,
		"Production mode dashboard must NOT render MOCK_LOCAL option")
}
