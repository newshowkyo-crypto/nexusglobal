package router

import (
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	acquirerhandler "github.com/nexusglobal/mamr/internal/acquirer/handler"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	riskcarding "github.com/nexusglobal/mamr/internal/risk/carding"
)

// TestDashboardRoutesRegistered UI-005 route contract：
// dashboard JS 通过 API_BASE（/admin）调用的每个静态路径前缀，必须能命中
// BuildRouter 真实注册的路由。路由被改名/删除而 dashboard 未同步时，本测试失败。
// 注：/api/v3/acquirer/channels 与 /risk/events 仅在对应 handler 装配时注册
// （生产组合根必装），测试 deps 中显式构造以覆盖生产形态。
func TestDashboardRoutesRegistered(t *testing.T) {
	deps := makeDeps()
	deps.AcquirerHandler = acquirerhandler.New(nil, nil)
	deps.RiskHandler = riskcarding.NewHandler(riskcarding.NewService(nil, nil, nil, nil))
	// R20: dashboard 静态调用 /api/v3/issuing/vault-credentials 与 /api/v3/channels/{id}/credentials，
	// 该路由仅在 deps.Enc != nil 时注册（生产组合根必装），测试 deps 显式构造以覆盖生产形态。
	kms, err := crypto.NewMockKMS(deps.Cfg.MasterKeyBase64)
	if err != nil {
		t.Fatalf("construct MockKMS failed: %v", err)
	}
	deps.Enc = crypto.NewEncryptor(kms)
	r := BuildRouter(deps)

	registered := map[string]bool{}
	walkRoutes(r, "", registered)

	called := httpx.DashboardCalledRoutesForTest()
	if len(called) == 0 {
		t.Fatal("dashboard exposes no called routes — template changed?")
	}

	for _, path := range called {
		if !pathCovered("/admin"+path, registered) {
			t.Errorf("UI-005 route contract violation: dashboard calls /admin%s but no registered route covers it", path)
		}
	}
}

// pathCovered 检查静态前缀是否命中某个注册路由模式。
// chi Routes() 对嵌套 mount 会在拼接路径中插入 "*" 占位段（如 /admin/*/api/v3/*/approvals），
// 比较时先剔除 "*" 段；{param} 段匹配任意静态段。dashboard 的静态前缀
// （如 /api/v3/approvals）需与模式的前 N 个段逐段对齐。
func pathCovered(path string, registered map[string]bool) bool {
	if registered[path] {
		return true
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for pattern := range registered {
		var patternSegs []string
		for _, ps := range strings.Split(strings.Trim(pattern, "/"), "/") {
			if ps == "*" {
				continue
			}
			patternSegs = append(patternSegs, ps)
		}
		if len(patternSegs) < len(segments) {
			continue
		}
		match := true
		for i, seg := range segments {
			ps := patternSegs[i]
			if strings.HasPrefix(ps, "{") && strings.HasSuffix(ps, "}") {
				continue
			}
			if ps != seg {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func walkRoutes(r chi.Router, prefix string, out map[string]bool) {
	for _, route := range r.Routes() {
		full := prefix + route.Pattern
		if route.SubRoutes != nil {
			walkRoutes(route.SubRoutes.(chi.Router), full, out)
			continue
		}
		out[full] = true
	}
}
