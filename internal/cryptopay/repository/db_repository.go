package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/cryptopay/domain"
)

type dbRepository struct {
	pool *pgxpool.Pool
}

func NewDBRepository(pool *pgxpool.Pool) Repository {
	return &dbRepository{pool: pool}
}

const depositIntentColumns = `id, tenant_id, corporate_entity_id, merchant_id, order_id, asset_code, network,
	expected_amount, deposit_address, memo_tag, confirmations_required, provider_ref, state,
	received_amount, confirmed_amount, confirmations, transaction_ref, metadata, expires_at,
	retry_count, last_error, created_at, updated_at`

func scanDepositIntent(row pgx.Row) (*domain.DepositIntent, error) {
	var d domain.DepositIntent
	var metadataJSON []byte
	err := row.Scan(
		&d.ID, &d.TenantID, &d.CorporateEntityID, &d.MerchantID, &d.OrderID, &d.AssetCode, &d.Network,
		&d.ExpectedAmount, &d.DepositAddress, &d.MemoTag, &d.ConfirmationsRequired, &d.ProviderRef, &d.State,
		&d.ReceivedAmount, &d.ConfirmedAmount, &d.Confirmations, &d.TransactionRef, &metadataJSON, &d.ExpiresAt,
		&d.RetryCount, &d.LastError, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if metadataJSON != nil {
		_ = json.Unmarshal(metadataJSON, &d.Metadata)
	}
	return &d, nil
}

func (r *dbRepository) CreateDepositIntent(ctx context.Context, intent *domain.DepositIntent) error {
	metadataJSON, _ := json.Marshal(intent.Metadata)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO crypto_pay_deposit_intent (id, tenant_id, corporate_entity_id, merchant_id, order_id, asset_code, network,
			expected_amount, deposit_address, memo_tag, confirmations_required, provider_ref, state,
			received_amount, confirmed_amount, confirmations, transaction_ref, metadata, expires_at, retry_count, last_error, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		intent.ID, intent.TenantID, intent.CorporateEntityID, intent.MerchantID, intent.OrderID, intent.AssetCode, intent.Network,
		intent.ExpectedAmount, intent.DepositAddress, intent.MemoTag, intent.ConfirmationsRequired, intent.ProviderRef, intent.State,
		intent.ReceivedAmount, intent.ConfirmedAmount, intent.Confirmations, intent.TransactionRef, metadataJSON, intent.ExpiresAt,
		intent.RetryCount, intent.LastError, intent.CreatedAt, intent.UpdatedAt)
	return err
}

func (r *dbRepository) GetDepositIntentByID(ctx context.Context, id uuid.UUID) (*domain.DepositIntent, error) {
	d, err := scanDepositIntent(r.pool.QueryRow(ctx, `SELECT `+depositIntentColumns+` FROM crypto_pay_deposit_intent WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrDepositIntentNotFound
	}
	return d, err
}

func (r *dbRepository) GetDepositIntentByProviderRef(ctx context.Context, providerRef string) (*domain.DepositIntent, error) {
	d, err := scanDepositIntent(r.pool.QueryRow(ctx, `SELECT `+depositIntentColumns+` FROM crypto_pay_deposit_intent WHERE provider_ref = $1`, providerRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrDepositIntentNotFound
	}
	return d, err
}

func (r *dbRepository) GetDepositIntentByAddress(ctx context.Context, address string) (*domain.DepositIntent, error) {
	d, err := scanDepositIntent(r.pool.QueryRow(ctx, `SELECT `+depositIntentColumns+` FROM crypto_pay_deposit_intent WHERE deposit_address = $1 AND state IN ('CREATED','WAITING_FUNDS','CONFIRMING')`, address))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrDepositIntentNotFound
	}
	return d, err
}

func (r *dbRepository) UpdateDepositIntent(ctx context.Context, intent *domain.DepositIntent) error {
	metadataJSON, _ := json.Marshal(intent.Metadata)
	_, err := r.pool.Exec(ctx, `
		UPDATE crypto_pay_deposit_intent SET
			state=$2, received_amount=$3, confirmed_amount=$4, confirmations=$5, transaction_ref=$6,
			metadata=$7, retry_count=$8, last_error=$9, updated_at=now()
		WHERE id=$1`,
		intent.ID, intent.State, intent.ReceivedAmount, intent.ConfirmedAmount, intent.Confirmations,
		intent.TransactionRef, metadataJSON, intent.RetryCount, intent.LastError)
	return err
}

func (r *dbRepository) GetPendingDepositIntents(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.DepositIntent, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+depositIntentColumns+` FROM crypto_pay_deposit_intent WHERE tenant_id=$1 AND state IN ('CREATED','WAITING_FUNDS','CONFIRMING') ORDER BY created_at LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var intents []domain.DepositIntent
	for rows.Next() {
		d, err := scanDepositIntent(rows)
		if err != nil {
			return nil, err
		}
		intents = append(intents, *d)
	}
	return intents, rows.Err()
}

func (r *dbRepository) GetExpiredDepositIntents(ctx context.Context, limit int) ([]domain.DepositIntent, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+depositIntentColumns+` FROM crypto_pay_deposit_intent WHERE state='WAITING_FUNDS' AND expires_at < now() LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var intents []domain.DepositIntent
	for rows.Next() {
		d, err := scanDepositIntent(rows)
		if err != nil {
			return nil, err
		}
		intents = append(intents, *d)
	}
	return intents, rows.Err()
}

func (r *dbRepository) DeleteDepositIntent(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM crypto_pay_deposit_intent WHERE id = $1`, id)
	return err
}

func (r *dbRepository) GetDepositIntentsByTenant(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]domain.DepositIntent, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+depositIntentColumns+` FROM crypto_pay_deposit_intent WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var intents []domain.DepositIntent
	for rows.Next() {
		d, err := scanDepositIntent(rows)
		if err != nil {
			return nil, err
		}
		intents = append(intents, *d)
	}
	return intents, rows.Err()
}

func (r *dbRepository) GetDepositIntentCountByState(ctx context.Context, tenantID uuid.UUID, state domain.DepositState) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM crypto_pay_deposit_intent WHERE tenant_id=$1 AND state=$2`, tenantID, state).Scan(&count)
	return count, err
}

