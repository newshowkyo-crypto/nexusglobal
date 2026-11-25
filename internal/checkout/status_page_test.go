package checkout

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// loadTestStatusTmpl 从仓库真实模板文件构造状态页模板。
func loadTestStatusTmpl(t *testing.T) *template.Template {
	t.Helper()
	path := filepath.Join("..", "..", "web", "checkout", "status.html")
	tmpl, err := template.ParseFiles(path)
	if err != nil {
		t.Fatalf("parse status.html failed: %v", err)
	}
	return tmpl
}

// PAY-003: 浏览器（Accept: text/html）访问 session resolver 得到 HTML 状态页。
func TestResolverSession_HTMLStatusPage(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))

	session, err := service.CreateSession(context.Background(), CreateSessionRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "html_page_order",
		AmountMinor:     1500,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		BaseURL:         "http://localhost:8080",
	})
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}

	handler := NewCheckoutResolverHandler(service, nil)
	handler.SetStatusTmpl(loadTestStatusTmpl(t))

	r := chi.NewRouter()
	r.Get("/checkout/v3/{session_id}", handler.ResolveSession)

	// 浏览器请求 → HTML 状态页（active）
	req := httptest.NewRequest(http.MethodGet, "/checkout/v3/"+session.SessionID, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "state-active") {
		t.Error("expected active state page body")
	}
	if strings.Contains(body, "15.00") == false && !strings.Contains(body, "$") {
		t.Errorf("expected amount 15.00 rendered, got body: %s", body)
	}

	// API 请求（无 Accept: text/html）→ 保持 JSON 行为
	req2 := httptest.NewRequest(http.MethodGet, "/checkout/v3/"+session.SessionID, nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if ct := rec2.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("default requests should stay JSON, got %q", ct)
	}

	// 无效 session → unknown 状态页（HTML），不泄露存在性
	req3 := httptest.NewRequest(http.MethodGet, "/checkout/v3/00000000-0000-0000-0000-000000000000", nil)
	req3.Header.Set("Accept", "text/html")
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec3.Code)
	}
	if !strings.Contains(rec3.Body.String(), "state-unknown") {
		t.Error("expected unknown state page for invalid session")
	}
}

// PAY-003: Payment Link resolver 的 HTML 状态页（active 与 unknown）。
func TestPaymentLink_HTMLStatusPage(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))

	paymentLinkSvc := NewPaymentLinkServiceWithEncryptor(pool, newTestTokenEncryptor(t))
	paymentLinkSvc.SetChannelRegistry(service.channelRegistry)

	result, err := paymentLinkSvc.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "link_html_order",
		AmountMinor:     2500,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("create payment link failed: %v", err)
	}

	handler := NewCheckoutResolverHandler(service, paymentLinkSvc)
	handler.SetStatusTmpl(loadTestStatusTmpl(t))

	r := chi.NewRouter()
	r.Get("/pay/{token}", handler.ResolvePaymentLink)

	req := httptest.NewRequest(http.MethodGet, "/pay/"+result.Token, nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "state-active") {
		t.Error("expected active state page for fresh link")
	}
	if !strings.Contains(body, "25.00") {
		t.Error("expected amount 25.00 rendered on page")
	}
	if strings.Contains(body, result.Token) {
		t.Error("page must never contain the raw token")
	}

	// 无效 token → unknown 状态页
	req2 := httptest.NewRequest(http.MethodGet, "/pay/"+strings.Repeat("f", 64), nil)
	req2.Header.Set("Accept", "text/html")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "state-unknown") {
		t.Error("expected unknown state page for invalid token")
	}
}

// statusPageState 状态映射表驱动测试。
func TestStatusPageState_Mapping(t *testing.T) {
	cases := []struct {
		link, attempt, want string
	}{
		{"PAID", "", "success"},
		{"EXPIRED", "", "expired"},
		{"REVOKED", "", "used"},
		{"", "CAPTURED", "success"},
		{"", "SETTLED", "success"},
		{"", "FAILED", "declined"},
		{"", "CHARGED_BACK", "declined"},
		{"", "CANCELED", "used"},
		{"", "UNKNOWN_EXTERNAL_STATE", "unknown"},
		{"", "REQUIRES_ACTION", "active"},
		{"", "PROCESSING", "active"},
		{"", "AUTHORIZED", "active"},
		{"", "", "active"},
	}
	for _, c := range cases {
		if got := statusPageState(c.link, c.attempt); got != c.want {
			t.Errorf("statusPageState(%q,%q) = %q, want %q", c.link, c.attempt, got, c.want)
		}
	}
}

