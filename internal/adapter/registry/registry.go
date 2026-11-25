package registry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
	"github.com/nexusglobal/mamr/internal/adapter/provider"
	"github.com/nexusglobal/mamr/internal/ledger/fee"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

// FailMode 失败模式
type FailMode string

const (
	FailModeClosed       FailMode = "FAIL_CLOSED"   // 失败时拒绝服务
	FailModeStaleServing FailMode = "STALE_SERVING" // 失败时使用旧版本
)

// UniversalAdapterRegistry 通用适配器注册表
// 使用 RWMutex 保证并发安全，交易热路径不得访问 PG/KMS
// 支持 CARD_ACQUIRER / BANK_COLLECTION / CRYPTO_PAY / BANK_RAIL / EXCHANGE / LP / ONCHAIN_WALLET
type UniversalAdapterRegistry struct {
	repo       Repository
	encryptor  *crypto.Encryptor
	rdb        *redis.Client
	logger     *slog.Logger
	failMode   FailMode
	isRealMode bool                    // real/production 模式下未知 provider 必须 fail-closed
	feeService fee.FeeServiceInterface // 费用摊销服务

	mu      sync.RWMutex
	bundles map[uuid.UUID]*domain.AdapterBundle // channelID -> bundle

	// 按类型索引
	byType map[domain.AdapterType]map[uuid.UUID]*domain.AdapterBundle

	// 按租户索引
	byTenant map[uuid.UUID]map[uuid.UUID]*domain.AdapterBundle

	// 按法人实体索引（V3）
	byEntity map[uuid.UUID]map[uuid.UUID]*domain.AdapterBundle
}

// RegistryConfig 注册表配置
type RegistryConfig struct {
	Repo       Repository
	Encryptor  *crypto.Encryptor
	RDB        *redis.Client
	Logger     *slog.Logger
	FailMode   FailMode
	IsRealMode bool                    // real/production 模式下未知 provider 必须 fail-closed
	FeeService fee.FeeServiceInterface // 费用摊销服务
}

// Repository 适配器仓库接口
type Repository interface {
	// GetAdapterProviderByID 获取提供商
	GetAdapterProviderByID(ctx context.Context, providerID uuid.UUID) (*domain.AdapterProvider, error)

	// GetAdapterChannelByID 获取通道
	GetAdapterChannelByID(ctx context.Context, channelID uuid.UUID) (*domain.AdapterChannel, error)

	// GetAdapterChannelsByTenant 获取租户所有通道
	GetAdapterChannelsByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.AdapterChannel, error)

	// GetAdapterChannelsByType 获取指定类型的通道
	GetAdapterChannelsByType(ctx context.Context, tenantID uuid.UUID, adapterType domain.AdapterType) ([]domain.AdapterChannel, error)

	// GetAllAdapterChannels 获取所有通道（不分租户，启动时预加载用）
	GetAllAdapterChannels(ctx context.Context) ([]domain.AdapterChannel, error)

	// GetAdapterCredential 获取凭证
	GetAdapterCredential(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID, credType domain.CredentialType) (*domain.AdapterCredentialVault, error)

	// DisableChannel 禁用通道
	DisableChannel(ctx context.Context, channelID uuid.UUID) error

	// EnableChannel 启用通道
	EnableChannel(ctx context.Context, channelID uuid.UUID) error
}

// NewUniversalAdapterRegistry 创建通用适配器注册表
func NewUniversalAdapterRegistry(cfg RegistryConfig) *UniversalAdapterRegistry {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.FailMode == "" {
		cfg.FailMode = FailModeClosed // 生产环境默认 fail-closed
	}

	return &UniversalAdapterRegistry{
		repo:       cfg.Repo,
		encryptor:  cfg.Encryptor,
		rdb:        cfg.RDB,
		logger:     cfg.Logger,
		failMode:   cfg.FailMode,
		isRealMode: cfg.IsRealMode,
		feeService: cfg.FeeService,
		bundles:    make(map[uuid.UUID]*domain.AdapterBundle),
		byType:     make(map[domain.AdapterType]map[uuid.UUID]*domain.AdapterBundle),
		byTenant:   make(map[uuid.UUID]map[uuid.UUID]*domain.AdapterBundle),
		byEntity:   make(map[uuid.UUID]map[uuid.UUID]*domain.AdapterBundle),
	}
}

