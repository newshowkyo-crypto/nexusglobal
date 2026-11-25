package treasury

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	issuing "github.com/nexusglobal/mamr/internal/issuing"
	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	ledgerservice "github.com/nexusglobal/mamr/internal/ledger/service"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/settlement/projection"
)

// ============================================================================
// P0-A2: VCCFundingRepository 的真实 DB 实现
//
// 之前 Treasury VCCFundingRouter 只有接口和 mock，没有 DB 实现，
// 导致 Worker 的 FundingRouter 只能装配 nil，VCC_FUNDING_REQUESTED 永远失败。
// 本文件提供基于真实表的实现：
//   - merchant_treasury_ledger（000005）：settled/pending 余额
//   - merchant_risk_profiles（000025）：商户状态
//   - acquirer_channels（000028）：商户来源 tier
//   - issuing_providers（000028）：目标发卡 provider
//   - transactional_outbox（000006）：事件写入
// ============================================================================

// VCCFundingDBRepository 是 VCCFundingRepository 的 pgx 直连实现。
//
// P7: 余额写入统一走 internal/settlement/projection（唯一投影写入口），
// 注入账本服务后同时落平衡分录，账本仍是唯一资金真相。
type VCCFundingDBRepository struct {
	pool   *pgxpool.Pool
	ledger *ledgerservice.Service
}

// NewVCCFundingDBRepository 创建 DB-backed VCCFundingRepository。
func NewVCCFundingDBRepository(pool *pgxpool.Pool) *VCCFundingDBRepository {
	return &VCCFundingDBRepository{pool: pool}
}

// SetLedger 注入账本服务（P7）。未注入时只写投影。
func (r *VCCFundingDBRepository) SetLedger(ledger *ledgerservice.Service) { r.ledger = ledger }

