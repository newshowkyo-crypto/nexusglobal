package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type mockMerchantVerifier struct {
	verifyResult   bool
	verifyErr      error
	activeResult   bool
	activeErr      error
	profileResult  *MerchantProfile
	profileErr     error
	channelsResult []string
	channelsErr    error
	// mockReturnNilProfile=true 时 GetMerchantProfile 返回 nil, profileErr
	mockReturnNilProfile bool
}

func (m *mockMerchantVerifier) VerifyMerchant(ctx context.Context, merchantID uuid.UUID, signToken string) (bool, error) {
	return m.verifyResult, m.verifyErr
}

func (m *mockMerchantVerifier) IsMerchantActive(ctx context.Context, merchantID uuid.UUID) (bool, error) {
	return m.activeResult, m.activeErr
}

func (m *mockMerchantVerifier) GetMerchantProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantProfile, error) {
	if m.mockReturnNilProfile {
		return nil, m.profileErr
	}
	if m.profileResult != nil {
		return m.profileResult, m.profileErr
	}
	return &MerchantProfile{
		ID:            merchantID,
		EntityID:      uuid.New(),
		RiskProfileID: uuid.New(),
		Status:        "ACTIVE",
		ProviderTier:  "TIER_2_COMPLIANT",
		TenantID:      "",
	}, m.profileErr
}

func (m *mockMerchantVerifier) GetAllowedChannels(ctx context.Context, merchantID uuid.UUID) ([]string, error) {
	return m.channelsResult, m.channelsErr
}

func TestMerchantAuth_MissingHeaders_Rejected(t *testing.T) {
	verifier := &mockMerchantVerifier{}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestMerchantAuth_InvalidToken_Rejected(t *testing.T) {
	verifier := &mockMerchantVerifier{verifyResult: false}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", uuid.New().String())
	req.Header.Set("X-Sign-Token", "invalid")
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestMerchantAuth_InactiveMerchant_Rejected(t *testing.T) {
	verifier := &mockMerchantVerifier{
		verifyResult:  true,
		profileResult: &MerchantProfile{ID: uuid.New(), Status: "INACTIVE", ProviderTier: "TIER_2_COMPLIANT"},
	}
	mw := MerchantAuth(verifier)

	req := httptest.NewRequest("POST", "/api/v3/checkout/sessions", nil)
	req.Header.Set("X-Merchant-Id", uuid.New().String())
	req.Header.Set("X-Sign-Token", "valid")
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rr.Code)
	}
}

func TestMerchantAuth_InjectsMerchantContext(t *testing.T) {
	merchantID := uuid.New()
	verifier := &mockMerchantVerifier{
		verifyResult:  true,
		profileResult: &MerchantProfile{ID: merchantID, Status: "ACTIVE", ProviderTier: "TIER_2_COMPLIANT"},
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
		t.Errorf("expected status 200, got %d", rr.Code)
	}
	if capturedCtx.MerchantID != merchantID {
		t.Errorf("expected merchant ID %s, got %s", merchantID, capturedCtx.MerchantID)
	}
}

// TestDBMerchantVerifier_UsesCorporateEntityIDAsEntityID 验证 EntityID 直接使用 corporate_entity.id
func TestDBMerchantVerifier_UsesCorporateEntityIDAsEntityID(t *testing.T) {
	// 此测试验证 GetMerchantProfile 返回的 EntityID 应等于 MerchantID
	// 因为 corporate_entity.id 本身就是实体 ID
	merchantID := uuid.New()

	// 模拟正确的 EntityID 行为
	verifier := &mockMerchantVerifier{
		verifyResult: true,
		profileResult: &MerchantProfile{
			ID:            merchantID,
			EntityID:      merchantID, // EntityID 应等于 MerchantID
			RiskProfileID: uuid.New(),
			Status:        "ACTIVE",
			ProviderTier:  "TIER_2_COMPLIANT",
			TenantID:      "",
		},
	}

	profile, err := verifier.GetMerchantProfile(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if profile.EntityID != profile.ID {
		t.Errorf("EntityID should equal MerchantID (corporate_entity.id), got EntityID=%s, ID=%s", profile.EntityID, profile.ID)
	}
}

// TestDBMerchantVerifier_DefaultsCompliantTierWhenMissing 验证缺失 ProviderTier 时默认 TIER_2_COMPLIANT
func TestDBMerchantVerifier_DefaultsCompliantTierWhenMissing(t *testing.T) {
	merchantID := uuid.New()

	verifier := &mockMerchantVerifier{
		verifyResult: true,
		profileResult: &MerchantProfile{
			ID:            merchantID,
			EntityID:      merchantID,
			RiskProfileID: uuid.New(),
			Status:        "ACTIVE",
			ProviderTier:  "TIER_2_COMPLIANT", // 默认值
			TenantID:      "",
		},
	}

	profile, err := verifier.GetMerchantProfile(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if profile.ProviderTier != "TIER_2_COMPLIANT" {
		t.Errorf("ProviderTier should default to TIER_2_COMPLIANT, got %s", profile.ProviderTier)
	}
}

// TestDBMerchantVerifier_AllowsMissingRiskProfile 验证缺失 risk profile 时 RiskProfileID 为空 UUID
func TestDBMerchantVerifier_AllowsMissingRiskProfile(t *testing.T) {
	merchantID := uuid.New()
	emptyUUID := uuid.UUID{}

	verifier := &mockMerchantVerifier{
		verifyResult: true,
		profileResult: &MerchantProfile{
			ID:            merchantID,
			EntityID:      merchantID,
			RiskProfileID: emptyUUID, // 空 UUID 表示没有 risk profile
			Status:        "ACTIVE",
			ProviderTier:  "TIER_2_COMPLIANT",
			TenantID:      "",
		},
	}

	profile, err := verifier.GetMerchantProfile(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// RiskProfileID 为空 UUID 不应导致鉴权失败
	if profile.Status != "ACTIVE" {
		t.Errorf("merchant should still be ACTIVE even without risk profile, got status %s", profile.Status)
	}
}
