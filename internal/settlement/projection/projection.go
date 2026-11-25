// Package projection 维护 merchant_treasury_ledger 读模型（P0 4.5）。
//
// 设计约束：
//   - 账本（ledger_transaction / ledger_entry / account_balance）是唯一资金真相。
//   - merchant_treasury_ledger 只是投影，供商户余额查询与出金使用。
//   - capture / settlement / payout 在该投影上的写入必须与账本分录处于同一 DB 事务，
//     并且只能通过本包的函数完成，禁止业务代码直接 UPDATE。
package projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrInsufficientProjection 表示投影余额不足（通常意味着账本与投影不一致）。
var ErrInsufficientProjection = errors.New("projection: insufficient balance")

// Once 保证同一 marker 的投影只应用一次（账本幂等重放时使用）。
// 返回 true 表示本次是首次应用，调用方应执行投影写入。
func Once(ctx context.Context, tx pgx.Tx, marker string) (bool, error) {
	var applied string
	err := tx.QueryRow(ctx, `
		INSERT INTO projection_applied (marker) VALUES ($1)
		ON CONFLICT (marker) DO NOTHING
		RETURNING marker
	`, marker).Scan(&applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("projection: claim marker %q: %w", marker, err)
	}
	return true, nil
}

func ensureRow(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO merchant_treasury_ledger (merchant_id, currency)
		VALUES ($1, $2)
		ON CONFLICT (merchant_id, currency) DO NOTHING
	`, merchantID, currency)
	if err != nil {
		return fmt.Errorf("projection: ensure ledger row: %w", err)
	}
	return nil
}

func apply(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string,
	pending, settled, frozen, reserve, dispute int64) error {
	if err := ensureRow(ctx, tx, merchantID, currency); err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE merchant_treasury_ledger
		SET pending_balance        = pending_balance + $3,
		    settled_balance        = settled_balance + $4,
		    frozen_payout_balance  = frozen_payout_balance + $5,
		    reserve_balance        = reserve_balance + $6,
		    dispute_hold_balance   = dispute_hold_balance + $7,
		    available_payout_balance = (settled_balance + $4)
		                             - (frozen_payout_balance + $5)
		                             - (reserve_balance + $6)
		                             - (dispute_hold_balance + $7),
		    version                = version + 1,
		    updated_at             = now()
		WHERE merchant_id = $1 AND currency = $2
		  AND pending_balance + $3 >= 0
		  AND settled_balance + $4 >= 0
		  AND frozen_payout_balance + $5 >= 0
		  AND reserve_balance + $6 >= 0
		  AND dispute_hold_balance + $7 >= 0
	`, merchantID, currency, pending, settled, frozen, reserve, dispute)
	if err != nil {
		return fmt.Errorf("projection: apply delta: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("projection: merchant=%s currency=%s delta(pending=%d settled=%d frozen=%d): %w",
			merchantID, currency, pending, settled, frozen, ErrInsufficientProjection)
	}
	return nil
}

// AddPendingTx 收单捕获：在途资金增加。
func AddPendingTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, amountMinor, 0, 0, 0, 0)
}

// MovePendingToSettledTx 结算结转：在途 → 可出金。
func MovePendingToSettledTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, -amountMinor, amountMinor, 0, 0, 0)
}

// MoveSettledToFrozenTx 出金冻结：可出金 → 冻结。
func MoveSettledToFrozenTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, 0, -amountMinor, amountMinor, 0, 0)
}

// MovePendingToSettledWithFeeTx 结算结转并扣手续费：
// 在途减少 total，可出金增加 net（total - fee）。
func MovePendingToSettledWithFeeTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID,
	currency string, totalMinor, netMinor int64) error {
	return apply(ctx, tx, merchantID, currency, -totalMinor, netMinor, 0, 0, 0)
}

// MovePendingToSettledWithFeeAndReserveTx 结算结转并扣手续费、计提准备金：
// 在途减少 total，可出金增加 net - reserveShare，准备金增加 reserveShare。
// reserveShare = 0 时退化为 MovePendingToSettledWithFeeTx。
func MovePendingToSettledWithFeeAndReserveTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID,
	currency string, totalMinor, netMinor, reserveShareMinor int64) error {
	return apply(ctx, tx, merchantID, currency, -totalMinor, netMinor-reserveShareMinor, 0, reserveShareMinor, 0)
}

// MoveFrozenToSettledTx 出金失败退回：冻结 → 可出金。
func MoveFrozenToSettledTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, 0, amountMinor, -amountMinor, 0, 0)
}

// RemoveFrozenTx 出金成功：冻结清零。
func RemoveFrozenTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, 0, 0, -amountMinor, 0, 0)
}

// RemoveSettledTx 退款：商户可出金余额减少（回退给买家）。
func RemoveSettledTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, 0, -amountMinor, 0, 0, 0)
}

// MoveSettledToPendingTx 归集/桥接发起：可出金 → 在途。
func MoveSettledToPendingTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, amountMinor, -amountMinor, 0, 0, 0)
}

// RemovePendingTx 在途资金出账（桥接到发卡池）：在途清零。
func RemovePendingTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, -amountMinor, 0, 0, 0, 0)
}

// AddSettledTx 可出金余额增加（归集回流等）。
func AddSettledTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, 0, amountMinor, 0, 0, 0)
}

// MoveSettledToReserveTx 准备金占用：可出金 → 准备金（不再计入可出金）。
func MoveSettledToReserveTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, 0, -amountMinor, 0, amountMinor, 0)
}

// MoveReserveToSettledTx 准备金释放：准备金 → 可出金。
func MoveReserveToSettledTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, 0, amountMinor, 0, -amountMinor, 0)
}

// MoveSettledToDisputeTx 争议冻结：可出金 → 争议冻结。
func MoveSettledToDisputeTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string, amountMinor int64) error {
	return apply(ctx, tx, merchantID, currency, 0, -amountMinor, 0, 0, amountMinor)
}
