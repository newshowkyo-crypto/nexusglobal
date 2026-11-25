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

	"github.com/nexusglobal/mamr/internal/acquirer/adapter"
	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

type ChannelRegistry struct {
	mu            sync.RWMutex
	bundles       map[uuid.UUID]*domain.ClientBundle
	statusCache   map[uuid.UUID]ChannelStatus
	statusCacheMu sync.RWMutex
	repo          AcquirerRepository
	encryptor     *crypto.Encryptor
	rdb           *redis.Client
	logger        *slog.Logger
	failMode      FailMode
	isRealMode    bool
	factory       AcquirerAdapterFactory
}

type ChannelStatus struct {
	ChannelID     uuid.UUID
	IsActive      bool
	IsBurnMode    bool
	ProviderTier  domain.ProviderTier
	Version       int
	LastUpdatedAt time.Time
}

type AcquirerRepository interface {
	ListAllActiveTenantAcquirerChannels(ctx context.Context) ([]domain.TenantAcquirerChannel, error)
	GetTenantAcquirerChannelByID(ctx context.Context, id uuid.UUID) (*domain.TenantAcquirerChannel, error)
	GetPaymentProviderByID(ctx context.Context, id uuid.UUID) (*domain.PaymentProvider, error)
	GetProviderCredential(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID, credType domain.CredentialType) (*domain.ProviderCredentialVault, error)
	DisableChannel(ctx context.Context, channelID uuid.UUID) error
	EnableChannel(ctx context.Context, channelID uuid.UUID) error
	ListChannelsByTier(ctx context.Context, entityID uuid.UUID, tier domain.ProviderTier) ([]domain.AcquirerChannel, error)
	GetAcquirerChannelByID(ctx context.Context, id uuid.UUID) (*domain.AcquirerChannel, error)
	// P0-B2: 按商户和币种筛选活跃通道（通道路由）
	ListActiveChannelsByMerchantAndCurrency(ctx context.Context, merchantID uuid.UUID, currency string) ([]domain.TenantAcquirerChannel, error)
	// P0-1: 获取商户权威路由策略
	GetMerchantRoutingPolicy(ctx context.Context, merchantID uuid.UUID) (*domain.MerchantRoutingPolicy, error)
}

type AcquirerAdapterFactory interface {
	Create(ctx context.Context, providerCode string, credentials map[domain.CredentialType]string, config map[string]any) (domain.AcquirerAdapter, error)
}

type FailMode string

const (
	FailModeClosed       FailMode = "fail-closed"
	FailModeStaleServing FailMode = "stale-but-serving"
)

type RegistryConfig struct {
	Repo       AcquirerRepository
	Encryptor  *crypto.Encryptor
	RDB        *redis.Client
	Logger     *slog.Logger
	FailMode   FailMode
	IsRealMode bool
	Factory    AcquirerAdapterFactory
}

func NewChannelRegistry(cfg RegistryConfig) *ChannelRegistry {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.FailMode == "" {
		cfg.FailMode = FailModeClosed
	}

	reg := &ChannelRegistry{
		bundles:     make(map[uuid.UUID]*domain.ClientBundle),
		statusCache: make(map[uuid.UUID]ChannelStatus),
		repo:        cfg.Repo,
		encryptor:   cfg.Encryptor,
		rdb:         cfg.RDB,
		logger:      cfg.Logger,
		failMode:    cfg.FailMode,
		isRealMode:  cfg.IsRealMode,
		factory:     cfg.Factory,
	}

	return reg
}

