package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

func canStoreAcquirerCredential(status string) bool {
	return status == "ACTIVE" || status == "INACTIVE"
}

func acquirerCredentialChecklist(providerCode string) []domain.CredentialType {
	switch providerCode {
	case "stripe":
		return []domain.CredentialType{domain.CredentialTypeSecret, domain.CredentialTypeWebhookSecret}
	case "nxtapay":
		return []domain.CredentialType{domain.CredentialTypeAPIKey, domain.CredentialTypeSecret, domain.CredentialTypeWebhookSecret}
	case "mock":
		return []domain.CredentialType{domain.CredentialTypeWebhookSecret}
	default:
		return nil
	}
}

func (h *AcquirerCredentialHandler) CredentialStatus(w http.ResponseWriter, r *http.Request) {
	identity, ok := httpx.GetAdminIdentity(r.Context())
	if !ok || identity == nil {
		httpx.WriteError(w, apperr.ErrForbidden)
		return
	}
	tenantID, err := uuid.Parse(identity.TenantID)
	if err != nil || tenantID == uuid.Nil {
		httpx.WriteError(w, apperr.ErrForbidden)
		return
	}
	channelID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, apperr.ErrInvalidArgument.WithMessage("invalid channel id"))
		return
	}
	if h.db == nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("credential database unavailable"))
		return
	}
	var providerCode, channelCode, channelStatus, merchantName string
	var bound bool
	var credentials json.RawMessage
	err = h.db.QueryRow(r.Context(), `
		SELECT ac.provider_code, ac.channel_code, ac.status, ce.name, tac.id IS NOT NULL,
		       COALESCE((
		           SELECT jsonb_object_agg(latest.credential_type, jsonb_build_object(
		               'version', latest.key_version, 'updated_at', latest.updated_at, 'expires_at', latest.expires_at,
		               'expired', COALESCE(latest.expires_at <= now(), false)))
		           FROM (
		               SELECT DISTINCT ON (credential_type) credential_type, key_version, updated_at, expires_at
		               FROM provider_credential_vault
		               WHERE channel_id = tac.id AND provider_id = tac.provider_id
		               ORDER BY credential_type, key_version DESC
		           ) latest
		       ), '{}'::jsonb)
		FROM acquirer_channels ac
		JOIN corporate_entity ce ON ce.id = ac.entity_id AND ce.tenant_id = $2
		LEFT JOIN payment_provider pp ON pp.provider_code = ac.provider_code
		LEFT JOIN tenant_acquirer_channel tac ON tac.acquirer_channel_id = ac.id
		     AND tac.tenant_id = ce.tenant_id AND tac.merchant_id = ce.id AND tac.provider_id = pp.id
		WHERE ac.id = $1`, channelID, tenantID).Scan(&providerCode, &channelCode, &channelStatus, &merchantName, &bound, &credentials)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, apperr.ErrNotFound.WithMessage("acquirer channel not found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, apperr.ErrInternal.WithMessage("failed to read credential status"))
		return
	}
	required := acquirerCredentialChecklist(providerCode)
	editBlock := ""
	switch {
	case !identity.HasPermission(httpx.PermAdminCredentialWrite):
		editBlock = "当前账号仅可查看凭证状态，请联系安全管理员录入。"
	case required == nil:
		editBlock = "此机构尚无可用的凭证录入配置，请先完成机构接入。"
	case !bound:
		editBlock = "通道尚未绑定收款账户，请先完成通道配置。"
	case h.enc == nil:
		editBlock = "凭证加密服务未配置，请联系管理员。"
	case !canStoreAcquirerCredential(channelStatus):
		editBlock = "当前通道状态不允许更新凭证。"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{
		"provider_code": providerCode, "channel_code": channelCode, "merchant_name": merchantName,
		"channel_status": channelStatus, "required": required, "credentials": credentials,
		"can_write": editBlock == "", "edit_block": editBlock,
	})
}
