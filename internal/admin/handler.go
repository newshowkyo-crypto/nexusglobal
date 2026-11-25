package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/pkg/validator"
)

type Handler struct {
	pool      *pgxpool.Pool
	encryptor *crypto.Encryptor
	svc       *ApprovalService // P0-9: 敏感操作委托给审批服务
}

func NewHandler(pool *pgxpool.Pool, encryptor *crypto.Encryptor) *Handler {
	return &Handler{pool: pool, encryptor: encryptor, svc: NewApprovalService(pool, nil)}
}

// ── Stats ──

func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats := map[string]interface{}{}

	// UI-010: 总览卡片所需计数与 dashboard JS 渲染键对齐
	var adapterProviderCount, adapterChannelCount, entityCount, bridgeProfileCount, walletPolicyCount int
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM adapter_provider`).Scan(&adapterProviderCount)
	stats["adapter_provider_count"] = adapterProviderCount
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM adapter_channel`).Scan(&adapterChannelCount)
	stats["adapter_channel_count"] = adapterChannelCount
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM corporate_entity`).Scan(&entityCount)
	stats["entity_count"] = entityCount
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM bridge_profile`).Scan(&bridgeProfileCount)
	stats["bridge_profile_count"] = bridgeProfileCount
	h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM transit_wallet_policy`).Scan(&walletPolicyCount)
	stats["wallet_policy_count"] = walletPolicyCount

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
	tenant, ok := requireAdminTenant(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	limit := queryLimit(r, 50)
	status := r.URL.Query().Get("status")

	query := `SELECT id::text, merchant_id::text, amount::text, fiat_currency, target_asset, target_network, status, idempotency_key, version, created_at::text, updated_at::text
		FROM payout_request p WHERE EXISTS(SELECT 1 FROM corporate_entity e WHERE e.id=p.merchant_id AND e.tenant_id=$1)`
	args := []interface{}{tenant}
	if status != "" {
		query += " AND status = $2"
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
		if err := rows.Scan(&id, &merchantID, &amount, &fiat, &asset, &network, &statusVal, &idemKey, &version, &created, &updated); err != nil {
			writeErr(w, err)
			return
		}
		items = append(items, map[string]interface{}{
			"id": id, "merchant_id": merchantID, "amount": amount,
			"fiat_currency": fiat, "target_asset": asset, "target_network": network,
			"status": statusVal, "idempotency_key": idemKey, "version": version,
			"created_at": created, "updated_at": updated,
		})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) GetPayout(w http.ResponseWriter, r *http.Request) {
	tenant, ok := requireAdminTenant(w, r)
	if !ok {
		return
	}
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
		 FROM payout_request p WHERE id = $1 AND EXISTS(SELECT 1 FROM corporate_entity e WHERE e.id=p.merchant_id AND e.tenant_id=$2)`, id, tenant)
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
	if err := rows.Scan(&merchantID, &addrID, &amount, &fiat, &asset, &network,
		&statusVal, &idemKey, &providerRef, &lockID, &version, &created, &updated); err != nil {
		writeErr(w, err)
		return
	}

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
	limit := queryLimit(r, 50)
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

func (h *Handler) GetOutbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	var item struct {
		ID            string
		AggregateType string
		AggregateID   string
		EventType     string
		IDKey         string
		Payload       []byte
		Status        string
		RetryCount    int32
		LastError     *string
		CreatedAt     string
		UpdatedAt     string
	}
	err := h.pool.QueryRow(ctx,
		`SELECT id::text, aggregate_type, aggregate_id::text, event_type, idempotency_key,
		        payload, status, retry_count, last_error, created_at::text, updated_at::text
		 FROM transactional_outbox WHERE id = $1`,
		id,
	).Scan(&item.ID, &item.AggregateType, &item.AggregateID, &item.EventType,
		&item.IDKey, &item.Payload, &item.Status, &item.RetryCount,
		&item.LastError, &item.CreatedAt, &item.UpdatedAt)

	if err != nil {
		writeErr(w, err)
		return
	}

	lastError := ""
	if item.LastError != nil {
		lastError = *item.LastError
	}

	var payloadJSON interface{}
	if len(item.Payload) > 0 {
		_ = json.Unmarshal(item.Payload, &payloadJSON)
	}

	writeJSON(w, map[string]interface{}{
		"id":              item.ID,
		"aggregate_type":  item.AggregateType,
		"aggregate_id":    item.AggregateID,
		"event_type":      item.EventType,
		"idempotency_key": item.IDKey,
		"payload":         payloadJSON,
		"status":          item.Status,
		"retry_count":     item.RetryCount,
		"last_error":      lastError,
		"created_at":      item.CreatedAt,
		"updated_at":      item.UpdatedAt,
	})
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
	limit := queryLimit(r, 50)
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
	limit := queryLimit(r, 50)
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
	limit := queryLimit(r, 50)
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