func (r *ChannelRegistry) LoadAll(ctx context.Context) error {
	// P0-3: real mode 下必须注入 AcquirerAdapterFactory
	if r.isRealMode && r.factory == nil {
		return fmt.Errorf("acquirer registry: real mode requires AcquirerAdapterFactory, fail-closed")
	}

	channels, err := r.repo.ListAllActiveTenantAcquirerChannels(ctx)
	if err != nil {
		r.logger.Error("load all channels failed", "err", err)
		return err
	}

	// P0-2: real mode 下检查未绑定的 active runtime channel
	if r.isRealMode {
		for _, channel := range channels {
			if channel.AcquirerChannelID == nil {
				return fmt.Errorf("acquirer registry: active runtime channel %s has no acquirer_channel_id binding, fail-closed", channel.ID)
			}
		}
	}

	for _, channel := range channels {
		bundle, err := r.loadBundle(ctx, channel)
		if err != nil {
			r.logger.Error("load channel bundle failed",
				"channel_id", channel.ID,
				"channel_code", channel.ChannelCode,
				"err", err)
			if r.isRealMode {
				return fmt.Errorf("acquirer registry: load channel bundle failed in real mode, fail-closed: channel_id=%s err=%w", channel.ID, err)
			}
			continue
		}

		r.mu.Lock()
		r.bundles[channel.ID] = bundle
		r.statusCache[channel.ID] = ChannelStatus{
			ChannelID:     channel.ID,
			IsActive:      bundle.IsActive,
			IsBurnMode:    r.checkBurnMode(channel.ChannelConfig),
			Version:       bundle.Version,
			LastUpdatedAt: time.Now(),
		}
		r.mu.Unlock()

		r.logger.Info("channel loaded",
			"channel_id", channel.ID,
			"channel_code", channel.ChannelCode,
			"provider_id", channel.ProviderID,
			"version", channel.Version)
	}

	return nil
}

func (r *ChannelRegistry) Get(channelID uuid.UUID) (*domain.ClientBundle, error) {
	// P0-5: BURN_MODE 的 bundle 不可用于路由，fail-closed
	r.statusCacheMu.RLock()
	status, ok := r.statusCache[channelID]
	r.statusCacheMu.RUnlock()
	if ok && status.IsBurnMode {
		return nil, fmt.Errorf("acquirer registry: channel %s is in BURN_MODE, fail-closed", channelID)
	}

	r.mu.RLock()
	bundle, ok := r.bundles[channelID]
	r.mu.RUnlock()

	if !ok {
		return nil, domain.ErrChannelNotFound
	}

	if !bundle.IsActive {
		return nil, domain.ErrChannelDisabled
	}

	if bundle.LoadError != nil {
		if r.failMode == FailModeClosed {
			return nil, bundle.LoadError
		}
		r.logger.Warn("using stale bundle",
			"channel_id", channelID,
			"load_error", bundle.LoadError)
	}

	return bundle, nil
}

func (r *ChannelRegistry) GetChannelStatus(channelID uuid.UUID) (ChannelStatus, bool) {
	r.statusCacheMu.RLock()
	status, ok := r.statusCache[channelID]
	r.statusCacheMu.RUnlock()
	return status, ok
}

func (r *ChannelRegistry) SelectByTier(ctx context.Context, entityID uuid.UUID, tier domain.ProviderTier) ([]domain.AcquirerChannel, error) {
	if err := tier.Validate(); err != nil {
		return nil, err
	}

	channels, err := r.repo.ListChannelsByTier(ctx, entityID, tier)
	if err != nil {
		return nil, err
	}

	activeChannels := make([]domain.AcquirerChannel, 0)
	for _, ch := range channels {
		if ch.Status != "ACTIVE" {
			continue
		}
		if ch.StrategyMode.IsBurnMode() {
			r.logger.Warn("channel in BURN_MODE skipped", "channel_id", ch.ID)
			continue
		}
		if ch.CredentialVaultKey == "" {
			r.logger.Warn("channel credential_vault_key empty", "channel_id", ch.ID)
			continue
		}
		activeChannels = append(activeChannels, ch)
	}

	return activeChannels, nil
}

// SelectChannel 选择单一通道（兼容包装）。
//
// P1: 多通道匹配不再视为配置错误，而是取候选集第一个；
// 真正的多候选择优由 internal/pool 的打分器负责。
// 无匹配通道时返回 ErrChannelNotFound（Fail-Closed，不调用 Provider）。
func (r *ChannelRegistry) SelectChannel(ctx context.Context, merchantID uuid.UUID, currency string, amountMinor int64) (*domain.TenantAcquirerChannel, error) {
	candidates, err := r.SelectCandidates(ctx, merchantID, currency, amountMinor)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, domain.ErrChannelNotFound
	}
	if len(candidates) > 1 {
		r.logger.Warn("multiple acquirer channels matched, using first candidate (pool should rank them)",
			"merchant_id", merchantID, "currency", currency, "count", len(candidates))
	}
	return &candidates[0], nil
}

