package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/pkg/validator"
)

type Handler struct {
	pool      *pgxpool.Pool
	encryptor *crypto.Encryptor
}

func NewHandler(pool *pgxpool.Pool, encryptor *crypto.Encryptor) *Handler {
	return &Handler{pool: pool, encryptor: encryptor}
}

// ── Stats ──

func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats := map[string]interface{}{}

	var maeCount int
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM mae_configuration`).Scan(&maeCount)
	stats["mae_count"] = maeCount

	var payoutCount int
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM payout_request`).Scan(&payoutCount)
	stats["payout_count"] = payoutCount

	var outboxPending int
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM transactional_outbox WHERE status IN ('Pending','Failed')`).Scan(&outboxPending)
	stats["outbox_pending"] = outboxPending

	var webhookCount int
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM webhook_ingress_event`).Scan(&webhookCount)
	stats["webhook_count"] = webhookCount

	var sweepCount int
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sweep_transfer`).Scan(&sweepCount)
	stats["sweep_count"] = sweepCount

	var merchantCount int
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM merchant_treasury_ledger`).Scan(&merchantCount)
	stats["merchant_count"] = merchantCount

	rows, _ := h.pool.Query(ctx, `SELECT status, COUNT(*) FROM payout_request GROUP BY status`)
	var payoutByStatus []map[string]interface{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var s string
			var c int
			rows.Scan(&s, &c)
			payoutByStatus = append(payoutByStatus, map[string]interface{}{"status": s, "count": c})
		}
	}
	stats["payout_by_status"] = payoutByStatus

	rows2, _ := h.pool.Query(ctx, `SELECT status, COUNT(*) FROM transactional_outbox GROUP BY status`)
	var outboxByStatus []map[string]interface{}
	if rows2 != nil {
		defer rows2.Close()
		for rows2.Next() {
			var s string
			var c int
			rows2.Scan(&s, &c)
			outboxByStatus = append(outboxByStatus, map[string]interface{}{"status": s, "count": c})
		}
	}
	stats["outbox_by_status"] = outboxByStatus

	writeJSON(w, stats)
}

// ── MAE ──

