package issuing

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

// ============================================================================
// P7: 发卡/归集接入新账本
//
// 科目设计（均挂在商户维度，最小单位整数语义，均为负债类，贷方增加）：
//   - ISSUING_POOL     ：已从可出金余额划拨到卡片额度的资金（商户资金子科目）。
//   - ISSUING_CLEARING ：卡片实际消费后应付卡组织/发卡行的款项。
//
// 分录：
//   授权冻结  Dr SETTLED        / Cr ISSUING_POOL
//   授权撤销  Dr ISSUING_POOL   / Cr SETTLED
//   清算入账  Dr ISSUING_POOL   / Cr ISSUING_CLEARING
//
// 幂等：以 authRef（授权唯一引用）作为幂等键后缀，重复回调不重复记账。
// ============================================================================

// LedgerPoster 发卡账本过账器。
type LedgerPoster struct {
	ledger *ledgerservice.Service
	txm    *postgres.TxManager
}

// NewLedgerPoster 创建发卡账本过账器。
func NewLedgerPoster(ledger *ledgerservice.Service, txm *postgres.TxManager) *LedgerPoster {
	return &LedgerPoster{ledger: ledger, txm: txm}
}

// MinorUnits 将金额转成最小货币单位（与结算/账本口径一致）。
func MinorUnits(amount decimal.Decimal) int64 {
	return amount.Mul(decimal.NewFromInt(100)).IntPart()
}

// AuthorizeHold 授权冻结：SETTLED → ISSUING_POOL。
func (p *LedgerPoster) AuthorizeHold(ctx context.Context, merchantID uuid.UUID, currency string,
	amountMinor int64, authRef string) error {
	return p.move(ctx, merchantID, currency, amountMinor, authRef,
		"SETTLED", "ISSUING_POOL", "issuing_hold", "issuing: authorize hold")
}

// ReleaseHold 授权撤销/过期：ISSUING_POOL → SETTLED。
func (p *LedgerPoster) ReleaseHold(ctx context.Context, merchantID uuid.UUID, currency string,
	amountMinor int64, authRef string) error {
	return p.move(ctx, merchantID, currency, amountMinor, authRef,
		"ISSUING_POOL", "SETTLED", "issuing_release", "issuing: release hold")
}

// ClearAuthorization 清算：ISSUING_POOL → ISSUING_CLEARING。
func (p *LedgerPoster) ClearAuthorization(ctx context.Context, merchantID uuid.UUID, currency string,
	amountMinor int64, authRef string) error {
	return p.move(ctx, merchantID, currency, amountMinor, authRef,
		"ISSUING_POOL", "ISSUING_CLEARING", "issuing_clear", "issuing: clear authorization")
}

// move 在单个账本事务内完成 from(DEBIT) → to(CREDIT)。
func (p *LedgerPoster) move(ctx context.Context, merchantID uuid.UUID, currency string,
	amountMinor int64, authRef, fromType, toType, keyPrefix, description string) error {
	if amountMinor <= 0 {
		return fmt.Errorf("issuing ledger: amount must be positive, got %d", amountMinor)
	}
	if authRef == "" {
		return fmt.Errorf("issuing ledger: auth reference is required")
	}
	if p.ledger == nil || p.txm == nil {
		return fmt.Errorf("issuing ledger: ledger poster not configured")
	}

	return p.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		fromID, err := p.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, fromType, currency)
		if err != nil {
			return fmt.Errorf("issuing ledger: get %s account: %w", fromType, err)
		}
		toID, err := p.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, toType, currency)
		if err != nil {
			return fmt.Errorf("issuing ledger: get %s account: %w", toType, err)
		}

		amount := money.DecimalFromInt64(amountMinor)
		return p.ledger.CreateTransaction(ctx, tx, domain.Transaction{
			IdempotencyKey:  keyPrefix + "_" + authRef,
			TransactionType: domain.TransactionTypeTransfer,
			AggregateType:   "issuing_authorization",
			AggregateID:     authRef,
			Description:     description,
			TenantID:        merchantID,
			AssetCode:       currency,
			Amount:          amount,
			Status:          domain.TransactionStatusConfirmed,
			SourceAccountID: fromID,
			TargetAccountID: toID,
			Entries: []domain.Entry{
				{
					AccountID:   fromID,
					Direction:   domain.DirectionDebit,
					Amount:      amount,
					AssetCode:   currency,
					Description: description,
				},
				{
					AccountID:   toID,
					Direction:   domain.DirectionCredit,
					Amount:      amount,
					AssetCode:   currency,
					Description: description,
				},
			},
		})
	})
}
