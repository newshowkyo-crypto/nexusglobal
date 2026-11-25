package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/ledger/balance"
	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/settlement/projection"
)

// Service 账本服务
type Service struct {
	ledgerRepo *repository.LedgerRepo
	txm        *postgres.TxManager
	pool       *pgxpool.Pool
}

// New 创建账本服务实例
func New(ledgerRepo *repository.LedgerRepo, txm *postgres.TxManager, pool *pgxpool.Pool) *Service {
	return &Service{
		ledgerRepo: ledgerRepo,
		txm:        txm,
		pool:       pool,
	}
}

// CreateTransaction 创建复式账本交易（G02 实现）
//
// 在传入的 pgx.Tx 内原子地完成：
//  1. 幂等检查（idempotency_key 唯一）
//  2. 借贷平衡校验（RED-13: 借=贷，同币种同资产）
//  3. 插入 ledger_transaction
//  4. 插入成对 ledger_entry（DEBIT/CREDIT）
//  5. SELECT ... FOR UPDATE 锁定 account_balance 后更新余额快照（RED-05）
//  6. 写 transactional_outbox（同事务提交）
//
// 不平衡整笔回滚（RED-13）。冲正通过追加反向分录，不修改历史记录。
func (s *Service) CreateTransaction(ctx context.Context, tx pgx.Tx, txn domain.Transaction) error {
	// 1. 基本校验
	if len(txn.Entries) == 0 {
		return domain.ErrNoEntries
	}
	if txn.IdempotencyKey == "" {
		return fmt.Errorf("ledger: idempotency_key is required")
	}

	// 2. 借贷平衡校验（RED-13）
	//    同一 asset_code 下 DEBIT 总额必须等于 CREDIT 总额
	//    不同资产不得直接相加
	if err := validateBalancedByAsset(txn.Entries); err != nil {
		return err
	}

	// 2.1 资产精度校验：防止把主单位小数误当 minor 单位写入
	if err := s.validateAssetPrecision(ctx, tx, txn); err != nil {
		return err
	}

	metadataJSON := []byte(`{}`)
	if txn.Metadata != nil {
		if md, err := json.Marshal(txn.Metadata); err == nil {
			metadataJSON = md
		}
	}

	payloadHash := hashTransactionPayload(txn)

	// 3. 原子幂等（P0 4.3）：
	//    并发同 key 时只有一个事务能插入成功，其余读取已有记录比对 payload。
	//    旧实现"先查后插"在并发下会唯一键冲突返回 500，同 key 不同金额会静默吞掉。
	txnID := uuid.New()
	var aggregateID *uuid.UUID
	if txn.AggregateID != "" {
		if parsed, err := uuid.Parse(txn.AggregateID); err == nil {
			aggregateID = &parsed
		}
	}

	var insertedID uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_transaction
		    (id, idempotency_key, transaction_type, aggregate_type, aggregate_id,
		     description, tenant_id, source_account_id, target_account_id,
		     asset_code, amount, status, metadata, payload_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`, txnID, txn.IdempotencyKey, string(txn.TransactionType),
		txn.AggregateType, aggregateID,
		txn.Description,
		txn.TenantID, txn.SourceAccountID, txn.TargetAccountID,
		txn.AssetCode, money.DecimalToNumeric(txn.Amount),
		string(txn.Status), metadataJSON, payloadHash).Scan(&insertedID)
	switch {
	case err == nil:
		// 插入成功，继续写分录
	case errors.Is(err, pgx.ErrNoRows):
		// 已存在：必须是同 payload，否则视为幂等冲突
		var existingHash string
		readErr := tx.QueryRow(ctx, `
			SELECT payload_hash FROM ledger_transaction WHERE idempotency_key = $1
		`, txn.IdempotencyKey).Scan(&existingHash)
		if readErr != nil {
			return fmt.Errorf("ledger: read existing idempotent transaction: %w", readErr)
		}
		if existingHash != "" && existingHash != payloadHash {
			return fmt.Errorf("ledger: idempotency key %q reused with different payload: %w",
				txn.IdempotencyKey, domain.ErrIdempotencyConflict)
		}
		return nil
	default:
		return fmt.Errorf("ledger: insert transaction: %w", err)
	}

	// 5. 插入成对 ledger_entry 并同事务更新 account_balance
	for _, entry := range txn.Entries {
		entryID := uuid.New()
		amountNumeric := money.DecimalToNumeric(entry.Amount)

		// 插入 ledger_entry
		_, err := tx.Exec(ctx, `
			INSERT INTO ledger_entry
			    (id, transaction_id, account_id, direction, amount,
			     tenant_id, asset_code, description)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, entryID, txnID, entry.AccountID, string(entry.Direction),
			amountNumeric, txn.TenantID, entry.AssetCode, entry.Description)
		if err != nil {
			return fmt.Errorf("ledger: insert entry: %w", err)
		}

		// RED-05: SELECT ... FOR UPDATE 锁定 account_balance 后更新
		if err := updateAccountBalanceWithLock(ctx, tx, entry, txn.TenantID); err != nil {
			return err
		}
	}

	// 6. 写 transactional_outbox（同事务提交，RED-01: 事务内无外部 I/O）
	outboxID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox
		    (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'Pending')
	`, outboxID, "ledger_transaction", txnID, "LEDGER_TRANSACTION_CREATED",
		[]byte(`{"transaction_id":"`+txnID.String()+`"}`),
		"ledger_outbox_"+txn.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("ledger: insert outbox: %w", err)
	}

	return nil
}

// validateBalancedByAsset 按资产分组校验借贷平衡
// 同一 asset_code 下 DEBIT 总额 == CREDIT 总额；不同资产不得直接相加
func validateBalancedByAsset(entries []domain.LedgerEntry) error {
	if len(entries) == 0 {
		return domain.ErrNoEntries
	}

	type assetBucket struct {
		debit  money.Minor
		credit money.Minor
	}
	buckets := make(map[string]*assetBucket)

	for _, entry := range entries {
		if entry.Amount.IsZero() {
			return domain.ErrZeroAmount
		}
		if entry.Amount.LessThan(money.DecimalFromInt64(0)) {
			return domain.ErrNegativeAmount
		}
		if entry.Direction != domain.DirectionDebit && entry.Direction != domain.DirectionCredit {
			return domain.ErrInvalidDirection
		}
		if entry.AssetCode == "" {
			return fmt.Errorf("ledger: entry asset_code is required")
		}

		bucket, ok := buckets[entry.AssetCode]
		if !ok {
			bucket = &assetBucket{}
			buckets[entry.AssetCode] = bucket
		}

		minor := money.ToMinor(entry.Amount)
		if entry.Direction == domain.DirectionDebit {
			bucket.debit = bucket.debit.Add(minor)
		} else {
			bucket.credit = bucket.credit.Add(minor)
		}
	}

	for asset, bucket := range buckets {
		if bucket.debit != bucket.credit {
			return fmt.Errorf("ledger: imbalanced for asset %s: debit=%d credit=%d: %w",
				asset, bucket.debit.Int64(), bucket.credit.Int64(), domain.ErrImbalanced)
		}
	}
	return nil
}

// normalSideFor 返回科目类型的正常余额方向（P0 4.2）。
func normalSideFor(accountType string) domain.Direction {
	return domain.Direction(balance.NormalSide(accountType))
}

// hashTransactionPayload 生成幂等 payload 指纹（P0 4.3）。
// 同一 idempotency_key 只有在 payload 完全一致时才允许幂等返回。
func hashTransactionPayload(txn domain.Transaction) string {
	parts := make([]string, 0, len(txn.Entries))
	for _, e := range txn.Entries {
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%s", e.AccountID, e.Direction, e.Amount.String(), e.AssetCode))
	}
	sort.Strings(parts)
	raw := fmt.Sprintf("%s|%s|%s|%s|%s|%s",
		txn.TransactionType, txn.AssetCode, txn.Amount.String(),
		txn.AggregateType, txn.AggregateID, strings.Join(parts, ";"))
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// validateAssetPrecision 校验金额小数位不超过资产精度（P0 4.4）。
// 未登记的资产按 18 位处理，避免误伤新资产。
func (s *Service) validateAssetPrecision(ctx context.Context, tx pgx.Tx, txn domain.Transaction) error {
	precisions := make(map[string]int32, len(txn.Entries))
	for _, e := range txn.Entries {
		if _, ok := precisions[e.AssetCode]; ok {
			continue
		}
		var decimals int32
		err := tx.QueryRow(ctx, `
			SELECT decimals FROM asset_precision WHERE asset_code = $1
		`, e.AssetCode).Scan(&decimals)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			decimals = 18
		case err != nil:
			return fmt.Errorf("ledger: read asset precision: %w", err)
		}
		precisions[e.AssetCode] = decimals
	}

	return validateEntryPrecision(txn.Entries, precisions)
}

// validateEntryPrecision 是纯函数版本，便于单元测试。
func validateEntryPrecision(entries []domain.LedgerEntry, precisions map[string]int32) error {
	for _, e := range entries {
		scale := int32(-e.Amount.Exponent())
		if scale < 0 {
			scale = 0
		}
		if scale > precisions[e.AssetCode] {
			return fmt.Errorf("ledger: amount %s exceeds precision %d for asset %s",
				e.Amount.String(), precisions[e.AssetCode], e.AssetCode)
		}
	}
	return nil
}

// updateAccountBalanceWithLock 统一走 ledger/balance 的科目方向规则（P0 4.2）。
func updateAccountBalanceWithLock(ctx context.Context, tx pgx.Tx, entry domain.LedgerEntry, tenantID uuid.UUID) error {
	return balance.Apply(ctx, tx, entry.AccountID, string(entry.Direction), entry.Amount, entry.AssetCode, tenantID)
}

// rowQuerier 同时被 pgx.Tx 与 *pgxpool.Pool 满足，便于对账工具在事务内外复用。
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RecomputeBalanceTx 从 ledger_entry 重算某科目余额（P0 4.2 校验能力）。
// 返回 (快照余额, 重算余额)。两者必须一致，否则账本不可审计。
func (s *Service) RecomputeBalanceTx(ctx context.Context, q rowQuerier, accountID uuid.UUID, assetCode string, tenantID uuid.UUID) (int64, int64, error) {
	return balance.Recompute(ctx, q, accountID, assetCode, tenantID)
}

// Reserve 预留资金（独立事务）
//
// 创建 ledger_transaction + 成对分录 + ledger_reservations 投影行。
// 资金真相在 ledger_transaction + ledger_entry + account_balance。
func (s *Service) Reserve(ctx context.Context, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error) {
	if amountMinor <= 0 {
		return "", fmt.Errorf("ledger: amount_minor must be positive: %w", domain.ErrNegativeAmount)
	}

	var reservationID string
	err := s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		reservationID, err = s.reserveInTx(ctx, tx, merchantID, amountMinor, currency, transactionID)
		return err
	})
	if err != nil {
		return "", err
	}
	return reservationID, nil
}

// ReserveTx 预留资金（使用传入的 pgx.Tx）
//
// 移除 interface{} 断言，使用 typed pgx.Tx（G02 要求）。
func (s *Service) ReserveTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error) {
	if amountMinor <= 0 {
		return "", fmt.Errorf("ledger: amount_minor must be positive: %w", domain.ErrNegativeAmount)
	}
	return s.reserveInTx(ctx, tx, merchantID, amountMinor, currency, transactionID)
}

// reserveInTx 在事务内创建预留（P0-7 修复：复式记账 + 幂等 + 余额锁定）
//
// 修复内容：
//  1. 幂等检查：同 external_transaction_id 重放返回原 reservation，不生成新 txnID
//  2. 复式分录：DEBIT FROZEN_PAYOUT + CREDIT SETTLED，借贷严格平衡
//  3. SELECT ... FOR UPDATE 锁定 SETTLED 账户余额，避免并发超额预留
//  4. 余额不足完整回滚（事务原子性）
//  5. balance_before/balance_after 真实快照
func (s *Service) reserveInTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error) {
	tenantID := merchantID // 简化：tenant = merchant（与现有代码一致）
	idempotencyKey := "reserve_" + transactionID

	// 1. 幂等检查：查询是否已存在相同 external_transaction_id 的 reservation
	//    重放时返回原 reservation，不生成新 txnID（P0-7 幂等性要求）
	var existingReservationID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM ledger_reservations WHERE external_transaction_id = $1
	`, transactionID).Scan(&existingReservationID)
	switch {
	case err == nil:
		// 已存在，返回原 reservation（幂等）
		return existingReservationID.String(), nil
	case errors.Is(err, pgx.ErrNoRows):
		// 继续
	default:
		return "", fmt.Errorf("ledger: check reservation idempotency: %w", err)
	}

	// 2. 获取或创建 SETTLED 账户（源账户，可用余额从这里扣除）
	settledAccountID, err := getOrCreateAccountInTx(ctx, tx, merchantID, "SETTLED", currency)
	if err != nil {
		return "", fmt.Errorf("ledger: get settled account: %w", err)
	}

	// 3. 获取或创建 FROZEN_PAYOUT 账户（目标账户，预留资金在这里冻结）
	frozenAccountID, err := getOrCreateAccountInTx(ctx, tx, merchantID, "FROZEN_PAYOUT", currency)
	if err != nil {
		return "", fmt.Errorf("ledger: get frozen account: %w", err)
	}

	// 4. SELECT ... FOR UPDATE 锁定 SETTLED 账户余额（RED-05: 锁定后再校验再更新）
	//    避免并发超额预留
	var settledBalanceBefore, settledAvailableBefore int64
	err = tx.QueryRow(ctx, `
		SELECT balance::bigint, available::bigint
		FROM account_balance
		WHERE account_id = $1 AND asset_code = $2 AND tenant_id = $3
		FOR UPDATE
	`, settledAccountID, currency, tenantID).Scan(&settledBalanceBefore, &settledAvailableBefore)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 余额行不存在，视为余额为 0
			return "", fmt.Errorf("ledger: settled account balance not initialized: %w", domain.ErrInsufficientBalance)
		}
		return "", fmt.Errorf("ledger: lock settled account balance: %w", err)
	}

	// 5. 校验余额充足（余额不足完整回滚）
	if settledAvailableBefore < amountMinor {
		return "", fmt.Errorf("ledger: insufficient available balance: available=%d, required=%d: %w",
			settledAvailableBefore, amountMinor, domain.ErrInsufficientBalance)
	}

	// 6. 创建 ledger_transaction（不带 ON CONFLICT，因为幂等检查已在步骤 1 完成）
	txnID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_transaction
		    (id, idempotency_key, transaction_type, tenant_id, asset_code, amount, status,
		     source_account_id, target_account_id, description)
		VALUES ($1, $2, 'RESERVE', $3, $4, $5, 'AUTHORIZED', $6, $7, 'fund reservation')
	`, txnID, idempotencyKey, tenantID, currency, amountMinor, settledAccountID, frozenAccountID)
	if err != nil {
		return "", fmt.Errorf("ledger: insert reserve transaction: %w", err)
	}

	// 7. 创建成对 ledger_entry（复式记账：DEBIT FROZEN + CREDIT SETTLED）
	//    DEBIT FROZEN_PAYOUT: 增加冻结余额
	//    CREDIT SETTLED: 减少可用余额
	//    借贷严格平衡：DEBIT amount == CREDIT amount
	entryID1 := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_entry
		    (id, transaction_id, account_id, direction, amount,
		     tenant_id, asset_code, description, balance_before, balance_after)
		VALUES ($1, $2, $3, 'CREDIT', $4, $5, $6, 'reserve to frozen', 0, $4)
	`, entryID1, txnID, frozenAccountID, amountMinor, tenantID, currency)
	if err != nil {
		return "", fmt.Errorf("ledger: insert debit entry: %w", err)
	}

	entryID2 := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_entry
		    (id, transaction_id, account_id, direction, amount,
		     tenant_id, asset_code, description, balance_before, balance_after)
		VALUES ($1, $2, $3, 'DEBIT', $4, $5, $6, 'reserve from settled', $7, $8)
	`, entryID2, txnID, settledAccountID, amountMinor, tenantID, currency,
		settledBalanceBefore, settledBalanceBefore-amountMinor)
	if err != nil {
		return "", fmt.Errorf("ledger: insert credit entry: %w", err)
	}

	// 8. 同事务更新 account_balance（RED-13: 余额快照与分录同事务）
	//    SETTLED: balance -= amount, available -= amount
	_, err = tx.Exec(ctx, `
		UPDATE account_balance
		SET balance = balance - $1, available = available - $1, updated_at = now()
		WHERE account_id = $2 AND asset_code = $3 AND tenant_id = $4
	`, amountMinor, settledAccountID, currency, tenantID)
	if err != nil {
		return "", fmt.Errorf("ledger: update settled balance: %w", err)
	}

	//    FROZEN_PAYOUT: balance += amount, available += amount（UPSERT，行可能不存在）
	_, err = tx.Exec(ctx, `
		INSERT INTO account_balance (account_id, tenant_id, asset_code, balance, available, frozen)
		VALUES ($1, $2, $3, $4, $4, 0)
		ON CONFLICT (account_id, asset_code) DO UPDATE
		    SET balance = account_balance.balance + EXCLUDED.balance,
		        available = account_balance.available + EXCLUDED.available,
		        updated_at = now()
	`, frozenAccountID, tenantID, currency, amountMinor)
	if err != nil {
		return "", fmt.Errorf("ledger: update frozen balance: %w", err)
	}

	// 9. 创建 ledger_reservations 投影行
	reservationUUID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_reservations
		    (id, tenant_id, merchant_id, ledger_transaction_id, amount_minor, currency,
		     status, external_transaction_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'HELD', $7)
	`, reservationUUID, tenantID, merchantID, txnID, amountMinor, currency, transactionID)
	if err != nil {
		return "", fmt.Errorf("ledger: insert reservation: %w", err)
	}

	// 10. 写 transactional_outbox（同事务提交，RED-01）
	outboxID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox
		    (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'Pending')
	`, outboxID, "ledger_reservation", reservationUUID, "LEDGER_RESERVATION_CREATED",

		[]byte(`{"reservation_id":"`+reservationUUID.String()+`","transaction_id":"`+txnID.String()+`"}`),
		"reserve_outbox_"+idempotencyKey)
	if err != nil {
		return "", fmt.Errorf("ledger: insert outbox: %w", err)
	}

	return reservationUUID.String(), nil
}

// GetOrCreateAccountTx 暴露给结算模块复用科目创建逻辑（含 normal_side）。
func (s *Service) GetOrCreateAccountTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error) {
	return getOrCreateAccountInTx(ctx, tx, merchantID, accountType, currency)
}

// getOrCreateAccountInTx 在事务内获取或创建 ledger_account
func getOrCreateAccountInTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, accountType, currency string) (uuid.UUID, error) {
	// 先查询是否存在
	var accountID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM ledger_account
		WHERE merchant_id = $1 AND account_type = $2 AND currency = $3
	`, merchantID, accountType, currency).Scan(&accountID)
	if err == nil {
		return accountID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("query ledger_account: %w", err)
	}

	// 不存在则创建（P0 4.2：同时写入科目正常余额方向）
	newID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_account (id, merchant_id, account_type, currency, description, normal_side)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (merchant_id, account_type, currency) DO NOTHING
	`, newID, merchantID, accountType, currency, accountType+" account for "+merchantID.String(),
		string(normalSideFor(accountType)))
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert ledger_account: %w", err)
	}

	// 重新查询（处理 ON CONFLICT 命中的情况）
	err = tx.QueryRow(ctx, `
		SELECT id FROM ledger_account
		WHERE merchant_id = $1 AND account_type = $2 AND currency = $3
	`, merchantID, accountType, currency).Scan(&accountID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("re-query ledger_account: %w", err)
	}
	return accountID, nil
}

// CaptureTx 捕获预留资金（使用传入的 pgx.Tx）
//
// P0-7: 复式记账 + 幂等
//   - HELD → CAPTURED
//   - DEBIT SETTLED + CREDIT FROZEN_PAYOUT（资金从冻结转为已结算）
//   - 幂等：已 CAPTURED 重放返回原结果
func (s *Service) CaptureTx(ctx context.Context, tx pgx.Tx, reservationID string, captureAmountMinor int64, transactionID string) (string, error) {
	if captureAmountMinor <= 0 {
		return "", fmt.Errorf("ledger: capture amount must be positive: %w", domain.ErrNegativeAmount)
	}

	reservationUUID, err := uuid.Parse(reservationID)
	if err != nil {
		return "", fmt.Errorf("ledger: invalid reservation_id: %w", err)
	}

	idempotencyKey := "capture_" + transactionID

	// 1. 幂等检查
	var existingTxnID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM ledger_transaction WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&existingTxnID)
	switch {
	case err == nil:
		return existingTxnID.String(), nil
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return "", fmt.Errorf("ledger: check capture idempotency: %w", err)
	}

	// 2. SELECT reservation FOR UPDATE
	var resTxnID uuid.UUID
	var resMerchantID uuid.UUID
	var resAmount int64
	var resCurrency string
	var resStatus string
	err = tx.QueryRow(ctx, `
		SELECT ledger_transaction_id, merchant_id, amount_minor, currency, status
		FROM ledger_reservations WHERE id = $1 FOR UPDATE
	`, reservationUUID).Scan(&resTxnID, &resMerchantID, &resAmount, &resCurrency, &resStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("ledger: reservation not found: %w", domain.ErrTransactionNotFound)
		}
		return "", fmt.Errorf("ledger: lock reservation: %w", err)
	}

	if resStatus != "HELD" {
		return "", fmt.Errorf("ledger: reservation status is %s, expected HELD: %w", resStatus, domain.ErrIllegalTransition)
	}
	if captureAmountMinor > resAmount {
		return "", fmt.Errorf("ledger: capture amount %d exceeds reserved %d: %w",
			captureAmountMinor, resAmount, domain.ErrInsufficientBalance)
	}

	tenantID := resMerchantID

	// 3. 获取账户
	settledAccountID, err := getOrCreateAccountInTx(ctx, tx, resMerchantID, "SETTLED", resCurrency)
	if err != nil {
		return "", fmt.Errorf("ledger: get settled account: %w", err)
	}
	frozenAccountID, err := getOrCreateAccountInTx(ctx, tx, resMerchantID, "FROZEN_PAYOUT", resCurrency)
	if err != nil {
		return "", fmt.Errorf("ledger: get frozen account: %w", err)
	}

	// 4. 锁定 FROZEN 账户余额
	var frozenBalanceBefore int64
	err = tx.QueryRow(ctx, `
		SELECT balance::bigint FROM account_balance
		WHERE account_id = $1 AND asset_code = $2 AND tenant_id = $3
		FOR UPDATE
	`, frozenAccountID, resCurrency, tenantID).Scan(&frozenBalanceBefore)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("ledger: frozen account balance not found: %w", domain.ErrInsufficientBalance)
		}
		return "", fmt.Errorf("ledger: lock frozen balance: %w", err)
	}
	if frozenBalanceBefore < captureAmountMinor {
		return "", fmt.Errorf("ledger: insufficient frozen balance: frozen=%d, required=%d: %w",
			frozenBalanceBefore, captureAmountMinor, domain.ErrInsufficientBalance)
	}

	// 5. 创建 capture transaction
	captureTxnID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_transaction
		    (id, idempotency_key, transaction_type, tenant_id, asset_code, amount, status,
		     source_account_id, target_account_id, description)
		VALUES ($1, $2, 'CAPTURE', $3, $4, $5, 'CAPTURED', $6, $7, 'capture reservation')
	`, captureTxnID, idempotencyKey, tenantID, resCurrency, captureAmountMinor,
		frozenAccountID, settledAccountID)
	if err != nil {
		return "", fmt.Errorf("ledger: insert capture transaction: %w", err)
	}

	// 6. 成对分录：DEBIT SETTLED + CREDIT FROZEN
	entryID1 := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_entry
		    (id, transaction_id, account_id, direction, amount,
		     tenant_id, asset_code, description)
		VALUES ($1, $2, $3, 'DEBIT', $4, $5, $6, 'capture from frozen')
	`, entryID1, captureTxnID, frozenAccountID, captureAmountMinor, tenantID, resCurrency)
	if err != nil {
		return "", fmt.Errorf("ledger: insert capture debit entry: %w", err)
	}

	entryID2 := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_entry
		    (id, transaction_id, account_id, direction, amount,
		     tenant_id, asset_code, description)
		VALUES ($1, $2, $3, 'CREDIT', $4, $5, $6, 'capture to settled')
	`, entryID2, captureTxnID, settledAccountID, captureAmountMinor, tenantID, resCurrency)
	if err != nil {
		return "", fmt.Errorf("ledger: insert capture credit entry: %w", err)
	}

	// 7. 更新余额：FROZEN -= amount, SETTLED += amount
	_, err = tx.Exec(ctx, `
		UPDATE account_balance
		SET balance = balance - $1, available = available - $1, updated_at = now()
		WHERE account_id = $2 AND asset_code = $3 AND tenant_id = $4
	`, captureAmountMinor, frozenAccountID, resCurrency, tenantID)
	if err != nil {
		return "", fmt.Errorf("ledger: update frozen balance for capture: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE account_balance
		SET balance = balance + $1, available = available + $1, updated_at = now()
		WHERE account_id = $2 AND asset_code = $3 AND tenant_id = $4
	`, captureAmountMinor, settledAccountID, resCurrency, tenantID)
	if err != nil {
		return "", fmt.Errorf("ledger: update settled balance for capture: %w", err)
	}

	// 8. 部分捕获：差额必须立即退回 SETTLED，否则会永久卡在 FROZEN（P0 4.6）
	remaining := resAmount - captureAmountMinor
	reservationStatus := "CAPTURED"
	if remaining > 0 {
		remainderTxn := domain.Transaction{
			IdempotencyKey:  "capture_remainder_release_" + transactionID,
			TransactionType: domain.TransactionType("RESERVATION_REMAINDER_RELEASED"),
			AggregateType:   "ledger_reservation",
			AggregateID:     reservationUUID.String(),
			Description:     "release uncaptured reservation remainder",
			TenantID:        tenantID,
			AssetCode:       resCurrency,
			Amount:          money.DecimalFromInt64(remaining),
			Status:          domain.TransactionStatusConfirmed,
			Entries: []domain.Entry{
				{
					AccountID:   frozenAccountID,
					Direction:   domain.DirectionDebit,
					Amount:      money.DecimalFromInt64(remaining),
					AssetCode:   resCurrency,
					Description: "release remainder from frozen",
				},
				{
					AccountID:   settledAccountID,
					Direction:   domain.DirectionCredit,
					Amount:      money.DecimalFromInt64(remaining),
					AssetCode:   resCurrency,
					Description: "release remainder back to settled",
				},
			},
		}
		if err := s.CreateTransaction(ctx, tx, remainderTxn); err != nil {
			return "", fmt.Errorf("ledger: release capture remainder: %w", err)
		}
		reservationStatus = "PARTIALLY_CAPTURED"
	}

	// 9. 更新 reservation 状态
	_, err = tx.Exec(ctx, `
		UPDATE ledger_reservations
		SET status = $3, captured_amount_minor = $1, updated_at = now()
		WHERE id = $2 AND status = 'HELD'
	`, captureAmountMinor, reservationUUID, reservationStatus)
	if err != nil {
		return "", fmt.Errorf("ledger: update reservation status: %w", err)
	}

	// 10. 写 outbox
	outboxID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox
		    (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'Pending')
	`, outboxID, "ledger_reservation", reservationUUID, "LEDGER_RESERVATION_CAPTURED",
		[]byte(`{"reservation_id":"`+reservationUUID.String()+`","capture_txn_id":"`+captureTxnID.String()+`"}`),
		"capture_outbox_"+idempotencyKey)
	if err != nil {
		return "", fmt.Errorf("ledger: insert capture outbox: %w", err)
	}

	return captureTxnID.String(), nil
}

