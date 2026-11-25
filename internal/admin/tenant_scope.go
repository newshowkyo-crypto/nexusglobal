package admin

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ADM-001: 对象级租户 scope。
//
// 身份层（admin_auth.go）已 fail-closed：生产 admin token 必须携带 tenant_id，
// 不存在跨租户的全局角色。因此对象级查询在调用方携带租户身份时必须按
// tenant_id 过滤（本 helper 返回 scoped=true）。
// 仅测试直调 handler（绕过 auth 中间件、无身份）时 scoped=false，不过滤。
func tenantScopeOf(ctx context.Context) (tenantID string, scoped bool) {
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil || identity.TenantID == "" {
		return "", false
	}
	return identity.TenantID, true
}

func requireAdminTenant(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	value, _ := tenantScopeOf(r.Context())
	tenant, err := uuid.Parse(value)
	if err != nil || tenant == uuid.Nil {
		writeErr(w, apperr.ErrForbidden)
		return uuid.Nil, false
	}
	return tenant, true
}