// LoadAll 加载所有适配器通道
// 启动时调用，可能访问 PG/KMS
func (r *UniversalAdapterRegistry) LoadAll(ctx context.Context) error {
	r.logger.Info("loading all adapter channels")

	channels, err := r.repo.GetAllAdapterChannels(ctx)
	if err != nil {
		r.logger.Error("failed to get all adapter channels", "err", err)
		return err
	}

	r.mu.Lock()
	r.bundles = make(map[uuid.UUID]*domain.AdapterBundle)
	r.byType = make(map[domain.AdapterType]map[uuid.UUID]*domain.AdapterBundle)
	r.byTenant = make(map[uuid.UUID]map[uuid.UUID]*domain.AdapterBundle)
	r.byEntity = make(map[uuid.UUID]map[uuid.UUID]*domain.AdapterBundle)
	r.mu.Unlock()

	for _, channel := range channels {
		bundle, err := r.loadBundle(ctx, channel)
		if err != nil {
			r.logger.Error("failed to load bundle",
				"channel_id", channel.ID,
				"channel_code", channel.ChannelCode,
				"adapter_type", channel.AdapterType,
				"err", err)

			bundle = &domain.AdapterBundle{
				ChannelID:   channel.ID,
				ChannelCode: channel.ChannelCode,
				AdapterType: channel.AdapterType,
				IsActive:    false,
				LoadError:   err,
				LoadedAt:    time.Now(),
			}
		}

		r.mu.Lock()
		r.bundles[channel.ID] = bundle

		if r.byType[bundle.AdapterType] == nil {
			r.byType[bundle.AdapterType] = make(map[uuid.UUID]*domain.AdapterBundle)
		}
		r.byType[bundle.AdapterType][channel.ID] = bundle

		if r.byTenant[channel.TenantID] == nil {
			r.byTenant[channel.TenantID] = make(map[uuid.UUID]*domain.AdapterBundle)
		}
		r.byTenant[channel.TenantID][channel.ID] = bundle

		if channel.CorporateEntityID != nil {
			if r.byEntity[*channel.CorporateEntityID] == nil {
				r.byEntity[*channel.CorporateEntityID] = make(map[uuid.UUID]*domain.AdapterBundle)
			}
			r.byEntity[*channel.CorporateEntityID][channel.ID] = bundle
		}
		r.mu.Unlock()

		r.logger.Info("adapter channel loaded",
			"channel_id", channel.ID,
			"channel_code", channel.ChannelCode,
			"adapter_type", channel.AdapterType,
			"provider_id", channel.ProviderID,
			"version", channel.Version)
	}

	r.logger.Info("all adapter channels loaded", "count", len(channels))
	return nil
}

// LoadTenant 加载指定租户的适配器通道
func (r *UniversalAdapterRegistry) LoadTenant(ctx context.Context, tenantID uuid.UUID) error {
	channels, err := r.repo.GetAdapterChannelsByTenant(ctx, tenantID)
	if err != nil {
		r.logger.Error("failed to get channels for tenant",
			"tenant_id", tenantID,
			"err", err)
		return err
	}

	for _, channel := range channels {
		bundle, err := r.loadBundle(ctx, channel)
		if err != nil {
			r.logger.Error("failed to load bundle",
				"channel_id", channel.ID,
				"channel_code", channel.ChannelCode,
				"adapter_type", channel.AdapterType,
				"err", err)

			// 记录错误但继续加载其他通道
			bundle = &domain.AdapterBundle{
				ChannelID:   channel.ID,
				ChannelCode: channel.ChannelCode,
				AdapterType: channel.AdapterType,
				IsActive:    false,
				LoadError:   err,
				LoadedAt:    time.Now(),
			}
		}

		r.mu.Lock()
		r.bundles[channel.ID] = bundle

		// 更新类型索引
		if r.byType[bundle.AdapterType] == nil {
			r.byType[bundle.AdapterType] = make(map[uuid.UUID]*domain.AdapterBundle)
		}
		r.byType[bundle.AdapterType][channel.ID] = bundle

		// 更新租户索引
		if r.byTenant[channel.TenantID] == nil {
			r.byTenant[channel.TenantID] = make(map[uuid.UUID]*domain.AdapterBundle)
		}
		r.byTenant[channel.TenantID][channel.ID] = bundle

		// 更新法人实体索引（V3）
		if channel.CorporateEntityID != nil {
			if r.byEntity[*channel.CorporateEntityID] == nil {
				r.byEntity[*channel.CorporateEntityID] = make(map[uuid.UUID]*domain.AdapterBundle)
			}
			r.byEntity[*channel.CorporateEntityID][channel.ID] = bundle
		}
		r.mu.Unlock()

		r.logger.Info("adapter channel loaded",
			"channel_id", channel.ID,
			"channel_code", channel.ChannelCode,
			"adapter_type", channel.AdapterType,
			"provider_id", channel.ProviderID,
			"version", channel.Version)
	}

	return nil
}

