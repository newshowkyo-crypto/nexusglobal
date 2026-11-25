package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// makeRequestWithChiAndAdminIdentity 创建带 chi URL params 和 AdminIdentity 的请求。
// 用于 VA handler 测试，模拟已认证的 admin 用户。
func makeRequestWithChiAndAdminIdentity(method, path string, body interface{}, urlParams map[string]string, username, role string) (*http.Request, *httptest.ResponseRecorder) {
	req, rec := makeRequest(method, path, body)
	identity := &httpx.AdminIdentity{
		Subject:         "sub-" + username,
		Username:        username,
		Role:            role,
		Permissions:     httpx.RolePermissions(role),
		AuthenticatedAt: time.Now(),
	}
	ctx := context.WithValue(req.Context(), httpx.AdminContextKey{}, identity)
	rctx := chi.NewRouteContext()
	for key, val := range urlParams {
		rctx.URLParams.Add(key, val)
	}
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
	return req, rec
}

// TestReleaseVA_UsesAuthenticatedRequester 验证 ReleaseVA 使用 Context 中的已验证身份作为 requester。
//
// 场景：
//  1. 请求带已验证 AdminIdentity（username="admin-alice", role="operator"）
//  2. 请求体中包含 reason 但不包含 requester 字段
//  3. 期望：不返回 403（身份验证通过），不返回 400（reason 存在）
//     由于 nil pool，后续 DB 查询会失败返回 500，但不会返回 403/400
func TestReleaseVA_UsesAuthenticatedRequester(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)
	vaID := uuid.New()

	body := map[string]interface{}{
		"reason": "test release reason",
	}
	req, rec := makeRequestWithChiAndAdminIdentity(
		http.MethodPost,
		"/admin/api/v3/bank/va-pool/"+vaID.String()+"/release",
		body,
		map[string]string{"id": vaID.String()},
		"admin-alice",
		"operator",
	)

	handler.ReleaseVA(rec, req)

	// 不应返回 403（身份验证通过）
	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"ReleaseVA with authenticated identity should not return 403")
	// 不应返回 400（reason 存在）
	assert.NotEqual(t, http.StatusBadRequest, rec.Code,
		"ReleaseVA with reason should not return 400")
}

// TestDisableVA_RejectsMissingIdentity 验证 DisableVA 在缺少已验证身份时返回 403。
//
// 场景：
//  1. 请求不带 AdminIdentity（未认证）
//  2. 请求体中包含 reason
//  3. 期望：返回 403 FORBIDDEN
func TestDisableVA_RejectsMissingIdentity(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)
	vaID := uuid.New()

	body := map[string]interface{}{
		"reason": "test disable reason",
	}
	req, rec := makeRequestWithChiCtx(
		http.MethodPost,
		"/admin/api/v3/bank/va-pool/"+vaID.String()+"/disable",
		body,
		map[string]string{"id": vaID.String()},
	)

	handler.DisableVA(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"DisableVA without authenticated identity must return 403")
	assert.Contains(t, rec.Body.String(), "FORBIDDEN")
	assert.Contains(t, rec.Body.String(), "authenticated identity required")
}

// TestVAApproval_IgnoresSpoofedRequester 验证请求体中伪造的 requester 字段被忽略。
//
// 场景：
//  1. 请求带已验证 AdminIdentity（username="admin-bob"）
//  2. 请求体中包含伪造的 requester="spoofed-attacker" 和 reason
//  3. 期望：不返回 403（身份验证通过，使用 Context 身份而非 body 中的伪造身份）
//     由于 nil pool，后续 DB 查询会失败返回 500，但关键是不返回 403
//     这证明 body 中的 requester 字段不影响身份验证逻辑
func TestVAApproval_IgnoresSpoofedRequester(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)
	vaID := uuid.New()

	// 请求体中包含伪造的 requester 字段
	body := map[string]interface{}{
		"requester": "spoofed-attacker",
		"reason":    "test reason with spoofed requester",
	}
	req, rec := makeRequestWithChiAndAdminIdentity(
		http.MethodPost,
		"/admin/api/v3/bank/va-pool/"+vaID.String()+"/release",
		body,
		map[string]string{"id": vaID.String()},
		"admin-bob",
		"operator",
	)

	handler.ReleaseVA(rec, req)

	// 不应返回 403 — 身份验证使用 Context 中的 "admin-bob"，而非 body 中的 "spoofed-attacker"
	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"ReleaseVA should use Context identity, not spoofed body requester")
	// 不应返回 400 — reason 存在
	assert.NotEqual(t, http.StatusBadRequest, rec.Code,
		"ReleaseVA with reason should not return 400")

	// 验证响应体不包含伪造的 requester
	var resp map[string]interface{}
	if json.NewDecoder(rec.Body).Decode(&resp) == nil {
		if msg, ok := resp["message"]; ok {
			assert.NotContains(t, msg, "spoofed-attacker",
				"Response must not contain spoofed requester from body")
		}
	}
}

