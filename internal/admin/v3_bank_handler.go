package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

type V3BankHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

func NewV3BankHandler(pool *pgxpool.Pool, rdb *redis.Client) *V3BankHandler {
	return &V3BankHandler{pool: pool, rdb: rdb}
}

func (h *V3BankHandler) publishBankConfigUpdate(vaID uuid.UUID) {
	if h.rdb != nil {
		h.rdb.Publish(context.Background(), "config:bank:reload", vaID.String())
	}
}

func (h *V3BankHandler) ListVAPool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	status := r.URL.Query().Get("status")
	assetCode := r.URL.Query().Get("asset_code")
	channelID := r.URL.Query().Get("channel_id")

	query := `SELECT id::text, tenant_id::text, channel_id::text, account_number, routing_number, iban, swift, bank_name, bank_address, asset_code, status, allocated_intent_id::text, allocated_at::text, released_at::text, created_at::text, updated_at::text FROM bank_collection_virtual_account_pool`
	args := []interface{}{}
	argIdx := 1

	var whereClauses []string
	// ADM-001: 对象级租户 scope
	if tenantID, scoped := tenantScopeOf(ctx); scoped {
		whereClauses = append(whereClauses, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, tenantID)
		argIdx++
	}
	if status != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, status)
		argIdx++
	}
	if assetCode != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("asset_code = $%d", argIdx))
		args = append(args, assetCode)
		argIdx++
	}
	if channelID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("channel_id = $%d", argIdx))
		args = append(args, channelID)
		argIdx++
	}

	if len(whereClauses) > 0 {
		query += " WHERE " + joinWithAnd(whereClauses)
	}

	query += " ORDER BY created_at DESC"

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, tenantID, channelID, accountNumber, assetCode, status string
		var routingNumber, iban, swift, bankName, bankAddress, allocatedIntentID, allocatedAt, releasedAt, createdAt, updatedAt *string
		if err := rows.Scan(&id, &tenantID, &channelID, &accountNumber, &routingNumber, &iban, &swift, &bankName, &bankAddress, &assetCode, &status, &allocatedIntentID, &allocatedAt, &releasedAt, &createdAt, &updatedAt); err != nil {
			slog.Error("v3_bank_handler internal error", "err", err)
			writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
			return
		}

		if accountNumber == "" {
			accountNumber = ""
		} else {
			accountNumber = maskAccountNumber(accountNumber)
		}

		item := map[string]interface{}{
			"id":                  id,
			"tenant_id":           tenantID,
			"channel_id":          channelID,
			"account_number":      accountNumber,
			"routing_number":      routingNumber,
			"iban":                iban,
			"swift":               swift,
			"bank_name":           bankName,
			"bank_address":        bankAddress,
			"asset_code":          assetCode,
			"status":              status,
			"allocated_intent_id": allocatedIntentID,
			"allocated_at":        allocatedAt,
			"released_at":         releasedAt,
			"created_at":          createdAt,
			"updated_at":          updatedAt,
		}
		items = append(items, item)
	}

	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3BankHandler) CreateVAAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID      string                 `json:"tenant_id"`
		ChannelID     string                 `json:"channel_id"`
		AccountNumber string                 `json:"account_number"`
		RoutingNumber string                 `json:"routing_number"`
		IBAN          string                 `json:"iban"`
		SWIFT         string                 `json:"swift"`
		BankName      string                 `json:"bank_name"`
		BankAddress   string                 `json:"bank_address"`
		AssetCode     string                 `json:"asset_code"`
		Metadata      map[string]interface{} `json:"metadata"`
		// G15: capability_mode 显式声明本次操作的语义。
		// "local_record"（默认）：仅本地账户记录 CRUD，不调用外部银行 API。
		// "external_open"：外部银行真实开户，需要真实 adapter + 凭证。
		// 红线：没有真实 adapter 时 "external_open" 必须失败，不可显示"开户成功"。
		CapabilityMode string `json:"capability_mode"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if req.TenantID == "" || req.ChannelID == "" || req.AccountNumber == "" || req.BankName == "" || req.AssetCode == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("tenant_id, channel_id, account_number, bank_name, asset_code are required"))
		return
	}

	// G15: 默认 capability_mode 为 "local_record"。
	// 显式声明可避免 UI 误以为"开户成功"。
	if req.CapabilityMode == "" {
		req.CapabilityMode = "local_record"
	}
	if req.CapabilityMode != "local_record" && req.CapabilityMode != "external_open" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("capability_mode must be one of: local_record, external_open"))
		return
	}

	tenantUUID, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid tenant_id"))
		return
	}

	channelUUID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid channel_id"))
		return
	}

	ctx := r.Context()

	var channelExists bool
	err = h.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM adapter_channel WHERE id = $1)`, channelUUID).Scan(&channelExists)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	if !channelExists {
		writeErr(w, apperr.ErrConfigInconsistent.WithMessage("channel_id not found"))
		return
	}

	// G15: VBA capability 边界。
	// "external_open" 模式需要真实外部银行 adapter + 凭证。
	// 当前未注册任何外部银行开户 adapter：明确拒绝，不显示"开户成功"。
	// 这是 fail-closed 设计：没有真实 adapter 时禁止外部开户。
	if req.CapabilityMode == "external_open" {
		// 检查 channel 是否有对应的 adapter provider 和凭证。
		// 当前实现：所有 channel 都不具备外部开户能力（未接通真实银行 API）。
		// 标记为 BLOCKED_EXTERNAL_CREDENTIAL：adapter 框架存在但未接通真实银行。
		slog.Warn("v3_bank_handler: VBA external_open requested but no real external bank adapter registered",
			"channel_id", channelUUID, "capability_mode", req.CapabilityMode)
		writeErr(w, apperr.ErrFailedPrecondition.WithMessage(
			"VBA EXTERNAL_BANK_OPEN capability not available: no real external bank adapter registered (BLOCKED_EXTERNAL_CREDENTIAL). Use capability_mode=local_record for local account record CRUD."))
		return
	}

	if req.Metadata == nil {
		req.Metadata = map[string]interface{}{}
	}
	// G15: 在 metadata 中标记本次操作的 capability_mode，供 UI 和审计追溯。
	req.Metadata["vba_capability_mode"] = req.CapabilityMode
	req.Metadata["vba_capability_note"] = "local account record CRUD only; not external bank account opening"
	metadataJSON, _ := json.Marshal(req.Metadata)

	var id uuid.UUID
	err = h.pool.QueryRow(ctx,
		`INSERT INTO bank_collection_virtual_account_pool (tenant_id, channel_id, account_number, routing_number, iban, swift, bank_name, bank_address, asset_code, status, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'AVAILABLE', $10)
		 RETURNING id`,
		tenantUUID, channelUUID, req.AccountNumber, req.RoutingNumber, req.IBAN, req.SWIFT, req.BankName, req.BankAddress, req.AssetCode, metadataJSON,
	).Scan(&id)

	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, apperr.ErrIdempotencyConflict.WithMessage("account_number already exists"))
			return
		}
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}

	h.publishBankConfigUpdate(id)

	// G15: 响应中明确标记 capability_mode，UI 据此决定显示文案。
	// "local_record" 模式下 status 为 "created"（本地记录已创建），
	// 但 UI 不可显示"开户成功"，应显示"本地账户记录已创建"。
	writeJSON(w, map[string]interface{}{
		"id":              id.String(),
		"status":          "created",
		"capability_mode": req.CapabilityMode,
		"capability_note": "local account record created; external bank account opening not performed",
	})
}