// Get 获取适配器 bundle
// 交易热路径调用，不得访问 PG/KMS
func (r *UniversalAdapterRegistry) Get(channelID uuid.UUID) (*domain.AdapterBundle, error) {
	r.mu.RLock()
	bundle, ok := r.bundles[channelID]
	r.mu.RUnlock()

	if !ok {
		return nil, domain.ErrAdapterNotFound
	}

	if !bundle.IsActive {
		return nil, domain.ErrAdapterDisabled
	}

	if bundle.LoadError != nil {
		// 根据 failMode 处理
		if r.failMode == FailModeClosed {
			return nil, bundle.LoadError
		}
		// stale-but-serving: 返回旧版本，记录警告
		r.logger.Warn("using stale bundle",
			"channel_id", channelID,
			"load_error", bundle.LoadError)
	}

	return bundle, nil
}

// GetByType 获取指定类型的适配器
// 交易热路径调用，不得访问 PG/KMS
func (r *UniversalAdapterRegistry) GetByType(tenantID uuid.UUID, adapterType domain.AdapterType) []*domain.AdapterBundle {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*domain.AdapterBundle, 0)

	// 先按租户过滤
	tenantBundles, ok := r.byTenant[tenantID]
	if !ok {
		return result
	}

	// 再按类型过滤
	for _, bundle := range tenantBundles {
		if bundle.AdapterType == adapterType && domain.IsAdapterAvailable(bundle) {
			result = append(result, bundle)
		}
	}

	return result
}

// GetByEntity 获取指定法人实体的适配器（V3）
func (r *UniversalAdapterRegistry) GetByEntity(entityID uuid.UUID) []*domain.AdapterBundle {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*domain.AdapterBundle, 0)
	entityBundles, ok := r.byEntity[entityID]
	if !ok {
		return result
	}

	for _, bundle := range entityBundles {
		if domain.IsAdapterAvailable(bundle) {
			result = append(result, bundle)
		}
	}

	return result
}

// Reload 重新加载单个通道
// 配置变更后调用，原子替换 bundle
func (r *UniversalAdapterRegistry) Reload(ctx context.Context, channelID uuid.UUID) error {
	channel, err := r.repo.GetAdapterChannelByID(ctx, channelID)
	if err != nil {
		return err
	}

	bundle, err := r.loadBundle(ctx, *channel)
	if err != nil {
		r.logger.Error("reload channel failed",
			"channel_id", channelID,
			"err", err)

		// 更新 bundle 记录错误
		r.mu.Lock()
		if oldBundle, ok := r.bundles[channelID]; ok {
			oldBundle.LoadError = err
			oldBundle.IsActive = false
		} else {
			r.bundles[channelID] = &domain.AdapterBundle{
				ChannelID: channelID,
				LoadError: err,
				IsActive:  false,
				LoadedAt:  time.Now(),
			}
		}
		r.mu.Unlock()

		return err
	}

	// 子替换
	r.mu.Lock()
	r.bundles[channelID] = bundle

	// 更新索引
	if r.byType[bundle.AdapterType] == nil {
		r.byType[bundle.AdapterType] = make(map[uuid.UUID]*domain.AdapterBundle)
	}
	r.byType[bundle.AdapterType][channelID] = bundle

	if r.byTenant[channel.TenantID] == nil {
		r.byTenant[channel.TenantID] = make(map[uuid.UUID]*domain.AdapterBundle)
	}
	r.byTenant[channel.TenantID][channelID] = bundle

	if channel.CorporateEntityID != nil {
		if r.byEntity[*channel.CorporateEntityID] == nil {
			r.byEntity[*channel.CorporateEntityID] = make(map[uuid.UUID]*domain.AdapterBundle)
		}
		r.byEntity[*channel.CorporateEntityID][channelID] = bundle
	}
	r.mu.Unlock()

	r.logger.Info("adapter channel reloaded",
		"channel_id", channelID,
		"adapter_type", bundle.AdapterType,
		"version", bundle.Version)

	return nil
}

