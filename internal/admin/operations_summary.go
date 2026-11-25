package admin

import (
	"context"
	"encoding/csv"
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
	start := workbenchDay()
	metrics, err := h.workbenchMetrics(r.Context(), tenant, start)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"business_date": start.Format("2006-01-02"), "metrics": metrics})
}

func workbenchDay() time.Time {
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

func (h *Handler) workbenchMetrics(ctx context.Context, tenant uuid.UUID, start time.Time) (json.RawMessage, error) {
	var metrics json.RawMessage
	err := h.pool.QueryRow(ctx, `
		WITH captured_flow AS (
			SELECT (captured_at AT TIME ZONE 'Asia/Shanghai')::date AS day, currency,
			       sum(amount_minor)::text AS amount_minor, count(*) AS count
			FROM payment_attempts
			WHERE tenant_id=$1 AND captured_at >= $3
			  AND captured_at < $2::timestamptz + interval '24 hours'
			GROUP BY 1,2
		), paid_flow AS (
			SELECT (txn.created_at AT TIME ZONE 'Asia/Shanghai')::date AS day,
			       payout.fiat_currency AS currency, sum(payout.amount)::text AS amount
			FROM ledger_transaction txn
			JOIN payout_request payout ON payout.id=txn.aggregate_id
			JOIN corporate_entity merchant ON merchant.id=payout.merchant_id AND merchant.tenant_id=$1
			WHERE txn.aggregate_type='payout_request' AND txn.transaction_type='PAYOUT_CONFIRM'
			  AND txn.created_at >= $3
			  AND txn.created_at < $2::timestamptz + interval '24 hours'
			GROUP BY 1,2
		), today AS (
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
			'open_refunds', (SELECT count(*) FROM refund_request WHERE tenant_id=$1 AND status = ANY($4::text[])),
			'pending_approvals', (SELECT count(*) FROM admin_approval_requests WHERE tenant_id=$1 AND status='PENDING'),
			'settled_batches', (SELECT count(*) FROM settlement_batch WHERE tenant_id=$1 AND status='SETTLED'
				AND business_date=($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date),
			'reconciliation_exceptions', (SELECT count(*) FROM bank_collection_reconciliation_log
				WHERE tenant_id=$1 AND NOT matched AND reconciliation_date=($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date),
			'captured_flow', COALESCE((SELECT jsonb_agg(to_jsonb(captured_flow) ORDER BY day,currency) FROM captured_flow),'[]'::jsonb),
			'paid_flow', COALESCE((SELECT jsonb_agg(to_jsonb(paid_flow) ORDER BY day,currency) FROM paid_flow),'[]'::jsonb),
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
		)`, tenant, start, start.AddDate(0, 0, -89), openRefundStatuses).Scan(&metrics)
	return metrics, err
}

func (h *Handler) WorkbenchExport(w http.ResponseWriter, r *http.Request) {
	tenant, ok := requireAdminTenant(w, r)
	if !ok {
		return
	}
	start := workbenchDay()
	metrics, err := h.workbenchMetrics(r.Context(), tenant, start)
	if err != nil {
		writeErr(w, err)
		return
	}
	var flow struct {
		Captured []struct {
			Day, Currency string
			Amount        string `json:"amount_minor"`
		} `json:"captured_flow"`
		Paid []struct{ Day, Currency, Amount string } `json:"paid_flow"`
	}
	if err := json.Unmarshal(metrics, &flow); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="nexusglobal-funds-`+start.Format("2006-01-02")+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte("\ufeff"))
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"日期 (UTC+8)", "币种", "方向", "金额（最小货币单位）"}); err != nil {
		return
	}
	for _, item := range flow.Captured {
		if err := writer.Write([]string{item.Day, csvText(item.Currency), "已收款", item.Amount}); err != nil {
			return
		}
	}
	for _, item := range flow.Paid {
		if err := writer.Write([]string{item.Day, csvText(item.Currency), "已确认出款", item.Amount}); err != nil {
			return
		}
	}
	writer.Flush()
}

func csvText(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
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
		b.status, b.total_amount_minor::text AS amount, b.currency, b.item_count, b.settled_at, b.id::text AS batch_id
		FROM settlement_batch b JOIN corporate_entity e ON e.id=b.merchant_id AND e.tenant_id=b.tenant_id
		WHERE b.tenant_id=$1`,
	"reconciliation-exceptions": `SELECT log.id, log.created_at, log.reconciliation_date::text AS reference,
		COALESCE(merchant.name,'') AS merchant, log.mismatch_type AS status,
		log.expected_amount::text AS amount, intent.asset_code AS currency,
		log.id::text AS batch_id
		FROM bank_collection_reconciliation_log log
		JOIN bank_collection_intent intent ON intent.id=log.intent_id AND intent.tenant_id=log.tenant_id
		LEFT JOIN corporate_entity merchant ON merchant.id=intent.merchant_id AND merchant.tenant_id=log.tenant_id
		WHERE log.tenant_id=$1 AND NOT log.matched`,
	"unsettled-orders": `SELECT a.id, a.created_at, a.merchant_order_id AS reference, e.name AS merchant,
		a.status, a.amount_minor::text AS amount, a.currency, a.id::text AS batch_id
		FROM payment_attempts a JOIN corporate_entity e ON e.id=a.merchant_id AND e.tenant_id=a.tenant_id
		WHERE a.tenant_id=$1 AND a.captured_at IS NOT NULL AND a.settled_at IS NULL AND a.status='CAPTURED'`,
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

// openRefundStatuses 是「退款待处理或待确认」的唯一口径：风险指标与退款记录下钻过滤共用，避免两处各维护一份状态列表。
var openRefundStatuses = []string{"PENDING", "PROCESSING", "UNKNOWN"}

// normalizeStatusFilter 将逗号分隔的状态过滤逐段 trim 后重组；完全空的输入表示不筛选，仅含空白或出现空 token 视为非法参数。
func normalizeStatusFilter(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", raw == ""
	}
	parts := strings.Split(trimmed, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			return "", false
		}
		values = append(values, value)
	}
	return strings.Join(values, ","), true
}

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
	filter, rawStatus := strings.TrimSpace(params.Get("q")), params.Get("status")
	if len(filter) > 256 || len(strings.TrimSpace(rawStatus)) > 128 {
		writeErr(w, apperr.ErrInvalidArgument)
		return
	}
	status, ok := normalizeStatusFilter(rawStatus)
	if !ok {
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
		WHERE ($2='' OR strpos(lower(row.reference), lower($2))>0 OR strpos(lower(row.merchant), lower($2))>0
			OR strpos(row.id::text, lower($2))>0 OR strpos(lower(to_jsonb(row)->>'entry_channel'), lower($2))>0)
		  AND ($3='' OR row.status = ANY(string_to_array($3, ',')))
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