func (h *V3BankHandler) ReleaseVA(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid va id"))
		return
	}

	ctx := r.Context()

	// RED-15: requester 只能从已验证 Context 获取，禁止从请求体或 header 读取
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil {
		writeErr(w, apperr.ErrForbidden.WithMessage("authenticated identity required"))
		return
	}
	requester := identity.Username
	if requester == "" {
		requester = identity.Subject
	}
	if requester == "" {
		writeErr(w, apperr.ErrForbidden.WithMessage("authenticated identity required"))
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	if decErr := json.NewDecoder(r.Body).Decode(&body); decErr != nil && !errors.Is(decErr, io.EOF) {
		slog.Warn("release VA: invalid request body", "err", decErr)
	}

	reason := body.Reason
	if reason == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("reason is required"))
		return
	}

	// P0-9: 不直接 UPDATE bank_collection_virtual_account_pool，创建 PENDING 审批
	if h.pool == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	var vaExists bool
	err = h.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bank_collection_virtual_account_pool WHERE id = $1)`, id).Scan(&vaExists)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	if !vaExists {
		writeErr(w, apperr.ErrNotFound.WithMessage("va not found"))
		return
	}

	// 读取变更前快照（不含敏感字段）
	var vaStatus, vaAssetCode string
	var vaTenantID, vaChannelID uuid.UUID
	err = h.pool.QueryRow(ctx,
		`SELECT tenant_id, channel_id, status, asset_code FROM bank_collection_virtual_account_pool WHERE id = $1`,
		id).Scan(&vaTenantID, &vaChannelID, &vaStatus, &vaAssetCode)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}

	beforeSnapshot, _ := json.Marshal(map[string]interface{}{
		"status":     vaStatus,
		"asset_code": vaAssetCode,
	})

	requestedPayload := map[string]string{"va_id": id.String()}
	payloadJSON, _ := json.Marshal(requestedPayload)

	var approvalID string
	// ADM-007: ApplyTransaction 读取 validated_payload——handler 创建的审批必须
	// 同步写入 validated_payload（typed payload），否则应用阶段 payload 为 nil。
	err = h.pool.QueryRow(ctx,
		`INSERT INTO admin_approval_requests (
			action_type, requested_payload, validated_payload, requester, requester_role, reason, status,
			target_type, target_id, before_snapshot
		)
		VALUES ($1, $2::jsonb, $2::jsonb, $3, $4, $5, 'PENDING', $6, $7, $8::jsonb)
		RETURNING id::text`,
		ActionReleaseVAAccount, string(payloadJSON), requester, identity.Role, reason,
		TargetTypeVAAccount, id, string(beforeSnapshot),
	).Scan(&approvalID)

	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]interface{}{
		"approval_id": approvalID,
		"va_id":       id.String(),
		"action_type": ActionReleaseVAAccount,
		"status":      "PENDING",
		"message":     "释放 VA 审批单已创建，待复核后生效",
	})
}

func (h *V3BankHandler) DisableVA(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid va id"))
		return
	}

	ctx := r.Context()

	// RED-15: requester 只能从已验证 Context 获取，禁止从请求体或 header 读取
	identity, ok := httpx.GetAdminIdentity(ctx)
	if !ok || identity == nil {
		writeErr(w, apperr.ErrForbidden.WithMessage("authenticated identity required"))
		return
	}
	requester := identity.Username
	if requester == "" {
		requester = identity.Subject
	}
	if requester == "" {
		writeErr(w, apperr.ErrForbidden.WithMessage("authenticated identity required"))
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	if decErr := json.NewDecoder(r.Body).Decode(&body); decErr != nil && !errors.Is(decErr, io.EOF) {
		slog.Warn("disable VA: invalid request body", "err", decErr)
	}

	reason := body.Reason
	if reason == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("reason is required"))
		return
	}

	if h.pool == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	var vaExists bool
	err = h.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bank_collection_virtual_account_pool WHERE id = $1)`, id).Scan(&vaExists)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	if !vaExists {
		writeErr(w, apperr.ErrNotFound.WithMessage("va not found"))
		return
	}

	// 读取变更前快照（不含敏感字段）
	var vaStatus, vaAssetCode string
	var vaTenantID, vaChannelID uuid.UUID
	err = h.pool.QueryRow(ctx,
		`SELECT tenant_id, channel_id, status, asset_code FROM bank_collection_virtual_account_pool WHERE id = $1`,
		id).Scan(&vaTenantID, &vaChannelID, &vaStatus, &vaAssetCode)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}

	beforeSnapshot, _ := json.Marshal(map[string]interface{}{
		"status":     vaStatus,
		"asset_code": vaAssetCode,
	})

	requestedPayload := map[string]string{"va_id": id.String()}
	payloadJSON, _ := json.Marshal(requestedPayload)

	var approvalID string
	// ADM-007: 同步写入 validated_payload（ApplyTransaction 读取该列）
	err = h.pool.QueryRow(ctx,
		`INSERT INTO admin_approval_requests (
			action_type, requested_payload, validated_payload, requester, requester_role, reason, status,
			target_type, target_id, before_snapshot
		)
		VALUES ($1, $2::jsonb, $2::jsonb, $3, $4, $5, 'PENDING', $6, $7, $8::jsonb)
		RETURNING id::text`,
		ActionDisableVAAccount, string(payloadJSON), requester, identity.Role, reason,
		TargetTypeVAAccount, id, string(beforeSnapshot),
	).Scan(&approvalID)

	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]interface{}{
		"approval_id": approvalID,
		"va_id":       id.String(),
		"action_type": ActionDisableVAAccount,
		"status":      "PENDING",
		"message":     "禁用 VA 审批单已创建，待复核后生效",
	})
}

