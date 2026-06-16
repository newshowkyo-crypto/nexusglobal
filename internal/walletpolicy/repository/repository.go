package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/walletpolicy/domain"
)

type Repository interface {
	GetPolicy(ctx context.Context, merchantID uuid.UUID, network domain.Network, assetCode string) (*domain.TransitWalletPolicy, error)
	ListAllowlistedAddresses(ctx context.Context, merchantID uuid.UUID, network domain.Network) ([]domain.AllowlistedAddress, error)
	CheckAllowlisted(ctx context.Context, merchantID uuid.UUID, network domain.Network, address string) (bool, error)
	RecordWithdrawal(ctx context.Context, req *domain.WithdrawalRequest) error
	GetDailyWithdrawalTotal(ctx context.Context, merchantID uuid.UUID, network domain.Network, assetCode string) (int64, error)
	CheckTxHashExists(ctx context.Context, txHash string) (bool, error)
}

type repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) Repository {
	return &repository{db: db}
}

func (r *repository) GetPolicy(ctx context.Context, merchantID uuid.UUID, network domain.Network, assetCode string) (*domain.TransitWalletPolicy, error) {
	var policy domain.TransitWalletPolicy
	query := `
		SELECT id, merchant_id, network, asset_code, single_limit_minor, daily_limit_minor, 
		       min_confirmations, allow_list_enabled, status, created_at, updated_at
		FROM transit_wallet_policy
		WHERE merchant_id = $1 AND network = $2 AND asset_code = $3 AND status = 'Active'
	`
	err := r.db.QueryRow(ctx, query, merchantID, network, assetCode).Scan(
		&policy.ID,
		&policy.MerchantID,
		&policy.Network,
		&policy.AssetCode,
		&policy.SingleLimitMinor,
		&policy.DailyLimitMinor,
		&policy.MinConfirmations,
		&policy.AllowListEnabled,
		&policy.Status,
		&policy.CreatedAt,
		&policy.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

func (r *repository) ListAllowlistedAddresses(ctx context.Context, merchantID uuid.UUID, network domain.Network) ([]domain.AllowlistedAddress, error) {
	var addresses []domain.AllowlistedAddress
	query := `
		SELECT id, address, network, asset_code, description, created_at
		FROM allowlisted_address
		WHERE merchant_id = $1 AND network = $2
	`
	rows, err := r.db.Query(ctx, query, merchantID, network)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var addr domain.AllowlistedAddress
		if err := rows.Scan(
			&addr.ID,
			&addr.Address,
			&addr.Network,
			&addr.AssetCode,
			&addr.Description,
			&addr.CreatedAt,
		); err != nil {
			return nil, err
		}
		addresses = append(addresses, addr)
	}

	return addresses, nil
}

func (r *repository) CheckAllowlisted(ctx context.Context, merchantID uuid.UUID, network domain.Network, address string) (bool, error) {
	var exists bool
	query := `
		SELECT EXISTS(
			SELECT 1 FROM allowlisted_address
			WHERE merchant_id = $1 AND network = $2 AND address = $3
		)
	`
	err := r.db.QueryRow(ctx, query, merchantID, network, address).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (r *repository) RecordWithdrawal(ctx context.Context, req *domain.WithdrawalRequest) error {
	query := `
		INSERT INTO withdrawal_request (id, merchant_id, network, asset_code, address, 
		                               amount_minor, tx_hash, confirmations, status, submitted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := r.db.Exec(ctx, query,
		req.ID,
		req.MerchantID,
		req.Network,
		req.AssetCode,
		req.Address,
		req.AmountMinor,
		req.TxHash,
		req.Confirmations,
		req.Status,
		req.SubmittedAt,
	)
	return err
}

func (r *repository) GetDailyWithdrawalTotal(ctx context.Context, merchantID uuid.UUID, network domain.Network, assetCode string) (int64, error) {
	var total int64
	query := `
		SELECT COALESCE(SUM(amount_minor), 0)
		FROM withdrawal_request
		WHERE merchant_id = $1 AND network = $2 AND asset_code = $3 
		  AND status = 'COMPLETED' AND submitted_at >= NOW() - INTERVAL '1 day'
	`
	err := r.db.QueryRow(ctx, query, merchantID, network, assetCode).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (r *repository) CheckTxHashExists(ctx context.Context, txHash string) (bool, error) {
	var exists bool
	query := `
		SELECT EXISTS(
			SELECT 1 FROM withdrawal_request WHERE tx_hash = $1
		)
	`
	err := r.db.QueryRow(ctx, query, txHash).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}