// PAY-002: 回跳 URL 未登记 host 被拒绝（session 与 payment link 双入口）。
func TestReturnURLAllowlist_RejectsUnregisteredHost(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	merchantID := uuid.New()
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))

	// Session：evil.com 不在 allowlist → RETURN_URL_NOT_ALLOWED
	_, err := service.CreateSession(context.Background(), CreateSessionRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "allowlist_order",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://evil.com/success",
		CancelURL:       "https://example.com/cancel",
		BaseURL:         "http://localhost:8080",
	})
	if err == nil {
		t.Fatal("expected RETURN_URL_NOT_ALLOWED for unregistered host")
	}
	if apiErr, ok := apperr.AsAPIError(err); !ok || apiErr.Code != "RETURN_URL_NOT_ALLOWED" {
		t.Errorf("expected RETURN_URL_NOT_ALLOWED, got %v", err)
	}

	// Payment Link：同样拒绝
	paymentLinkSvc := NewPaymentLinkServiceWithEncryptor(pool, newTestTokenEncryptor(t))
	paymentLinkSvc.SetChannelRegistry(service.channelRegistry)
	_, err = paymentLinkSvc.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "allowlist_link",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://evil.com/cancel",
	})
	if err == nil {
		t.Fatal("expected RETURN_URL_NOT_ALLOWED for unregistered cancel host")
	}
	if apiErr, ok := apperr.AsAPIError(err); !ok || apiErr.Code != "RETURN_URL_NOT_ALLOWED" {
		t.Errorf("expected RETURN_URL_NOT_ALLOWED, got %v", err)
	}

	// allowlist 命中 → 成功创建
	_, err = paymentLinkSvc.CreatePaymentLink(context.Background(), CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "allowlist_link_ok",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("expected allowlisted host to pass, got %v", err)
	}
}

// PAY-001: checkout.html 不再收集 raw PAN/CVV；无 PSP SDK 时显示 BLOCKED。
func TestCheckoutHTML_NoRawCardCollection(t *testing.T) {
	path := filepath.Join("..", "..", "web", "checkout", "checkout.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read checkout.html failed: %v", err)
	}
	html := string(content)

	for _, banned := range []string{"card_number", "card_cvv", `id="card-number"`, `id="card-cvv"`} {
		if strings.Contains(html, banned) {
			t.Errorf("PAY-001 VIOLATION: checkout.html still collects card data (%q)", banned)
		}
	}
	if !strings.Contains(html, "payment_method_token") {
		t.Error("checkout.html must submit provider-tokenized payment_method_token")
	}
	if !strings.Contains(html, "blocked-notice") {
		t.Error("checkout.html must show BLOCKED notice when PSP Hosted Fields are unavailable")
	}

	// 模板必须可解析
	if _, err := template.ParseFiles(path); err != nil {
		t.Errorf("checkout.html must remain a valid template: %v", err)
	}
}

// Phase 6 / UI-020: 状态页 Accept-Language 协商——en-US 与 zh-CN 双语。
func TestStatusPage_I18nNegotiation(t *testing.T) {
	merchantID := uuid.New()
	pool := getTestPool(t)
	service := NewService(pool, nil, nil, nil)
	service.SetChannelRegistry(seedAcquirerChannelForMerchant(context.Background(), t, pool, merchantID))
	service.SetTokenEncryptor(newTestTokenEncryptor(t))

	session, err := service.CreateSession(context.Background(), CreateSessionRequest{
		MerchantID:      merchantID,
		MerchantOrderID: "i18n_order",
		AmountMinor:     1000,
		Currency:        "USD",
		SuccessURL:      "https://example.com/success",
		CancelURL:       "https://example.com/cancel",
		BaseURL:         "http://localhost:8080",
	})
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}

	handler := NewCheckoutResolverHandler(service, nil)
	handler.SetStatusTmpl(loadTestStatusTmpl(t))
	r := chi.NewRouter()
	r.Get("/checkout/v3/{session_id}", handler.ResolveSession)

	// en-US：英文文案
	req := httptest.NewRequest(http.MethodGet, "/checkout/v3/"+session.SessionID, nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Payment Pending") {
		t.Error("expected en-US title for Accept-Language: en-US")
	}
	if !strings.Contains(rec.Body.String(), `lang="en-US"`) {
		t.Error("expected lang=en-US attribute")
	}

	// zh-CN（默认）：中文文案
	req2 := httptest.NewRequest(http.MethodGet, "/checkout/v3/"+session.SessionID, nil)
	req2.Header.Set("Accept", "text/html")
	req2.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "支付处理中") {
		t.Error("expected zh-CN title for Accept-Language: zh-CN")
	}
	if !strings.Contains(rec2.Body.String(), `lang="zh-CN"`) {
		t.Error("expected lang=zh-CN attribute")
	}
}

// negotiateLang 表驱动。
func TestNegotiateLang(t *testing.T) {
	cases := map[string]string{
		"":                     "zh-CN",
		"en-US,en;q=0.9":       "en-US",
		"zh-CN,zh;q=0.9":       "zh-CN",
		"fr-FR,fr;q=0.9":       "zh-CN",
		"en;q=0.5,zh-CN;q=0.9": "zh-CN",
	}
	for al, want := range cases {
		if got := negotiateLang(al); got != want {
			t.Errorf("negotiateLang(%q)=%q want %q", al, got, want)
		}
	}
}
