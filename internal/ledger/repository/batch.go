package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
)

// BatchConfig 批量写入配置
type BatchConfig struct {
	// MaxBatchSize 单批次最大记录数
	MaxBatchSize int
	// BatchTimeout 批次超时时间
	BatchTimeout int // milliseconds
}

// DefaultBatchConfig 默认批量配置
var DefaultBatchConfig = BatchConfig{
	MaxBatchSize: 100,
	BatchTimeout: 5000, // 5秒
}

// BatchWriter 批量写入接口（为高频订单流优化）
type BatchWriter interface {
	// BatchCreateLedgerEntries 批量创建账本流水
	// 减少数据库往返次数，降低行锁争用
	BatchCreateLedgerEntries(ctx context.Context, entries []domain.LedgerEntry, cfg *BatchConfig) error

	// BatchCreateFeeAllocations 批量创建费用分配
	BatchCreateFeeAllocations(ctx context.Context, allocations []domain.SettlementFeeAllocation, cfg *BatchConfig) error
}

// BatchFeeRepo 支持批量写入的费用仓库
type BatchFeeRepo struct {
	pool *pgxpool.Pool
}

// NewBatchFeeRepo 创建批量费用仓库
func NewBatchFeeRepo(pool *pgxpool.Pool) *BatchFeeRepo {
	return &BatchFeeRepo{pool: pool}
}

// BatchCreateFeeAllocations 批量创建费用分配记录
// 使用 pgx.Batch 减少数据库往返，降低行锁争用
func (r *BatchFeeRepo) BatchCreateFeeAllocations(ctx context.Context, allocations []domain.SettlementFeeAllocation, cfg *BatchConfig) error {
	if len(allocations) == 0 {
		return nil
	}

	if cfg == nil {
		cfg = &DefaultBatchConfig
	}

	// 分批处理
	for i := 0; i < len(allocations); i += cfg.MaxBatchSize {
		end := i + cfg.MaxBatchSize
		if end > len(allocations) {
			end = len(allocations)
		}
		batch := allocations[i:end]

		if err := r.execBatch(ctx, batch); err != nil {
			return err
		}
	}

	return nil
}

// execBatch 执行单批次
func (r *BatchFeeRepo) execBatch(ctx context.Context, allocations []domain.SettlementFeeAllocation) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	batch := &pgx.Batch{}
	for _, alloc := range allocations {
		batch.Queue(
			`INSERT INTO settlement_fee_allocation
			(id, transaction_id, merchant_id, fee_type, amount, currency, source_data_hash, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			alloc.ID, alloc.TransactionID, alloc.MerchantID, alloc.FeeType,
			alloc.Amount, alloc.Currency, alloc.SourceDataHash, alloc.Status, alloc.CreatedAt,
		)
	}

	br := conn.SendBatch(ctx, batch)
	defer br.Close()

	// 检查所有结果
	for range allocations {
		cmdTag, err := br.Exec()
		if err != nil {
			return err
		}
		if cmdTag.RowsAffected() != 1 {
			return ErrBatchInsertFailed
		}
	}

	return nil
}

// BatchLedgerRepo 支持批量写入的账本仓库
type BatchLedgerRepo struct {
	pool *pgxpool.Pool
}

// NewBatchLedgerRepo 创建批量账本仓库
func NewBatchLedgerRepo(pool *pgxpool.Pool) *BatchLedgerRepo {
	return &BatchLedgerRepo{pool: pool}
}

// BatchCreateLedgerEntries 批量创建账本流水
// 减少网络往返，降低行锁争用（Contention Rate）
func (r *BatchLedgerRepo) BatchCreateLedgerEntries(ctx context.Context, entries []domain.LedgerEntry, cfg *BatchConfig) error {
	if len(entries) == 0 {
		return nil
	}

	if cfg == nil {
		cfg = &DefaultBatchConfig
	}

	// 分批处理
	for i := 0; i < len(entries); i += cfg.MaxBatchSize {
		end := i + cfg.MaxBatchSize
		if end > len(entries) {
			end = len(entries)
		}
		batch := entries[i:end]

		if err := r.execEntryBatch(ctx, batch); err != nil {
			return err
		}
	}

	return nil
}

// execEntryBatch 执行单批次账本流水
func (r *BatchLedgerRepo) execEntryBatch(ctx context.Context, entries []domain.LedgerEntry) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	batch := &pgx.Batch{}
	for _, entry := range entries {
		batch.Queue(
			`INSERT INTO ledger_entries
			(id, tenant_id, corporate_entity_id, account_id, transaction_id, transaction_type, asset_code, amount, balance_before, balance_after, description, metadata, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			entry.ID, entry.TenantID, entry.CorporateEntityID, entry.AccountID,
			entry.TransactionID, entry.TransactionType, entry.AssetCode,
			entry.Amount, entry.BalanceBefore, entry.BalanceAfter,
			entry.Description, entry.Metadata, entry.CreatedAt,
		)
	}

	br := conn.SendBatch(ctx, batch)
	defer br.Close()

	// 检查所有结果
	for range entries {
		cmdTag, err := br.Exec()
		if err != nil {
			return err
		}
		if cmdTag.RowsAffected() != 1 {
			return ErrBatchInsertFailed
		}
	}

	return nil
}

// ErrBatchInsertFailed 批量插入失败
var ErrBatchInsertFailed = &BatchError{Message: "batch insert failed: expected 1 row affected"}

// BatchError 批量操作错误
type BatchError struct {
	Message string
}

func (e *BatchError) Error() string {
	return e.Message
}

// BatchCreateTransactions 批量创建交易记录
func (r *BatchLedgerRepo) BatchCreateTransactions(ctx context.Context, transactions []domain.Transaction, cfg *BatchConfig) error {
	if len(transactions) == 0 {
		return nil
	}

	if cfg == nil {
		cfg = &DefaultBatchConfig
	}

	// 分批处理
	for i := 0; i < len(transactions); i += cfg.MaxBatchSize {
		end := i + cfg.MaxBatchSize
		if end > len(transactions) {
			end = len(transactions)
		}
		batch := transactions[i:end]

		if err := r.execTxBatch(ctx, batch); err != nil {
			return err
		}
	}

	return nil
}

// execTxBatch 执行单批次交易记录
func (r *BatchLedgerRepo) execTxBatch(ctx context.Context, transactions []domain.Transaction) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	batch := &pgx.Batch{}
	for _, tx := range transactions {
		batch.Queue(
			`INSERT INTO transactions
			(id, tenant_id, corporate_entity_id, order_id, type, source_account_id, target_account_id, asset_code, amount, fee_amount, fee_asset_code, status, external_ref, bridge_profile_id, metadata, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
			tx.ID, tx.TenantID, tx.CorporateEntityID, tx.OrderID, tx.Type,
			tx.SourceAccountID, tx.TargetAccountID, tx.AssetCode, tx.Amount,
			tx.FeeAmount, tx.FeeAssetCode, tx.Status, tx.ExternalRef,
			tx.BridgeProfileID, tx.Metadata, tx.CreatedAt, tx.UpdatedAt,
		)
	}

	br := conn.SendBatch(ctx, batch)
	defer br.Close()

	// 检查所有结果
	for range transactions {
		cmdTag, err := br.Exec()
		if err != nil {
			return err
		}
		if cmdTag.RowsAffected() != 1 {
			return ErrBatchInsertFailed
		}
	}

	return nil
}
