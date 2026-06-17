package httpx

import (
	"strings"
	"testing"
)

func TestDashboardHTMLNoIllegalTags(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	if strings.Contains(html, "< button") {
		t.Error("Dashboard HTML contains illegal tag '< button' (should be '<button')")
	}
}

func TestDashboardHTMLNoGarbledCharacters(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	garbledChars := []string{"鎬", "閫", "绠", "鈹"}
	for _, char := range garbledChars {
		if strings.Contains(html, char) {
			t.Errorf("Dashboard HTML contains garbled character: %s", char)
		}
	}
}

func TestDashboardHTMLContainsChineseLabels(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	requiredChineseLabels := []string{
		"通用适配器",
		"法人实体",
		"资产桥配置",
		"钱包白名单",
		"风控运维",
	}

	for _, label := range requiredChineseLabels {
		if !strings.Contains(html, label) {
			t.Errorf("Dashboard HTML missing required Chinese label: %s", label)
		}
	}
}

func TestDashboardHTMLNoEnglishLabels(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	// These English labels should NOT appear in the visible HTML content
	prohibitedEnglishLabels := []string{
		"Universal Adapter",
		"Corporate Entity",
		"Asset Bridge",
		"Wallet Policy",
		"Risk / Ops",
	}

	for _, label := range prohibitedEnglishLabels {
		if strings.Contains(html, label) {
			t.Errorf("Dashboard HTML contains prohibited English label: %s", label)
		}
	}
}

func TestDashboardHTMLContainsStatusTranslations(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	requiredStatusTranslations := []string{
		"'ACTIVE':'启用'",
		"'DISABLED':'禁用'",
		"'RISK_HOLD':'风险冻结'",
	}

	for _, translation := range requiredStatusTranslations {
		if !strings.Contains(html, translation) {
			t.Errorf("Dashboard HTML missing required status translation: %s", translation)
		}
	}
}

func TestDashboardHTMLContainsTypeTranslations(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	requiredTypeTranslations := []string{
		"'CARD_ACQUIRER':'卡网收单'",
		"'BANK_COLLECTION':'银行虚拟户入金'",
		"'CRYPTO_PAY':'加密支付'",
	}

	for _, translation := range requiredTypeTranslations {
		if !strings.Contains(html, translation) {
			t.Errorf("Dashboard HTML missing required type translation: %s", translation)
		}
	}
}

func TestDashboardHTMLContainsErrMsgFunction(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	if !strings.Contains(html, "function errMsg") {
		t.Error("Dashboard HTML missing errMsg function for unified error handling")
	}
}

func TestDashboardHTMLContainsConfirmDialogs(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	// Check that confirm dialogs exist for dangerous operations
	requiredConfirmPatterns := []string{
		"confirmDisableAdapterChannel",
		"confirmSetEntityRiskHold",
		"confirmDisableBridgeProfile",
		"confirmDisableWalletPolicy",
	}

	for _, pattern := range requiredConfirmPatterns {
		if !strings.Contains(html, pattern) {
			t.Errorf("Dashboard HTML missing confirm dialog function: %s", pattern)
		}
	}
}

func TestDashboardHTMLNoDatabaseQueryHints(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	// Dashboard should NOT ask users to query the database directly
	prohibitedPhrases := []string{
		"请到数据库查询",
		"到数据库查询",
		"请查询数据库",
		"查询数据库",
	}

	for _, phrase := range prohibitedPhrases {
		if strings.Contains(html, phrase) {
			t.Errorf("Dashboard HTML contains prohibited database query hint: %s", phrase)
		}
	}
}

func TestDashboardHTMLDetailAPICalls(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	// showRiskEventDetail should call the API, not just show an alert
	if strings.Contains(html, "function showRiskEventDetail") && !strings.Contains(html, "/risk/events/") {
		t.Error("showRiskEventDetail should call /admin/risk/events/{id} API")
	}

	// showTaskDetail should call the API, not just show an alert
	if strings.Contains(html, "function showTaskDetail") && !strings.Contains(html, "/outbox/") {
		t.Error("showTaskDetail should call /admin/outbox/{id} API")
	}
}

func TestDashboardHTMLContainsTierFundingGuard(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	required := []string{
		"function syncTierFundingGuard",
		"TIER_1_HIGH_RISK",
		"CRYPTO_DESENSITIZED",
		"FIAT_DIRECT",
		"高风险资金已强制进入异步合规隔离路径",
		"fiat-direct-panel",
		"crypto-pool-panel",
	}

	for _, pattern := range required {
		if !strings.Contains(html, pattern) {
			t.Errorf("Dashboard HTML missing tier funding guard pattern: %s", pattern)
		}
	}
}

func TestDashboardHTMLContainsIssuingPoolDeclineSlider(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	required := []string{
		"new-iss-decline-limit",
		"declined_count_sliding_window",
		"BIN 码健康滑块",
	}

	for _, pattern := range required {
		if !strings.Contains(html, pattern) {
			t.Errorf("Dashboard HTML missing issuing pool decline slider pattern: %s", pattern)
		}
	}
}

func TestDashboardHTMLContainsLockdownAndSSE(t *testing.T) {
	html := dashboardHTML("test", "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)

	required := []string{
		"triggerSystemLockdown",
		"/api/v3/system/lockdown",
		"系统紧急全盘锁死",
		"EventSource",
		"/api/v3/events/stream",
		"ISSUING_POOL_FRAUD_TRIPPED",
	}

	for _, pattern := range required {
		if !strings.Contains(html, pattern) {
			t.Errorf("Dashboard HTML missing lockdown/SSE pattern: %s", pattern)
		}
	}
}