// LockMerchantSettledBalance 锁定商户全部 treasury 余额行（SELECT ... FOR UPDATE）。
// 必须在事务内调用；Lock → Debit/Credit 在同一事务中由 router 调用方保证。
func (r *VCCFundingDBRepository) LockMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID) error {
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM merchant_treasury_ledger WHERE merchant_id = $1 FOR UPDATE
	`, merchantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	return rows.Err()
}

// DebitMerchantSettledBalance 扣减商户 settled 余额，并转入在途（P7：走 projection）。
// 余额不足时返回 ErrInsufficientBalance（不产生负余额）。
func (r *VCCFundingDBRepository) DebitMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64, txID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.DebitMerchantSettledBalanceTx(ctx, tx, merchantID, amountMinor, txID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreditPendingCryptoBridgeBalance 兼容方法：settled → pending 已在 Debit 阶段同事务完成。
func (r *VCCFundingDBRepository) CreditPendingCryptoBridgeBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64) error {
	return nil
}

// WriteOutboxEvent 写入 VCC 事件到 transactional_outbox。
//
// P0-A3/A4: 提供全部必填字段；幂等键从 payload 中提取 bridge_batch_id 派生
// （vcc_crypto_bridge:<bridge_batch_id>），配合 ON CONFLICT DO NOTHING，
// 重复写入安全。
func (r *VCCFundingDBRepository) WriteOutboxEvent(ctx context.Context, event OutboxEvent) error {
	// 从 payload 提取 bridge_batch_id 生成稳定幂等键
	var payloadFields struct {
		BridgeBatchID string `json:"bridge_batch_id"`
	}
	if err := json.Unmarshal(event.Payload, &payloadFields); err != nil {
		return fmt.Errorf("treasury: invalid VCC outbox payload: %w", err)
	}
	idempotencyKey := "vcc_crypto_bridge:" + payloadFields.BridgeBatchID

	_, err := r.pool.Exec(ctx, `
		INSERT INTO transactional_outbox
			(id, aggregate_type, aggregate_id, event_type, payload, idempotency_key,
			 status, retry_count, next_retry_at, created_at, updated_at)
		VALUES ($1, 'crypto_bridge', $2, $3, $4, $5,
			 'Pending', $6, now(), $7, now())
		ON CONFLICT (idempotency_key) DO NOTHING`,
		event.ID, uuid.NewSHA1(uuid.NameSpaceOID, []byte("crypto_bridge:"+payloadFields.BridgeBatchID)),
		event.EventType, event.Payload, idempotencyKey,
		event.RetryCount, event.CreatedAt)
	return err
}

// GetMerchantRiskProfile 读取商户风险画像。
//   - IsBlocked：merchant_risk_profiles.status != 'ACTIVE'
//   - RiskScore：表未建模评分，恒为 0（不虚构数据）
//   - SourceTier：取该商户 ACTIVE acquirer 通道的 provider_tier；无通道时默认 compliant
func (r *VCCFundingDBRepository) GetMerchantRiskProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error) {
	var status string
	err := r.pool.QueryRow(ctx, `
		SELECT status FROM merchant_risk_profiles WHERE merchant_id = $1
	`, merchantID).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantRiskProfileMissing
		}
		return nil, err
	}

	sourceTier := domain.ProviderTierCompliant
	var tier string
	err = r.pool.QueryRow(ctx, `
		SELECT provider_tier::text FROM acquirer_channels
		WHERE entity_id = $1 AND status = 'ACTIVE'
		ORDER BY created_at
		LIMIT 1
	`, merchantID).Scan(&tier)
	if err == nil && tier != "" {
		sourceTier = domain.ProviderTier(tier)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	return &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  0,
		IsBlocked:  status != "ACTIVE",
		SourceTier: sourceTier,
	}, nil
}

// GetIssuingProvider 读取目标发卡 provider。
func (r *VCCFundingDBRepository) GetIssuingProvider(ctx context.Context, providerID uuid.UUID) (*issuing.IssuingProvider, error) {
	var p issuing.IssuingProvider
	var entityID *uuid.UUID
	var providerConfig, velocityConfig []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, entity_id, issuing_tier::text, funding_mode::text,
		       provider_code, pool_code, credential_vault_key, status, version,
		       provider_config, velocity_config, created_at, updated_at
		FROM issuing_providers WHERE id = $1
	`, providerID).Scan(
		&p.ID, &entityID, &p.IssuingTier, &p.FundingMode,
		&p.ProviderCode, &p.PoolCode, &p.CredentialVaultKey, &p.Status, &p.Version,
		&providerConfig, &velocityConfig, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIssuingProviderMissing
		}
		return nil, err
	}
	p.EntityID = entityID
	if len(providerConfig) > 0 {
		_ = json.Unmarshal(providerConfig, &p.ProviderConfig)
	}
	if len(velocityConfig) > 0 {
		_ = json.Unmarshal(velocityConfig, &p.VelocityConfig)
	}
	return &p, nil
}

// ============================================================================
// P0-A2: 事务版本方法 — 所有余额操作共享同一 pgx.Tx，保证原子性
// ============================================================================

// BeginTx 开启一个新事务。
func (r *VCCFundingDBRepository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

// LockMerchantSettledBalanceTx 在指定事务内锁定商户余额行（SELECT ... FOR UPDATE）。
func (r *VCCFundingDBRepository) LockMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID) error {
	rows, err := tx.Query(ctx, `
		SELECT id FROM merchant_treasury_ledger WHERE merchant_id = $1 FOR UPDATE
	`, merchantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	return rows.Err()
}

// DebitMerchantSettledBalanceTx 在指定事务内扣减 settled 并转入在途（P7）。
//
// 账本：Dr SETTLED / Cr VCC_BRIDGE_PENDING（注入账本时）
// 投影：settled -= amount, pending += amount
// 幂等：以 source ledger tx id 作为 marker，重复调用不重复扣减。
func (r *VCCFundingDBRepository) DebitMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, txID string) error {
	currency, err := r.currencyForSourceTx(ctx, tx, merchantID, txID)
	if err != nil {
		return err
	}

	first, err := projection.Once(ctx, tx, "vcc_bridge_start:"+txID)
	if err != nil {
		return err
	}
	if !first {
		return nil
	}

	if r.ledger != nil {
		settledID, err := r.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "SETTLED", currency)
		if err != nil {
			return err
		}
		bridgeID, err := r.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "VCC_BRIDGE_PENDING", currency)
		if err != nil {
			return err
		}
		amount := money.DecimalFromInt64(amountMinor)
		if err := r.ledger.CreateTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  "vcc_bridge_start_" + txID,
			TransactionType: ledgerdomain.TransactionTypeTransfer,
			AggregateType:   "vcc_funding",
			AggregateID:     txID,
			Description:     "vcc funding: settled -> bridge pending",
			TenantID:        merchantID,
			AssetCode:       currency,
			Amount:          amount,
			Status:          ledgerdomain.TransactionStatusConfirmed,
			SourceAccountID: settledID,
			TargetAccountID: bridgeID,
			Entries: []ledgerdomain.Entry{
				{AccountID: settledID, Direction: ledgerdomain.DirectionDebit, Amount: amount,
					AssetCode: currency, Description: "vcc funding: settled decrease"},
				{AccountID: bridgeID, Direction: ledgerdomain.DirectionCredit, Amount: amount,
					AssetCode: currency, Description: "vcc funding: bridge pending increase"},
			},
		}); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "insufficient") {
				return ErrInsufficientBalance
			}
			return err
		}
	}

	if err := projection.MoveSettledToPendingTx(ctx, tx, merchantID, currency, amountMinor); err != nil {
		if errors.Is(err, projection.ErrInsufficientProjection) {
			return ErrInsufficientBalance
		}
		return err
	}
	return nil
}