// createPendingApproval 创建 PENDING 审批请求（P0-9: 敏感操作不直接修改业务表）
//
// 身份从已验证 Context 获取（RED-15），拒绝 body 中伪造的 requester。
// 返回创建的审批 ID。
func (h *Handler) createPendingApproval(ctx context.Context, actionType, targetType string, targetID *string, payload map[string]interface{}, reason string) (string, error) {
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil {
		return "", apperr.ErrForbidden.WithMessage("admin identity not found in authenticated context")
	}
	requester := identity.Username
	if requester == "" {
		requester = identity.Subject
	}
	if requester == "" || requester == "unknown" {
		return "", apperr.ErrForbidden.WithMessage("requester identity not found in authenticated context")
	}

	if reason == "" {
		reason = actionType + " request"
	}

	// P0-5: 审批 payload 递归脱敏，凭证字段不得原样落库
	sanitizedPayload := SanitizeMap(payload)
	payloadJSON, _ := json.Marshal(sanitizedPayload)

	var targetUUID *uuid.UUID
	if targetID != nil && *targetID != "" {
		if tid, err := uuid.Parse(*targetID); err == nil {
			targetUUID = &tid
		}
	}

	var tenantID *uuid.UUID
	if identity.TenantID != "" {
		if tid, err := uuid.Parse(identity.TenantID); err == nil {
			tenantID = &tid
		}
	}

	var id uuid.UUID
	// ADM-007/ADM-003: view（requested_payload）全脱敏；execution（validated_payload）
	// 保留 vault:// 引用、密钥值仍脱敏
	execJSON, _ := json.Marshal(SanitizeMapForExecution(payload))
	err := h.pool.QueryRow(ctx, `
		INSERT INTO admin_approval_requests (
			tenant_id, action_type, target_type, target_id,
			requested_payload, validated_payload, requester, requester_role, reason, status
		) VALUES (
			$1, $2, $3, $4,
			$5::jsonb, $6::jsonb, $7, $8, $9, 'PENDING'
		)
		RETURNING id
	`, tenantID, actionType, targetType, targetUUID,
		string(payloadJSON), string(execJSON), requester, identity.Role, reason,
	).Scan(&id)
	if err != nil {
		return "", apperr.ErrInternal.WithMessage("failed to create approval request: " + err.Error())
	}

	return id.String(), nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	// ADM-015: 默认不发 CORS 头（管理端为同源/内部使用）；如需跨域须显式 allowlist
	json.NewEncoder(w).Encode(v)
}

