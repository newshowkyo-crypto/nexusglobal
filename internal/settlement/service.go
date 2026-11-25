// Package settlement 负责 A 面收单到 B 面可出金的结转（P0 4.5）。
//
// 资金流：
//
//	capture  → PENDING（在途，不可出金）
//	settle   → SETTLED（可出金），同时更新 merchant_treasury_ledger 投影
//	payout   → FROZEN_PAYOUT → 外部出金
package settlement

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	merchantwebhook "github.com/nexusglobal/mamr/internal/merchantportal/webhook"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/settlement/projection"
)

const maxItemsPerBatch = 1000

// Service 结算结转服务。
type Service struct {
	pool   *pgxpool.Pool
	txm    *postgres.TxManager
	ledger *ledgerservice.Service
	fees   FeeEngine
	splits SplitEngine
}

func New(pool *pgxpool.Pool, txm *postgres.TxManager, ledger *ledgerservice.Service) *Service {
	return &Service{pool: pool, txm: txm, ledger: ledger}
}

// Result 单次结算结果。
type Result struct {
	BatchID      string `json:"batch_id"`
	MerchantID   string `json:"merchant_id"`
	Currency     string `json:"currency"`
	ItemCount    int    `json:"item_count"`
	TotalMinor   int64  `json:"total_amount_minor"`
	FeeMinor     int64  `json:"fee_amount_minor"`
	SplitMinor   int64  `json:"split_amount_minor"`
	ReserveMinor int64  `json:"reserve_amount_minor"`
	NetMinor     int64  `json:"net_amount_minor"`
}

type item struct {
	ID          uuid.UUID
	AmountMinor int64
	ChannelID   uuid.UUID
}

// FeeEngine 结算时计费（可选）。由 internal/fee.Repository 实现。
type FeeEngine interface {
	ChargeTx(ctx context.Context, tx pgx.Tx, attemptID, merchantID uuid.UUID,
		currency string, channelID uuid.UUID, baseAmount int64) (int64, error)
}

// SplitEngine 结算时分账（可选）。由 internal/split.Repository 实现。
type SplitEngine interface {
	AllocateTx(ctx context.Context, tx pgx.Tx, attemptID, merchantID uuid.UUID,
		currency string, batchID uuid.UUID, baseMinor int64) (int64, error)
}

// SetFeeEngine 注入计费引擎；未注入时手续费为 0。
func (s *Service) SetFeeEngine(f FeeEngine) { s.fees = f }

// SetSplitEngine 注入分账引擎；未注入时分账为 0。
func (s *Service) SetSplitEngine(sp SplitEngine) { s.splits = sp }