// CreditPendingCryptoBridgeBalanceTx 兼容方法：settled → pending 已在上一步同事务完成。
func (r *VCCFundingDBRepository) CreditPendingCryptoBridgeBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error {
	return nil
}

// WriteOutboxEventTx 在指定事务内写入 VCC 事件到 transactional_outbox。
func (r *VCCFundingDBRepository) WriteOutboxEventTx(ctx context.Context, tx pgx.Tx, event OutboxEvent) error {
	var payloadFields struct {
		BridgeBatchID string `json:"bridge_batch_id"`
	}
	if err := json.Unmarshal(event.Payload, &payloadFields); err != nil {
		return fmt.Errorf("treasury: invalid VCC outbox payload: %w", err)
	}
	idempotencyKey := "vcc_crypto_bridge:" + payloadFields.BridgeBatchID

	_, err := tx.Exec(ctx, `
		INSERT INTO transactional_outbox
			(id, aggregate_type, aggregate_id, event_type, payload, idempotency_key,
			 status, retry_count, next_retry_at, created_at, updated_at)
		VALUES ($1, 'crypto_bridge', $2, $3, $4, $5,
			 'Pending', $6, now(), $7, now())
		ON CONFLICT (idempotency_key) DO NOTHING`,
		event.ID, uuid.NewSHA1(uuid.NameSpaceOID, []byte("crypto_bridge:"+payloadFields.BridgeBatchID)),
		event.EventType, event.Payload, idempotencyKey,
		event.RetryCount, event.CreatedAt)
	return err
}

// ============================================================================
// P0-A1: Finalize 阶段事务版本方法 — 桥接完成后的最终入金
// ============================================================================

// DebitPendingCryptoBridgeBalanceTx 在指定事务内扣减在途（桥接完成）。
//
// 账本：Dr VCC_BRIDGE_PENDING / Cr ISSUING_POOL（资金进入发卡池）
// 投影：pending -= amount
func (r *VCCFundingDBRepository) DebitPendingCryptoBridgeBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error {
	currency, err := r.merchantCurrency(ctx, tx, merchantID)
	if err != nil {
		return err
	}

	if r.ledger != nil {
		bridgeID, err := r.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "VCC_BRIDGE_PENDING", currency)
		if err != nil {
			return err
		}
		poolID, err := r.ledger.GetOrCreateAccountTx(ctx, tx, merchantID, "ISSUING_POOL", currency)
		if err != nil {
			return err
		}
		amount := money.DecimalFromInt64(amountMinor)
		if err := r.ledger.CreateTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  fmt.Sprintf("vcc_bridge_finalize_%s_%d", merchantID, amountMinor),
			TransactionType: ledgerdomain.TransactionTypeTransfer,
			AggregateType:   "vcc_funding",
			AggregateID:     merchantID.String(),
			Description:     "vcc funding: bridge pending -> issuing pool",
			TenantID:        merchantID,
			AssetCode:       currency,
			Amount:          amount,
			Status:          ledgerdomain.TransactionStatusConfirmed,
			SourceAccountID: bridgeID,
			TargetAccountID: poolID,
			Entries: []ledgerdomain.Entry{
				{AccountID: bridgeID, Direction: ledgerdomain.DirectionDebit, Amount: amount,
					AssetCode: currency, Description: "vcc funding: bridge pending decrease"},
				{AccountID: poolID, Direction: ledgerdomain.DirectionCredit, Amount: amount,
					AssetCode: currency, Description: "vcc funding: issuing pool increase"},
			},
		}); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "insufficient") {
				return ErrInsufficientBalance
			}
			return err
		}
	}

	if err := projection.RemovePendingTx(ctx, tx, merchantID, currency, amountMinor); err != nil {
		if errors.Is(err, projection.ErrInsufficientProjection) {
			return ErrInsufficientBalance
		}
		return err
	}
	return nil
}