func (h *Handler) ListMAEs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, mae_id, display_name, mae_type, status, version, daily_vmax_minor, routing_weight, created_at::text, updated_at::text
		 FROM mae_configuration ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, maeID, display, maeType, status, created, updated string
		var version, routingWeight int32
		var dailyVmax int64
		rows.Scan(&id, &maeID, &display, &maeType, &status, &version, &dailyVmax, &routingWeight, &created, &updated)
		items = append(items, map[string]interface{}{
			"id": id, "mae_id": maeID, "display_name": display, "mae_type": maeType,
			"status": status, "version": version, "daily_vmax_minor": dailyVmax,
			"routing_weight": routingWeight, "created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) GetMAE(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	maeID := chi.URLParam(r, "mae_id")

	rows, err := h.pool.Query(ctx,
		`SELECT id::text, mae_id, display_name, mae_type, tenant_scope::text, jurisdiction,
		 supported_currencies::text, api_base_url, webhook_proxy_url, risk_label,
		 settlement_bank_account_id::text, daily_vmax_minor, monthly_vmax_minor,
		 cmax_bps, card_testing_limit, routing_weight, cooldown_minutes,
		 testing_traffic_bps, testing_min_txn_count, testing_min_success_rate_bps,
		 testing_max_p95_latency_ms, status, status_reason, version,
		 created_at::text, updated_at::text,
		 CASE WHEN api_key_ciphertext IS NOT NULL AND length(api_key_ciphertext) > 0 THEN true ELSE false END as has_api_key,
		 CASE WHEN webhook_secret_ciphertext IS NOT NULL AND length(webhook_secret_ciphertext) > 0 THEN true ELSE false END as has_webhook_secret
		 FROM mae_configuration WHERE mae_id = $1`, maeID)
	if err != nil {
		writeErr(w, apperr.ErrNotFound.WithMessage("MAE not found"))
		return
	}
	defer rows.Close()

	if !rows.Next() {
		writeErr(w, apperr.ErrNotFound.WithMessage("MAE not found"))
		return
	}

	var id, maeIDStr, display, maeType, tenantScope, jurisdiction, currencies, apiBase, webhookProxy, riskLabel, settlementID string
	var dailyVmax, monthlyVmax int64
	var cmaxBps, cardTest, routing, cooldown, testBps, testMinTxn, testMinRate, testMaxP95, version int32
	var status, statusReason, created, updated string
	var hasAPIKey, hasWebhookSecret bool

	rows.Scan(&id, &maeIDStr, &display, &maeType, &tenantScope, &jurisdiction,
		&currencies, &apiBase, &webhookProxy, &riskLabel, &settlementID,
		&dailyVmax, &monthlyVmax, &cmaxBps, &cardTest, &routing, &cooldown,
		&testBps, &testMinTxn, &testMinRate, &testMaxP95, &status, &statusReason,
		&version, &created, &updated, &hasAPIKey, &hasWebhookSecret)

	writeJSON(w, map[string]interface{}{
		"id": id, "mae_id": maeIDStr, "display_name": display, "mae_type": maeType,
		"tenant_scope": tenantScope, "jurisdiction": jurisdiction,
		"supported_currencies": currencies, "api_base_url": apiBase,
		"has_api_key": hasAPIKey, "has_webhook_secret": hasWebhookSecret,
		"webhook_proxy_url": webhookProxy, "risk_label": riskLabel,
		"settlement_bank_account_id": settlementID, "daily_vmax_minor": dailyVmax,
		"monthly_vmax_minor": monthlyVmax, "cmax_bps": cmaxBps,
		"card_testing_limit": cardTest, "routing_weight": routing,
		"cooldown_minutes": cooldown, "testing_traffic_bps": testBps,
		"testing_min_txn_count": testMinTxn, "testing_min_success_rate_bps": testMinRate,
		"testing_max_p95_latency_ms": testMaxP95, "status": status,
		"status_reason": statusReason, "version": version,
		"created_at": created, "updated_at": updated,
	})
}

// ── Payout ──

func (h *Handler) ListPayouts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := queryInt(r, "limit", 50)
	status := r.URL.Query().Get("status")

	query := `SELECT id::text, merchant_id::text, amount::text, fiat_currency, target_asset, target_network, status, idempotency_key, version, created_at::text, updated_at::text FROM payout_request`
	args := []interface{}{}
	if status != "" {
		query += " WHERE status = $1"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(len(args)+1)
	args = append(args, limit)

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, merchantID, amount, fiat, asset, network, statusVal, idemKey, created, updated string
		var version int32
		rows.Scan(&id, &merchantID, &amount, &fiat, &asset, &network, &statusVal, &idemKey, &version, &created, &updated)
		items = append(items, map[string]interface{}{
			"id": id, "merchant_id": merchantID, "amount": amount,
			"fiat_currency": fiat, "target_asset": asset, "target_network": network,
			"status": statusVal, "idempotency_key": idemKey, "version": version,
			"created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) GetPayout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	rows, err := h.pool.Query(ctx,
		`SELECT merchant_id::text, payout_address_id::text, amount::text, fiat_currency, target_asset, target_network,
		 status, idempotency_key, provider_ref, lock_id, version, created_at::text, updated_at::text
		 FROM payout_request WHERE id = $1`, id)
	if err != nil {
		writeErr(w, apperr.ErrNotFound.WithMessage("payout not found"))
		return
	}
	defer rows.Close()

	if !rows.Next() {
		writeErr(w, apperr.ErrNotFound.WithMessage("payout not found"))
		return
	}

	var merchantID, addrID, amount, fiat, asset, network, statusVal, idemKey, providerRef, lockID, created, updated string
	var version int32
	rows.Scan(&merchantID, &addrID, &amount, &fiat, &asset, &network,
		&statusVal, &idemKey, &providerRef, &lockID, &version, &created, &updated)

	writeJSON(w, map[string]interface{}{
		"id": id.String(), "merchant_id": merchantID, "payout_address_id": addrID,
		"amount": amount, "fiat_currency": fiat, "target_asset": asset,
		"target_network": network, "status": statusVal, "idempotency_key": idemKey,
		"provider_ref": providerRef, "lock_id": lockID, "version": version,
		"created_at": created, "updated_at": updated,
	})
}

// ── Outbox ──

func (h *Handler) ListOutbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := queryInt(r, "limit", 50)
	status := r.URL.Query().Get("status")

	query := `SELECT id::text, aggregate_type, aggregate_id::text, event_type, idempotency_key, status, retry_count, created_at::text, updated_at::text FROM transactional_outbox`
	args := []interface{}{}
	if status != "" {
		query += " WHERE status = $1"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(len(args)+1)
	args = append(args, limit)

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, aggType, aggID, eventType, idemKey, statusVal, created, updated string
		var retryCount int32
		rows.Scan(&id, &aggType, &aggID, &eventType, &idemKey, &statusVal, &retryCount, &created, &updated)
		items = append(items, map[string]interface{}{
			"id": id, "aggregate_type": aggType, "aggregate_id": aggID,
			"event_type": eventType, "idempotency_key": idemKey,
			"status": statusVal, "retry_count": retryCount,
			"created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// ── Treasury ──

func (h *Handler) ListCalendars(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx, `SELECT id::text, calendar_name, jurisdiction, timezone, status, created_at::text FROM banking_calendar ORDER BY created_at`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, name, jur, tz, status, created string
		rows.Scan(&id, &name, &jur, &tz, &status, &created)
		items = append(items, map[string]interface{}{"id": id, "calendar_name": name, "jurisdiction": jur, "timezone": tz, "status": status, "created_at": created})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) ListSweepRules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, source_bank_account_id::text, target_bank_account_id::text, currency, rail, beneficiary_type,
		 trigger_threshold_minor, max_daily_sweep_count, sweep_mode, status, version, created_at::text
		 FROM sweep_rule ORDER BY created_at`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, src, tgt, cur, rail, ben, mode, status, created string
		var threshold int64
		var maxDaily, ver int32
		rows.Scan(&id, &src, &tgt, &cur, &rail, &ben, &threshold, &maxDaily, &mode, &status, &ver, &created)
		items = append(items, map[string]interface{}{
			"id": id, "source_account": src, "target_account": tgt, "currency": cur,
			"rail": rail, "beneficiary_type": ben, "trigger_threshold_minor": threshold,
			"max_daily_sweep_count": maxDaily, "sweep_mode": mode, "status": status,
			"version": ver, "created_at": created,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) ListSweepTransfers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := queryInt(r, "limit", 50)
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, sweep_rule_id::text, amount_minor, currency, rail, status, idempotency_key, bank_ref, version, created_at::text
		 FROM sweep_transfer ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, ruleID, cur, rail, status, idemKey, bankRef, created string
		var amount int64
		var ver int32
		rows.Scan(&id, &ruleID, &amount, &cur, &rail, &status, &idemKey, &bankRef, &ver, &created)
		items = append(items, map[string]interface{}{
			"id": id, "sweep_rule_id": ruleID, "amount_minor": amount, "currency": cur,
			"rail": rail, "status": status, "idempotency_key": idemKey,
			"bank_ref": bankRef, "version": ver, "created_at": created,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// ── Webhook ──

func (h *Handler) ListWebhookEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := queryInt(r, "limit", 50)
	providerType := r.URL.Query().Get("provider_type")

	query := `SELECT id::text, provider_type, provider_id, provider_event_id, event_type, idempotency_key, status, created_at::text FROM webhook_ingress_event`
	args := []interface{}{}
	if providerType != "" {
		query += " WHERE provider_type = $1"
		args = append(args, providerType)
	}
	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(len(args)+1)
	args = append(args, limit)

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, pt, pid, peid, et, ik, status, created string
		rows.Scan(&id, &pt, &pid, &peid, &et, &ik, &status, &created)
		items = append(items, map[string]interface{}{
			"id": id, "provider_type": pt, "provider_id": pid,
			"provider_event_id": peid, "event_type": et,
			"idempotency_key": ik, "status": status, "created_at": created,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) GetWebhookEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	rows, err := h.pool.Query(ctx,
		`SELECT provider_type, provider_id, provider_event_id, event_type, idempotency_key,
		 payload_hash, status, error_message, created_at::text, updated_at::text
		 FROM webhook_ingress_event WHERE id = $1`, id)
	if err != nil {
		writeErr(w, apperr.ErrNotFound.WithMessage("event not found"))
		return
	}
	defer rows.Close()

	if !rows.Next() {
		writeErr(w, apperr.ErrNotFound.WithMessage("event not found"))
		return
	}

	var pt, pid, peid, et, ik, ph, status, errMsg, created, updated string
	rows.Scan(&pt, &pid, &peid, &et, &ik, &ph, &status, &errMsg, &created, &updated)

	writeJSON(w, map[string]interface{}{
		"id": id.String(), "provider_type": pt, "provider_id": pid,
		"provider_event_id": peid, "event_type": et,
		"idempotency_key": ik, "payload_hash": ph,
		"status": status, "error_message": errMsg,
		"created_at": created, "updated_at": updated,
	})
}

// ── Ledger ──

func (h *Handler) ListLedgerTransactions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := queryInt(r, "limit", 50)
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, idempotency_key, transaction_type, aggregate_type, description, created_at::text
		 FROM ledger_transaction ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, ik, tt, at, desc, created string
		rows.Scan(&id, &ik, &tt, &at, &desc, &created)
		items = append(items, map[string]interface{}{
			"id": id, "idempotency_key": ik, "transaction_type": tt,
			"aggregate_type": at, "description": desc, "created_at": created,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) ListLedgerEntries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	txnIDStr := r.URL.Query().Get("transaction_id")
	if txnIDStr == "" {
		writeJSON(w, map[string]interface{}{"items": []interface{}{}, "total": 0})
		return
	}
	txnID, err := uuid.Parse(txnIDStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid transaction_id"))
		return
	}

	rows, err := h.pool.Query(ctx,
		`SELECT id::text, transaction_id::text, account_id::text, direction, amount::text, created_at::text
		 FROM ledger_entry WHERE transaction_id = $1 ORDER BY created_at`, txnID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, txnID2, accID, dir, amt, created string
		rows.Scan(&id, &txnID2, &accID, &dir, &amt, &created)
		items = append(items, map[string]interface{}{
			"id": id, "transaction_id": txnID2, "account_id": accID,
			"direction": dir, "amount": amt, "created_at": created,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// ── Merchants ──

func (h *Handler) ListMerchants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, merchant_id::text, currency, settled_balance::text, frozen_payout_balance::text,
		 reserve_balance::text, dispute_hold_balance::text, version, updated_at::text
		 FROM merchant_treasury_ledger ORDER BY updated_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, mid, cur, settled, frozen, reserve, dispute, updated string
		var ver int32
		rows.Scan(&id, &mid, &cur, &settled, &frozen, &reserve, &dispute, &ver, &updated)
		items = append(items, map[string]interface{}{
			"id": id, "merchant_id": mid, "currency": cur,
			"settled_balance": settled, "frozen_payout_balance": frozen,
			"reserve_balance": reserve, "dispute_hold_balance": dispute,
			"version": ver, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// ── helpers ──

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	if appErr, ok := apperr.AsAPIError(err); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(appErr.HTTP)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"code": appErr.Code, "message": appErr.Message},
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(500)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"code": "INTERNAL", "message": err.Error()},
	})
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// ── R3-3: 数据录入端点 ──

// CreateBankCalendar 创建银行日历
func (h *Handler) CreateBankCalendar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CalendarName string `json:"calendar_name"`
		Jurisdiction string `json:"jurisdiction"`
		Timezone     string `json:"timezone"`
		BusinessDays string `json:"business_days"`
		Holidays     string `json:"holidays"`
		CutOffMatrix string `json:"cut_off_matrix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	var id uuid.UUID
	err := h.pool.QueryRow(ctx,
		`INSERT INTO banking_calendar (calendar_name, jurisdiction, timezone, business_days, holidays, cut_off_matrix, status)
		 VALUES ($1, $2, $3, $4, $5, $6, 'Active') RETURNING id`,
		req.CalendarName, req.Jurisdiction, req.Timezone,
		[]byte(req.BusinessDays), []byte(req.Holidays), []byte(req.CutOffMatrix),
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

// CreateMAEConfig 创建 MAE 配置
func (h *Handler) CreateMAEConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MAEID                   string   `json:"mae_id"`
		DisplayName             string   `json:"display_name"`
		MAEType                 string   `json:"mae_type"`
		TenantScope             []string `json:"tenant_scope"`
		Jurisdiction            string   `json:"jurisdiction"`
		SupportedCurrencies     []string `json:"supported_currencies"`
		APIBaseURL              string   `json:"api_base_url"`
		SettlementBankAccountID string   `json:"settlement_bank_account_id"`
		APIKey                  string   `json:"api_key"`
		WebhookSecret           string   `json:"webhook_secret"`
		DailyVmaxMinor          int64    `json:"daily_vmax_minor"`
		MonthlyVmaxMinor        int64    `json:"monthly_vmax_minor"`
		RoutingWeight           int32    `json:"routing_weight"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	settlementBankID, err := uuid.Parse(req.SettlementBankAccountID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid settlement_bank_account_id"))
		return
	}

	ctx := r.Context()

	// R3-3 复修：敏感字段必须经 Encryptor 加密后再落库（之前「简化处理」直接存明文是安全基线倒退）。
	// 缺 encryptor 时 fail-closed：拒绝写入而非退化为明文。
	if h.encryptor == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("encryptor not configured; refusing to store API key in plaintext"))
		return
	}
	apiKeyEnc, err := h.encryptor.Encrypt(ctx, []byte(req.APIKey), crypto.Meta{
		TenantID: req.MAEID, RecordID: "mae_configuration", FieldName: "api_key",
	})
	if err != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("encrypt api_key: "+err.Error()))
		return
	}
	webhookSecretEnc, err := h.encryptor.Encrypt(ctx, []byte(req.WebhookSecret), crypto.Meta{
		TenantID: req.MAEID, RecordID: "mae_configuration", FieldName: "webhook_secret",
	})
	if err != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("encrypt webhook_secret: "+err.Error()))
		return
	}

	tenantScopeJSON, _ := json.Marshal(req.TenantScope)
	currenciesJSON, _ := json.Marshal(req.SupportedCurrencies)

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO mae_configuration (
			mae_id, display_name, mae_type, tenant_scope, jurisdiction,
			supported_currencies, api_base_url, settlement_bank_account_id,
			api_key_ciphertext, webhook_secret_ciphertext, daily_vmax_minor,
			monthly_vmax_minor, routing_weight, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'Draft') RETURNING id`,
		req.MAEID, req.DisplayName, req.MAEType, tenantScopeJSON, req.Jurisdiction,
		currenciesJSON, req.APIBaseURL, settlementBankID,
		apiKeyEnc, webhookSecretEnc, req.DailyVmaxMinor,
		req.MonthlyVmaxMinor, req.RoutingWeight,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

// CreateSweepRule 创建归集规则
func (h *Handler) CreateSweepRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceBankAccountID string `json:"source_bank_account_id"`
		TargetBankAccountID string `json:"target_bank_account_id"`
		Currency            string `json:"currency"`
		Rail                string `json:"rail"`
		BeneficiaryType     string `json:"beneficiary_type"`
		TriggerThreshold    int64  `json:"trigger_threshold_minor"`
		SweepRatioBps       int32  `json:"sweep_ratio_bps"`
		MaxSingleSweep      int64  `json:"max_single_sweep_minor"`
		MinResidual         int64  `json:"min_residual_minor"`
		MaxDailySweepCount  int32  `json:"max_daily_sweep_count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	sourceID, _ := uuid.Parse(req.SourceBankAccountID)
	targetID, _ := uuid.Parse(req.TargetBankAccountID)

	var id uuid.UUID
	err := h.pool.QueryRow(ctx,
		`INSERT INTO sweep_rule (
			source_bank_account_id, target_bank_account_id, currency, rail,
			beneficiary_type, trigger_threshold_minor, sweep_ratio_bps,
			max_single_sweep_minor, min_residual_minor, max_daily_sweep_count, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'Active') RETURNING id`,
		sourceID, targetID, req.Currency, req.Rail,
		req.BeneficiaryType, req.TriggerThreshold, req.SweepRatioBps,
		req.MaxSingleSweep, req.MinResidual, req.MaxDailySweepCount,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

// CreateWebhookProxy 创建 Webhook 代理
func (h *Handler) CreateWebhookProxy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderType  string   `json:"provider_type"`
		ProviderID    string   `json:"provider_id"`
		WebhookURL    string   `json:"webhook_url"`
		SignatureAlgo string   `json:"signature_algo"`
		Secret        string   `json:"secret"`
		IPAllowlist   []string `json:"ip_allowlist"`
		RateLimit     int32    `json:"rate_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()

	// R3-3 复修：webhook secret 必须经 Encryptor 加密后存 secret_ciphertext（与 webhook handler
	// 验签前的 Decrypt 配对），缺 encryptor 时 fail-closed 拒写。
	if h.encryptor == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("encryptor not configured; refusing to store webhook secret in plaintext"))
		return
	}
	secretEnc, err := h.encryptor.Encrypt(ctx, []byte(req.Secret), crypto.Meta{
		TenantID: req.ProviderID, RecordID: "webhook_receiver_proxy", FieldName: "secret",
	})
	if err != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("encrypt secret: "+err.Error()))
		return
	}

	// ip_allowlist 列是 jsonb：序列化为 JSON 数组（空数组 = "[]" 满足 NOT NULL DEFAULT '[]'）。
	ipAllowlistJSON, _ := json.Marshal(req.IPAllowlist)

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO webhook_receiver_proxy (
			provider_type, provider_id, webhook_url, signature_algo,
			secret_ciphertext, ip_allowlist, rate_limit, status
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, 'Active') RETURNING id`,
		req.ProviderType, req.ProviderID, req.WebhookURL, req.SignatureAlgo,
		secretEnc, string(ipAllowlistJSON), req.RateLimit,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

// CreateMerchant 创建商户（在 treasury ledger 中注册）
func (h *Handler) CreateMerchant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MerchantID string `json:"merchant_id"`
		Currency   string `json:"currency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	merchantID, _ := uuid.Parse(req.MerchantID)

	_, err := h.pool.Exec(ctx,
		`INSERT INTO merchant_treasury_ledger (merchant_id, currency)
		 VALUES ($1, $2) ON CONFLICT (merchant_id, currency) DO NOTHING`,
		merchantID, req.Currency)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"merchant_id": req.MerchantID, "status": "created"})
}