// SelectCandidates 返回该商户在该币种下可用的全部通道候选（P1）。
//
// 使用 MerchantRoutingPolicy 的 ProviderTier 精确匹配通道，不得接受 body/header/payload 指定 tier。
// 候选集为空时返回空切片（调用方 fail-closed）。
func (r *ChannelRegistry) SelectCandidates(ctx context.Context, merchantID uuid.UUID, currency string, amountMinor int64) ([]domain.TenantAcquirerChannel, error) {
	// 1. 获取商户权威 tier policy
	policy, err := r.repo.GetMerchantRoutingPolicy(ctx, merchantID)
	if err != nil {
		return nil, fmt.Errorf("acquirer registry: resolve merchant routing policy: %w", err)
	}
	if policy == nil {
		return nil, fmt.Errorf("acquirer registry: no routing policy for merchant %s, fail-closed", merchantID)
	}
	if policy.Status != "ACTIVE" {
		return nil, fmt.Errorf("acquirer registry: merchant %s policy status is %s, fail-closed", merchantID, policy.Status)
	}
	if err := policy.ProviderTier.Validate(); err != nil {
		return nil, fmt.Errorf("acquirer registry: merchant %s policy tier invalid: %w", merchantID, err)
	}

	// 2. 从 DB 获取该 merchant 的、支持指定币种的活跃通道（INNER JOIN 已过滤未绑定和非 ACTIVE）
	channels, err := r.repo.ListActiveChannelsByMerchantAndCurrency(ctx, merchantID, currency)
	if err != nil {
		return nil, fmt.Errorf("acquirer registry: list channels for merchant %s: %w", merchantID, err)
	}

	if len(channels) == 0 {
		return nil, nil
	}

	// 3. 过滤：非 BURN_MODE、ProviderTier 与权威 policy 精确匹配
	filtered := make([]domain.TenantAcquirerChannel, 0)
	for _, ch := range channels {
		if !ch.IsActive {
			continue
		}
		if ch.ChannelConfig != nil {
			if burnMode, ok := ch.ChannelConfig["burn_mode"].(bool); ok && burnMode {
				r.logger.Warn("channel in BURN_MODE skipped for routing", "channel_id", ch.ID)
				continue
			}
		}
		// 精确匹配 tier：通道的 ProviderTier 必须与商户权威 policy 一致
		if ch.ProviderTier != policy.ProviderTier {
			r.logger.Warn("channel provider_tier does not match merchant policy, skipped",
				"channel_id", ch.ID,
				"channel_tier", ch.ProviderTier,
				"policy_tier", policy.ProviderTier)
			continue
		}
		filtered = append(filtered, ch)
	}

	return filtered, nil
}

func (r *ChannelRegistry) checkBurnMode(config map[string]any) bool {
	if config == nil {
		return false
	}
	if burnMode, ok := config["burn_mode"].(bool); ok && burnMode {
		return true
	}
	return false
}

func (r *ChannelRegistry) Reload(ctx context.Context, channelID uuid.UUID) error {
	// P0-3: real mode 下必须注入 AcquirerAdapterFactory
	if r.isRealMode && r.factory == nil {
		return fmt.Errorf("acquirer registry: real mode requires AcquirerAdapterFactory, fail-closed")
	}

	channel, err := r.repo.GetTenantAcquirerChannelByID(ctx, channelID)
	if err != nil {
		return err
	}

	bundle, err := r.loadBundle(ctx, *channel)
	if err != nil {
		r.logger.Error("reload channel failed",
			"channel_id", channelID,
			"err", err)

		r.mu.Lock()
		if oldBundle, ok := r.bundles[channelID]; ok {
			oldBundle.LoadError = err
			oldBundle.IsActive = false
		} else {
			r.bundles[channelID] = &domain.ClientBundle{
				ChannelID: channelID,
				LoadError: err,
				IsActive:  false,
			}
		}
		r.mu.Unlock()

		return err
	}

	// P0-1: BURN_MODE 防御性检查 — 即使 DB is_active=true，BURN_MODE bundle 也不可用
	if r.checkBurnMode(channel.ChannelConfig) {
		bundle.IsActive = false
	}

	r.mu.Lock()
	r.bundles[channelID] = bundle
	r.statusCache[channelID] = ChannelStatus{
		ChannelID:     channelID,
		IsActive:      bundle.IsActive,
		IsBurnMode:    r.checkBurnMode(channel.ChannelConfig),
		Version:       bundle.Version,
		LastUpdatedAt: time.Now(),
	}
	r.mu.Unlock()

	r.logger.Info("channel reloaded",
		"channel_id", channelID,
		"version", bundle.Version)

	return nil
}