// CreditMerchantSettledBalanceTx 在指定事务内增加商户 settled 余额（归集回流）。
func (r *VCCFundingDBRepository) CreditMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error {
	currency, err := r.merchantCurrency(ctx, tx, merchantID)
	if err != nil {
		return err
	}
	return projection.AddSettledTx(ctx, tx, merchantID, currency, amountMinor)
}

// currencyForSourceTx 从 source ledger transaction 解析币种；缺失时回退到商户唯一币种。
func (r *VCCFundingDBRepository) currencyForSourceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, sourceTxID string) (string, error) {
	var currency string
	err := tx.QueryRow(ctx, `
		SELECT asset_code FROM ledger_transaction WHERE id::text = $1
	`, sourceTxID).Scan(&currency)
	if err == nil && currency != "" {
		return currency, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return r.merchantCurrency(ctx, tx, merchantID)
}

// merchantCurrency 返回商户唯一币种；多币种时 fail-closed（避免打错币种）。
func (r *VCCFundingDBRepository) merchantCurrency(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT currency FROM merchant_treasury_ledger WHERE merchant_id = $1
	`, merchantID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var currencies []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		currencies = append(currencies, c)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(currencies) {
	case 1:
		return currencies[0], nil
	case 0:
		return "", fmt.Errorf("treasury: merchant %s has no treasury ledger row", merchantID)
	default:
		return "", fmt.Errorf("treasury: merchant %s has multiple currencies, currency must be supplied explicitly", merchantID)
	}
}

// WriteOutboxEventFinalizeTx 在指定事务内写入 VCC_FUNDING_COMPLETED 事件。
// 使用调用方提供的 idempotencyKey（格式：vcc_funding_finalize:<bridge_batch_id>:<target_provider_id>），
// 配合 ON CONFLICT DO NOTHING 保证幂等。
func (r *VCCFundingDBRepository) WriteOutboxEventFinalizeTx(ctx context.Context, tx pgx.Tx, event OutboxEvent, idempotencyKey string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO transactional_outbox
			(id, aggregate_type, aggregate_id, event_type, payload, idempotency_key,
			 status, retry_count, next_retry_at, created_at, updated_at)
		VALUES ($1, 'vcc_funding', $2, $3, $4, $5,
			 'Pending', $6, now(), $7, now())
		ON CONFLICT (idempotency_key) DO NOTHING`,
		event.ID, uuid.NewSHA1(uuid.NameSpaceOID, []byte(idempotencyKey)),
		event.EventType, event.Payload, idempotencyKey,
		event.RetryCount, event.CreatedAt)
	return err
}

// ============================================================================
// R8: 业务幂等 + issuing provider 资金落点
// ============================================================================

