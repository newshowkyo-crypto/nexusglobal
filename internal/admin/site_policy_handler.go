package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

var siteCodePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

type SitePolicyHandler struct{ pool *pgxpool.Pool }

func NewSitePolicyHandler(pool *pgxpool.Pool) *SitePolicyHandler {
	return &SitePolicyHandler{pool: pool}
}
func (h *SitePolicyHandler) ListSites(w http.ResponseWriter, r *http.Request) {
	q := `SELECT id::text,merchant_id::text,site_code,display_name,origin,status,version,created_at::text,updated_at::text FROM merchant_site`
	args := []any{}
	if t, ok := tenantScopeOf(r.Context()); ok {
		q += ` WHERE tenant_id=$1`
		args = append(args, t)
	}
	q += ` ORDER BY updated_at DESC`
	rows, e := h.pool.Query(r.Context(), q, args...)
	if e != nil {
		writeErr(w, e)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, mid, code, name, origin, status, ca, ua string
		var v int
		if e = rows.Scan(&id, &mid, &code, &name, &origin, &status, &v, &ca, &ua); e != nil {
			writeErr(w, e)
			return
		}
		items = append(items, map[string]any{"id": id, "merchant_id": mid, "site_code": code, "display_name": name, "origin": origin, "status": status, "version": v, "created_at": ca, "updated_at": ua})
	}
	writeJSON(w, map[string]any{"items": items, "total": len(items)})
}
func (h *SitePolicyHandler) CreateSite(w http.ResponseWriter, r *http.Request) {
	var x struct {
		MerchantID  string `json:"merchant_id"`
		SiteCode    string `json:"site_code"`
		DisplayName string `json:"display_name"`
		Origin      string `json:"origin"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&x) != nil {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	x.SiteCode = strings.TrimSpace(x.SiteCode)
	x.DisplayName = strings.TrimSpace(x.DisplayName)
	mid, e := uuid.Parse(x.MerchantID)
	if e != nil || mid == uuid.Nil || !siteCodePattern.MatchString(x.SiteCode) || len([]rune(x.DisplayName)) > 100 {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("请选择商户，并填写 1 到 64 位的站点代码"))
		return
	}
	u, e := url.Parse(strings.TrimSpace(x.Origin))
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(x.Origin) > 2048 {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("填写站点的 HTTPS 域名，如 https://shop.example.com"))
		return
	}
	x.Origin = u.Scheme + "://" + u.Host
	t, ok := tenantScopeOf(r.Context())
	if !ok {
		writeErr(w, apperr.ErrForbidden)
		return
	}
	var id uuid.UUID
	e = h.pool.QueryRow(r.Context(), `INSERT INTO merchant_site(tenant_id,merchant_id,site_code,display_name,origin) SELECT $1,$2,$3,$4,$5 WHERE EXISTS(SELECT 1 FROM corporate_entity WHERE id=$2 AND tenant_id=$1) RETURNING id`, t, mid, x.SiteCode, x.DisplayName, x.Origin).Scan(&id)
	if e != nil {
		if e == pgx.ErrNoRows {
			writeErr(w, apperr.ErrForbidden)
			return
		}
		writeErr(w, e)
		return
	}
	writeJSON(w, map[string]any{"id": id.String(), "status": "PENDING"})
}
func (h *SitePolicyHandler) SetSiteStatus(w http.ResponseWriter, r *http.Request) {
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	var x struct {
		Status string `json:"status"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || x.Status != "ACTIVE" && x.Status != "SUSPENDED" && x.Status != "REVOKED" {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	t, ok := tenantScopeOf(r.Context())
	if !ok {
		writeErr(w, apperr.ErrForbidden)
		return
	}
	tag, e := h.pool.Exec(r.Context(), `UPDATE merchant_site SET status=$3,version=version+1,updated_at=now() WHERE id=$1 AND tenant_id=$2`, id, t, x.Status)
	if e != nil {
		writeErr(w, e)
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, apperr.ErrNotFound)
		return
	}
	writeJSON(w, map[string]any{"id": id.String(), "status": x.Status})
}
func (h *SitePolicyHandler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	q := `SELECT id::text,site_id::text,channel_id::text,business_scope,status,version,created_at::text,updated_at::text FROM site_payment_policy`
	args := []any{}
	if t, ok := tenantScopeOf(r.Context()); ok {
		q += ` WHERE tenant_id=$1`
		args = append(args, t)
	}
	q += ` ORDER BY updated_at DESC`
	rows, e := h.pool.Query(r.Context(), q, args...)
	if e != nil {
		writeErr(w, e)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, sid, cid, scope, status, ca, ua string
		var v int
		if e = rows.Scan(&id, &sid, &cid, &scope, &status, &v, &ca, &ua); e != nil {
			writeErr(w, e)
			return
		}
		items = append(items, map[string]any{"id": id, "site_id": sid, "channel_id": cid, "business_scope": scope, "status": status, "version": v, "created_at": ca, "updated_at": ua})
	}
	writeJSON(w, map[string]any{"items": items, "total": len(items)})
}
func (h *SitePolicyHandler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	var x struct {
		SiteID         string   `json:"site_id"`
		ChannelID      string   `json:"channel_id"`
		BusinessScope  string   `json:"business_scope"`
		Currencies     []string `json:"currencies"`
		Countries      []string `json:"countries"`
		PaymentMethods []string `json:"payment_methods"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	sid, e := uuid.Parse(x.SiteID)
	if e != nil {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	cid, e := uuid.Parse(x.ChannelID)
	if e != nil {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	t, ok := tenantScopeOf(r.Context())
	if !ok {
		writeErr(w, apperr.ErrForbidden)
		return
	}
	a, _ := json.Marshal(x.Currencies)
	co, _ := json.Marshal(x.Countries)
	pm, _ := json.Marshal(x.PaymentMethods)
	var id uuid.UUID
	e = h.pool.QueryRow(r.Context(), `INSERT INTO site_payment_policy(tenant_id,merchant_id,site_id,channel_id,business_scope,currencies,countries,payment_methods) SELECT $1,ms.merchant_id,$2,$3,$4,$5,$6,$7 FROM merchant_site ms JOIN tenant_acquirer_channel c ON c.id=$3 AND c.tenant_id=$1 AND c.merchant_id=ms.merchant_id WHERE ms.id=$2 AND ms.tenant_id=$1 RETURNING id`, t, sid, cid, x.BusinessScope, a, co, pm).Scan(&id)
	if e != nil {
		if e == pgx.ErrNoRows {
			writeErr(w, apperr.ErrForbidden)
			return
		}
		writeErr(w, e)
		return
	}
	writeJSON(w, map[string]any{"id": id.String(), "status": "PENDING"})
}
func (h *SitePolicyHandler) SetPolicyStatus(w http.ResponseWriter, r *http.Request) {
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	var x struct {
		Status string `json:"status"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || x.Status != "ACTIVE" && x.Status != "EXPIRED" && x.Status != "REVOKED" {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	t, ok := tenantScopeOf(r.Context())
	if !ok {
		writeErr(w, apperr.ErrForbidden)
		return
	}
	tag, e := h.pool.Exec(r.Context(), `UPDATE site_payment_policy SET status=$3,version=version+1,updated_at=now() WHERE id=$1 AND tenant_id=$2`, id, t, x.Status)
	if e != nil {
		writeErr(w, e)
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, apperr.ErrNotFound)
		return
	}
	writeJSON(w, map[string]any{"id": id.String(), "status": x.Status})
}