func (h *V3BankHandler) ListSettlementAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	currency := r.URL.Query().Get("currency")

	query := `SELECT id::text, bank_account_id::text, currency, accumulated_minor, updated_at::text FROM bank_account_balance`
	args := []interface{}{}
	argIdx := 1

	if currency != "" {
		query += fmt.Sprintf(" WHERE currency = $%d", argIdx)
		args = append(args, currency)
		argIdx++
	}

	query += " ORDER BY updated_at DESC"

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, bankAccountID, currency, updatedAt string
		var accumulatedMinor int64
		if err := rows.Scan(&id, &bankAccountID, &currency, &accumulatedMinor, &updatedAt); err != nil {
			slog.Error("v3_bank_handler internal error", "err", err)
			writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
			return
		}
		items = append(items, map[string]interface{}{
			"id":                id,
			"bank_account_id":   bankAccountID,
			"currency":          currency,
			"accumulated_minor": accumulatedMinor,
			"updated_at":        updatedAt,
		})
	}

	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3BankHandler) CreateSettlementAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BankAccountID    string `json:"bank_account_id"`
		Currency         string `json:"currency"`
		AccumulatedMinor int64  `json:"accumulated_minor"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
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
	_, err = h.pool.Exec(ctx,
		`INSERT INTO bank_account_balance (bank_account_id, currency, accumulated_minor, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (bank_account_id, currency)
		 DO UPDATE SET accumulated_minor = EXCLUDED.accumulated_minor, updated_at = now()`,
		bankAccountID, req.Currency, req.AccumulatedMinor)

	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}

	writeJSON(w, map[string]interface{}{
		"bank_account_id":   bankAccountID.String(),
		"currency":          req.Currency,
		"accumulated_minor": req.AccumulatedMinor,
		"status":            "created",
	})
}

func (h *V3BankHandler) ListIntents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	state := r.URL.Query().Get("state")
	tenantID := r.URL.Query().Get("tenant_id")

	query := `SELECT id::text, tenant_id::text, order_id, expected_amount, received_amount, confirmed_amount, state, expires_at::text, created_at::text FROM bank_collection_intent`
	args := []interface{}{}
	argIdx := 1

	var whereClauses []string
	if state != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("state = $%d", argIdx))
		args = append(args, state)
		argIdx++
	}
	if tenantID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, tenantID)
		argIdx++
	}

	if len(whereClauses) > 0 {
		query += " WHERE " + joinWithAnd(whereClauses)
	}

	query += " ORDER BY created_at DESC"

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, tenantID, orderID, state, expiresAt, createdAt string
		var expectedAmount, receivedAmount, confirmedAmount float64
		if err := rows.Scan(&id, &tenantID, &orderID, &expectedAmount, &receivedAmount, &confirmedAmount, &state, &expiresAt, &createdAt); err != nil {
			slog.Error("v3_bank_handler internal error", "err", err)
			writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
			return
		}
		items = append(items, map[string]interface{}{
			"id":               id,
			"tenant_id":        tenantID,
			"order_id":         orderID,
			"expected_amount":  expectedAmount,
			"received_amount":  receivedAmount,
			"confirmed_amount": confirmedAmount,
			"state":            state,
			"expires_at":       expiresAt,
			"created_at":       createdAt,
		})
	}

	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3BankHandler) ListWebhookLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	processedStr := r.URL.Query().Get("processed")
	channelID := r.URL.Query().Get("channel_id")

	query := `SELECT id::text, tenant_id::text, channel_id::text, event_id, event_type, provider_ref, payment_reference, account_number, amount, asset_code, transaction_ref, signature_valid, processed, processed_at::text, error_message, received_at::text FROM bank_collection_webhook_log`
	args := []interface{}{}
	argIdx := 1

	var whereClauses []string
	if processedStr != "" {
		processed, err := strconv.ParseBool(processedStr)
		if err != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid processed parameter"))
			return
		}
		whereClauses = append(whereClauses, fmt.Sprintf("processed = $%d", argIdx))
		args = append(args, processed)
		argIdx++
	}
	if channelID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("channel_id = $%d", argIdx))
		args = append(args, channelID)
		argIdx++
	}

	if len(whereClauses) > 0 {
		query += " WHERE " + joinWithAnd(whereClauses)
	}

	query += " ORDER BY received_at DESC"

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, tenantID, channelID, eventID, eventType, providerRef, paymentReference, accountNumber, assetCode, transactionRef, errorMessage, processedAt, receivedAt string
		var amount float64
		var signatureValid, processed bool
		if err := rows.Scan(&id, &tenantID, &channelID, &eventID, &eventType, &providerRef, &paymentReference, &accountNumber, &amount, &assetCode, &transactionRef, &signatureValid, &processed, &processedAt, &errorMessage, &receivedAt); err != nil {
			slog.Error("v3_bank_handler internal error", "err", err)
			writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
			return
		}

		items = append(items, map[string]interface{}{
			"id":                id,
			"tenant_id":         tenantID,
			"channel_id":        channelID,
			"event_id":          eventID,
			"event_type":        eventType,
			"provider_ref":      nullIfEmpty(providerRef),
			"payment_reference": paymentReference,
			"account_number":    nullIfEmpty(accountNumber),
			"amount":            amount,
			"asset_code":        nullIfEmpty(assetCode),
			"transaction_ref":   nullIfEmpty(transactionRef),
			"signature_valid":   signatureValid,
			"processed":         processed,
			"processed_at":      nullIfEmpty(processedAt),
			"error_message":     nullIfEmpty(errorMessage),
			"raw_payload":       "[BINARY]",
			"received_at":       receivedAt,
		})
	}

	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3BankHandler) ListReconciliationLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	matchedStr := r.URL.Query().Get("matched")
	reconciliationDate := r.URL.Query().Get("reconciliation_date")

	query := `SELECT id::text, tenant_id::text, intent_id::text, reconciliation_date::text, matched, mismatch_type, received_amount, expected_amount, received_reference, expected_reference, notes, created_at::text FROM bank_collection_reconciliation_log`
	args := []interface{}{}
	argIdx := 1

	var whereClauses []string
	if matchedStr != "" {
		matched, err := strconv.ParseBool(matchedStr)
		if err != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid matched parameter"))
			return
		}
		whereClauses = append(whereClauses, fmt.Sprintf("matched = $%d", argIdx))
		args = append(args, matched)
		argIdx++
	}
	if reconciliationDate != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("reconciliation_date = $%d", argIdx))
		args = append(args, reconciliationDate)
		argIdx++
	}

	if len(whereClauses) > 0 {
		query += " WHERE " + joinWithAnd(whereClauses)
	}

	query += " ORDER BY reconciliation_date DESC"

	rows, err := h.pool.Query(ctx, query, args...)
	if err != nil {
		slog.Error("v3_bank_handler internal error", "err", err)
		writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, tenantID, intentID, reconciliationDate, mismatchType, receivedReference, expectedReference, notes, createdAt string
		var matched bool
		var receivedAmount, expectedAmount float64
		if err := rows.Scan(&id, &tenantID, &intentID, &reconciliationDate, &matched, &mismatchType, &receivedAmount, &expectedAmount, &receivedReference, &expectedReference, &notes, &createdAt); err != nil {
			slog.Error("v3_bank_handler internal error", "err", err)
			writeErr(w, apperr.ErrInternal.WithMessage("internal server error"))
			return
		}
		items = append(items, map[string]interface{}{
			"id":                  id,
			"tenant_id":           tenantID,
			"intent_id":           intentID,
			"reconciliation_date": reconciliationDate,
			"matched":             matched,
			"mismatch_type":       nullIfEmpty(mismatchType),
			"received_amount":     receivedAmount,
			"expected_amount":     expectedAmount,
			"received_reference":  nullIfEmpty(receivedReference),
			"expected_reference":  nullIfEmpty(expectedReference),
			"notes":               nullIfEmpty(notes),
			"created_at":          createdAt,
		})
	}

	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func maskAccountNumber(accountNumber string) string {
	if len(accountNumber) <= 3 {
		return "****" + accountNumber
	}
	return "****" + accountNumber[len(accountNumber)-3:]
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func isUniqueViolation(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "23505") || strings.Contains(errStr, "unique") || strings.Contains(errStr, "UNIQUE")
}
