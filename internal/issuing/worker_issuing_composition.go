package issuing

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OwnershipChecker 是 Worker 启动时 DB ownership 预检接口。
// R20 P0-3: Worker 启动时对每个 ACTIVE 真实 Provider 验证 DB ownership。
type OwnershipChecker interface {
	// CheckProviderOwnership 在 DB 事务中验证 ACTIVE Provider 的完整 ownership。
	// 检查内容：
	// - entity 存在且 tenant_id 非空
	// - adapter_provider ACTIVE CARD_ISSUER
	// - Vault 记录 ACTIVE 未过期
	// - Vault tenant == entity tenant
	// - issuing_provider_id == 当前 provider
	// - provider_id == adapter_provider_id
	// - credential_type == API_KEY
	// - record/version 与 vault path 一致
	CheckProviderOwnership(ctx context.Context, provider IssuingProvider) error
}

// ownershipDB 是 dbOwnershipChecker 依赖的窄 DB 接口。
// R23 Item 4: 便于测试注入 fake 事务，验证 CheckProviderOwnership 的行为。
type ownershipDB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// dbOwnershipChecker 是 OwnershipChecker 的 DB 实现。
// R22 Item 2: 必须在同一只读 RepeatableRead 事务中读取 entity、adapter、Vault。
// 查询失败立即回滚；校验成功再提交。不得在事务中执行 Vault 解密、KMS 或 Provider 网络请求。
type dbOwnershipChecker struct {
	pool ownershipDB
}

// NewDBOwnershipChecker 创建 DB 实现的 OwnershipChecker。
func NewDBOwnershipChecker(pool *pgxpool.Pool) OwnershipChecker {
	return &dbOwnershipChecker{pool: pool}
}