func (r *dbRepository) MarkAsReconcileMismatch(ctx context.Context, intentID uuid.UUID, reason string) error {
	_, err := r.pool.Exec(ctx, `UPDATE crypto_pay_deposit_intent SET state='RECONCILE_MISMATCH', last_error=$2, updated_at=now() WHERE id=$1`, intentID, reason)
	return err
}

func (r *dbRepository) ConfirmDepositIntent(ctx context.Context, intentID uuid.UUID, receivedAmount string, confirmations int, transactionRef string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE crypto_pay_deposit_intent SET state='CONFIRMED', received_amount=$2, confirmations=$3, transaction_ref=$4, updated_at=now()
		WHERE id=$1`, intentID, receivedAmount, confirmations, transactionRef)
	return err
}

const addressPoolColumns = `id, tenant_id, channel_id, asset_code, network, address, status,
	allocated_intent_id, allocated_at, released_at, used_at, metadata, created_at, updated_at`

func scanAddressPool(row pgx.Row) (*domain.DepositAddressPool, error) {
	var a domain.DepositAddressPool
	var metadataJSON []byte
	err := row.Scan(
		&a.ID, &a.TenantID, &a.ChannelID, &a.AssetCode, &a.Network, &a.Address, &a.Status,
		&a.AllocatedIntentID, &a.AllocatedAt, &a.ReleasedAt, &a.UsedAt, &metadataJSON, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if metadataJSON != nil {
		_ = json.Unmarshal(metadataJSON, &a.Metadata)
	}
	return &a, nil
}

func (r *dbRepository) CreateAddressPoolEntry(ctx context.Context, entry *domain.DepositAddressPool) error {
	metadataJSON, _ := json.Marshal(entry.Metadata)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO crypto_pay_address_pool (id, tenant_id, channel_id, asset_code, network, address, status, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		entry.ID, entry.TenantID, entry.ChannelID, entry.AssetCode, entry.Network, entry.Address, entry.Status, metadataJSON, entry.CreatedAt, entry.UpdatedAt)
	return err
}

func (r *dbRepository) GetAvailableAddress(ctx context.Context, tenantID uuid.UUID, assetCode string, network domain.NetworkType) (*domain.DepositAddressPool, error) {
	a, err := scanAddressPool(r.pool.QueryRow(ctx, `SELECT `+addressPoolColumns+` FROM crypto_pay_address_pool WHERE tenant_id=$1 AND asset_code=$2 AND network=$3 AND status='AVAILABLE' ORDER BY created_at LIMIT 1`, tenantID, assetCode, network))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAddressPoolEmpty
	}
	return a, err
}

func (r *dbRepository) AllocateAddress(ctx context.Context, addressID uuid.UUID, intentID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE crypto_pay_address_pool SET status='ALLOCATED', allocated_intent_id=$2, allocated_at=now(), updated_at=now() WHERE id=$1 AND status='AVAILABLE'`, addressID, intentID)
	return err
}

