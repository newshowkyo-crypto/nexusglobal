package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

func (h *Handler) WorkbenchSummary(w http.ResponseWriter, r *http.Request) {
	tenant, ok := requireAdminTenant(w, r)
	if !ok {
		return
	}
	// Operations reports use Beijing business time. Capture timestamps remain in UTC.
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var metrics json.RawMessage
	err := h.pool.QueryRow(r.Context(), `
		WITH today AS (
			SELECT status, created_at, captured_at FROM payment_attempts
			WHERE tenant_id=$1 AND created_at >= $2 AND created_at < $2::timestamptz + interval '24 hours'
		), hourly AS (
			SELECT extract(hour FROM created_at AT TIME ZONE 'Asia/Shanghai') AS hour,
			       count(*) AS attempts, count(*) FILTER (WHERE captured_at IS NOT NULL OR
			       status IN ('CAPTURED','SETTLED','REFUNDED','PARTIALLY_REFUNDED','CHARGED_BACK')) AS captured,
			       count(*) FILTER (WHERE status='FAILED') AS failed
			FROM today GROUP BY 1
		)
		SELECT jsonb_build_object(
			'attempts', COALESCE((SELECT sum(attempts) FROM hourly),0),
			'captured', COALESCE((SELECT sum(captured) FROM hourly),0),
			'hours', COALESCE((SELECT jsonb_agg(to_jsonb(hourly) ORDER BY hour) FROM hourly),'[]'::jsonb),
			'unknown_payments', (SELECT count(*) FROM payment_attempts WHERE tenant_id=$1 AND status='UNKNOWN_EXTERNAL_STATE'),
			'open_refunds', (SELECT count(*) FROM refund_request WHERE tenant_id=$1 AND status IN ('PENDING','PROCESSING','UNKNOWN')),
			'pending_approvals', (SELECT count(*) FROM admin_approval_requests WHERE tenant_id=$1 AND status='PENDING'),
			'webhook_failures', (SELECT count(*) FROM unified_webhook_events w JOIN payment_attempts a ON a.id=w.payment_attempt_id
				WHERE a.tenant_id=$1 AND w.created_at >= $2 AND w.created_at < $2::timestamptz + interval '24 hours' AND w.status='FAILED'),
			'available_amounts', COALESCE((
				SELECT jsonb_agg(to_jsonb(amounts) ORDER BY currency) FROM (
					SELECT l.currency, sum(l.available_payout_balance)::text AS amount FROM merchant_treasury_ledger l
					JOIN corporate_entity e ON e.id=l.merchant_id WHERE e.tenant_id=$1 GROUP BY l.currency
				) amounts), '[]'::jsonb),
			'pending_amounts', COALESCE((
				SELECT jsonb_agg(to_jsonb(amounts) ORDER BY currency) FROM (
					SELECT currency, sum(amount_minor)::text AS amount FROM payment_attempts
					WHERE tenant_id=$1 AND captured_at IS NOT NULL AND settled_at IS NULL AND status='CAPTURED'
					GROUP BY currency) amounts), '[]'::jsonb)
		)`, tenant, start).Scan(&metrics)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"business_date": start.Format("2006-01-02"), "metrics": metrics})
}