// CheckProviderOwnership 在 DB 事务中验证 ACTIVE Provider 的完整 ownership。
// R20 P0?3: Admin(Admin applyPoolStatus) 和 Worker 必须复用同一 ownership 规则核心。
// R21 Step B: 调用纯函数 ValidateProviderOwnershipFacts，禁止复制判断逻辑。
// R22 Item 2: 使用 RepeatableRead 事务确保读取一致性快照。
func (c *dbOwnershipChecker) CheckProviderOwnership(ctx context.Context, provider IssuingProvider) error {
	// R22 Item 2: 使用 RepeatableRead 事务确保读取一致性快照
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("issuing: provider %s begin ownership tx: %w", provider.ProviderCode, err)
	}
	defer tx.Rollback(ctx)

	// 1. entity 存在且 tenant_id 非空
	if provider.EntityID == nil || *provider.EntityID == uuid.Nil {
		return fmt.Errorf("issuing: provider %s has no entity_id", provider.ProviderCode)
	}
	var entityTenantID *uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT ce.tenant_id FROM corporate_entity ce
		WHERE ce.id = $1`, *provider.EntityID).Scan(&entityTenantID)
	if err != nil {
		return fmt.Errorf("issuing: provider %s entity not found: %w", provider.ProviderCode, err)
	}

	// 2. adapter_provider ACTIVE CARD_ISSUER
	if provider.AdapterProviderID == nil || *provider.AdapterProviderID == uuid.Nil {
		return fmt.Errorf("issuing: provider %s has no adapter_provider_id", provider.ProviderCode)
	}
	var apStatus, apAdapterType string
	err = tx.QueryRow(ctx,
		`SELECT status, adapter_type::text FROM adapter_provider WHERE id = $1`,
		*provider.AdapterProviderID).Scan(&apStatus, &apAdapterType)
	if err != nil {
		return fmt.Errorf("issuing: provider %s adapter_provider not found: %w", provider.ProviderCode, err)
	}

	// 3. Vault 记录 ACTIVE 未过期，归属一致
	if provider.CredentialVaultKey == "" {
		return fmt.Errorf("issuing: provider %s has empty credential_vault_key", provider.ProviderCode)
	}
	vaultPath, vpErr := ParseVaultPath(provider.CredentialVaultKey)
	if vpErr != nil {
		return fmt.Errorf("issuing: provider %s invalid vault key: %w", provider.ProviderCode, vpErr)
	}

	var vStatus, vCredType string
	var vTenantID, vIssuingProviderID, vProviderID, vRecordID *uuid.UUID
	var vExpiresAt *time.Time
	var vKeyVersion int
	err = tx.QueryRow(ctx, `
		SELECT id, status, credential_type, tenant_id, issuing_provider_id, provider_id, expires_at, key_version
		FROM adapter_credential_vault
		WHERE id = $1 AND key_version = $2`,
		vaultPath.RecordID, vaultPath.KeyVersion).Scan(&vRecordID, &vStatus, &vCredType, &vTenantID, &vIssuingProviderID, &vProviderID, &vExpiresAt, &vKeyVersion)
	if err != nil {
		return fmt.Errorf("issuing: provider %s vault credential not found: %w", provider.ProviderCode, err)
	}

	// R21 Step B: 调用纯函数校验全部 ownership 事实
	if err := ValidateProviderOwnershipFacts(&OwnershipFacts{
		PathTenantID:           vaultPath.TenantID,
		PathRecordID:           vaultPath.RecordID,
		PathField:              vaultPath.FieldName,
		PathKeyVersion:         vaultPath.KeyVersion,
		EntityTenantID:         entityTenantID,
		AdapterStatus:          apStatus,
		AdapterType:            apAdapterType,
		VaultRecordID:          vRecordID,
		VaultStatus:            vStatus,
		VaultCredentialType:    vCredType,
		VaultTenantID:          vTenantID,
		VaultIssuingProviderID: vIssuingProviderID,
		VaultProviderID:        vProviderID,
		VaultExpiresAt:         vExpiresAt,
		VaultKeyVersion:        vKeyVersion,
		ProviderID:             provider.ID,
		AdapterProviderID:      provider.AdapterProviderID,
	}); err != nil {
		return err
	}

	// R22 Item 2: 校验成功再提交（只读事务提交释放快照）
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("issuing: provider %s ownership tx commit: %w", provider.ProviderCode, err)
	}
	return nil
}

// RegisterProviderConfig 是注册单个 Provider 到 Factory 的配置。
type RegisterProviderConfig struct {
	ProviderCode      string
	AdapterProviderID *uuid.UUID // F-01: UUID 强类型
	AdapterType       AdapterType
	BaseURL           string
	IsProduction      bool
}

// RegisterProviderToFactory 将单个 Provider 注册到 Factory。
// P0-7 最终闭环修复:
// - 只有 NEXUS_ISSUING_HTTP_V1 注册 HTTP Adapter
// - production 拒绝 MOCK_LOCAL/UNSUPPORTED/未知
// - production 要求非空 AdapterProviderID
// - URL 用 net/url 解析（非 strings.HasPrefix）
// - F-14: 要求非空 host、禁止 userinfo、禁止 fragment
// R18 P0-5: MOCK_LOCAL 也注册到 Factory（按 ProviderCode）
func RegisterProviderToFactory(factory IssuingAdapterFactory, cfg RegisterProviderConfig) error {
	switch cfg.AdapterType {
	case AdapterTypeNexusIssuingHTTPV1:
		// 只有 NEXUS_ISSUING_HTTP_V1 注册 HTTP Adapter
		// production 要求非空 AdapterProviderID
		if cfg.IsProduction && (cfg.AdapterProviderID == nil || *cfg.AdapterProviderID == uuid.Nil) {
			return fmt.Errorf("issuing: production mode requires non-empty adapter_provider_id for NEXUS_ISSUING_HTTP_V1: %w", ErrAdapterTypeUnsupported)
		}
	case AdapterTypeMockLocal:
		if cfg.IsProduction {
			return fmt.Errorf("issuing: production mode rejects MOCK_LOCAL adapter_type: %w", ErrAdapterTypeUnsupported)
		}
		// R18 P0-5: MOCK_LOCAL 注册 MockIssuingAdapter 到 Factory
		mockAdapter := NewMockIssuingAdapter()
		factory.Register(cfg.ProviderCode, func(_ context.Context, _ map[string]string) (IssuingAdapter, error) {
			return mockAdapter, nil
		})
		return nil
	case AdapterTypeUnsupported:
		if cfg.IsProduction {
			return fmt.Errorf("issuing: production mode rejects UNSUPPORTED adapter_type: %w", ErrAdapterTypeUnsupported)
		}
		return nil
	default:
		if cfg.IsProduction {
			return fmt.Errorf("issuing: production mode rejects unknown adapter_type: %w", ErrAdapterTypeUnsupported)
		}
		return nil
	}

	if cfg.BaseURL == "" {
		return fmt.Errorf("issuing: ACTIVE provider missing base_url: %w", ErrAdapterTypeUnsupported)
	}

	parsedURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return fmt.Errorf("issuing: invalid base_url: %w", err)
	}

	if cfg.IsProduction && parsedURL.Scheme != "https" {
		return fmt.Errorf("issuing: production mode requires HTTPS base_url: %w", ErrAdapterTypeUnsupported)
	}

	// F-14: 要求非空 host
	if parsedURL.Host == "" {
		return fmt.Errorf("issuing: base_url must have non-empty host: %w", ErrAdapterTypeUnsupported)
	}

	if parsedURL.User != nil {
		return fmt.Errorf("issuing: base_url must not contain userinfo: %w", ErrAdapterTypeUnsupported)
	}

	// 禁止 URL fragment
	if parsedURL.Fragment != "" {
		return fmt.Errorf("issuing: base_url must not contain fragment: %w", ErrAdapterTypeUnsupported)
	}

	baseURL := cfg.BaseURL
	factory.Register(cfg.ProviderCode, func(_ context.Context, credential map[string]string) (IssuingAdapter, error) {
		apiKey := credential["api_key"]
		if apiKey == "" {
			return nil, ErrCredentialVaultKeyEmpty
		}
		return NewHTTPAdapter(baseURL, apiKey), nil
	})

	return nil
}

// BuildWorkerIssuingRuntime 是生产组合根函数。
// cmd/worker/main.go 和组合测试都必须调用此函数。
// F-11: 统一运行时和测试的组合路径。
// R18 P0-5: 负责注册所有 ACTIVE Provider（包括 MOCK_LOCAL）。
// R19 P0-2: 调用共享 Activation validator (ValidateProviderActivation)。
// R20 P0-3: production 模式下对每个 ACTIVE 真实 Provider 执行 DB ownership 预检。
func BuildWorkerIssuingRuntime(factory IssuingAdapterFactory, providers []IssuingProvider, mode RuntimeMode, ownershipChecker OwnershipChecker) (int, error) {
	registeredCount := 0
	for _, p := range providers {
		if p.Status != "ACTIVE" {
			continue
		}

		// R19 P0-2: 调用共享 Activation validator
		baseURL, _ := p.ProviderConfig["base_url"].(string)
		adapterProviderIDStr := ""
		if p.AdapterProviderID != nil {
			adapterProviderIDStr = p.AdapterProviderID.String()
		}
		entityIDStr := ""
		if p.EntityID != nil {
			entityIDStr = p.EntityID.String()
		}
		if err := ValidateProviderActivation(&ActivationConfig{
			EntityID:           entityIDStr,
			IssuingTier:        string(p.IssuingTier),
			FundingMode:        string(p.FundingMode),
			ProviderCode:       p.ProviderCode,
			PoolCode:           p.PoolCode,
			CredentialVaultKey: p.CredentialVaultKey,
			ProviderConfig:     p.ProviderConfig,
			AdapterType:        string(p.AdapterType),
			AdapterProviderID:  adapterProviderIDStr,
		}, mode); err != nil {
			return registeredCount, fmt.Errorf("issuing: activation validation failed for provider %s: %w", p.ProviderCode, err)
		}

		// R21 P0-2: production 模式下 ownershipChecker 为 nil 必须报错
		if mode.IsProduction() && ownershipChecker == nil {
			return registeredCount, fmt.Errorf("issuing: production mode requires non-nil ownershipChecker")
		}
		// R20 P0-3: production 模式下执行 DB ownership 预检
		if mode.IsProduction() {
			if err := ownershipChecker.CheckProviderOwnership(context.Background(), p); err != nil {
				return registeredCount, fmt.Errorf("issuing: ownership check failed for provider %s: %w", p.ProviderCode, err)
			}
		}

		if err := RegisterProviderToFactory(factory, RegisterProviderConfig{
			ProviderCode:      p.ProviderCode,
			AdapterProviderID: p.AdapterProviderID,
			AdapterType:       p.AdapterType,
			BaseURL:           baseURL,
			IsProduction:      mode.IsProduction(),
		}); err != nil {
			return registeredCount, fmt.Errorf("issuing: register provider %s: %w", p.ProviderCode, err)
		}

		// R18 P0-5: 统计所有已注册的 provider（NEXUS_ISSUING_HTTP_V1 和 MOCK_LOCAL）
		if p.AdapterType == AdapterTypeNexusIssuingHTTPV1 || p.AdapterType == AdapterTypeMockLocal {
			registeredCount++
		}
	}
	return registeredCount, nil
}