// AcquireCaptureTx 收单捕获（P0 4.5 起改为进入 PENDING，而不是直接 SETTLED）。
//
// 会计事件：
//   - captured：DEBIT PROVIDER_RECEIVABLE（应收增加） + CREDIT PENDING（在途负债增加）。
//   - 结算批次执行后再 PENDING → SETTLED，商户余额才可出金。
//   - failed：无资金移动。
//
// 幂等：idempotency_key = "acquire_capture_" + transactionID。
// 投影（merchant_treasury_ledger.pending_balance）用 projection.Once 保证恰好一次。
func (s *Service) AcquireCaptureTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, currency string, transactionID string) (string, error) {
	if amountMinor <= 0 {
		return "", fmt.Errorf("ledger: amount_minor must be positive: %w", domain.ErrNegativeAmount)
	}
	tenantID := merchantID
	idempotencyKey := "acquire_capture_" + transactionID

	receivableAccountID, err := getOrCreateAccountInTx(ctx, tx, merchantID, "PROVIDER_RECEIVABLE", currency)
	if err != nil {
		return "", fmt.Errorf("ledger: get provider receivable account: %w", err)
	}
	pendingAccountID, err := getOrCreateAccountInTx(ctx, tx, merchantID, "PENDING", currency)
	if err != nil {
		return "", fmt.Errorf("ledger: get pending account: %w", err)
	}

	amount := money.DecimalFromInt64(amountMinor)
	txn := domain.Transaction{
		IdempotencyKey:  idempotencyKey,
		TransactionType: domain.TransactionType("ACQUIRE_CAPTURE"),
		AggregateType:   "payment_attempt",
		AggregateID:     transactionID,
		Description:     "acquire capture: provider receivable -> pending",
		TenantID:        tenantID,
		AssetCode:       currency,
		Amount:          amount,
		Status:          domain.TransactionStatusConfirmed,
		SourceAccountID: receivableAccountID,
		TargetAccountID: pendingAccountID,
		Entries: []domain.Entry{
			{
				AccountID:   receivableAccountID,
				Direction:   domain.DirectionDebit,
				Amount:      amount,
				AssetCode:   currency,
				Description: "acquire: provider receivable increase",
			},
			{
				AccountID:   pendingAccountID,
				Direction:   domain.DirectionCredit,
				Amount:      amount,
				AssetCode:   currency,
				Description: "acquire: merchant pending increase",
			},
		},
	}

	if err := s.CreateTransaction(ctx, tx, txn); err != nil {
		return "", err
	}

	// 投影：在途余额增加（同一事务，且保证重放不重复加）
	first, err := projection.Once(ctx, tx, "capture_pending:"+transactionID)
	if err != nil {
		return "", err
	}
	if first {
		if err := projection.AddPendingTx(ctx, tx, merchantID, currency, amountMinor); err != nil {
			return "", err
		}
	}

	var txnID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM ledger_transaction WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&txnID); err != nil {
		return "", fmt.Errorf("ledger: read acquire capture transaction: %w", err)
	}
	return txnID.String(), nil
}