// Disable 禁用通道
func (r *UniversalAdapterRegistry) Disable(ctx context.Context, channelID uuid.UUID) error {
	// 先禁用数据库
	if err := r.repo.DisableChannel(ctx, channelID); err != nil {
		return err
	}

	// 更新内存状态
	r.mu.Lock()
	if bundle, ok := r.bundles[channelID]; ok {
		bundle.IsActive = false
	}
	r.mu.Unlock()

	r.logger.Warn("adapter channel disabled", "channel_id", channelID)
	return nil
}

// Enable 启用通道并重新加载
func (r *UniversalAdapterRegistry) Enable(ctx context.Context, channelID uuid.UUID) error {
	// 先启用数据库
	if err := r.repo.EnableChannel(ctx, channelID); err != nil {
		return err
	}

	// 重新加载
	return r.Reload(ctx, channelID)
}

// loadBundle 加载适配器 bundle
// 内部方法，可能访问 PG/KMS
func (r *UniversalAdapterRegistry) loadBundle(ctx context.Context, channel domain.AdapterChannel) (*domain.AdapterBundle, error) {
	// 获取提供商配置
	provider, err := r.repo.GetAdapterProviderByID(ctx, channel.ProviderID)
	if err != nil {
		return nil, err
	}

	// 获取凭证并解密
	credentials := make(map[domain.CredentialType]string)
	credTypes := []domain.CredentialType{
		domain.CredentialTypeAPIKey,
		domain.CredentialTypeAPISecret,
		domain.CredentialTypeWebhookSecret,
		domain.CredentialTypeClientCert,
		domain.CredentialTypeSigningKeyRef,
		domain.CredentialTypeRPCToken,
		domain.CredentialTypeWithdrawKeyRef,
	}

	for _, ct := range credTypes {
		cred, err := r.repo.GetAdapterCredential(ctx, provider.ID, &channel.ID, ct)
		if err != nil {
			// 某些凭证可能不存在，继续
			continue
		}

		if cred == nil {
			continue
		}

		// 解密
		if r.encryptor != nil {
			decrypted, err := r.encryptor.Decrypt(ctx, cred.Ciphertext, crypto.Meta{
				TenantID:   channel.TenantID.String(),
				RecordID:   channel.ID.String(),
				FieldName:  string(ct),
				KeyVersion: fmt.Sprintf("%d", cred.KeyVersion),
			})
			if err != nil {
				r.logger.Error("decrypt credential failed",
					"channel_id", channel.ID,
					"credential_type", ct,
					"err", err)
				return nil, domain.ErrCredentialDecrypt
			}
			credentials[ct] = string(decrypted)
		} else {
			// 无加密器时，直接使用密文（仅用于测试）
			credentials[ct] = string(cred.Ciphertext)
		}
	}

	// 创建客户端
	client, err := r.createClient(provider, credentials, channel.ChannelConfig)
	if err != nil {
		r.logger.Error("create adapter client failed",
			"channel_id", channel.ID,
			"adapter_type", channel.AdapterType,
			"err", err)
		return nil, err
	}

	return &domain.AdapterBundle{
		ChannelID:         channel.ID,
		TenantID:          channel.TenantID,
		CorporateEntityID: channel.CorporateEntityID,
		ChannelCode:       channel.ChannelCode,
		ProviderID:        provider.ID,
		ProviderCode:      provider.ProviderCode,
		AdapterType:       channel.AdapterType,
		Client:            client,
		SupportedAssets:   channel.SupportedAssets,
		SupportedNetworks: channel.SupportedNetworks,
		Config:            channel.ChannelConfig,
		RoutingConfig:     channel.RoutingConfig,
		Version:           channel.Version,
		IsActive:          channel.Status == domain.AdapterStatusActive,
		LoadedAt:          time.Now(),
		LoadError:         nil,
	}, nil
}