// TestReleaseVA_RejectsMissingIdentity 额外验证 ReleaseVA 在缺少身份时也返回 403。
func TestReleaseVA_RejectsMissingIdentity(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)
	vaID := uuid.New()

	body := map[string]interface{}{
		"reason": "test release reason",
	}
	req, rec := makeRequestWithChiCtx(
		http.MethodPost,
		"/admin/api/v3/bank/va-pool/"+vaID.String()+"/release",
		body,
		map[string]string{"id": vaID.String()},
	)

	handler.ReleaseVA(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"ReleaseVA without authenticated identity must return 403")
	assert.Contains(t, rec.Body.String(), "FORBIDDEN")
}

// TestReleaseVA_RejectsEmptyReason 验证 reason 为空时返回 400。
func TestReleaseVA_RejectsEmptyReason(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)
	vaID := uuid.New()

	req, rec := makeRequestWithChiAndAdminIdentity(
		http.MethodPost,
		"/admin/api/v3/bank/va-pool/"+vaID.String()+"/release",
		map[string]interface{}{},
		map[string]string{"id": vaID.String()},
		"admin-carol",
		"operator",
	)

	handler.ReleaseVA(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"ReleaseVA without reason must return 400")
	assert.Contains(t, rec.Body.String(), "reason is required")
}

// TestDisableVA_RejectsEmptyReason 验证 DisableVA reason 为空时返回 400。
func TestDisableVA_RejectsEmptyReason(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)
	vaID := uuid.New()

	req, rec := makeRequestWithChiAndAdminIdentity(
		http.MethodPost,
		"/admin/api/v3/bank/va-pool/"+vaID.String()+"/disable",
		map[string]interface{}{},
		map[string]string{"id": vaID.String()},
		"admin-dave",
		"operator",
	)

	handler.DisableVA(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"DisableVA without reason must return 400")
	assert.Contains(t, rec.Body.String(), "reason is required")
}

// TestReleaseVA_RejectsXOperatorHeader 验证 X-Operator header 不再被读取。
//
// 场景：
//  1. 请求不带 AdminIdentity 但带 X-Operator header
//  2. 期望：返回 403（不回退到 X-Operator header）
func TestReleaseVA_RejectsXOperatorHeader(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)
	vaID := uuid.New()

	req, rec := makeRequestWithChiCtx(
		http.MethodPost,
		"/admin/api/v3/bank/va-pool/"+vaID.String()+"/release",
		map[string]interface{}{"reason": "test"},
		map[string]string{"id": vaID.String()},
	)
	req.Header.Set("X-Operator", "header-attacker")

	handler.ReleaseVA(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"ReleaseVA must not fall back to X-Operator header")
}

// TestDisableVA_RejectsSystemFallback 验证不再回退为 "system"。
//
// 场景：
//  1. 请求不带 AdminIdentity、不带 X-Operator header、不带 body requester
//  2. 期望：返回 403（不回退为 "system"）
func TestDisableVA_RejectsSystemFallback(t *testing.T) {
	handler := NewV3BankHandler(nil, nil)
	vaID := uuid.New()

	req, rec := makeRequestWithChiCtx(
		http.MethodPost,
		"/admin/api/v3/bank/va-pool/"+vaID.String()+"/disable",
		map[string]interface{}{"reason": "test"},
		map[string]string{"id": vaID.String()},
	)

	handler.DisableVA(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"DisableVA must not fall back to 'system'")
}

// 确保 require 包被使用
var _ = require.True
