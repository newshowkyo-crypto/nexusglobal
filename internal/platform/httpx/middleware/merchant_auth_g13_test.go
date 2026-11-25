package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// G13: verifier == nil 不能 panic，应返回 500
func TestMerchantAuth_NilVerifier_NoPanic(t *testing.T) {
	mw := MerchantAuth(nil)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", uuid.New().String())
	req.Header.Set("X-Sign-Token", "any")
	rr := httptest.NewRecorder()

	// 不应 panic
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for nil verifier, got %d", rr.Code)
	}
}

// G13: GetMerchantProfile 返回 nil, nil 不能 panic，应返回 401
func TestMerchantAuth_NilProfile_NoPanic(t *testing.T) {
	verifier := &mockMerchantVerifier{
		verifyResult:         true,
		mockReturnNilProfile: true, // 显式返回 nil, nil
	}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", uuid.New().String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	// 不应 panic；应返回 401
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for nil profile, got %d", rr.Code)
	}
}

// G13: GetMerchantProfile 返回 ErrMerchantNotFound 应返回 401
func TestMerchantAuth_MerchantNotFound_401(t *testing.T) {
	verifier := &mockMerchantVerifier{
		verifyResult: true,
		profileErr:   ErrMerchantNotFound,
	}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", uuid.New().String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for merchant not found, got %d", rr.Code)
	}
}

// G13: VerifyMerchant 返回数据库错误应返回 503，不泄露内部错误
func TestMerchantAuth_VerifyDBError_503(t *testing.T) {
	verifier := &mockMerchantVerifier{
		verifyResult: false,
		verifyErr:    errors.New("connection refused"),
	}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", uuid.New().String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for DB error, got %d", rr.Code)
	}
	// 不应泄露内部错误
	if contains(rr.Body.String(), "connection refused") {
		t.Error("response leaked internal error")
	}
}

// G13: AllowedChannels 必须来自数据库，不硬编码 ["card","bank"]
func TestMerchantAuth_AllowedChannels_FromDB(t *testing.T) {
	merchantID := uuid.New()
	verifier := &mockMerchantVerifier{
		verifyResult:   true,
		profileResult:  &MerchantProfile{ID: merchantID, Status: "ACTIVE", ProviderTier: "TIER_2_COMPLIANT"},
		channelsResult: []string{"card_only"}, // 数据库只授权 card
	}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", merchantID.String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	var capturedCtx MerchantContext
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCtx, _ = GetMerchantContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if len(capturedCtx.AllowedChannels) != 1 || capturedCtx.AllowedChannels[0] != "card_only" {
		t.Errorf("expected AllowedChannels=[card_only], got %v", capturedCtx.AllowedChannels)
	}
}

// G13: AllowedChannels 为空时 fail-closed（空切片，不硬编码）
func TestMerchantAuth_AllowedChannels_EmptyFailClosed(t *testing.T) {
	merchantID := uuid.New()
	verifier := &mockMerchantVerifier{
		verifyResult:   true,
		profileResult:  &MerchantProfile{ID: merchantID, Status: "ACTIVE", ProviderTier: "TIER_2_COMPLIANT"},
		channelsResult: nil, // 数据库无配置
	}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", merchantID.String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	var capturedCtx MerchantContext
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCtx, _ = GetMerchantContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	// fail-closed：空切片，不是 ["card","bank"]
	if len(capturedCtx.AllowedChannels) != 0 {
		t.Errorf("expected empty AllowedChannels (fail-closed), got %v", capturedCtx.AllowedChannels)
	}
}

// G13: 并发删除（verify 成功但 profile 查询时商户被删）应稳定 401
func TestMerchantAuth_ConcurrentDelete_401(t *testing.T) {
	verifier := &mockMerchantVerifier{
		verifyResult: true,
		profileErr:   ErrMerchantNotFound, // verify 后被删
	}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", uuid.New().String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for concurrent delete, got %d", rr.Code)
	}
}

// G13: TenantID 必须来自数据库，不接受请求体声明
func TestMerchantAuth_TenantID_FromDB(t *testing.T) {
	merchantID := uuid.New()
	verifier := &mockMerchantVerifier{
		verifyResult:  true,
		profileResult: &MerchantProfile{ID: merchantID, Status: "ACTIVE", TenantID: "tenant-abc"},
	}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", merchantID.String())
	req.Header.Set("X-Sign-Token", "valid")
	// 请求体中伪造 tenant_id（应被忽略）
	req.Header.Set("X-Tenant-Id", "forged-tenant")
	rr := httptest.NewRecorder()

	var capturedCtx MerchantContext
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCtx, _ = GetMerchantContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if capturedCtx.TenantID != "tenant-abc" {
		t.Errorf("expected TenantID from DB (tenant-abc), got %s", capturedCtx.TenantID)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// 确保上下文测试编译时使用 context 包
var _ = context.Background
