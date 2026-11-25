package checkout

import (
	"net/http"
	"strconv"
	"strings"
)

// PAY-003: GET /checkout/v3/{session_id} 与 /pay/{token} 的 HTML 状态页。
//
// 浏览器（Accept: text/html）收到真实状态页；API 客户端（默认 JSON）行为
// 保持不变。六种状态：active/success/declined/expired/used/unknown。
// 页面只展示非敏感信息（金额、币种、商户订单号），不包含 token/内部 ID。

// StatusPageData 状态页模板数据。
type StatusPageData struct {
	Lang            string
	State           string // active | success | declined | expired | used | unknown
	Icon            string
	Title           string
	Description     string
	ShowAmount      bool
	AmountLabel     string
	Currency        string
	Amount          string
	MerchantOrderID string
}

// UI-020 (Phase 6): 状态页 i18n 资源——zh-CN/en-US 双语，Accept-Language 协商。
var statusPageText = map[string]map[string]StatusPageData{
	"zh-CN": {
		"active":   {State: "active", Icon: "⏳", Title: "支付处理中", Description: "该笔支付正在等待完成。", ShowAmount: true, AmountLabel: "金额"},
		"success":  {State: "success", Icon: "✓", Title: "支付完成", Description: "您的支付已收到，感谢您的支持。", ShowAmount: true, AmountLabel: "实付金额"},
		"declined": {State: "declined", Icon: "✕", Title: "支付被拒绝", Description: "该笔支付未完成，请联系商户或更换支付方式重试。"},
		"expired":  {State: "expired", Icon: "⏱", Title: "链接已过期", Description: "该支付链接已过期，请联系商户重新获取。"},
		"used":     {State: "used", Icon: "—", Title: "链接已失效", Description: "该支付链接已不可用。"},
		"unknown":  {State: "unknown", Icon: "?", Title: "未找到支付", Description: "该支付不存在或不可用，请核对链接后重试。"},
	},
	"en-US": {
		"active":   {State: "active", Icon: "⏳", Title: "Payment Pending", Description: "This payment is awaiting completion.", ShowAmount: true, AmountLabel: "Amount"},
		"success":  {State: "success", Icon: "✓", Title: "Payment Complete", Description: "Your payment has been received. Thank you.", ShowAmount: true, AmountLabel: "Amount Paid"},
		"declined": {State: "declined", Icon: "✕", Title: "Payment Declined", Description: "The payment was not completed. Please contact the merchant or try another payment method."},
		"expired":  {State: "expired", Icon: "⏱", Title: "Link Expired", Description: "This payment link has expired. Please request a new one from the merchant."},
		"used":     {State: "used", Icon: "—", Title: "Link No Longer Available", Description: "This payment link is no longer available."},
		"unknown":  {State: "unknown", Icon: "?", Title: "Payment Not Found", Description: "This payment is not available. Please verify the link and try again."},
	},
}

// negotiateLang 按 Accept-Language 协商展示语言；仅支持 zh-CN（默认）与 en-US。
func negotiateLang(acceptLanguage string) string {
	lang := "zh-CN"
	if al := strings.ToLower(acceptLanguage); strings.Contains(al, "en") && !strings.Contains(al, "zh") {
		lang = "en-US"
	}
	return lang
}

// wantsHTML 判断请求方是否为浏览器（Accept 含 text/html）。
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// statusPageState 由 link 状态与 attempt 状态映射出状态页 state。
// link 状态优先（PAID/EXPIRED/REVOKED 是链接级终态），其次 attempt 状态。
func statusPageState(linkStatus, attemptStatus string) string {
	switch linkStatus {
	case "PAID":
		return "success"
	case "EXPIRED":
		return "expired"
	case "REVOKED":
		return "used"
	}
	switch attemptStatus {
	case "CAPTURED", "SETTLED", "PARTIALLY_CAPTURED", "REFUNDED", "PARTIALLY_REFUNDED":
		return "success"
	case "FAILED", "CHARGED_BACK":
		return "declined"
	case "CANCELED":
		return "used"
	case "UNKNOWN_EXTERNAL_STATE":
		return "unknown"
	default:
		// REQUIRES_ACTION / PROCESSING / AUTHORIZED / 空状态 → 活跃
		return "active"
	}
}

// statusPageDataFor 构造指定语言与状态的页面文案（UI-020 i18n）。
func statusPageDataFor(lang, state string) StatusPageData {
	langMap, ok := statusPageText[lang]
	if !ok {
		langMap = statusPageText["zh-CN"]
		lang = "zh-CN"
	}
	data, ok := langMap[state]
	if !ok {
		data = langMap["unknown"]
		state = "unknown"
	}
	data.Lang = lang
	data.State = state
	return data
}

// renderStatusPage 渲染状态页。statusTmpl 为 nil 时返回 false（调用方回退 JSON）。
func (h *CheckoutResolverHandler) renderStatusPage(w http.ResponseWriter, r *http.Request, state string, httpStatus int, amountMinor int64, currency, merchantOrderID string) bool {
	if h.statusTmpl == nil {
		return false
	}
	data := statusPageDataFor(negotiateLang(r.Header.Get("Accept-Language")), state)
	if data.ShowAmount && amountMinor > 0 {
		data.Amount = formatMinorAmount(amountMinor, currency)
		data.Currency = currency
	}
	data.MerchantOrderID = merchantOrderID

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(httpStatus)
	if err := h.statusTmpl.Execute(w, data); err != nil {
		// 头已写出，无法切换为 JSON——记录于响应末尾即可
		_, _ = w.Write([]byte("<!-- render error -->"))
	}
	return true
}

// formatMinorAmount 将 minor 单位金额格式化为主要单位字符串（按币种小数位）。
func formatMinorAmount(minor int64, currency string) string {
	// JPY 等 0 位小数货币按 0 位展示；其余默认 2 位
	exp := 2
	switch strings.ToUpper(currency) {
	case "JPY", "KRW", "VND", "CLP", "ISK":
		exp = 0
	}
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	div := int64(1)
	for i := 0; i < exp; i++ {
		div *= 10
	}
	whole := strconv.FormatInt(minor/div, 10)
	if exp == 0 {
		return sign + whole
	}
	frac := strconv.FormatInt(minor%div, 10)
	for len(frac) < exp {
		frac = "0" + frac
	}
	return sign + whole + "." + frac
}

// renderStatusError 以状态页形式输出稳定错误（HTML 请求方）；nil 模板返回 false。
func (h *CheckoutResolverHandler) renderStatusError(w http.ResponseWriter, r *http.Request, httpStatus int) bool {
	return h.renderStatusPage(w, r, "unknown", httpStatus, 0, "", "")
}
