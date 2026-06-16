package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/platform/money"
)

// decimalToNumeric 将 decimal.Decimal 转换为 pgtype.Numeric
func decimalToNumeric(d decimal.Decimal) (pgtype.Numeric, error) {
	return money.DecimalToNumeric(d), nil
}

// numericToDecimal 将 pgtype.Numeric 转换为 decimal.Decimal
func numericToDecimal(n pgtype.Numeric) decimal.Decimal {
	return money.NumericToDecimal(n)
}

// FeeRepo 费用摊销仓库
type FeeRepo struct {
	pool *pgxpool.Pool
}

// NewFeeRepo 创建费用摊销仓库
func NewFeeRepo(pool *pgxpool.Pool) *FeeRepo {
	return &FeeRepo{pool: pool}
}

// CreateAllocations 创建费用分配记录
func (r *FeeRepo) CreateAllocations(ctx context.Context, allocations []domain.SettlementFeeAllocation) error {
	if len(allocations) == 0 {
		return nil
	}

	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	for _, alloc := range allocations {
		_, err := conn.Exec(ctx,
			`INSERT INTO settlement_fee_allocation
			(id, transaction_id, merchant_id, fee_type, amount, currency, source_data_hash, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			alloc.ID, alloc.TransactionID, alloc.MerchantID, alloc.FeeType,
			alloc.Amount, alloc.Currency, alloc.SourceDataHash, alloc.Status, alloc.CreatedAt)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetByTransactionID 根据交易ID获取费用分配
func (r *FeeRepo) GetByTransactionID(ctx context.Context, transactionID uuid.UUID) ([]domain.SettlementFeeAllocation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, transaction_id, merchant_id, fee_type, amount, currency, source_data_hash, status, created_at
		FROM settlement_fee_allocation
		WHERE transaction_id = $1
		ORDER BY created_at DESC`,
		transactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var allocations []domain.SettlementFeeAllocation
	for rows.Next() {
		var alloc domain.SettlementFeeAllocation
		err := rows.Scan(
			&alloc.ID, &alloc.TransactionID, &alloc.MerchantID, &alloc.FeeType,
			&alloc.Amount, &alloc.Currency, &alloc.SourceDataHash, &alloc.Status, &alloc.CreatedAt)
		if err != nil {
			return nil, err
		}
		allocations = append(allocations, alloc)
	}

	return allocations, nil
}

// GetByMerchantID 根据商户ID获取费用分配
func (r *FeeRepo) GetByMerchantID(ctx context.Context, merchantID uuid.UUID) ([]domain.SettlementFeeAllocation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, transaction_id, merchant_id, fee_type, amount, currency, source_data_hash, status, created_at
		FROM settlement_fee_allocation
		WHERE merchant_id = $1
		ORDER BY created_at DESC`,
		merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var allocations []domain.SettlementFeeAllocation
	for rows.Next() {
		var alloc domain.SettlementFeeAllocation
		err := rows.Scan(
			&alloc.ID, &alloc.TransactionID, &alloc.MerchantID, &alloc.FeeType,
			&alloc.Amount, &alloc.Currency, &alloc.SourceDataHash, &alloc.Status, &alloc.CreatedAt)
		if err != nil {
			return nil, err
		}
		allocations = append(allocations, alloc)
	}

	return allocations, nil
}

// Repository 账本仓库接口
type Repository interface {
	// Transaction methods
	RecordTransaction(ctx context.Context, tx *domain.Transaction) error
	GetTransactionByID(ctx context.Context, id uuid.UUID) (*domain.Transaction, error)
	GetTransactionByOrderID(ctx context.Context, orderID string) (*domain.Transaction, error)
	UpdateTransactionStatus(ctx context.Context, id uuid.UUID, status domain.TransactionStatus) error
	ListTransactions(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]domain.Transaction, error)

	// Ledger entry methods
	CreateLedgerEntry(ctx context.Context, entry *domain.LedgerEntry) error
	GetLedgerEntriesByAccount(ctx context.Context, accountID uuid.UUID, limit int, offset int) ([]domain.LedgerEntry, error)
	GetLedgerEntriesByTransaction(ctx context.Context, transactionID uuid.UUID) ([]domain.LedgerEntry, error)

	// Account balance methods
	GetAccountBalance(ctx context.Context, accountID uuid.UUID, assetCode string) (*domain.AccountBalance, error)
	UpdateAccountBalance(ctx context.Context, accountID uuid.UUID, assetCode string, delta decimal.Decimal) error
	FreezeBalance(ctx context.Context, accountID uuid.UUID, assetCode string, amount decimal.Decimal) error
	UnfreezeBalance(ctx context.Context, accountID uuid.UUID, assetCode string, amount decimal.Decimal) error

	// Bridge profile methods
	CreateBridgeProfile(ctx context.Context, profile *domain.BridgeProfile) error
	GetBridgeProfile(ctx context.Context, id uuid.UUID) (*domain.BridgeProfile, error)
	GetBridgeProfileByName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.BridgeProfile, error)
	UpdateBridgeProfile(ctx context.Context, profile *domain.BridgeProfile) error
	DeleteBridgeProfile(ctx context.Context, id uuid.UUID) error
	ListBridgeProfiles(ctx context.Context, tenantID uuid.UUID) ([]domain.BridgeProfile, error)

	// Bridge operation methods
	CreateBridgeOperation(ctx context.Context, operation *domain.BridgeOperation) error
	GetBridgeOperation(ctx context.Context, id uuid.UUID) (*domain.BridgeOperation, error)
	GetBridgeOperationByOrderID(ctx context.Context, orderID string) (*domain.BridgeOperation, error)
	UpdateBridgeOperation(ctx context.Context, operation *domain.BridgeOperation) error
	ListBridgeOperations(ctx context.Context, tenantID uuid.UUID, status domain.BridgeStatus, limit int) ([]domain.BridgeOperation, error)
	GetDailyBridgeUsage(ctx context.Context, tenantID uuid.UUID, profileID uuid.UUID) (decimal.Decimal, error)

	// Balance check methods
	CheckBalance(ctx context.Context, accountID uuid.UUID, assetCode string, amount decimal.Decimal) (bool, error)
	GetTotalBalance(ctx context.Context, tenantID uuid.UUID, assetCode string) (decimal.Decimal, error)
}