func (r *ChannelRegistry) Disable(ctx context.Context, channelID uuid.UUID) error {
	if err := r.repo.DisableChannel(ctx, channelID); err != nil {
		return err
	}

	r.mu.Lock()
	if bundle, ok := r.bundles[channelID]; ok {
		bundle.IsActive = false
	}
	r.statusCache[channelID] = ChannelStatus{
		ChannelID:     channelID,
		IsActive:      false,
		IsBurnMode:    false,
		LastUpdatedAt: time.Now(),
	}
	r.mu.Unlock()

	r.logger.Warn("channel disabled", "channel_id", channelID)
	return nil
}

func (r *ChannelRegistry) Enable(ctx context.Context, channelID uuid.UUID) error {
	if err := r.repo.EnableChannel(ctx, channelID); err != nil {
		return err
	}

	return r.Reload(ctx, channelID)
}

func (r *ChannelRegistry) loadBundle(ctx context.Context, channel domain.TenantAcquirerChannel) (*domain.ClientBundle, error) {
	provider, err := r.repo.GetPaymentProviderByID(ctx, channel.ProviderID)
	if err != nil {
		return nil, err
	}

	credentials := make(map[domain.CredentialType]string)
	credTypes := []domain.CredentialType{
		domain.CredentialTypeAPIKey,
		domain.CredentialTypeSecret,
		domain.CredentialTypePrivateKey,
		domain.CredentialTypeWebhookSecret,
	}

	for _, ct := range credTypes {
		cred, err := r.repo.GetProviderCredential(ctx, provider.ID, &channel.ID, ct)
		if err != nil {
			continue
		}

		if cred == nil {
			continue
		}

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
			credentials[ct] = string(cred.Ciphertext)
		}
	}

	client, err := r.createClient(provider, credentials, channel.ChannelConfig)
	if err != nil {
		r.logger.Error("create client failed",
			"channel_id", channel.ID,
			"provider_code", provider.ProviderCode,
			"err", err)
		return nil, err
	}

	return &domain.ClientBundle{
		ChannelID:        channel.ID,
		ChannelCode:      channel.ChannelCode,
		ProviderID:       provider.ID,
		ProviderCode:     provider.ProviderCode,
		MerchantID:       channel.MerchantID,
		Client:           client,
		Config:           channel.ChannelConfig,
		IsolationGroupID: channel.IsolationGroupID,
		Version:          channel.Version,
		IsActive:         channel.IsActive,
		LoadedAt:         time.Now(),
		LoadError:        nil,
	}, nil
}

func (r *ChannelRegistry) createClient(provider *domain.PaymentProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AcquirerClient, error) {
	if r.factory != nil {
		// P0-2: real mode 下禁止 mock provider
		if r.isRealMode && strings.ToLower(provider.ProviderCode) == "mock" {
			return nil, domain.ErrProviderCodeUnregistered
		}
		adapter, err := r.factory.Create(context.Background(), provider.ProviderCode, credentials, channelConfig)
		if err != nil {
			return nil, err
		}
		return &adapterWrapper{adapter: adapter}, nil
	}

	// P0-3: real mode 下禁止 createClientLegacy
	if r.isRealMode {
		return nil, fmt.Errorf("acquirer registry: real mode requires AcquirerAdapterFactory, fail-closed")
	}

	return r.createClientLegacy(provider, credentials, channelConfig)
}

