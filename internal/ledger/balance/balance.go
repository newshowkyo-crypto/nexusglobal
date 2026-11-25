// Package balance 统一账本科目余额的增减规则（P0 4.2）。
//
// 规则：每个科目有 normal_side（DEBIT / CREDIT），分录方向等于 normal_side 时余额增加，
// 否则减少。商户资金类科目（SETTLED / PENDING / FROZEN_PAYOUT / RESERVE）是平台对商户的
// 负债，normal_side = CREDIT；应收与在途资产类科目 normal_side = DEBIT。
package balance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// NormalSide 返回科目类型的正常余额方向。
//
// 负债/应付类（SETTLED、PENDING、PARTNER_PAYABLE、ISSUING_POOL、ISSUING_CLEARING 等）在贷方；
// 资产类（PROVIDER_RECEIVABLE 等）在借方，未列出的科目一律按资产处理。
//
// 发卡池模型：
//   - ISSUING_POOL 是商户资金的子科目（负债），授权时从 SETTLED 划入；
//   - ISSUING_CLEARING 是应付卡组织/发卡行的负债，清算时从 ISSUING_POOL 划入。
func NormalSide(accountType string) string {
	switch strings.ToUpper(accountType) {
	case "SETTLED", "PENDING", "FROZEN_PAYOUT", "FEE", "PLATFORM_FEE",
		"PARTNER_PAYABLE", "INTERCOMPANY_PAYABLE", "LP_PAYABLE",
		"RESERVE", "DISPUTE_HOLD", "MERCHANT_RESERVE",
		"ISSUING_POOL", "ISSUING_CLEARING", "VCC_BRIDGE_PENDING":
		return "CREDIT"
	default:
		return "DEBIT"
	}
}

// Apply 在事务内锁定并更新科目余额快照。
// amount 使用最小单位整数语义（与 ledger_entry.amount 一致）。
func Apply(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, direction string,
	amount decimal.Decimal, assetCode string, tenantID uuid.UUID) error {
	var normalSide string
	err := tx.QueryRow(ctx, `
		SELECT normal_side FROM ledger_account WHERE id = $1
	`, accountID).Scan(&normalSide)
	if err != nil {
		return fmt.Errorf("balance: read account normal side: %w", err)
	}

	// 兼容只传 accountID 的历史调用方：从科目解析 asset/tenant
	if assetCode == "" || tenantID == uuid.Nil {
		var merchantID uuid.UUID
		var currency string
		if err := tx.QueryRow(ctx, `
			SELECT merchant_id, currency FROM ledger_account WHERE id = $1
		`, accountID).Scan(&merchantID, &currency); err != nil {
			return fmt.Errorf("balance: resolve account asset/tenant: %w", err)
		}
		if assetCode == "" {
			assetCode = currency
		}
		if tenantID == uuid.Nil {
			tenantID = merchantID
		}
	}

	delta := amount.IntPart()
	if strings.EqualFold(direction, normalSide) {
		// 方向与正常余额一致 → 增加
	} else {
		delta = -delta
	}

	var currentBalance, currentAvailable int64
	err = tx.QueryRow(ctx, `
		SELECT balance::bigint, available::bigint
		FROM account_balance
		WHERE account_id = $1 AND asset_code = $2 AND tenant_id = $3
		FOR UPDATE
	`, accountID, assetCode, tenantID).Scan(&currentBalance, &currentAvailable)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("balance: lock account_balance: %w", err)
		}
		if delta < 0 {
			return fmt.Errorf("balance: insufficient balance for new account")
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO account_balance
			    (account_id, tenant_id, asset_code, balance, available, frozen)
			VALUES ($1, $2, $3, $4, $4, 0)
		`, accountID, tenantID, assetCode, delta)
		if err != nil {
			return fmt.Errorf("balance: insert initial account_balance: %w", err)
		}
		return nil
	}

	newBalance := currentBalance + delta
	newAvailable := currentAvailable + delta
	if newBalance < 0 || newAvailable < 0 {
		return fmt.Errorf("balance: insufficient available balance: current=%d delta=%d",
			currentAvailable, delta)
	}

	_, err = tx.Exec(ctx, `
		UPDATE account_balance
		SET balance = $1, available = $2, updated_at = now()
		WHERE account_id = $3 AND asset_code = $4 AND tenant_id = $5
	`, newBalance, newAvailable, accountID, assetCode, tenantID)
	if err != nil {
		return fmt.Errorf("balance: update account_balance: %w", err)
	}
	return nil
}

// Recompute 从分录重算余额（用于对账）。
func Recompute(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, accountID uuid.UUID, assetCode string, tenantID uuid.UUID) (int64, int64, error) {
	var snapshot int64
	err := q.QueryRow(ctx, `
		SELECT balance::bigint FROM account_balance
		WHERE account_id = $1 AND asset_code = $2 AND tenant_id = $3
	`, accountID, assetCode, tenantID).Scan(&snapshot)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, fmt.Errorf("balance: read snapshot: %w", err)
		}
		snapshot = 0
	}

	var recomputed int64
	err = q.QueryRow(ctx, `
		SELECT COALESCE(SUM(
		    CASE WHEN e.direction = a.normal_side THEN e.amount ELSE -e.amount END
		), 0)::bigint
		FROM ledger_entry e
		JOIN ledger_account a ON a.id = e.account_id
		WHERE e.account_id = $1 AND e.asset_code = $2 AND e.tenant_id = $3
	`, accountID, assetCode, tenantID).Scan(&recomputed)
	if err != nil {
		return 0, 0, fmt.Errorf("balance: recompute: %w", err)
	}
	return snapshot, recomputed, nil
}