func (r *dbRepository) ReleaseAddress(ctx context.Context, addressID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE crypto_pay_address_pool SET status='AVAILABLE', allocated_intent_id=NULL, released_at=now(), updated_at=now() WHERE id=$1`, addressID)
	return err
}

func (r *dbRepository) MarkAddressUsed(ctx context.Context, addressID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE crypto_pay_address_pool SET status='USED', used_at=now(), updated_at=now() WHERE id=$1`, addressID)
	return err
}

const chainTxColumns = `id, tenant_id, intent_id, transaction_hash, block_hash, block_number,
	from_address, to_address, amount, asset_code, network, confirmations, status, memo_tag, timestamp, created_at, updated_at`

func scanChainTransaction(row pgx.Row) (*domain.ChainTransaction, error) {
	var t domain.ChainTransaction
	err := row.Scan(
		&t.ID, &t.TenantID, &t.IntentID, &t.TransactionHash, &t.BlockHash, &t.BlockNumber,
		&t.FromAddress, &t.ToAddress, &t.Amount, &t.AssetCode, &t.Network, &t.Confirmations,
		&t.Status, &t.MemoTag, &t.Timestamp, &t.CreatedAt, &t.UpdatedAt)
	return &t, err
}

func (r *dbRepository) CreateChainTransaction(ctx context.Context, tx *domain.ChainTransaction) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO crypto_pay_chain_transaction (id, tenant_id, intent_id, transaction_hash, block_hash, block_number,
			from_address, to_address, amount, asset_code, network, confirmations, status, memo_tag, timestamp, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		tx.ID, tx.TenantID, tx.IntentID, tx.TransactionHash, tx.BlockHash, tx.BlockNumber,
		tx.FromAddress, tx.ToAddress, tx.Amount, tx.AssetCode, tx.Network, tx.Confirmations,
		tx.Status, tx.MemoTag, tx.Timestamp, tx.CreatedAt, tx.UpdatedAt)
	return err
}

func (r *dbRepository) GetChainTransactionByHash(ctx context.Context, txHash string) (*domain.ChainTransaction, error) {
	t, err := scanChainTransaction(r.pool.QueryRow(ctx, `SELECT `+chainTxColumns+` FROM crypto_pay_chain_transaction WHERE transaction_hash = $1`, txHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("crypto pay: chain transaction not found")
	}
	return t, err
}

func (r *dbRepository) UpdateChainTransaction(ctx context.Context, tx *domain.ChainTransaction) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE crypto_pay_chain_transaction SET block_hash=$2, block_number=$3, confirmations=$4, status=$5, updated_at=now()
		WHERE id=$1`, tx.ID, tx.BlockHash, tx.BlockNumber, tx.Confirmations, tx.Status)
	return err
}

func (r *dbRepository) GetUnconfirmedTransactions(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.ChainTransaction, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+chainTxColumns+` FROM crypto_pay_chain_transaction WHERE tenant_id=$1 AND status='PENDING' ORDER BY timestamp LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var txs []domain.ChainTransaction
	for rows.Next() {
		t, err := scanChainTransaction(rows)
		if err != nil {
			return nil, err
		}
		txs = append(txs, *t)
	}
	return txs, rows.Err()
}