// queryLimit 解析 limit 参数并钳制到 [1, 500]（ADM-017：列表 limit 无上限会放大查询压力）。
func queryLimit(r *http.Request, def int) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		if def < 1 {
			def = 50
		}
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 50
	}
	if n < 1 {
		return 1
	}
	if n > 500 {
		return 500
	}
	return n
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
	// ADM-016: 不回显底层错误细节，只返回稳定 code
	w.WriteHeader(500)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"code": "INTERNAL", "message": "internal server error"},
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
		slog.Error("encrypt api_key failed", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("failed to encrypt api key"))
		return
	}
	webhookSecretEnc, err := h.encryptor.Encrypt(ctx, []byte(req.WebhookSecret), crypto.Meta{
		TenantID: req.MAEID, RecordID: "mae_configuration", FieldName: "webhook_secret",
	})
	if err != nil {
		slog.Error("encrypt webhook_secret failed", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("failed to encrypt webhook secret"))
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
		slog.Error("encrypt webhook secret failed", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("failed to encrypt secret"))
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
	tenant, ok := requireAdminTenant(w, r)
	if !ok {
		return
	}
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
	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil || merchantID == uuid.Nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant id"))
		return
	}

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
		slog.Error("encrypt payout address failed", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("failed to encrypt address"))
		return
	}

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO merchant_payout_address (
			merchant_id, target_asset, target_network, address_format_type,
			target_address_enc, target_address_hash, target_address_masked,
			address_label, screening_status, whitelist_status
		) SELECT $1, $2, $3, $4, $5, $6, $7, $8, 'Pending', 'Pending'
		  WHERE EXISTS(SELECT 1 FROM corporate_entity WHERE id=$1 AND tenant_id=$9) RETURNING id`,
		merchantID, req.TargetAsset, req.TargetNetwork, req.FormatType,
		addressEnc, cleaned.AddressHash, cleaned.TargetAddressMasked, req.AddressLabel, tenant,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, apperr.ErrNotFound.WithMessage("merchant not found"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

// ── Universal Adapter Registry API ──

func (h *Handler) ListAdapterProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, provider_code, provider_name, adapter_type, provider_config::text, status, created_at::text, updated_at::text
		 FROM adapter_provider ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, code, name, adapterType, config, status, created, updated string
		rows.Scan(&id, &code, &name, &adapterType, &config, &status, &created, &updated)
		items = append(items, map[string]interface{}{
			"id": id, "provider_code": code, "provider_name": name, "adapter_type": adapterType,
			"provider_config": sanitizeJSONText(config), "status": status,
			"created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) CreateAdapterProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderCode   string          `json:"provider_code"`
		ProviderName   string          `json:"provider_name"`
		AdapterType    string          `json:"adapter_type"`
		ProviderConfig json.RawMessage `json:"provider_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	if req.ProviderConfig == nil {
		req.ProviderConfig = json.RawMessage("{}")
	}

	ctx := r.Context()
	var id uuid.UUID
	// P0-4: 新建适配器提供商默认 DISABLED，需经 ENABLE_ADAPTER_PROVIDER 审批后启用
	err := h.pool.QueryRow(ctx,
		`INSERT INTO adapter_provider (provider_code, provider_name, adapter_type, provider_config, status)
		 VALUES ($1, $2, $3, $4, 'DISABLED') RETURNING id`,
		req.ProviderCode, req.ProviderName, req.AdapterType, req.ProviderConfig,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *Handler) UpdateAdapterProvider(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	var req struct {
		ProviderName   string          `json:"provider_name"`
		ProviderConfig json.RawMessage `json:"provider_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	if req.ProviderConfig == nil {
		req.ProviderConfig = json.RawMessage("{}")
	}

	// P0-4: 更新适配器提供商走 Maker-Checker 审批，不直接修改业务表
	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionUpdateAdapterProvider, TargetTypeAdapterProvider, &idStr,
		map[string]interface{}{
			"provider_id":     idStr,
			"provider_name":   req.ProviderName,
			"provider_config": json.RawMessage(req.ProviderConfig),
		},
		"update adapter provider")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

// DeleteAdapterProvider 删除适配器提供商 — P0-4: 改为经审批的软禁用，永不物理删除
func (h *Handler) DeleteAdapterProvider(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid provider id"))
		return
	}

	ctx := r.Context()

	// 检查是否有子通道关联
	var count int
	err = h.pool.QueryRow(ctx, `SELECT COUNT(*) FROM adapter_channel WHERE provider_id = $1`, id).Scan(&count)
	if err != nil {
		writeErr(w, err)
		return
	}
	if count > 0 {
		writeErr(w, errors.New("cannot disable provider with existing channels"))
		return
	}

	// P0-4: 物理删除改为经审批的软禁用
	approvalID, err := h.createPendingApproval(ctx,
		ActionDisableAdapterProvider, TargetTypeAdapterProvider, &idStr,
		map[string]interface{}{"provider_id": idStr},
		"disable adapter provider (replaces physical delete)")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) ListAdapterChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT c.id::text, c.tenant_id::text, c.channel_code, c.channel_name, c.adapter_type, p.provider_code,
		 c.supported_assets::text, c.supported_networks::text, c.status, c.version, c.created_at::text, c.updated_at::text
		 FROM adapter_channel c
		 LEFT JOIN adapter_provider p ON c.provider_id = p.id
		 ORDER BY c.created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, tenantID, code, name, adapterType, providerCode, assets, networks, status, created, updated string
		var version int32
		rows.Scan(&id, &tenantID, &code, &name, &adapterType, &providerCode, &assets, &networks, &status, &version, &created, &updated)
		// UI-007: jsonb::text 是 JSON 字符串，直接作为 string 序列化会二次编码，
		// 前端 .join() 对 string 抛错。此处还原为真数组（非法 JSON 时降级为原字符串）。
		assetsJSON := rawJSONOrString(assets)
		networksJSON := rawJSONOrString(networks)
		items = append(items, map[string]interface{}{
			"id": id, "tenant_id": tenantID, "channel_code": code, "channel_name": name, "adapter_type": adapterType,
			"provider_code": providerCode, "supported_assets": assetsJSON, "supported_networks": networksJSON,
			"status": status, "version": version, "created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// rawJSONOrString 将 jsonb::text 的输出还原为可序列化的 JSON 数组；解析失败时按字符串返回。
func rawJSONOrString(s string) interface{} {
	if s == "" {
		return []interface{}{}
	}
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

func (h *Handler) CreateAdapterChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID          string          `json:"tenant_id"`
		ChannelCode       string          `json:"channel_code"`
		ChannelName       string          `json:"channel_name"`
		AdapterType       string          `json:"adapter_type"`
		ProviderCode      string          `json:"provider_code"`
		SupportedAssets   json.RawMessage `json:"supported_assets"`
		SupportedNetworks json.RawMessage `json:"supported_networks"`
		ChannelConfig     json.RawMessage `json:"channel_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	if req.SupportedAssets == nil {
		req.SupportedAssets = json.RawMessage("[]")
	}
	if req.SupportedNetworks == nil {
		req.SupportedNetworks = json.RawMessage("[]")
	}
	if req.ChannelConfig == nil {
		req.ChannelConfig = json.RawMessage("{}")
	}

	ctx := r.Context()
	// Get provider_id from provider_code
	var providerID uuid.UUID
	err := h.pool.QueryRow(ctx, `SELECT id FROM adapter_provider WHERE provider_code = $1`, req.ProviderCode).Scan(&providerID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("provider not found: "+req.ProviderCode))
		return
	}

	tenantUUID, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid tenant_id"))
		return
	}

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO adapter_channel (tenant_id, provider_id, channel_code, channel_name, adapter_type, supported_assets, supported_networks, channel_config, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'ACTIVE') RETURNING id`,
		tenantUUID, providerID, req.ChannelCode, req.ChannelName, req.AdapterType,
		req.SupportedAssets, req.SupportedNetworks, req.ChannelConfig,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *Handler) UpdateAdapterChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	var req struct {
		ChannelName       string          `json:"channel_name"`
		SupportedAssets   json.RawMessage `json:"supported_assets"`
		SupportedNetworks json.RawMessage `json:"supported_networks"`
		ChannelConfig     json.RawMessage `json:"channel_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	if req.SupportedAssets == nil {
		req.SupportedAssets = json.RawMessage("[]")
	}
	if req.SupportedNetworks == nil {
		req.SupportedNetworks = json.RawMessage("[]")
	}
	if req.ChannelConfig == nil {
		req.ChannelConfig = json.RawMessage("{}")
	}

	ctx := r.Context()
	_, err = h.pool.Exec(ctx,
		`UPDATE adapter_channel SET channel_name = $1, supported_assets = $2, supported_networks = $3, channel_config = $4, updated_at = now() WHERE id = $5`,
		req.ChannelName, req.SupportedAssets, req.SupportedNetworks, req.ChannelConfig, id,
	)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "updated"})
}

func (h *Handler) EnableAdapterChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionEnableAdapterChannel, TargetTypeAdapterChannel, &idStr,
		map[string]interface{}{"channel_id": idStr},
		"enable adapter channel")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) DisableAdapterChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionDisableAdapterChannel, TargetTypeAdapterChannel, &idStr,
		map[string]interface{}{"channel_id": idStr},
		"disable adapter channel")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) ReloadAdapterChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	_, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	writeJSON(w, map[string]interface{}{"status": "reload_triggered"})
}

// ── Corporate Entity API ──