// CreateBankAccount 创建银行账户（表 bank_account_balance，第 7 张 B 类表）。
// bank_account_id 是银行账户的稳定标识（由业务侧分配）；accumulated_minor 为该 (bank_account, currency)
// 起始余额，单位为最小币种单位（minor），用 int64 不走 decimal（与 schema bigint 一致）。
func (h *Handler) CreateBankAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BankAccountID    string `json:"bank_account_id"`
		Currency         string `json:"currency"`
		AccumulatedMinor int64  `json:"accumulated_minor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	bankAccountID, err := uuid.Parse(req.BankAccountID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid bank_account_id"))
		return
	}
	if req.Currency == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("currency required"))
		return
	}
	if req.AccumulatedMinor < 0 {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("accumulated_minor must be >= 0"))
		return
	}

	ctx := r.Context()
	// UPSERT：若 (bank_account_id, currency) 已存在则覆盖余额（数据修正/对账场景），保持幂等。
	_, err = h.pool.Exec(ctx,
		`INSERT INTO bank_account_balance (bank_account_id, currency, accumulated_minor, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (bank_account_id, currency)
		 DO UPDATE SET accumulated_minor = EXCLUDED.accumulated_minor, updated_at = now()`,
		bankAccountID, req.Currency, req.AccumulatedMinor)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"bank_account_id":   bankAccountID.String(),
		"currency":          req.Currency,
		"accumulated_minor": req.AccumulatedMinor,
		"status":            "created",
	})
}

// CreatePayoutAddress 创建出金地址
func (h *Handler) CreatePayoutAddress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MerchantID    string `json:"merchant_id"`
		TargetAsset   string `json:"target_asset"`
		TargetNetwork string `json:"target_network"`
		FormatType    string `json:"address_format_type"`
		TargetAddress string `json:"target_address"`
		AddressLabel  string `json:"address_label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	merchantID, _ := uuid.Parse(req.MerchantID)

	// R3-3 复修：出金地址经 domain.CleanAddress 做格式校验/哈希/掩码，
	// 再经 Encryptor 加密后存 target_address_enc（之前直接存明文 + 把地址当 hash 是双重错误）。
	if h.encryptor == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("encryptor not configured; refusing to store payout address in plaintext"))
		return
	}
	cleaned := domain.CleanAddress(req.TargetAddress, validator.Network(req.TargetNetwork))
	if !cleaned.IsValid {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage(cleaned.ValidationError))
		return
	}
	addressEnc, err := h.encryptor.Encrypt(ctx, []byte(cleaned.TargetAddress), crypto.Meta{
		TenantID: merchantID.String(), RecordID: "payout_address", FieldName: "target_address",
	})
	if err != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("encrypt address: "+err.Error()))
		return
	}

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO merchant_payout_address (
			merchant_id, target_asset, target_network, address_format_type,
			target_address_enc, target_address_hash, target_address_masked,
			address_label, screening_status, whitelist_status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'Pending', 'Pending') RETURNING id`,
		merchantID, req.TargetAsset, req.TargetNetwork, req.FormatType,
		addressEnc, cleaned.AddressHash, cleaned.TargetAddressMasked, req.AddressLabel,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}