// createClient 创建适配器客户端
// 根据 adapter_type 创建对应客户端
func (r *UniversalAdapterRegistry) createClient(provider *domain.AdapterProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AdapterClient, error) {
	switch provider.AdapterType {
	case domain.AdapterTypeCardAcquirer:
		return r.createCardAcquirerClient(provider, credentials, channelConfig)

	case domain.AdapterTypeBankCollection:
		return r.createBankCollectionClient(provider, credentials, channelConfig)

	case domain.AdapterTypeCryptoPay:
		return r.createCryptoPayClient(provider, credentials, channelConfig)

	case domain.AdapterTypeBankRail:
		return r.createBankRailClient(provider, credentials, channelConfig)

	case domain.AdapterTypeExchange:
		return r.createExchangeClient(provider, credentials, channelConfig)

	case domain.AdapterTypeLP:
		return r.createLPClient(provider, credentials, channelConfig)

	case domain.AdapterTypeOnchainWallet:
		return r.createOnchainWalletClient(provider, credentials, channelConfig)

	default:
		r.logger.Warn("unsupported adapter type",
			"adapter_type", provider.AdapterType,
			"provider_code", provider.ProviderCode)
		return nil, domain.ErrUnsupportedAdapterType
	}
}

// createCardAcquirerClient 创建卡网收单客户端
// 兼容现有 acquirer adapter
func (r *UniversalAdapterRegistry) createCardAcquirerClient(p *domain.AdapterProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AdapterClient, error) {
	apiKey := credentials[domain.CredentialTypeAPIKey]
	apiSecret := credentials[domain.CredentialTypeAPISecret]
	webhookSecret := credentials[domain.CredentialTypeWebhookSecret]

	// 从 p.ProviderCode 判断具体是哪个卡网
	switch strings.ToLower(p.ProviderCode) {
	case "mock":
		// mock 是显式声明的 mock provider，仅允许在非 real 模式（local/test）使用。
		// real/production 模式下 mock provider 必须拒绝启动。
		if r.isRealMode {
			r.logger.Error("mock card acquirer provider not allowed in real mode",
				"provider_code", p.ProviderCode)
			return nil, fmt.Errorf("mock card acquirer provider not allowed in real mode: %s", p.ProviderCode)
		}
		return provider.NewMockCardAcquirer(webhookSecret, channelConfig)

	case "stripe":
		return provider.NewStripeCardAcquirer(apiSecret, webhookSecret, channelConfig)

	case "nxtapay":
		return provider.NewNxtapayCardAcquirer(apiKey, apiSecret, webhookSecret, channelConfig)

	case "hyperswitch":
		return provider.NewHyperswitchCardAcquirer(apiKey, webhookSecret, channelConfig)

	default:
		// G06: 未知 provider_code 一律拒绝，禁止回退 mock。
		// 旧逻辑在非 real 模式下 fallback 到 mock，导致非 mock VCC 配置接 Mock bridge。
		r.logger.Error("unknown card acquirer provider, fail-closed (no mock fallback)",
			"provider_code", p.ProviderCode,
			"is_real_mode", r.isRealMode)
		return nil, fmt.Errorf("unknown card acquirer provider %q: no mock fallback (UNSUPPORTED_CAPABILITY)", p.ProviderCode)
	}
}

// createBankCollectionClient 创建银行收款客户端
func (r *UniversalAdapterRegistry) createBankCollectionClient(p *domain.AdapterProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AdapterClient, error) {
	apiKey := credentials[domain.CredentialTypeAPIKey]
	apiSecret := credentials[domain.CredentialTypeAPISecret]
	webhookSecret := credentials[domain.CredentialTypeWebhookSecret]

	if apiKey == "" {
		r.logger.Error("bank collection: API key is required")
		return nil, domain.ErrCredentialMissing
	}

	return provider.NewBankCollectionAdapter(p.ProviderCode, apiKey, apiSecret, webhookSecret, channelConfig)
}

