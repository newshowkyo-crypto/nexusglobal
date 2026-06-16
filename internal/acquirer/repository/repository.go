package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
)

// Repository 数据访问仓库
type Repository struct {
	pool *pgxpool.Pool
}

// New 创建仓库
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// GetPaymentProviderByID 根据 ID 获取提供商
func (r *Repository) GetPaymentProviderByID(ctx context.Context, id uuid.UUID) (*domain.PaymentProvider, error) {
	var p domain.PaymentProvider
	var configJSON []byte

	err := r.pool.QueryRow(ctx,
		`SELECT id, provider_code, provider_name, provider_type, config_json, is_active, version, created_at, updated_at
		FROM payment_provider WHERE id = $1`, id).Scan(
		&p.ID, &p.ProviderCode, &p.ProviderName, &p.ProviderType,
		&configJSON, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if len(configJSON) > 0 {
		json.Unmarshal(configJSON, &p.ConfigJSON)
	}
	return &p, nil
}

// GetPaymentProviderByCode 根据 Code 获取提供商
func (r *Repository) GetPaymentProviderByCode(ctx context.Context, code string) (*domain.PaymentProvider, error) {
	var p domain.PaymentProvider
	var configJSON []byte

	err := r.pool.QueryRow(ctx,
		`SELECT id, provider_code, provider_name, provider_type, config_json, is_active, version, created_at, updated_at
		FROM payment_provider WHERE provider_code = $1`, code).Scan(
		&p.ID, &p.ProviderCode, &p.ProviderName, &p.ProviderType,
		&configJSON, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if len(configJSON) > 0 {
		json.Unmarshal(configJSON, &p.ConfigJSON)
	}
	return &p, nil
}

// ListActivePaymentProviders 获取所有活跃提供商
func (r *Repository) ListActivePaymentProviders(ctx context.Context) ([]domain.PaymentProvider, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, provider_code, provider_name, provider_type, config_json, is_active, version, created_at, updated_at
		FROM payment_provider WHERE is_active = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []domain.PaymentProvider
	for rows.Next() {
		var p domain.PaymentProvider
		var configJSON []byte
		err := rows.Scan(
			&p.ID, &p.ProviderCode, &p.ProviderName, &p.ProviderType,
			&configJSON, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if len(configJSON) > 0 {
			json.Unmarshal(configJSON, &p.ConfigJSON)
		}
		providers = append(providers, p)
	}
	return providers, nil
}

// GetTenantAcquirerChannelByID 根据 ID 获取通道
func (r *Repository) GetTenantAcquirerChannelByID(ctx context.Context, id uuid.UUID) (*domain.TenantAcquirerChannel, error) {
	var c domain.TenantAcquirerChannel
	var configJSON []byte

	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, merchant_id, channel_code, provider_id, channel_config, is_active, version, created_at, updated_at
		FROM tenant_acquirer_channel WHERE id = $1`, id).Scan(
		&c.ID, &c.TenantID, &c.MerchantID, &c.ChannelCode, &c.ProviderID,
		&configJSON, &c.IsActive, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if len(configJSON) > 0 {
		json.Unmarshal(configJSON, &c.ChannelConfig)
	}
	return &c, nil
}

// GetTenantAcquirerChannelByCode 根据租户和通道代码获取
func (r *Repository) GetTenantAcquirerChannelByCode(ctx context.Context, tenantID, merchantID uuid.UUID, channelCode string) (*domain.TenantAcquirerChannel, error) {
	var c domain.TenantAcquirerChannel
	var configJSON []byte

	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, merchant_id, channel_code, provider_id, channel_config, is_active, version, created_at, updated_at
		FROM tenant_acquirer_channel WHERE tenant_id = $1 AND merchant_id = $2 AND channel_code = $3`,
		tenantID, merchantID, channelCode).Scan(
		&c.ID, &c.TenantID, &c.MerchantID, &c.ChannelCode, &c.ProviderID,
		&configJSON, &c.IsActive, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if len(configJSON) > 0 {
		json.Unmarshal(configJSON, &c.ChannelConfig)
	}
	return &c, nil
}

// ListActiveTenantAcquirerChannels 获取租户所有活跃通道
func (r *Repository) ListActiveTenantAcquirerChannels(ctx context.Context, tenantID uuid.UUID) ([]domain.TenantAcquirerChannel, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, merchant_id, channel_code, provider_id, channel_config, is_active, version, created_at, updated_at
		FROM tenant_acquirer_channel WHERE tenant_id = $1 AND is_active = true`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []domain.TenantAcquirerChannel
	for rows.Next() {
		var c domain.TenantAcquirerChannel
		var configJSON []byte
		err := rows.Scan(
			&c.ID, &c.TenantID, &c.MerchantID, &c.ChannelCode, &c.ProviderID,
			&configJSON, &c.IsActive, &c.Version, &c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if len(configJSON) > 0 {
			json.Unmarshal(configJSON, &c.ChannelConfig)
		}
		channels = append(channels, c)
	}
	return channels, nil
}

// ListAllActiveTenantAcquirerChannels 获取所有活跃通道
func (r *Repository) ListAllActiveTenantAcquirerChannels(ctx context.Context) ([]domain.TenantAcquirerChannel, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, merchant_id, channel_code, provider_id, channel_config, is_active, version, created_at, updated_at
		FROM tenant_acquirer_channel WHERE is_active = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []domain.TenantAcquirerChannel
	for rows.Next() {
		var c domain.TenantAcquirerChannel
		var configJSON []byte
		err := rows.Scan(
			&c.ID, &c.TenantID, &c.MerchantID, &c.ChannelCode, &c.ProviderID,
			&configJSON, &c.IsActive, &c.Version, &c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if len(configJSON) > 0 {
			json.Unmarshal(configJSON, &c.ChannelConfig)
		}
		channels = append(channels, c)
	}
	return channels, nil
}

// GetProviderCredential 获取提供商凭证（加密）
func (r *Repository) GetProviderCredential(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID, credType domain.CredentialType) (*domain.ProviderCredentialVault, error) {
	var cred domain.ProviderCredentialVault

	err := r.pool.QueryRow(ctx,
		`SELECT id, provider_id, channel_id, credential_type, key_version, ciphertext, aad_context, expires_at, created_at, updated_at
		FROM provider_credential_vault
		WHERE provider_id = $1 AND (channel_id = $2 OR channel_id IS NULL AND $2 IS NULL) AND credential_type = $3
		ORDER BY key_version DESC LIMIT 1`,
		providerID, channelID, credType).Scan(
		&cred.ID, &cred.ProviderID, &cred.ChannelID, &cred.CredentialType,
		&cred.KeyVersion, &cred.Ciphertext, &cred.AADContext,
		&cred.ExpiresAt, &cred.CreatedAt, &cred.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return &cred, nil
}

// GetProviderCredentials 获取提供商所有凭证
func (r *Repository) GetProviderCredentials(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID) ([]domain.ProviderCredentialVault, error) {
	var channelIDVal uuid.UUID
	if channelID != nil {
		channelIDVal = *channelID
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, provider_id, channel_id, credential_type, key_version, ciphertext, aad_context, expires_at, created_at, updated_at
		FROM provider_credential_vault
		WHERE provider_id = $1 AND (channel_id = $2 OR (channel_id IS NULL AND $2 = '00000000-0000-0000-0000-000000000000'))
		ORDER BY key_version DESC`,
		providerID, channelIDVal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []domain.ProviderCredentialVault
	for rows.Next() {
		var cred domain.ProviderCredentialVault
		err := rows.Scan(
			&cred.ID, &cred.ProviderID, &cred.ChannelID, &cred.CredentialType,
			&cred.KeyVersion, &cred.Ciphertext, &cred.AADContext,
			&cred.ExpiresAt, &cred.CreatedAt, &cred.UpdatedAt)
		if err != nil {
			return nil, err
		}
		creds = append(creds, cred)
	}
	return creds, nil
}

// DisableChannel 禁用通道
func (r *Repository) DisableChannel(ctx context.Context, channelID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE tenant_acquirer_channel SET is_active = false, updated_at = $2 WHERE id = $1`,
		channelID, time.Now())
	return err
}

// EnableChannel 启用通道
func (r *Repository) EnableChannel(ctx context.Context, channelID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE tenant_acquirer_channel SET is_active = true, updated_at = $2 WHERE id = $1`,
		channelID, time.Now())
	return err
}

// ListAllPaymentProviders 获取所有提供商
func (r *Repository) ListAllPaymentProviders(ctx context.Context) ([]domain.PaymentProvider, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, provider_code, provider_name, provider_type, config_json, is_active, version, created_at, updated_at
		FROM payment_provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []domain.PaymentProvider
	for rows.Next() {
		var p domain.PaymentProvider
		var configJSON []byte
		err := rows.Scan(
			&p.ID, &p.ProviderCode, &p.ProviderName, &p.ProviderType,
			&configJSON, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if len(configJSON) > 0 {
			json.Unmarshal(configJSON, &p.ConfigJSON)
		}
		providers = append(providers, p)
	}
	return providers, nil
}

// CreatePaymentProvider 创建提供商
func (r *Repository) CreatePaymentProvider(ctx context.Context, provider *domain.PaymentProvider) error {
	configJSON, _ := json.Marshal(provider.ConfigJSON)
	_, err := r.pool.Exec(ctx,
		`INSERT INTO payment_provider (id, provider_code, provider_name, provider_type, config_json, is_active, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		provider.ID, provider.ProviderCode, provider.ProviderName, provider.ProviderType,
		configJSON, provider.IsActive, provider.Version, provider.CreatedAt, provider.UpdatedAt)
	return err
}

// ListAllChannels 获取所有通道
func (r *Repository) ListAllChannels(ctx context.Context) ([]domain.TenantAcquirerChannel, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, merchant_id, provider_id, channel_code, channel_name, channel_config, currencies, payment_method, is_active, version, created_at, updated_at
		FROM tenant_acquirer_channel ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []domain.TenantAcquirerChannel
	for rows.Next() {
		var c domain.TenantAcquirerChannel
		var configJSON []byte
		err := rows.Scan(
			&c.ID, &c.TenantID, &c.MerchantID, &c.ProviderID,
			&c.ChannelCode, &c.ChannelName, &configJSON, &c.Currencies,
			&c.PaymentMethod, &c.IsActive, &c.Version, &c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if len(configJSON) > 0 {
			json.Unmarshal(configJSON, &c.ChannelConfig)
		}
		channels = append(channels, c)
	}
	return channels, nil
}

// GetChannelByID 根据 ID 获取通道
func (r *Repository) GetChannelByID(ctx context.Context, id uuid.UUID) (*domain.TenantAcquirerChannel, error) {
	var c domain.TenantAcquirerChannel
	var configJSON []byte

	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, merchant_id, provider_id, channel_code, channel_name, channel_config, currencies, payment_method, is_active, version, created_at, updated_at
		FROM tenant_acquirer_channel WHERE id = $1`, id).Scan(
		&c.ID, &c.TenantID, &c.MerchantID, &c.ProviderID,
		&c.ChannelCode, &c.ChannelName, &configJSON, &c.Currencies,
		&c.PaymentMethod, &c.IsActive, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if len(configJSON) > 0 {
		json.Unmarshal(configJSON, &c.ChannelConfig)
	}
	return &c, nil
}

// CreateChannel 创建通道
func (r *Repository) CreateChannel(ctx context.Context, channel *domain.TenantAcquirerChannel) error {
	configJSON, _ := json.Marshal(channel.ChannelConfig)
	currencies, _ := json.Marshal(channel.Currencies)
	_, err := r.pool.Exec(ctx,
		`INSERT INTO tenant_acquirer_channel (id, tenant_id, merchant_id, provider_id, channel_code, channel_name, channel_config, currencies, payment_method, is_active, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		channel.ID, channel.TenantID, channel.MerchantID, channel.ProviderID,
		channel.ChannelCode, channel.ChannelName, configJSON, currencies,
		channel.PaymentMethod, channel.IsActive, channel.Version, time.Now(), time.Now())
	return err
}

// UpdateChannel 更新通道
func (r *Repository) UpdateChannel(ctx context.Context, channel *domain.TenantAcquirerChannel) error {
	configJSON, _ := json.Marshal(channel.ChannelConfig)
	currencies, _ := json.Marshal(channel.Currencies)
	_, err := r.pool.Exec(ctx,
		`UPDATE tenant_acquirer_channel SET channel_name = $2, channel_config = $3, currencies = $4, is_active = $5, updated_at = $6 WHERE id = $1`,
		channel.ID, channel.ChannelName, configJSON, currencies, channel.IsActive, time.Now())
	return err
}

// UpdateChannelVersion 更新通道版本
func (r *Repository) UpdateChannelVersion(ctx context.Context, channelID uuid.UUID, version int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE tenant_acquirer_channel SET version = $2, updated_at = $3 WHERE id = $1`,
		channelID, version, time.Now())
	return err
}

// UpsertCredential 插入或更新凭证
func (r *Repository) UpsertCredential(ctx context.Context, cred *domain.ProviderCredentialVault) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO provider_credential_vault (id, provider_id, channel_id, credential_type, key_version, ciphertext, aad_context, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (provider_id, channel_id, credential_type, key_version) DO UPDATE SET ciphertext = $6, updated_at = $10`,
		cred.ID, cred.ProviderID, cred.ChannelID, cred.CredentialType,
		cred.KeyVersion, cred.Ciphertext, cred.AADContext,
		cred.ExpiresAt, cred.CreatedAt, cred.UpdatedAt)
	return err
}

// ListChannelsByTier 根据 provider_tier 获取通道列表
func (r *Repository) ListChannelsByTier(ctx context.Context, entityID uuid.UUID, tier domain.ProviderTier) ([]domain.AcquirerChannel, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status, version, provider_config, routing_config, created_at, updated_at
		FROM acquirer_channels WHERE entity_id = $1 AND provider_tier = $2 AND status = 'ACTIVE'`,
		entityID, tier)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []domain.AcquirerChannel
	for rows.Next() {
		var c domain.AcquirerChannel
		var providerConfigJSON, routingConfigJSON []byte
		if err := rows.Scan(
			&c.ID, &c.EntityID, &c.ProviderTier, &c.ProviderCode,
			&c.ChannelCode, &c.CredentialVaultKey, &c.StrategyMode,
			&c.Status, &c.Version, &providerConfigJSON, &routingConfigJSON,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if len(providerConfigJSON) > 0 {
			json.Unmarshal(providerConfigJSON, &c.ProviderConfig)
		}
		if len(routingConfigJSON) > 0 {
			json.Unmarshal(routingConfigJSON, &c.RoutingConfig)
		}
		channels = append(channels, c)
	}
	return channels, nil
}

// GetAcquirerChannelByID 根据 ID 获取 acquirer_channel
func (r *Repository) GetAcquirerChannelByID(ctx context.Context, id uuid.UUID) (*domain.AcquirerChannel, error) {
	var c domain.AcquirerChannel
	var providerConfigJSON, routingConfigJSON []byte

	err := r.pool.QueryRow(ctx,
		`SELECT id, entity_id, provider_tier, provider_code, channel_code, credential_vault_key, strategy_mode, status, version, provider_config, routing_config, created_at, updated_at
		FROM acquirer_channels WHERE id = $1`, id).Scan(
		&c.ID, &c.EntityID, &c.ProviderTier, &c.ProviderCode,
		&c.ChannelCode, &c.CredentialVaultKey, &c.StrategyMode,
		&c.Status, &c.Version, &providerConfigJSON, &routingConfigJSON,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if len(providerConfigJSON) > 0 {
		json.Unmarshal(providerConfigJSON, &c.ProviderConfig)
	}
	if len(routingConfigJSON) > 0 {
		json.Unmarshal(routingConfigJSON, &c.RoutingConfig)
	}
	return &c, nil
}