// SettleMerchant 将某商户某币种的已捕获（未结算）尝试结转至 SETTLED。
// 幂等：批次按 (merchant, currency, business_date) 唯一；item 按 attempt 唯一。
func (s *Service) SettleMerchant(ctx context.Context, merchantID uuid.UUID, currency string, businessDate time.Time) (*Result, error) {
	res := &Result{MerchantID: merchantID.String(), Currency: currency}
	dateOnly := businessDate.UTC().Truncate(24 * time.Hour)

	err := s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id, a.amount_minor, a.acquirer_channel_id
			FROM payment_attempts a
			LEFT JOIN pool_channel pc ON pc.id = a.pool_channel_id
			WHERE a.merchant_id = $1
			  AND a.currency = $2
			  AND a.settled_at IS NULL
			  AND a.status IN ('CAPTURED', 'SETTLED')
			  -- P5: 按通道结算周期（settlement_days）控制可结算日；未配置通道为 T+0
			  AND COALESCE(a.captured_at, a.created_at)
			      <= now() - make_interval(days => COALESCE(pc.settlement_days, 0))
			ORDER BY a.created_at
			LIMIT $3
			FOR UPDATE OF a SKIP LOCKED
		`, merchantID, currency, maxItemsPerBatch)
		if err != nil {
			return fmt.Errorf("settlement: select attempts: %w", err)
		}
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.ID, &it.AmountMinor, &it.ChannelID); err != nil {
				rows.Close()
				return fmt.Errorf("settlement: scan attempt: %w", err)
			}
			items = append(items, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("settlement: iterate attempts: %w", err)
		}
		if len(items) == 0 {
			return nil
		}

		var total int64
		for _, it := range items {
			total += it.AmountMinor
		}

		batchID, err := upsertBatch(ctx, tx, merchantID, currency, dateOnly, total, len(items))
		if err != nil {
			return err
		}
		res.BatchID = batchID.String()
		res.ItemCount = len(items)
		res.TotalMinor = total

		for _, it := range items {
			if _, err := tx.Exec(ctx, `
				INSERT INTO settlement_item (batch_id, payment_attempt_id, merchant_id, currency, amount_minor, status)
				VALUES ($1, $2, $3, $4, $5, 'SETTLED')
				ON CONFLICT (payment_attempt_id) DO NOTHING
			`, batchID, it.ID, merchantID, currency, it.AmountMinor); err != nil {
				return fmt.Errorf("settlement: insert item: %w", err)
			}
		}

		if _, err := tx.Exec(ctx, `
			UPDATE payment_attempts
			SET settled_at = now(), updated_at = now()
			WHERE merchant_id = $1 AND currency = $2
			  AND settled_at IS NULL
			  AND id = ANY($3)
		`, merchantID, currency, ids(items)); err != nil {
			return fmt.Errorf("settlement: mark attempts settled: %w", err)
		}

		// 计费（可选）：逐笔落 fee_record，汇总后从结算额中扣除
		itemFees := make([]int64, len(items))
		var totalFee int64
		if s.fees != nil {
			for i, it := range items {
				f, ferr := s.fees.ChargeTx(ctx, tx, it.ID, merchantID, currency, it.ChannelID, it.AmountMinor)
				if ferr != nil {
					return fmt.Errorf("settlement: charge fee: %w", ferr)
				}
				itemFees[i] = f
				totalFee += f
			}
		}
		if totalFee > total {
			totalFee = total
		}
		netAfterFee := total - totalFee

		// 分账（可选）：按每笔净额分配给合作方
		var totalSplit int64
		if s.splits != nil {
			for i, it := range items {
				itemNet := it.AmountMinor - itemFees[i]
				if itemNet < 0 {
					itemNet = 0
				}
				alloc, serr := s.splits.AllocateTx(ctx, tx, it.ID, merchantID, currency, batchID, itemNet)
				if serr != nil {
					return fmt.Errorf("settlement: allocate split: %w", serr)
				}
				totalSplit += alloc
			}
		}
		if totalSplit > netAfterFee {
			totalSplit = netAfterFee
		}
		net := netAfterFee - totalSplit

		// 计提准备金：按投影行上的 reserve_ratio_bps 从净额中留存（ratio=0 时与原行为一致）。
		// 两步计算避免 ratio*net 溢出；结果钳位 [0, net]。
		var ratioBps int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(reserve_ratio_bps), 0)
			FROM merchant_treasury_ledger
			WHERE merchant_id = $1 AND currency = $2
		`, merchantID, currency).Scan(&ratioBps); err != nil {
			return fmt.Errorf("settlement: read reserve ratio: %w", err)
		}
		reserveShare := net/10000*ratioBps + net%10000*ratioBps/10000
		if reserveShare < 0 {
			reserveShare = 0
		}
		if reserveShare > net {
			reserveShare = net
		}
		settledCredit := net - reserveShare

		// 账本：PENDING → SETTLED（净额扣准备金）+ RESERVE（计提）+ PLATFORM_FEE（手续费）
		pendingID, err := s.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "PENDING", currency)
		if err != nil {
			return fmt.Errorf("settlement: get pending account: %w", err)
		}
		settledID, err := s.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "SETTLED", currency)
		if err != nil {
			return fmt.Errorf("settlement: get settled account: %w", err)
		}

		amount := money.DecimalFromInt64(total)
		entries := []domain.Entry{
			{
				AccountID:   pendingID,
				Direction:   domain.DirectionDebit,
				Amount:      amount,
				AssetCode:   currency,
				Description: "settlement: pending decrease",
			},
		}
		if settledCredit > 0 {
			entries = append(entries, domain.Entry{
				AccountID:   settledID,
				Direction:   domain.DirectionCredit,
				Amount:      money.DecimalFromInt64(settledCredit),
				AssetCode:   currency,
				Description: "settlement: settled increase (net)",
			})
		}
		if reserveShare > 0 {
			reserveID, rerr := s.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "RESERVE", currency)
			if rerr != nil {
				return fmt.Errorf("settlement: get reserve account: %w", rerr)
			}
			entries = append(entries, domain.Entry{
				AccountID:   reserveID,
				Direction:   domain.DirectionCredit,
				Amount:      money.DecimalFromInt64(reserveShare),
				AssetCode:   currency,
				Description: "settlement: reserve hold",
			})
		}
		if totalFee > 0 {
			feeAccountID, ferr := s.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "PLATFORM_FEE", currency)
			if ferr != nil {
				return fmt.Errorf("settlement: get fee account: %w", ferr)
			}
			entries = append(entries, domain.Entry{
				AccountID:   feeAccountID,
				Direction:   domain.DirectionCredit,
				Amount:      money.DecimalFromInt64(totalFee),
				AssetCode:   currency,
				Description: "settlement: platform fee",
			})
		}
		if totalSplit > 0 {
			partnerAccountID, perr := s.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "PARTNER_PAYABLE", currency)
			if perr != nil {
				return fmt.Errorf("settlement: get partner payable account: %w", perr)
			}
			entries = append(entries, domain.Entry{
				AccountID:   partnerAccountID,
				Direction:   domain.DirectionCredit,
				Amount:      money.DecimalFromInt64(totalSplit),
				AssetCode:   currency,
				Description: "settlement: partner payable",
			})
		}

		txn := domain.Transaction{
			IdempotencyKey:  "settle_batch_" + batchID.String(),
			TransactionType: domain.TransactionTypeSettlement,
			AggregateType:   "settlement_batch",
			AggregateID:     batchID.String(),
			Description:     "settlement: pending -> settled",
			TenantID:        merchantID,
			AssetCode:       currency,
			Amount:          amount,
			Status:          domain.TransactionStatusConfirmed,
			SourceAccountID: pendingID,
			TargetAccountID: settledID,
			Entries:         entries,
		}
		if err := s.ledger.CreateTransaction(ctx, tx, txn); err != nil {
			return fmt.Errorf("settlement: post ledger transaction: %w", err)
		}

		// 投影：pending → settled（扣手续费后净额）
		first, err := projection.Once(ctx, tx, "settle:"+batchID.String())
		if err != nil {
			return err
		}
		if first {
			if err := projection.MovePendingToSettledWithFeeAndReserveTx(ctx, tx, merchantID, currency, total, net, reserveShare); err != nil {
				return err
			}
		}

		// A2: 结算完成事件 → 商户出站回调（与业务同事务，Outbox 语义）
		if err := merchantwebhook.EnqueueTx(ctx, tx, merchantID,
			merchantwebhook.EventSettlementCompleted, batchID.String(), map[string]any{
				"batch_id":             batchID.String(),
				"merchant_id":          merchantID.String(),
				"currency":             currency,
				"business_date":        dateOnly.Format("2006-01-02"),
				"item_count":           len(items),
				"total_amount_minor":   total,
				"fee_amount_minor":     totalFee,
				"split_amount_minor":   totalSplit,
				"reserve_amount_minor": reserveShare,
				"net_amount_minor":     net,
			}); err != nil {
			return fmt.Errorf("settlement: enqueue merchant webhook: %w", err)
		}

		res.FeeMinor = totalFee
		res.SplitMinor = totalSplit
		res.ReserveMinor = reserveShare
		res.NetMinor = net
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// SettleAll 扫描所有存在未结算尝试的 (merchant, currency) 并逐一结转。
func (s *Service) SettleAll(ctx context.Context) ([]Result, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT a.merchant_id, a.currency
		FROM payment_attempts a
		LEFT JOIN pool_channel pc ON pc.id = a.pool_channel_id
		WHERE a.settled_at IS NULL AND a.status IN ('CAPTURED', 'SETTLED')
		  AND COALESCE(a.captured_at, a.created_at)
		      <= now() - make_interval(days => COALESCE(pc.settlement_days, 0))
	`)
	if err != nil {
		return nil, fmt.Errorf("settlement: list pending merchants: %w", err)
	}
	type key struct {
		MerchantID uuid.UUID
		Currency   string
	}
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.MerchantID, &k.Currency); err != nil {
			rows.Close()
			return nil, fmt.Errorf("settlement: scan pending merchant: %w", err)
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settlement: iterate pending merchants: %w", err)
	}

	now := time.Now()
	results := make([]Result, 0, len(keys))
	for _, k := range keys {
		res, err := s.SettleMerchant(ctx, k.MerchantID, k.Currency, now)
		if err != nil {
			return results, err
		}
		if res != nil && res.ItemCount > 0 {
			results = append(results, *res)
		}
	}
	return results, nil
}

func upsertBatch(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, currency string,
	businessDate time.Time, total int64, count int) (uuid.UUID, error) {
	var existing uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM settlement_batch
		WHERE merchant_id = $1 AND currency = $2 AND business_date = $3
		FOR UPDATE
	`, merchantID, currency, businessDate).Scan(&existing)
	switch {
	case err == nil:
		if _, err := tx.Exec(ctx, `
			UPDATE settlement_batch
			SET total_amount_minor = total_amount_minor + $2,
			    item_count = item_count + $3,
			    settled_at = now()
			WHERE id = $1
		`, existing, total, count); err != nil {
			return uuid.Nil, fmt.Errorf("settlement: update batch: %w", err)
		}
		return existing, nil
	case errors.Is(err, pgx.ErrNoRows):
		var id uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO settlement_batch
			    (tenant_id, merchant_id, currency, business_date, status, total_amount_minor, item_count, settled_at)
			VALUES ($1, $1, $2, $3, 'SETTLED', $4, $5, now())
			RETURNING id
		`, merchantID, currency, businessDate, total, count).Scan(&id)
		if err != nil {
			return uuid.Nil, fmt.Errorf("settlement: insert batch: %w", err)
		}
		return id, nil
	default:
		return uuid.Nil, fmt.Errorf("settlement: lock batch: %w", err)
	}
}

func ids(items []item) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}
