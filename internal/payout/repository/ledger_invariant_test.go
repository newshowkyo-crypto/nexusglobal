// ledger_invariant_test.go — SEC-005 证据测试：旧账本入口强制平衡分录。
//
// ValidateBalanced 在任何 DB 访问之前执行，因此可用零值 Repository 验证
// 空分录/不平衡分录被拒绝（fail fast），不需要真实数据库。
package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
)

func TestLedgerRepo_PostTransaction_RejectsEmptyEntries(t *testing.T) {
	// 零值 repo：校验发生在 DB 访问前，nil pool 不会panic。
	r := &LedgerRepo{}
	_, err := r.PostTransaction(context.Background(), nil, ledgerdomain.Transaction{
		IdempotencyKey:  "sec005-empty-entries",
		TransactionType: "PAYOUT_CONFIRM",
		AggregateType:   "payout_request",
		Amount:          decimal.NewFromInt(100),
	})
	assert.Error(t, err, "empty-entries transaction must be rejected (SEC-005)")
	assert.ErrorIs(t, err, ledgerdomain.ErrNoEntries)
}

func TestLedgerRepo_PostTransaction_RejectsUnbalancedEntries(t *testing.T) {
	r := &LedgerRepo{}
	amount := decimal.NewFromInt(100)
	_, err := r.PostTransaction(context.Background(), nil, ledgerdomain.Transaction{
		IdempotencyKey:  "sec005-unbalanced",
		TransactionType: "PAYOUT_CONFIRM",
		Amount:          amount,
		Entries: []ledgerdomain.Entry{
			{AccountID: uuid.Nil, Direction: ledgerdomain.DirectionDebit, Amount: amount},
			{AccountID: uuid.Nil, Direction: ledgerdomain.DirectionCredit, Amount: amount.Sub(decimal.NewFromInt(1))},
		},
	})
	assert.Error(t, err, "unbalanced entries must be rejected (SEC-005)")
	assert.ErrorIs(t, err, ledgerdomain.ErrImbalanced)
}