// createCryptoPayClient 创建加密货币支付客户端
func (r *UniversalAdapterRegistry) createCryptoPayClient(p *domain.AdapterProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AdapterClient, error) {
	rpcToken := credentials[domain.CredentialTypeRPCToken]
	webhookSecret := credentials[domain.CredentialTypeWebhookSecret]

	return provider.NewCryptoPayAdapter(p.ProviderCode, rpcToken, webhookSecret, channelConfig)
}

// createBankRailClient 创建银行通道客户端
func (r *UniversalAdapterRegistry) createBankRailClient(p *domain.AdapterProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AdapterClient, error) {
	apiKey := credentials[domain.CredentialTypeAPIKey]
	apiSecret := credentials[domain.CredentialTypeAPISecret]

	if apiKey == "" {
		r.logger.Error("bank rail: API key is required")
		return nil, domain.ErrCredentialMissing
	}

	return provider.NewBankRailAdapter(p.ProviderCode, apiKey, apiSecret, channelConfig)
}

// createExchangeClient 创建交易所客户端
func (r *UniversalAdapterRegistry) createExchangeClient(p *domain.AdapterProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AdapterClient, error) {
	apiKey := credentials[domain.CredentialTypeAPIKey]
	apiSecret := credentials[domain.CredentialTypeAPISecret]
	withdrawKeyRef := credentials[domain.CredentialTypeWithdrawKeyRef]

	if apiKey == "" {
		r.logger.Error("exchange: API key is required")
		return nil, domain.ErrCredentialMissing
	}

	return provider.NewExchangeAdapter(p.ProviderCode, apiKey, apiSecret, withdrawKeyRef, r.feeService, channelConfig)
}

// createLPClient 创建流动性提供商客户端
func (r *UniversalAdapterRegistry) createLPClient(p *domain.AdapterProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AdapterClient, error) {
	apiKey := credentials[domain.CredentialTypeAPIKey]
	apiSecret := credentials[domain.CredentialTypeAPISecret]

	if apiKey == "" {
		r.logger.Error("LP: API key is required")
		return nil, domain.ErrCredentialMissing
	}

	return provider.NewLPAdapter(p.ProviderCode, apiKey, apiSecret, r.feeService, channelConfig)
}

// createOnchainWalletClient 创建链上钱包客户端
func (r *UniversalAdapterRegistry) createOnchainWalletClient(p *domain.AdapterProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AdapterClient, error) {
	signingKeyRef := credentials[domain.CredentialTypeSigningKeyRef]

	return provider.NewOnchainWalletAdapter(p.ProviderCode, signingKeyRef, channelConfig)
}

// SubscribeConfigUpdates 订阅配置变更事件
// Redis Pub/Sub 监听 adapter.config.updated
func (r *UniversalAdapterRegistry) SubscribeConfigUpdates(ctx context.Context) error {
	if r.rdb == nil {
		r.logger.Warn("redis not available, config update subscription disabled")
		return nil
	}

	pubsub := r.rdb.Subscribe(ctx, "adapter.config.updated")
	ch := pubsub.Channel()

	go func() {
		for msg := range ch {
			// payload 格式: channel_id|version
			parts := strings.Split(msg.Payload, "|")
			if len(parts) != 2 {
				r.logger.Error("invalid config update payload format", "payload", msg.Payload)
				continue
			}

			channelID, err := uuid.Parse(parts[0])
			if err != nil {
				r.logger.Error("invalid channel id in config update", "payload", msg.Payload, "err", err)
				continue
			}

			var newVersion int
			if _, err := fmt.Sscanf(parts[1], "%d", &newVersion); err != nil {
				r.logger.Error("invalid version number in config update", "payload", msg.Payload, "err", err)
				continue
			}

			// 检查版本，避免旧版本覆盖新版本
			r.mu.RLock()
			existingBundle, ok := r.bundles[channelID]
			r.mu.RUnlock()

			if ok && existingBundle.Version >= newVersion {
				r.logger.Info("skip reload: existing version is newer or equal",
					"channel_id", channelID,
					"existing_version", existingBundle.Version,
					"event_version", newVersion)
				continue
			}

			r.logger.Info("received config update",
				"channel_id", channelID,
				"version", newVersion)

			// 重新加载
			if err := r.Reload(ctx, channelID); err != nil {
				r.logger.Error("reload failed on config update",
					"channel_id", channelID,
					"err", err)
			}
		}
	}()

	return nil
}