func (h *Handler) ListEntities(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, tenant_id::text, name, legal_name, country, jurisdiction, status, created_at::text, updated_at::text
		 FROM corporate_entity ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, tenantID, name, legalName, country, jurisdiction, status, created, updated string
		rows.Scan(&id, &tenantID, &name, &legalName, &country, &jurisdiction, &status, &created, &updated)
		items = append(items, map[string]interface{}{
			"id": id, "tenant_id": tenantID, "name": name, "legal_name": legalName,
			"country": country, "jurisdiction": jurisdiction, "status": status,
			"created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) CreateEntity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID     string `json:"tenant_id"`
		Name         string `json:"name"`
		LegalName    string `json:"legal_name"`
		Country      string `json:"country"`
		Jurisdiction string `json:"jurisdiction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	tenantUUID, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid tenant_id"))
		return
	}

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO corporate_entity (tenant_id, name, legal_name, country, jurisdiction, status)
		 VALUES ($1, $2, $3, $4, $5, 'ACTIVE') RETURNING id`,
		tenantUUID, req.Name, req.LegalName, req.Country, req.Jurisdiction,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *Handler) UpdateEntity(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	var req struct {
		Name         string `json:"name"`
		LegalName    string `json:"legal_name"`
		Jurisdiction string `json:"jurisdiction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	// ADM-002: 实体信息更新为高风险写，收敛到 Maker-Checker 审批
	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionUpdateCorporateEntity, TargetTypeCorporateEntity, &idStr,
		map[string]interface{}{
			"entity_id":    idStr,
			"name":         req.Name,
			"legal_name":   req.LegalName,
			"jurisdiction": req.Jurisdiction,
		},
		"")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) SetEntityRiskHold(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionSetEntityRiskHold, TargetTypeCorporateEntity, &idStr,
		map[string]interface{}{"entity_id": idStr},
		"set entity risk hold")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) EnableEntity(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	ctx := r.Context()
	// P0-4: 启用实体走 Maker-Checker 审批，不直接修改业务表
	approvalID, err := h.createPendingApproval(ctx,
		ActionEnableEntity, TargetTypeCorporateEntity, &idStr,
		map[string]interface{}{"entity_id": idStr},
		"enable corporate entity")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

// ── Asset Bridge API ──

func (h *Handler) ListBridgeProfiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, tenant_id::text, name, source_asset, target_asset, buffer_percent, max_slippage_bps,
		 batching_policy, execution_policy, withdrawal_policy, enabled, created_at::text, updated_at::text
		 FROM bridge_profile ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, tenantID, name, sourceAsset, targetAsset, batchingPolicy, executionPolicy, withdrawalPolicy, created, updated string
		var bufferPercent, maxSlippage int32
		var enabled bool
		rows.Scan(&id, &tenantID, &name, &sourceAsset, &targetAsset, &bufferPercent, &maxSlippage,
			&batchingPolicy, &executionPolicy, &withdrawalPolicy, &enabled, &created, &updated)
		status := "ACTIVE"
		if !enabled {
			status = "DISABLED"
		}
		items = append(items, map[string]interface{}{
			"id": id, "tenant_id": tenantID, "name": name, "source_asset": sourceAsset, "target_asset": targetAsset,
			"buffer_percent": bufferPercent, "max_slippage_bps": maxSlippage,
			"batching_policy": batchingPolicy, "execution_policy": executionPolicy,
			"withdrawal_policy": withdrawalPolicy, "enabled": enabled, "status": status,
			"created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) CreateBridgeProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID         string `json:"tenant_id"`
		Name             string `json:"name"`
		SourceAsset      string `json:"source_asset"`
		TargetAsset      string `json:"target_asset"`
		BufferPercent    int32  `json:"buffer_percent"`
		MaxSlippageBps   int32  `json:"max_slippage_bps"`
		BatchingPolicy   string `json:"batching_policy"`
		ExecutionPolicy  string `json:"execution_policy"`
		WithdrawalPolicy string `json:"withdrawal_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	tenantUUID, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid tenant_id"))
		return
	}

	var id uuid.UUID
	// P0-4: 新建桥接配置默认 enabled=false，需经 ENABLE_BRIDGE_PROFILE 审批后启用
	err = h.pool.QueryRow(ctx,
		`INSERT INTO bridge_profile (tenant_id, name, source_asset, target_asset, buffer_percent, max_slippage_bps,
		 batching_policy, execution_policy, withdrawal_policy, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, false) RETURNING id`,
		tenantUUID, req.Name, req.SourceAsset, req.TargetAsset, req.BufferPercent, req.MaxSlippageBps,
		req.BatchingPolicy, req.ExecutionPolicy, req.WithdrawalPolicy,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *Handler) UpdateBridgeProfile(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	var req struct {
		Name             string `json:"name"`
		BufferPercent    int32  `json:"buffer_percent"`
		MaxSlippageBps   int32  `json:"max_slippage_bps"`
		BatchingPolicy   string `json:"batching_policy"`
		ExecutionPolicy  string `json:"execution_policy"`
		WithdrawalPolicy string `json:"withdrawal_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	// ADM-002: 桥接配置更新为高风险写，收敛到 Maker-Checker 审批
	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionUpdateBridgeProfile, TargetTypeBridgeProfile, &idStr,
		map[string]interface{}{
			"profile_id":        idStr,
			"name":              req.Name,
			"buffer_percent":    req.BufferPercent,
			"max_slippage_bps":  req.MaxSlippageBps,
			"batching_policy":   req.BatchingPolicy,
			"execution_policy":  req.ExecutionPolicy,
			"withdrawal_policy": req.WithdrawalPolicy,
		},
		"")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) EnableBridgeProfile(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	ctx := r.Context()
	// P0-4: 启用桥接配置走 Maker-Checker 审批，不直接修改业务表
	approvalID, err := h.createPendingApproval(ctx,
		ActionEnableBridgeProfile, TargetTypeBridgeProfile, &idStr,
		map[string]interface{}{"profile_id": idStr},
		"enable bridge profile")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) DisableBridgeProfile(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionDisableBridgeProfile, TargetTypeBridgeProfile, &idStr,
		map[string]interface{}{"profile_id": idStr},
		"disable bridge profile")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

// ── Wallet Policy API ──

func (h *Handler) ListWalletPolicies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT w.id::text, w.tenant_id::text, w.policy_code, a.asset_code, w.network, w.address, w.destination_type, w.label, w.max_single_withdrawal, w.daily_limit, w.status, w.created_at::text, w.updated_at::text
		 FROM transit_wallet_policy w
		 LEFT JOIN asset_definition a ON w.asset_id = a.id
		 ORDER BY w.created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, tenantID, policyCode, assetCode, network, address, destinationType, label, status, created, updated string
		var maxSingleWithdrawal, dailyLimit float64
		rows.Scan(&id, &tenantID, &policyCode, &assetCode, &network, &address, &destinationType, &label, &maxSingleWithdrawal, &dailyLimit, &status, &created, &updated)
		items = append(items, map[string]interface{}{
			"id": id, "tenant_id": tenantID, "policy_code": policyCode, "asset": assetCode, "network": network, "address": address,
			"destination_type": destinationType, "label": label,
			"max_single_withdrawal": maxSingleWithdrawal, "daily_limit": dailyLimit, "status": status,
			"created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *Handler) CreateWalletPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID            string  `json:"tenant_id"`
		PolicyCode          string  `json:"policy_code"`
		Asset               string  `json:"asset"`
		Network             string  `json:"network"`
		Address             string  `json:"address"`
		DestinationType     string  `json:"destination_type"`
		Label               string  `json:"label"`
		MaxSingleWithdrawal float64 `json:"max_single_withdrawal"`
		DailyLimit          float64 `json:"daily_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	tenantUUID, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid tenant_id"))
		return
	}

	// Get asset_id from asset_code
	var assetID uuid.UUID
	err = h.pool.QueryRow(ctx, `SELECT id FROM asset_definition WHERE asset_code = $1`, req.Asset).Scan(&assetID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("asset not found: "+req.Asset))
		return
	}

	var id uuid.UUID
	// P0-4: 新建钱包策略默认 DISABLED，需经 ENABLE_WALLET_POLICY 审批后启用
	err = h.pool.QueryRow(ctx,
		`INSERT INTO transit_wallet_policy (tenant_id, policy_code, asset_id, network, address, destination_type, label, max_single_withdrawal, daily_limit, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'DISABLED') RETURNING id`,
		tenantUUID, req.PolicyCode, assetID, req.Network, req.Address, req.DestinationType, req.Label, req.MaxSingleWithdrawal, req.DailyLimit,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *Handler) UpdateWalletPolicy(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	var req struct {
		MaxSingleWithdrawal float64 `json:"max_single_withdrawal"`
		DailyLimit          float64 `json:"daily_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	// ADM-002: 钱包策略限额更新为高风险写，收敛到 Maker-Checker 审批
	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionUpdateWalletPolicy, TargetTypeWalletPolicy, &idStr,
		map[string]interface{}{
			"policy_id":             idStr,
			"max_single_withdrawal": req.MaxSingleWithdrawal,
			"daily_limit":           req.DailyLimit,
		},
		"")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) EnableWalletPolicy(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	ctx := r.Context()
	// P0-4: 启用钱包策略走 Maker-Checker 审批，不直接修改业务表
	approvalID, err := h.createPendingApproval(ctx,
		ActionEnableWalletPolicy, TargetTypeWalletPolicy, &idStr,
		map[string]interface{}{"policy_id": idStr},
		"enable wallet policy")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

func (h *Handler) DisableWalletPolicy(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	ctx := r.Context()
	approvalID, err := h.createPendingApproval(ctx,
		ActionDisableWalletPolicy, TargetTypeWalletPolicy, &idStr,
		map[string]interface{}{"policy_id": idStr},
		"disable wallet policy")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

// ── V3 合规升级 API ──

// ListChannelRiskConfigs 通道风控配置列表
func (h *Handler) ListChannelRiskConfigs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT c.id::text, c.channel_code, c.channel_name, c.strategy_mode, c.chargeback_threshold_bps,
		 c.risk_override_enabled, c.risk_override_reason, c.risk_override_operator, c.risk_override_expires_at::text,
		 c.status, c.created_at::text, c.updated_at::text
		 FROM adapter_channel c
		 ORDER BY c.created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, code, name, strategyMode, status, created, updated string
		var chargebackThreshold int32
		var overrideEnabled bool
		var overrideReason, overrideOperator *string
		var overrideExpiresAt *string
		rows.Scan(&id, &code, &name, &strategyMode, &chargebackThreshold,
			&overrideEnabled, &overrideReason, &overrideOperator, &overrideExpiresAt,
			&status, &created, &updated)

		item := map[string]interface{}{
			"id": id, "channel_code": code, "channel_name": name,
			"strategy_mode": strategyMode, "chargeback_threshold_bps": chargebackThreshold,
			"risk_override_enabled": overrideEnabled, "status": status,
			"created_at": created, "updated_at": updated,
		}
		if overrideReason != nil {
			item["risk_override_reason"] = *overrideReason
		}
		if overrideOperator != nil {
			item["risk_override_operator"] = *overrideOperator
		}
		if overrideExpiresAt != nil {
			item["risk_override_expires_at"] = *overrideExpiresAt
		}
		items = append(items, item)
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// UpdateChannelRiskConfig 更新通道风控配置
func (h *Handler) UpdateChannelRiskConfig(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	var req struct {
		StrategyMode           string  `json:"strategy_mode"`
		ChargebackThresholdBps int32   `json:"chargeback_threshold_bps"`
		RiskOverrideEnabled    bool    `json:"risk_override_enabled"`
		RiskOverrideReason     *string `json:"risk_override_reason"`
		RiskOverrideOperator   *string `json:"risk_override_operator"`
		RiskOverrideExpiresAt  *string `json:"risk_override_expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	// 红线检查：Override 必须有 reason/operator/expires_at
	if req.RiskOverrideEnabled {
		if req.RiskOverrideReason == nil || *req.RiskOverrideReason == "" {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("risk_override_reason required when override enabled"))
			return
		}
		if req.RiskOverrideOperator == nil || *req.RiskOverrideOperator == "" {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("risk_override_operator required when override enabled"))
			return
		}
		if req.RiskOverrideExpiresAt == nil || *req.RiskOverrideExpiresAt == "" {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("risk_override_expires_at required when override enabled"))
			return
		}
	}

	ctx := r.Context()

	// P0-9: 不直接修改 adapter_channel，创建 PENDING 审批
	payload := map[string]interface{}{
		"channel_id":               idStr,
		"strategy_mode":            req.StrategyMode,
		"chargeback_threshold_bps": req.ChargebackThresholdBps,
		"risk_override_enabled":    req.RiskOverrideEnabled,
	}
	if req.RiskOverrideReason != nil {
		payload["risk_override_reason"] = *req.RiskOverrideReason
	}
	if req.RiskOverrideOperator != nil {
		payload["risk_override_operator"] = *req.RiskOverrideOperator
	}
	if req.RiskOverrideExpiresAt != nil {
		payload["risk_override_expires_at"] = *req.RiskOverrideExpiresAt
	}

	approvalID, err := h.createPendingApproval(ctx,
		ActionUpdateChannelRisk, TargetTypeChannelRisk, &idStr,
		payload,
		"update channel risk config")
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

