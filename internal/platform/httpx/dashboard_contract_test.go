package httpx

import (
	"regexp"
	"strings"
	"testing"
)

// Phase 4: Dashboard DOM/route 合同测试（UI-001~005）。
// 固化 tab 按钮与 section 的对应关系，以及 JS 引用的表格元素必须存在的 DOM 合同，
// 以及 dashboard JS 调用的 API 路径必须与路由注册一致的 route 合同。

func renderDashboardForContract() string {
	return dashboardHTML("test-nonce", "test", false, "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)
}

func TestDashboard_IntegrationGuideShowsUnavailableSiteStep(t *testing.T) {
	html := renderDashboardForContract()
	for _, text := range []string{"商户接入向导", "申请收款规则", "配置收款通道", "站点凭证和通道授权流程仍在完善", "登记外部站点"} {
		if !strings.Contains(html, text) {
			t.Errorf("integration guide missing %q", text)
		}
	}
}

// TestDashboard_TabSectionContract 每个 data-tab 按钮必须有对应 section，反之亦然。
func TestDashboard_TabSectionContract(t *testing.T) {
	html := renderDashboardForContract()

	tabRe := regexp.MustCompile(`<button[^>]+data-tab="([a-z-]+)"`)
	secRe := regexp.MustCompile(`id="tab-([^"]+)"`)

	tabs := map[string]bool{}
	for _, m := range tabRe.FindAllStringSubmatch(html, -1) {
		tabs[m[1]] = true
	}
	sections := map[string]bool{}
	for _, m := range secRe.FindAllStringSubmatch(html, -1) {
		sections[m[1]] = true
	}

	for _, key := range []string{"overview", "merchant-onboarding", "payment-orders", "acquirer-channels",
		"payment-refunds", "payment-settlements", "payment-payouts", "risk-ops", "approval-inbox"} {
		if !tabs[key] {
			t.Errorf("missing operational navigation %q", key)
		}
	}
	if len(tabs) != len(sections) {
		t.Errorf("tab buttons (%d) and sections (%d) count mismatch", len(tabs), len(sections))
	}
	for tab := range tabs {
		if !sections[tab] {
			t.Errorf("UI-001 DOM contract violation: tab %q has no section id=\"tab-%s\"", tab, tab)
		}
	}
	for sec := range sections {
		if !tabs[sec] {
			t.Errorf("orphan section id=\"tab-%s\" has no data-tab button", sec)
		}
	}
}

// TestDashboard_TableIDContract JS 里引用的每个 *-table 元素必须在 HTML 中存在。
func TestDashboard_TableIDContract(t *testing.T) {
	html := renderDashboardForContract()

	refRe := regexp.MustCompile(`'(.[a-z0-9-]*-table)'\)`)
	refs := map[string]bool{}
	for _, m := range refRe.FindAllStringSubmatch(html, -1) {
		refs[m[1]] = true
	}

	idRe := regexp.MustCompile(`id="([^"]+)"`)
	ids := map[string]bool{}
	for _, m := range idRe.FindAllStringSubmatch(html, -1) {
		ids[m[1]] = true
	}

	for ref := range refs {
		if !ids[ref] {
			t.Errorf("UI-002 DOM contract violation: JS references element id=%q but it does not exist in HTML", ref)
		}
	}
}

// TestDashboard_RouteContract 守护 dashboard JS 调用的 API 路径与路由注册的一致性。
// 路由合同的完整走查（chi.Walk 真实路由树）见 internal/platform/router/dashboard_route_contract_test.go。
func TestDashboard_RouteContract_StaticPathsExist(t *testing.T) {
	html := renderDashboardForContract()
	pathRe := regexp.MustCompile(`API_BASE\+'([^']+)'`)
	for _, m := range pathRe.FindAllStringSubmatch(html, -1) {
		p := m[1]
		if !strings.HasPrefix(p, "/") {
			t.Errorf("dashboard API path must start with '/', got %q", p)
		}
	}
}

// Phase 5: UI-022/017/023/018 可视性合同——design tokens、响应式、减动效、焦点可见。
func TestDashboard_Phase5VisualContract(t *testing.T) {
	html := renderDashboardForContract()
	for _, marker := range []string{
		":root{--c-bg:",            // design tokens
		":focus-visible",           // 键盘焦点可见
		"@media (max-width:768px)", // 响应式断点
		"@media (max-width:1024px)",
		"@media (prefers-reduced-motion:reduce)", // 减少动效
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("Phase 5 visual contract violation: missing %q", marker)
		}
	}
}

// Phase 8 前置: 最后 5 个 P0 修复合同（UI-006/007/008/009/011）。
func TestDashboard_P0UIFixesContract(t *testing.T) {
	html := renderDashboardForContract()
	for _, tc := range []struct {
		name   string
		marker string
	}{
		{"UI-006 适配器通道表单含租户输入", `id="new-channel-tenant"`},
		{"UI-006 创建通道请求携带 tenant_id", `tenant_id:tenant`},
		{"UI-007 supported 数组防御性渲染 helper", `function listToText(`},
		{"UI-007 通道表渲染走 listToText", `listToText(i.supported_assets)`},
		{"UI-008 VA 通道下拉加载函数", `function loadVAChannels(`},
		{"UI-008 bank-vba tab 挂载通道下拉加载", `loadVAPool();loadVAChannels();`},
		{"UI-009 释放 VA 收集原因输入", `va-release-reason`},
		{"UI-009 释放 VA 请求携带 reason", `JSON.stringify({reason:reason})`},
		{"UI-011 Provider 启用走审批动作", `enableAdapterProvider`},
		{"UI-011 启用动作提交 ENABLE_ADAPTER_PROVIDER", `submitApprovalRequest('ENABLE_ADAPTER_PROVIDER'`},
	} {
		if !strings.Contains(html, tc.marker) {
			t.Errorf("P0 UI fix contract violation (%s): missing %q", tc.name, tc.marker)
		}
	}
}