// These projections deliberately omit customer data, credential material, and raw upstream responses.
// Amounts are sent as text to preserve values beyond JavaScript's safe integer range.
var workbenchSources = map[string]string{
	"orders": `SELECT a.id, a.created_at, a.updated_at, a.merchant_order_id AS reference, e.name AS merchant,
		a.status, a.amount_minor::text AS amount, a.currency, o.site_id AS site,
		a.entry_channel, a.captured_at, a.settled_at
		FROM payment_attempts a JOIN corporate_entity e ON e.id=a.merchant_id AND e.tenant_id=a.tenant_id
		LEFT JOIN checkout_orders o ON o.id=a.order_id AND o.tenant_id=a.tenant_id AND o.merchant_id=a.merchant_id
		WHERE a.tenant_id=$1`,
	"refunds": `SELECT r.id, r.created_at, r.updated_at, r.merchant_refund_id AS reference, e.name AS merchant,
		r.status, r.amount_minor::text AS amount, r.currency, a.merchant_order_id AS order_reference, r.succeeded_at
		FROM refund_request r JOIN corporate_entity e ON e.id=r.merchant_id AND e.tenant_id=r.tenant_id
		JOIN payment_attempts a ON a.id=r.payment_attempt_id AND a.tenant_id=r.tenant_id AND a.merchant_id=r.merchant_id
		WHERE r.tenant_id=$1`,
	"settlements": `SELECT b.id, b.created_at, b.business_date::text AS reference, e.name AS merchant,
		b.status, b.total_amount_minor::text AS amount, b.currency, b.item_count, b.settled_at
		FROM settlement_batch b JOIN corporate_entity e ON e.id=b.merchant_id AND e.tenant_id=b.tenant_id
		WHERE b.tenant_id=$1`,
	"payouts": `SELECT p.id, p.created_at, p.updated_at, p.id::text AS reference, e.name AS merchant,
		p.status, p.amount::text AS amount, p.fiat_currency AS currency, p.target_asset, p.target_network
		FROM payout_request p JOIN corporate_entity e ON e.id=p.merchant_id WHERE e.tenant_id=$1`,
	"authorizations": `SELECT COALESCE(p.id,s.id) AS id, COALESCE(p.created_at,s.created_at) AS created_at,
		s.site_code AS reference, e.name AS merchant, s.display_name AS site, s.origin, s.status AS site_status,
		CASE WHEN p.id IS NULL THEN 'UNCONFIGURED'
		     WHEN p.status='ACTIVE' AND p.expires_at<=now() THEN 'EXPIRED'
		     WHEN p.status='ACTIVE' AND p.effective_at>now() THEN 'SCHEDULED'
		     ELSE p.status END AS status,
		COALESCE(NULLIF(c.channel_name,''),c.channel_code) AS channel,
		CASE WHEN c.id IS NOT NULL THEN CASE WHEN c.is_active THEN 'ACTIVE' ELSE 'INACTIVE' END END AS channel_status,
		pp.provider_code AS provider, p.business_scope
		FROM merchant_site s JOIN corporate_entity e ON e.id=s.merchant_id AND e.tenant_id=s.tenant_id
		LEFT JOIN site_payment_policy p ON p.site_id=s.id AND p.tenant_id=s.tenant_id AND p.merchant_id=s.merchant_id
		LEFT JOIN tenant_acquirer_channel c ON c.id=p.channel_id AND c.tenant_id=s.tenant_id AND c.merchant_id=s.merchant_id
		LEFT JOIN payment_provider pp ON pp.id=c.provider_id
		WHERE s.tenant_id=$1`,
}

const workbenchPageSize = 25

func (h *Handler) WorkbenchRecords(w http.ResponseWriter, r *http.Request) {
	tenant, ok := requireAdminTenant(w, r)
	if !ok {
		return
	}
	source, ok := workbenchSources[chi.URLParam(r, "resource")]
	if !ok {
		writeErr(w, apperr.ErrNotFound)
		return
	}
	params := r.URL.Query()
	filter, status := strings.TrimSpace(params.Get("q")), strings.TrimSpace(params.Get("status"))
	if len(filter) > 256 || len(status) > 32 {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	var before *time.Time
	var beforeID *uuid.UUID
	if cursor := params.Get("before"); cursor != "" {
		date, id, found := strings.Cut(cursor, "/")
		d, dateErr := time.Parse(time.RFC3339Nano, date)
		u, uuidErr := uuid.Parse(id)
		if !found || dateErr != nil || uuidErr != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid pagination cursor"))
			return
		}
		before, beforeID = &d, &u
	}
	rows, err := h.pool.Query(r.Context(), `SELECT row.id, row.created_at, to_jsonb(row)-'id'
		FROM (`+source+`) row
		WHERE ($2='' OR strpos(lower(row.reference), lower($2))>0 OR strpos(lower(row.merchant), lower($2))>0)
		  AND ($3='' OR row.status=$3)
		  AND ($4::timestamptz IS NULL OR (row.created_at,row.id)<($4,$5::uuid))
		ORDER BY row.created_at DESC,row.id DESC LIMIT $6`, tenant, filter, status, before, beforeID, workbenchPageSize+1)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	items := make([]json.RawMessage, 0, workbenchPageSize)
	var cursor, next string
	for rows.Next() {
		var id uuid.UUID
		var created time.Time
		var item json.RawMessage
		if err := rows.Scan(&id, &created, &item); err != nil {
			writeErr(w, err)
			return
		}
		if len(items) == workbenchPageSize {
			next = cursor
			break
		}
		items = append(items, item)
		cursor = created.UTC().Format(time.RFC3339Nano) + "/" + id.String()
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"items": items, "next": next})
}

// FeeSummary keeps decimal amounts in PostgreSQL and groups currencies separately.
func (h *Handler) FeeSummary(w http.ResponseWriter, r *http.Request) {
	query := `SELECT f.currency, f.fee_type, COUNT(*), SUM(f.amount)::text
		FROM settlement_fee_allocation f JOIN corporate_entity ce ON ce.id = f.merchant_id`
	args := []interface{}{}
	if tenant, scoped := tenantScopeOf(r.Context()); scoped {
		query += ` WHERE ce.tenant_id = $1`
		args = append(args, tenant)
	}
	query += ` GROUP BY f.currency, f.fee_type ORDER BY f.currency, f.fee_type`
	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]interface{}{}
	for rows.Next() {
		var currency, feeType, amount string
		var count int64
		if err := rows.Scan(&currency, &feeType, &count, &amount); err != nil {
			writeErr(w, err)
			return
		}
		items = append(items, map[string]interface{}{"currency": currency, "fee_type": feeType, "count": count, "amount": amount})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"items": items})
}