// ListMerchantRiskProfiles 商户风险配置列表
func (h *Handler) ListMerchantRiskProfiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, merchant_id::text, rolling_reserve_rate_bps, dispute_reserve_rate_bps,
		 reserve_release_days, status, created_at::text, updated_at::text
		 FROM merchant_risk_profiles ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, merchantID, status, created, updated string
		var rollingReserveBps, disputeReserveBps, releaseDays int32
		rows.Scan(&id, &merchantID, &rollingReserveBps, &disputeReserveBps, &releaseDays, &status, &created, &updated)
		items = append(items, map[string]interface{}{
			"id": id, "merchant_id": merchantID,
			"rolling_reserve_rate_bps": rollingReserveBps,
			"dispute_reserve_rate_bps": disputeReserveBps,
			"reserve_release_days":     releaseDays,
			"status":                   status, "created_at": created, "updated_at": updated,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// CreateMerchantRiskProfile 创建商户风险配置
func (h *Handler) CreateMerchantRiskProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MerchantID            string `json:"merchant_id"`
		RollingReserveRateBps int32  `json:"rolling_reserve_rate_bps"`
		DisputeReserveRateBps int32  `json:"dispute_reserve_rate_bps"`
		ReserveReleaseDays    int32  `json:"reserve_release_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	merchantUUID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid merchant_id"))
		return
	}

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO merchant_risk_profiles (merchant_id, rolling_reserve_rate_bps, dispute_reserve_rate_bps, reserve_release_days, status)
		 VALUES ($1, $2, $3, $4, 'ACTIVE') RETURNING id`,
		merchantUUID, req.RollingReserveRateBps, req.DisputeReserveRateBps, req.ReserveReleaseDays,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

// ListRollingReserveEntries 滚动准备金记录列表
func (h *Handler) ListRollingReserveEntries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	merchantID := r.URL.Query().Get("merchant_id")
	status := r.URL.Query().Get("status")
	limit := queryLimit(r, 50)

	query := `SELECT id::text, merchant_id::text, transaction_id::text, amount::text, reserve_type, release_date::text, status, created_at::text
		FROM rolling_reserve_ledger`
	args := []interface{}{}
	argIdx := 1

	if merchantID != "" {
		query += " WHERE merchant_id = $" + strconv.Itoa(argIdx)
		args = append(args, merchantID)
		argIdx++
	}
	if status != "" {
		if argIdx > 1 {
			query += " AND status = $" + strconv.Itoa(argIdx)
		} else {
			query += " WHERE status = $" + strconv.Itoa(argIdx)
		}
		args = append(args, status)
		argIdx++
	}
	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(argIdx)
	args = append(args, limit)

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, merchantIDVal, txnID, amount, reserveType, releaseDate, statusVal, created string
		if err := rows.Scan(&id, &merchantIDVal, &txnID, &amount, &reserveType, &releaseDate, &statusVal, &created); err != nil {
			writeErr(w, err)
			return
		}
		items = append(items, map[string]interface{}{
			"id": id, "merchant_id": merchantIDVal, "transaction_id": txnID,
			"amount": amount, "reserve_type": reserveType,
			"release_date": releaseDate, "status": statusVal, "created_at": created,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// ListFeeAllocations 费用摊销记录列表
func (h *Handler) ListFeeAllocations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	transactionID := r.URL.Query().Get("transaction_id")
	feeType := r.URL.Query().Get("fee_type")
	limit := queryLimit(r, 50)

	query := `SELECT id::text, transaction_id::text, merchant_id::text, fee_type, amount::text, currency, source_data_hash, status, created_at::text
		FROM settlement_fee_allocation`
	args := []interface{}{}
	argIdx := 1

	if transactionID != "" {
		query += " WHERE transaction_id = $" + strconv.Itoa(argIdx)
		args = append(args, transactionID)
		argIdx++
	}
	if feeType != "" {
		if argIdx > 1 {
			query += " AND fee_type = $" + strconv.Itoa(argIdx)
		} else {
			query += " WHERE fee_type = $" + strconv.Itoa(argIdx)
		}
		args = append(args, feeType)
		argIdx++
	}
	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(argIdx)
	args = append(args, limit)

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, txnID, merchantIDVal, feeTypeVal, amount, currency, source, statusVal, created string
		if err := rows.Scan(&id, &txnID, &merchantIDVal, &feeTypeVal, &amount, &currency, &source, &statusVal, &created); err != nil {
			writeErr(w, err)
			return
		}
		items = append(items, map[string]interface{}{
			"id": id, "transaction_id": txnID, "fee_type": feeTypeVal,
			"merchant_id": merchantIDVal, "amount": amount, "currency": currency,
			"source_data_hash": source, "status": statusVal, "created_at": created,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// ListIsolationGroups 管道隔离组列表
func (h *Handler) ListIsolationGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, name, status, frozen_reason, frozen_by, frozen_at::text, created_at::text, updated_at::text
		 FROM pipeline_isolation_groups ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, name, status, created, updated string
		var frozenReason, frozenBy *string
		var frozenAt *string
		rows.Scan(&id, &name, &status, &frozenReason, &frozenBy, &frozenAt, &created, &updated)

		item := map[string]interface{}{
			"id": id, "name": name, "status": status,
			"created_at": created, "updated_at": updated,
		}
		if frozenReason != nil {
			item["frozen_reason"] = *frozenReason
		}
		if frozenBy != nil {
			item["frozen_by"] = *frozenBy
		}
		if frozenAt != nil {
			item["frozen_at"] = *frozenAt
		}
		items = append(items, item)
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// CreateIsolationGroup 创建管道隔离组
func (h *Handler) CreateIsolationGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	var id uuid.UUID
	err := h.pool.QueryRow(ctx,
		`INSERT INTO pipeline_isolation_groups (name, status) VALUES ($1, 'ACTIVE') RETURNING id`,
		req.Name,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

// FreezeIsolationGroup 冻结管道隔离组
func (h *Handler) FreezeIsolationGroup(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	var req struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	// 红线检查：必须有 reason/operator
	if req.Reason == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("reason required"))
		return
	}
	if req.Operator == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("operator required"))
		return
	}

	ctx := r.Context()

	// P0-9: 不直接修改 pipeline_isolation_groups，创建 PENDING 审批
	approvalID, err := h.createPendingApproval(ctx,
		ActionFreezeIsolationGroup, TargetTypeIsolationGroup, &idStr,
		map[string]interface{}{"group_id": idStr, "reason": req.Reason, "operator": req.Operator},
		req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

// UnfreezeIsolationGroup 解冻管道隔离组
func (h *Handler) UnfreezeIsolationGroup(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid id"))
		return
	}

	var req struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	// 红线检查：必须有 reason/operator
	if req.Reason == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("reason required"))
		return
	}
	if req.Operator == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("operator required"))
		return
	}

	ctx := r.Context()

	// P0-9: 不直接修改 pipeline_isolation_groups，创建 PENDING 审批
	approvalID, err := h.createPendingApproval(ctx,
		ActionUnfreezeIsolationGroup, TargetTypeIsolationGroup, &idStr,
		map[string]interface{}{"group_id": idStr, "reason": req.Reason, "operator": req.Operator},
		req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "pending_approval", "approval_id": approvalID})
}