// CreditIssuingProviderBalanceTx 在指定事务内向 issuing_provider_funding_balance 表插入 FUNDING_PENDING 记录。
// 使用 provider_idempotency_key 配合 ON CONFLICT DO NOTHING 保证幂等。
func (r *VCCFundingDBRepository) CreditIssuingProviderBalanceTx(ctx context.Context, tx pgx.Tx, providerID uuid.UUID, merchantID uuid.UUID, bridgeBatchID string, amountMinor int64, currency string, idempotencyKey string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO issuing_provider_funding_balance
			(provider_id, merchant_id, bridge_batch_id, amount_minor, currency, status, provider_idempotency_key)
		VALUES ($1, $2, $3, $4, $5, 'FUNDING_PENDING', $6)
		ON CONFLICT (provider_idempotency_key) DO NOTHING`,
		providerID, merchantID, bridgeBatchID, amountMinor, currency, idempotencyKey)
	return err
}

// ClaimFundingRequestTx 在事务内尝试取得源资金阶段业务幂等所有权。
// INSERT ON CONFLICT DO NOTHING RETURNING — 未取得所有权时返回 false。
func (r *VCCFundingDBRepository) ClaimFundingRequestTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, sourceLedgerTxID string, targetProviderID uuid.UUID, bridgeBatchID string, amountMinor int64, currency string, routeType string) (bool, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO vcc_funding_requests
			(merchant_id, source_ledger_tx_id, target_provider_id, bridge_batch_id, amount_minor, currency, route_type, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'REQUESTED')
		ON CONFLICT (merchant_id, source_ledger_tx_id, target_provider_id) DO NOTHING
		RETURNING id`,
		merchantID, sourceLedgerTxID, targetProviderID, bridgeBatchID, amountMinor, currency, routeType).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT — 已存在，未取得所有权
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ClaimFundingFinalizationTx 在事务内尝试取得最终入金阶段业务幂等所有权。
// INSERT ON CONFLICT DO NOTHING RETURNING — 未取得所有权时返回 false。
func (r *VCCFundingDBRepository) ClaimFundingFinalizationTx(ctx context.Context, tx pgx.Tx, bridgeBatchID string, targetProviderID uuid.UUID, merchantID uuid.UUID, amountMinor int64, currency string) (bool, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO vcc_funding_finalizations
			(bridge_batch_id, target_provider_id, merchant_id, amount_minor, currency, status)
		VALUES ($1, $2, $3, $4, $5, 'FINALIZING')
		ON CONFLICT (bridge_batch_id, target_provider_id) DO NOTHING
		RETURNING id`,
		bridgeBatchID, targetProviderID, merchantID, amountMinor, currency).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT — 已存在，未取得所有权
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// UpdateFundingRequestStatusTx 更新源资金请求状态。
func (r *VCCFundingDBRepository) UpdateFundingRequestStatusTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, sourceLedgerTxID string, targetProviderID uuid.UUID, status string) error {
	_, err := tx.Exec(ctx, `
		UPDATE vcc_funding_requests
		SET status = $4, updated_at = now()
		WHERE merchant_id = $1 AND source_ledger_tx_id = $2 AND target_provider_id = $3`,
		merchantID, sourceLedgerTxID, targetProviderID, status)
	return err
}

// WriteOutboxIssuingFundingTx 在指定事务内写入 VCC_ISSUING_FUNDING_REQUESTED 事件。
// 使用调用方提供的 idempotencyKey（格式：vcc_issuing_funding:<bridge_batch_id>:<target_provider_id>），
// 配合 ON CONFLICT DO NOTHING 保证幂等。aggregate_type 为 'vcc_issuing_funding'。
func (r *VCCFundingDBRepository) WriteOutboxIssuingFundingTx(ctx context.Context, tx pgx.Tx, event OutboxEvent, idempotencyKey string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO transactional_outbox
			(id, aggregate_type, aggregate_id, event_type, payload, idempotency_key,
			 status, retry_count, next_retry_at, created_at, updated_at)
		VALUES ($1, 'vcc_issuing_funding', $2, $3, $4, $5,
			 'Pending', $6, now(), $7, now())
		ON CONFLICT (idempotency_key) DO NOTHING`,
		event.ID, uuid.NewSHA1(uuid.NameSpaceOID, []byte(idempotencyKey)),
		event.EventType, event.Payload, idempotencyKey,
		event.RetryCount, event.CreatedAt)
	return err
}

// UpdateFundingFinalizationStatusTx 更新最终入金记录状态。
func (r *VCCFundingDBRepository) UpdateFundingFinalizationStatusTx(ctx context.Context, tx pgx.Tx, bridgeBatchID string, targetProviderID uuid.UUID, status string) error {
	_, err := tx.Exec(ctx, `
		UPDATE vcc_funding_finalizations
		SET status = $3, updated_at = now()
		WHERE bridge_batch_id = $1 AND target_provider_id = $2`,
		bridgeBatchID, targetProviderID, status)
	return err
}
