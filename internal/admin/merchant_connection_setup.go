package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// Only onboarding information is saved here. This never enables a channel,
// changes deployment configuration, or accepts private payment credentials.
type merchantConnectionSetup struct {
	ProviderName string `json:"provider_name"`
	Country      string `json:"country"`
	ServiceURL   string `json:"service_url"`
	SalesURL     string `json:"sales_url"`
}

func (s merchantConnectionSetup) validate() bool {
	if len(s.ProviderName) > 120 || len(s.Country) > 2 {
		return false
	}
	for _, raw := range []string{s.ServiceURL, s.SalesURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(raw) > 2048 {
			return false
		}
	}
	return true
}

func (h *V3MerchantHandler) ConnectionSetup(w http.ResponseWriter, r *http.Request) {
	tenant, scoped := tenantScopeOf(r.Context())
	if !scoped {
		writeErr(w, apperr.ErrForbidden.WithMessage("account scope required"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("请选择有效商户"))
		return
	}
	if r.Method == http.MethodPut {
		var setup merchantConnectionSetup
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&setup); err != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("请检查填写的开通资料"))
			return
		}
		setup.ProviderName = strings.TrimSpace(setup.ProviderName)
		setup.Country = strings.ToUpper(strings.TrimSpace(setup.Country))
		setup.ServiceURL = strings.TrimSpace(setup.ServiceURL)
		setup.SalesURL = strings.TrimSpace(setup.SalesURL)
		if !setup.validate() {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("请填写正确的公司注册地区和以 https:// 开头的网址"))
			return
		}
		data, err := json.Marshal(setup)
		if err != nil {
			writeErr(w, err)
			return
		}
		result, err := h.pool.Exec(r.Context(), `UPDATE corporate_entity SET metadata=jsonb_set(COALESCE(metadata,'{}'::jsonb),'{connection_setup}',$1::jsonb),updated_at=now() WHERE id=$2 AND tenant_id=$3`, data, id, tenant)
		if err != nil {
			writeErr(w, err)
			return
		}
		if result.RowsAffected() != 1 {
			writeErr(w, apperr.ErrNotFound.WithMessage("未找到当前账户下的商户"))
			return
		}
		writeJSON(w, map[string]interface{}{"saved": true, "message": "开通资料已保存，完成账户连接和交易核对后才能确认收款可用。"})
		return
	}
	var data []byte
	if err := h.pool.QueryRow(r.Context(), `SELECT COALESCE(metadata->'connection_setup','{}'::jsonb) FROM corporate_entity WHERE id=$1 AND tenant_id=$2`, id, tenant).Scan(&data); err != nil {
		writeErr(w, apperr.ErrNotFound.WithMessage("未找到当前账户下的商户"))
		return
	}
	var setup merchantConnectionSetup
	if err := json.Unmarshal(data, &setup); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, setup)
}