func (r *ChannelRegistry) createClientLegacy(provider *domain.PaymentProvider, credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AcquirerClient, error) {
	webhookSecret := credentials[domain.CredentialTypeWebhookSecret]

	switch strings.ToLower(provider.ProviderCode) {
	case "mock":
		return r.createMockClient(webhookSecret, channelConfig)

	case "stripe":
		return r.createStripeClient(credentials, channelConfig)

	case "nxtapay":
		return r.createNxtapayClient(credentials, channelConfig)

	default:
		// P0-3: 禁止 unknown provider 自动降级 mock，所有模式都返回错误
		r.logger.Error("unsupported provider code", "provider_code", provider.ProviderCode)
		return nil, domain.ErrProviderCodeUnregistered
	}
}

type adapterWrapper struct {
	adapter domain.AcquirerAdapter
}

func (w *adapterWrapper) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.AuthorizeResult, error) {
	return w.adapter.Authorize(ctx, req)
}

func (w *adapterWrapper) Capture(ctx context.Context, req domain.CaptureRequest) (*domain.CaptureResult, error) {
	return w.adapter.Capture(ctx, req)
}

func (w *adapterWrapper) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	return w.adapter.Refund(ctx, req)
}

func (w *adapterWrapper) VerifyWebhook(ctx context.Context, req domain.VerifyWebhookRequest) (*domain.VerifiedWebhookEvent, error) {
	return w.adapter.VerifyWebhook(ctx, req)
}

func (r *ChannelRegistry) createMockClient(webhookSecret string, channelConfig map[string]any) (domain.AcquirerClient, error) {
	mockAcquirer := adapter.NewMockAcquirer(webhookSecret)

	if channelConfig != nil {
		if scenarios, ok := channelConfig["scenarios"].(map[string]interface{}); ok {
			for orderID, scenarioStr := range scenarios {
				switch strings.ToLower(fmt.Sprintf("%v", scenarioStr)) {
				case "success":
					mockAcquirer.SetScenario(orderID, adapter.ScenarioSuccess)
				case "failure":
					mockAcquirer.SetScenario(orderID, adapter.ScenarioFailure)
				case "timeout":
					mockAcquirer.SetScenario(orderID, adapter.ScenarioTimeout)
				case "partial":
					mockAcquirer.SetScenario(orderID, adapter.ScenarioPartial)
				}
			}
		}

		if timeoutMs, ok := channelConfig["timeout_ms"].(float64); ok && timeoutMs > 0 {
			mockAcquirer.SetDefaultDelay(time.Duration(timeoutMs) * time.Millisecond)
		} else {
			mockAcquirer.SetDefaultDelay(100 * time.Millisecond)
		}
	}

	return mockAcquirer, nil
}

func (r *ChannelRegistry) createStripeClient(credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AcquirerClient, error) {
	secretKey := credentials[domain.CredentialTypeSecret]
	webhookSecret := credentials[domain.CredentialTypeWebhookSecret]

	if secretKey == "" {
		r.logger.Error("stripe: secret key is required")
		return nil, domain.ErrCredentialVaultKeyEmpty
	}

	timeout := 30 * time.Second
	if cfgTimeout, ok := channelConfig["timeout_ms"].(float64); ok && cfgTimeout > 0 {
		timeout = time.Duration(cfgTimeout) * time.Millisecond
	}

	stripeClient, err := adapter.NewStripeClient(adapter.StripeConfig{
		SecretKey:     secretKey,
		WebhookSecret: webhookSecret,
		Timeout:       timeout,
	})
	if err != nil {
		r.logger.Error("stripe: create client failed", "err", err)
		return nil, err
	}

	return adapter.NewStripeAcquirer(stripeClient), nil
}