// GenerateRFIDocument 生成 RFI 文档
func (h *Handler) GenerateRFIDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		OrderID      string `json:"order_id"`
		DocumentType string `json:"document_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	if req.OrderID == "" || req.DocumentType == "" {
		writeErr(w, errors.New("order_id and document_type are required"))
		return
	}

	orderUUID, err := uuid.Parse(req.OrderID)
	if err != nil {
		writeErr(w, errors.New("invalid order_id"))
		return
	}

	// ADM-011: 锚定真实 checkout 事实——checkout_orders 是订单的权威表
	var merchantID uuid.UUID
	err = h.pool.QueryRow(ctx,
		`SELECT merchant_id FROM checkout_orders WHERE id = $1`,
		orderUUID,
	).Scan(&merchantID)
	if err != nil {
		writeErr(w, errors.New("order not found"))
		return
	}

	// ADM-011: 源数据检查基于真实订单字段——订单缺失客户信息时需向商户征集
	var customerPresent bool
	err = h.pool.QueryRow(ctx,
		`SELECT customer IS NOT NULL FROM checkout_orders WHERE id = $1`,
		orderUUID,
	).Scan(&customerPresent)
	if err != nil && err != pgx.ErrNoRows {
		// 查询错误不影响主流程，只记录日志
		fmt.Printf("warning: check source data failed: %v\n", err)
	}

	// 源数据缺失时标记 REVIEW_REQUIRED
	status := "PENDING"
	if !customerPresent {
		status = "REVIEW_REQUIRED"
	}

	sourceHash := generateSourceHash(req.OrderID, req.DocumentType)

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO compliance_rfi_documents (order_id, merchant_id, document_type, source_data_hash, status, document_payload, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		orderUUID, merchantID, req.DocumentType, sourceHash, status, generateDocumentPayload(req.OrderID, req.DocumentType),
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": status})
}

// GetRFIDocument 获取 RFI 文档详情
func (h *Handler) GetRFIDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, errors.New("invalid document id"))
		return
	}

	var (
		orderID, merchantID, docType, sourceHash, status, created, updated string
		pdfContent                                                         []byte
		approvedBy, approvedAt, rejectedBy, rejectedAt, rejectedReason     *string
	)
	err = h.pool.QueryRow(ctx,
		`SELECT id::text, order_id::text, merchant_id::text, document_type, source_data_hash, status,
		 document_payload, approved_by, approved_at::text, rejected_by, rejected_at::text, rejected_reason,
		 created_at::text, updated_at::text
		 FROM compliance_rfi_documents WHERE id = $1`,
		id,
	).Scan(&idStr, &orderID, &merchantID, &docType, &sourceHash, &status,
		&pdfContent, &approvedBy, &approvedAt, &rejectedBy, &rejectedAt, &rejectedReason,
		&created, &updated)
	if err != nil {
		writeErr(w, err)
		return
	}

	result := map[string]interface{}{
		"id": idStr, "order_id": orderID, "merchant_id": merchantID,
		"document_type": docType, "source_data_hash": sourceHash, "status": status,
		"created_at": created, "updated_at": updated,
	}
	if pdfContent != nil {
		result["document_payload_base64"] = base64.StdEncoding.EncodeToString(pdfContent)
	}
	if approvedBy != nil {
		result["approved_by"] = *approvedBy
	}
	if approvedAt != nil {
		result["approved_at"] = *approvedAt
	}
	if rejectedBy != nil {
		result["rejected_by"] = *rejectedBy
	}
	if rejectedAt != nil {
		result["rejected_at"] = *rejectedAt
	}
	if rejectedReason != nil {
		result["rejected_reason"] = *rejectedReason
	}

	writeJSON(w, result)
}

// ApproveRFIDocument 审批通过 RFI 文档
func (h *Handler) ApproveRFIDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, errors.New("invalid document id"))
		return
	}

	// ADM-013: actor 只取认证 Context，拒绝 body 中伪造的 operator
	operator := httpx.ResolveOperator(ctx)
	if operator == "" || operator == "unknown" {
		writeErr(w, apperr.ErrForbidden.WithMessage("operator identity not found in authenticated context"))
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`UPDATE compliance_rfi_documents SET status = 'APPROVED', approved_by = $1, approved_at = NOW(), updated_at = NOW() WHERE id = $2`,
		operator, id)
	if err != nil {
		writeErr(w, err)
		return
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO admin_override_audit_log (entity_type, entity_id, override_type, reason, operator, created_at)
		 VALUES ('compliance_rfi_documents', $1, 'APPROVE', 'Document approved', $2, NOW())`,
		id, operator)
	if err != nil {
		writeErr(w, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"status": "approved"})
}