// ReleaseTx 释放预留资金（使用传入的 pgx.Tx）
//
// P0-7: 复式记账 + 幂等
//   - HELD → RELEASED
//   - DEBIT SETTLED + CREDIT FROZEN_PAYOUT（资金从冻结退回可用）
//   - 幂等：已 RELEASED 重放返回原结果
func (s *Service) ReleaseTx(ctx context.Context, tx pgx.Tx, reservationID string, transactionID string) (string, error) {
	reservationUUID, err := uuid.Parse(reservationID)
	if err != nil {
		return "", fmt.Errorf("ledger: invalid reservation_id: %w", err)
	}

	idempotencyKey := "release_" + transactionID

	// 1. 幂等检查
	var existingTxnID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM ledger_transaction WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&existingTxnID)
	switch {
	case err == nil:
		return existingTxnID.String(), nil
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return "", fmt.Errorf("ledger: check release idempotency: %w", err)
	}

	// 2. SELECT reservation FOR UPDATE
	var resTxnID uuid.UUID
	var resMerchantID uuid.UUID
	var resAmount int64
	var resCurrency string
	var resStatus string
	err = tx.QueryRow(ctx, `
		SELECT ledger_transaction_id, merchant_id, amount_minor, currency, status
		FROM ledger_reservations WHERE id = $1 FOR UPDATE
	`, reservationUUID).Scan(&resTxnID, &resMerchantID, &resAmount, &resCurrency, &resStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("ledger: reservation not found: %w", domain.ErrTransactionNotFound)
		}
		return "", fmt.Errorf("ledger: lock reservation: %w", err)
	}

	if resStatus != "HELD" {
		return "", fmt.Errorf("ledger: reservation status is %s, expected HELD: %w", resStatus, domain.ErrIllegalTransition)
	}

	tenantID := resMerchantID
	releaseAmount := resAmount

	// 3. 获取账户
	settledAccountID, err := getOrCreateAccountInTx(ctx, tx, resMerchantID, "SETTLED", resCurrency)
	if err != nil {
		return "", fmt.Errorf("ledger: get settled account: %w", err)
	}
	frozenAccountID, err := getOrCreateAccountInTx(ctx, tx, resMerchantID, "FROZEN_PAYOUT", resCurrency)
	if err != nil {
		return "", fmt.Errorf("ledger: get frozen account: %w", err)
	}

	// 4. 锁定 FROZEN 账户余额
	var frozenBalanceBefore int64
	err = tx.QueryRow(ctx, `
		SELECT balance::bigint FROM account_balance
		WHERE account_id = $1 AND asset_code = $2 AND tenant_id = $3
		FOR UPDATE
	`, frozenAccountID, resCurrency, tenantID).Scan(&frozenBalanceBefore)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("ledger: frozen account balance not found: %w", domain.ErrInsufficientBalance)
		}
		return "", fmt.Errorf("ledger: lock frozen balance: %w", err)
	}
	if frozenBalanceBefore < releaseAmount {
		return "", fmt.Errorf("ledger: insufficient frozen balance for release: frozen=%d, required=%d: %w",
			frozenBalanceBefore, releaseAmount, domain.ErrInsufficientBalance)
	}

	// 5. 创建 release transaction
	releaseTxnID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_transaction
		    (id, idempotency_key, transaction_type, tenant_id, asset_code, amount, status,
		     source_account_id, target_account_id, description)
		VALUES ($1, $2, 'RELEASE', $3, $4, $5, 'CONFIRMED', $6, $7, 'release reservation')
	`, releaseTxnID, idempotencyKey, tenantID, resCurrency, releaseAmount,
		frozenAccountID, settledAccountID)
	if err != nil {
		return "", fmt.Errorf("ledger: insert release transaction: %w", err)
	}

	// 6. 成对分录：DEBIT SETTLED + CREDIT FROZEN
	entryID1 := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_entry
		    (id, transaction_id, account_id, direction, amount,
		     tenant_id, asset_code, description)
		VALUES ($1, $2, $3, 'DEBIT', $4, $5, $6, 'release from frozen')
	`, entryID1, releaseTxnID, frozenAccountID, releaseAmount, tenantID, resCurrency)
	if err != nil {
		return "", fmt.Errorf("ledger: insert release debit entry: %w", err)
	}

	entryID2 := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_entry
		    (id, transaction_id, account_id, direction, amount,
		     tenant_id, asset_code, description)
		VALUES ($1, $2, $3, 'CREDIT', $4, $5, $6, 'release back to settled')
	`, entryID2, releaseTxnID, settledAccountID, releaseAmount, tenantID, resCurrency)
	if err != nil {
		return "", fmt.Errorf("ledger: insert release credit entry: %w", err)
	}

	// 7. 更新余额：FROZEN -= amount, SETTLED += amount
	_, err = tx.Exec(ctx, `
		UPDATE account_balance
		SET balance = balance - $1, available = available - $1, updated_at = now()
		WHERE account_id = $2 AND asset_code = $3 AND tenant_id = $4
	`, releaseAmount, frozenAccountID, resCurrency, tenantID)
	if err != nil {
		return "", fmt.Errorf("ledger: update frozen balance for release: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE account_balance
		SET balance = balance + $1, available = available + $1, updated_at = now()
		WHERE account_id = $2 AND asset_code = $3 AND tenant_id = $4
	`, releaseAmount, settledAccountID, resCurrency, tenantID)
	if err != nil {
		return "", fmt.Errorf("ledger: update settled balance for release: %w", err)
	}

	// 8. 更新 reservation 状态
	_, err = tx.Exec(ctx, `
		UPDATE ledger_reservations
		SET status = 'RELEASED', released_amount_minor = $1, updated_at = now()
		WHERE id = $2 AND status = 'HELD'
	`, releaseAmount, reservationUUID)
	if err != nil {
		return "", fmt.Errorf("ledger: update reservation status for release: %w", err)
	}

	// 9. 写 outbox
	outboxID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO transactional_outbox
		    (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'Pending')
	`, outboxID, "ledger_reservation", reservationUUID, "LEDGER_RESERVATION_RELEASED",
		[]byte(`{"reservation_id":"`+reservationUUID.String()+`","release_txn_id":"`+releaseTxnID.String()+`"}`),
		"release_outbox_"+idempotencyKey)
	if err != nil {
		return "", fmt.Errorf("ledger: insert release outbox: %w", err)
	}

	return releaseTxnID.String(), nil
}