func (r *ChannelRegistry) createNxtapayClient(credentials map[domain.CredentialType]string, channelConfig map[string]any) (domain.AcquirerClient, error) {
	apiKey := credentials[domain.CredentialTypeAPIKey]
	apiSecret := credentials[domain.CredentialTypeSecret]
	webhookSecret := credentials[domain.CredentialTypeWebhookSecret]

	if apiKey == "" {
		r.logger.Error("nxtapay: API key is required")
		return nil, domain.ErrCredentialVaultKeyEmpty
	}
	if apiSecret == "" {
		r.logger.Error("nxtapay: API secret is required")
		return nil, domain.ErrCredentialVaultKeyEmpty
	}

	baseURL := "https://sandbox.nxtapay.com/api/v1"
	if cfgBaseURL, ok := channelConfig["base_url"].(string); ok && cfgBaseURL != "" {
		baseURL = cfgBaseURL
	}

	timeout := 30 * time.Second
	if cfgTimeout, ok := channelConfig["timeout_ms"].(float64); ok && cfgTimeout > 0 {
		timeout = time.Duration(cfgTimeout) * time.Millisecond
	}

	merchantID := ""
	if cfgMerchantID, ok := channelConfig["merchant_id"].(string); ok {
		merchantID = cfgMerchantID
	}

	nxtapayClient, err := adapter.NewNxtapayClient(adapter.NxtapayConfig{
		APIKey:        apiKey,
		APISecret:     apiSecret,
		MerchantID:    merchantID,
		WebhookSecret: webhookSecret,
		BaseURL:       baseURL,
		Timeout:       timeout,
	})
	if err != nil {
		r.logger.Error("nxtapay: create client failed", "err", err)
		return nil, err
	}

	return adapter.NewNxtapayAcquirer(nxtapayClient), nil
}

func (r *ChannelRegistry) SubscribeConfigUpdates(ctx context.Context) error {
	if r.rdb == nil {
		r.logger.Warn("redis not available, config update subscription disabled")
		return nil
	}

	pubsub := r.rdb.Subscribe(ctx, "channel.config.updated", "config:acquirer_channel:reload")
	ch := pubsub.Channel()

	go func() {
		for msg := range ch {
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

			r.logger.Info("received config update", "channel_id", channelID, "version", newVersion)

			r.mu.RLock()
			existingBundle, exists := r.bundles[channelID]
			r.mu.RUnlock()

			if exists && existingBundle.Version >= newVersion {
				r.logger.Info("skip reload: existing version is newer or equal",
					"channel_id", channelID,
					"existing_version", existingBundle.Version,
					"event_version", newVersion)
				continue
			}

			if err := r.Reload(ctx, channelID); err != nil {
				r.logger.Error("reload after config update failed", "channel_id", channelID, "err", err)
			}
		}
	}()

	return nil
}

func (r *ChannelRegistry) PublishConfigUpdate(ctx context.Context, channelID uuid.UUID, version int) error {
	if r.rdb == nil {
		return nil
	}

	payload := fmt.Sprintf("%s|%d", channelID.String(), version)
	return r.rdb.Publish(ctx, "config:acquirer_channel:reload", payload).Err()
}

func (r *ChannelRegistry) ListAll() []*domain.ChannelSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	summaries := make([]*domain.ChannelSummary, 0, len(r.bundles))
	for id, bundle := range r.bundles {
		summaries = append(summaries, &domain.ChannelSummary{
			ChannelID:    id,
			ChannelCode:  bundle.ChannelCode,
			ProviderCode: bundle.ProviderCode,
			IsActive:     bundle.IsActive,
			Version:      bundle.Version,
		})
	}
	return summaries
}

// AdapterConstructor 是 provider adapter 的构造器函数。
type AdapterConstructor func(credentials map[domain.CredentialType]string, config map[string]any) (domain.AcquirerAdapter, error)

// DefaultAdapterFactory 是基于注册的 AcquirerAdapterFactory 实现。
// provider 的具体逻辑由注册的构造器持有，不散回 Registry。
type DefaultAdapterFactory struct {
	constructors map[string]AdapterConstructor
	logger       *slog.Logger
}

func NewDefaultAdapterFactory(logger *slog.Logger) *DefaultAdapterFactory {
	if logger == nil {
		logger = slog.Default()
	}
	return &DefaultAdapterFactory{
		constructors: make(map[string]AdapterConstructor),
		logger:       logger,
	}
}

func (f *DefaultAdapterFactory) Register(providerCode string, constructor AdapterConstructor) {
	f.constructors[strings.ToLower(providerCode)] = constructor
}

func (f *DefaultAdapterFactory) Create(ctx context.Context, providerCode string, credentials map[domain.CredentialType]string, config map[string]any) (domain.AcquirerAdapter, error) {
	constructor, ok := f.constructors[strings.ToLower(providerCode)]
	if !ok {
		return nil, domain.ErrProviderCodeUnregistered
	}
	return constructor(credentials, config)
}