// RejectRFIDocument 审批拒绝 RFI 文档
func (h *Handler) RejectRFIDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, errors.New("invalid document id"))
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}

	// ADM-013: actor 只取认证 Context，拒绝 body 中伪造的 operator
	operator := httpx.ResolveOperator(ctx)
	if operator == "" || operator == "unknown" {
		writeErr(w, apperr.ErrForbidden.WithMessage("operator identity not found in authenticated context"))
		return
	}
	if req.Reason == "" {
		writeErr(w, errors.New("reason is required"))
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`UPDATE compliance_rfi_documents SET status = 'REJECTED', rejected_by = $1, rejected_at = NOW(), rejected_reason = $2, updated_at = NOW() WHERE id = $3`,
		operator, req.Reason, id)
	if err != nil {
		writeErr(w, err)
		return
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO admin_override_audit_log (entity_type, entity_id, override_type, reason, operator, created_at)
		 VALUES ('compliance_rfi_documents', $1, 'REJECT', $2, $3, NOW())`,
		id, req.Reason, operator)
	if err != nil {
		writeErr(w, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"status": "rejected"})
}

// generateSourceHash 生成源数据哈希
func generateSourceHash(orderID, docType string) string {
	data := orderID + ":" + docType + ":" + time.Now().String()
	return fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
}

// generateDocumentPayload 生成 PDF 内容
func generateDocumentPayload(orderID, docType string) []byte {
	metadata := fmt.Sprintf(`{"order_id":"%s","document_type":"%s","source_data_hash":"%s","generated_at":"%s"}`,
		orderID, docType, generateSourceHash(orderID, docType), time.Now().UTC().Format(time.RFC3339))
	return []byte(metadata)
}

// ListRFIDocuments RFI 合规资料包列表
func (h *Handler) ListRFIDocuments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orderID := r.URL.Query().Get("order_id")
	merchantID := r.URL.Query().Get("merchant_id")
	status := r.URL.Query().Get("status")
	limit := queryLimit(r, 50)

	query := `SELECT id::text, order_id::text, merchant_id::text, document_type, source_data_hash, status, metadata->>'review_required_reason', created_at::text, updated_at::text
		FROM compliance_rfi_documents`
	args := []interface{}{}
	argIdx := 1

	if orderID != "" {
		query += " WHERE order_id = $" + strconv.Itoa(argIdx)
		args = append(args, orderID)
		argIdx++
	}
	if merchantID != "" {
		if argIdx > 1 {
			query += " AND merchant_id = $" + strconv.Itoa(argIdx)
		} else {
			query += " WHERE merchant_id = $" + strconv.Itoa(argIdx)
		}
		args = append(args, merchantID)
		argIdx++
	}
	if status != "" {
		if argIdx > 1 {
			query += " AND status = $" + strconv.Itoa(argIdx)
		} else {
			query += " WHERE status = $" + strconv.Itoa(argIdx)
		}
		args = append(args, status)
		argIdx++
	}
	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(argIdx)
	args = append(args, limit)

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, orderIDVal, merchantIDVal, docType, sourceHash, statusVal, created, updated string
		var reviewReason *string
		if err := rows.Scan(&id, &orderIDVal, &merchantIDVal, &docType, &sourceHash, &statusVal, &reviewReason, &created, &updated); err != nil {
			writeErr(w, err)
			return
		}

		item := map[string]interface{}{
			"id": id, "order_id": orderIDVal, "merchant_id": merchantIDVal,
			"document_type": docType, "source_data_hash": sourceHash,
			"status": statusVal, "created_at": created, "updated_at": updated,
		}
		if reviewReason != nil {
			item["review_required_reason"] = *reviewReason
		}
		items = append(items, item)
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

// ListOverrideAuditLogs Override 审计日志列表
func (h *Handler) ListOverrideAuditLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entityType := r.URL.Query().Get("entity_type")
	limit := queryLimit(r, 100)

	query := `SELECT id::text, entity_type, entity_id::text, override_type, reason, operator, expires_at::text, created_at::text
		FROM admin_override_audit_log`
	args := []interface{}{}
	argIdx := 1

	if entityType != "" {
		query += " WHERE entity_type = $" + strconv.Itoa(argIdx)
		args = append(args, entityType)
		argIdx++
	}
	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(argIdx)
	args = append(args, limit)

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, entityTypeVal, entityID, overrideType, reason, operator, created string
		var expiresAt *string
		rows.Scan(&id, &entityTypeVal, &entityID, &overrideType, &reason, &operator, &expiresAt, &created)

		item := map[string]interface{}{
			"id": id, "entity_type": entityTypeVal, "entity_id": entityID,
			"override_type": overrideType, "reason": reason, "operator": operator,
			"created_at": created,
			"human_text": PresentOverrideAuditLogHumanText(overrideType, operator, reason),
		}
		if expiresAt != nil {
			item["expires_at"] = *expiresAt
		}
		items = append(items, item)
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}
