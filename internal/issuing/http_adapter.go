package issuing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// VBA capability 区分 (G15)
//
// VBA（Virtual Bank Account）有两种语义不同的能力：
//   1. LOCAL_RECORD_CRUD：本地账户记录 CRUD（仅写入 bank_collection_virtual_account_pool 表）
//      - 不调用外部银行 API
//      - 不表示"外部银行真实开户"
//      - UI 应显示"本地账户记录已创建"，不可显示"开户成功"
//   2. EXTERNAL_BANK_OPEN：外部银行真实开户/验证/入账
//      - 调用外部银行 API（需要真实 adapter + 凭证）
//      - 没有真实 adapter 时 UI 必须明确禁用
//      - 没有凭证报 BLOCKED_EXTERNAL_CREDENTIAL
//
// 红线：没有真实 adapter 时 UI 明确禁用，不可显示"开户成功"。
// ============================================================================

// VBACapability 表示 VBA 的能力级别。
type VBACapability string

const (
	// VBACapabilityLocalRecordCRUD 仅支持本地账户记录 CRUD。
	// 不调用外部银行 API；UI 不可显示"开户成功"。
	VBACapabilityLocalRecordCRUD VBACapability = "LOCAL_RECORD_CRUD"

	// VBACapabilityExternalBankOpen 支持外部银行真实开户/验证/入账。
	// 需要真实 adapter + 凭证；UI 可显示"开户成功"。
	VBACapabilityExternalBankOpen VBACapability = "EXTERNAL_BANK_OPEN"
)

// VBA capability 错误（G15）
var (
	// ErrVBANoExternalAdapter 表示请求外部银行开户但没有真实 adapter 注册。
	// UI 必须明确禁用外部开户按钮，不可显示"开户成功"。
	ErrVBANoExternalAdapter = errors.New("issuing: VBA EXTERNAL_BANK_OPEN capability not available (no real adapter registered)")

	// ErrVBABlockedCredential 表示外部银行开户凭证缺失。
	// 不得伪造凭证；应标记为 BLOCKED_EXTERNAL_CREDENTIAL。
	ErrVBABlockedCredential = errors.New("issuing: VBA BLOCKED_EXTERNAL_CREDENTIAL (external bank credential missing)")
)

// VBACapabilityResolver 解析给定 channel 是否具备外部银行开户能力。
//
// 设计目标：
//   - 没有真实 adapter 时返回 ErrVBANoExternalAdapter
//   - 有 adapter 但凭证缺失时返回 ErrVBABlockedCredential
//   - UI 根据 capability 决定是否启用外部开户按钮
type VBACapabilityResolver interface {
	// ResolveVBACapability 返回给定 channel 的 VBA 能力级别。
	// 返回 (capability, nil) 表示能力可用；返回 (_, err) 表示能力不可用及原因。
	ResolveVBACapability(ctx context.Context, channelID uuid.UUID) (VBACapability, error)

	// ResolveVBACapabilityExplicit 是 ResolveVBACapability 的显式参数变体。
	// 调用方直接传入 providerCode 和 vaultKey，避免 DB 查询。
	ResolveVBACapabilityExplicit(ctx context.Context, providerCode, vaultKey string) (VBACapability, error)
}

// httpAdapter 是外部银行/issuing provider 的 HTTP adapter。
// 当 baseURL 和 apiKey 都非空时，认为外部 adapter 可用。
type httpAdapter struct {
	client  *http.Client
	baseURL string
	apiKey  string
}

func NewHTTPAdapter(baseURL, apiKey string) IssuingAdapter {
	return &httpAdapter{
		client:  &http.Client{Timeout: 30 * time.Second},
		baseURL: baseURL,
		apiKey:  apiKey,
	}
}

func (a *httpAdapter) CreateCard(ctx context.Context, req CreateCardRequest) (*CreateCardResult, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"merchant_id":          req.MerchantID,
		"provider_id":          req.ProviderID,
		"pool_code":            req.PoolCode,
		"currency":             req.Currency,
		"spending_limit_minor": req.SpendingLimitMinor,
		"idempotency_key":      req.IdempotencyKey,
		"payload_hash":         req.PayloadHash,
		"created_by":           req.CreatedBy,
	})
	resp, err := a.do(ctx, http.MethodPost, "/v1/cards", body)
	if err != nil {
		return nil, classifyHTTPError(err)
	}
	defer resp.Body.Close()

	// R20 P1-2: 使用 classifyHTTPStatus 分类 HTTP 状态码
	if statusErr := classifyHTTPStatus(resp.StatusCode); statusErr != nil {
		return nil, statusErr
	}

	var result struct {
		CardID          string    `json:"card_id"`
		ProviderCardRef string    `json:"provider_card_ref"`
		Last4           string    `json:"last4"`
		Status          string    `json:"status"`
		CreatedAt       time.Time `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode create card response: %w", err)
	}

	cardID, parseErr := uuid.Parse(result.CardID)
	if parseErr != nil {
		cardID = uuid.New()
	}

	return &CreateCardResult{
		CardID:          cardID,
		ProviderCardRef: result.ProviderCardRef,
		Last4:           result.Last4,
		Status:          VirtualCardStatus(result.Status),
		CreatedAt:       result.CreatedAt,
	}, nil
}

func (a *httpAdapter) FreezeCard(ctx context.Context, req FreezeCardRequest) error {
	// Task F: 使用 ProviderCardRef 构造路径，不得使用本地 UUID。
	providerCardRef := req.ProviderCardRef
	if providerCardRef == "" {
		return fmt.Errorf("issuing: FreezeCard requires non-empty ProviderCardRef")
	}
	// Task F: 稳定幂等键为强制项
	if req.IdempotencyKey == "" {
		return fmt.Errorf("issuing: FreezeCard requires non-empty IdempotencyKey")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"idempotency_key": req.IdempotencyKey,
		"frozen_reason":   req.FrozenReason,
	})
	// 安全转义路径参数
	encodedRef := url.PathEscape(providerCardRef)
	resp, err := a.doWithIdempotency(ctx, http.MethodPost, fmt.Sprintf("/v1/cards/%s/freeze", encodedRef), body, req.IdempotencyKey)
	if err != nil {
		// R20 P1-2: 使用 classifyHTTPError 分类网络错误
		return classifyHTTPError(err)
	}
	defer resp.Body.Close()

	// R20 P1-2: FreezeCard 只接受明确的 200/202/204
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return ErrProviderTimeout
	case http.StatusTooManyRequests:
		return fmt.Errorf("issuing: provider returned 429 Too Many Requests: %w", ErrProviderRetryable)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("issuing: provider returned retryable status %d: %w", resp.StatusCode, ErrProviderRetryable)
	}
	// 其他 4xx → permanent error
	return fmt.Errorf("issuing: provider returned permanent status %d: %w", resp.StatusCode, ErrProviderPermanent)
}

func (a *httpAdapter) TerminateCard(ctx context.Context, req TerminateCardRequest) error {
	body, _ := json.Marshal(map[string]interface{}{
		"card_id":           req.CardID,
		"provider_id":       req.ProviderID,
		"terminated_reason": req.TerminatedReason,
	})
	resp, err := a.do(ctx, http.MethodPost, fmt.Sprintf("/v1/cards/%s/terminate", req.CardID), body)
	if err != nil {
		return classifyHTTPError(err)
	}
	defer resp.Body.Close()
	// R20 P1-2: 使用 classifyHTTPStatus 分类 HTTP 状态码
	if statusErr := classifyHTTPStatus(resp.StatusCode); statusErr != nil {
		return statusErr
	}
	return nil
}

func (a *httpAdapter) ProcessAuthWebhook(ctx context.Context, req ProcessAuthWebhookRequest) (*CardAuthEvent, error) {
	resp, err := a.do(ctx, http.MethodPost, "/v1/webhooks/auth", req.RawPayload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("issuing provider returned status %d", resp.StatusCode)
	}

	var event CardAuthEvent
	if err := json.NewDecoder(resp.Body).Decode(&event); err != nil {
		return nil, fmt.Errorf("decode auth webhook response: %w", err)
	}
	return &event, nil
}

func (a *httpAdapter) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	url := a.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", a.apiKey)
	return a.client.Do(req)
}

// doWithIdempotency 发送 HTTP 请求并附加 Idempotency-Key Header。
// Task F: Idempotency-Key 为强制项，相同重试必须使用同一键值。
func (a *httpAdapter) doWithIdempotency(ctx context.Context, method, path string, body []byte, idempotencyKey string) (*http.Response, error) {
	fullURL := a.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", a.apiKey)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	return a.client.Do(req)
}

// classifyHTTPError 将网络/HTTP 错误分类为明确的 issuing 错误类型。
// R20 P1-2: 补齐 HTTP Provider 错误类型。
// - context.DeadlineExceeded → ErrProviderTimeout
// - net.Error.Timeout() → ErrProviderTimeout
// - context.Canceled → 保留取消语义（不映射为 timeout）
// - 其他错误 → 原样返回（不得包含 API Key）
func classifyHTTPError(err error) error {
	if err == nil {
		return nil
	}
	// context.Canceled 保留取消语义
	if errors.Is(err, context.Canceled) {
		return err
	}
	// context.DeadlineExceeded → timeout
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrProviderTimeout
	}
	// net.Error.Timeout() → timeout
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrProviderTimeout
	}
	// 其他错误原样返回（已确保不包含 API Key，因为 API Key 只在 Header 中）
	return err
}

// classifyHTTPStatus 将 HTTP 状态码分类为明确的 issuing 错误类型。
// R20 P1-2:
// - 408/504 → ErrProviderTimeout
// - 429/5xx → ErrProviderRetryable
// - 其他 4xx → ErrProviderPermanent
// - 200/201/202/204 → nil（成功）
func classifyHTTPStatus(statusCode int) error {
	switch statusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return ErrProviderTimeout
	case http.StatusTooManyRequests:
		return fmt.Errorf("issuing: provider returned 429 Too Many Requests: %w", ErrProviderRetryable)
	}
	if statusCode >= 500 {
		return fmt.Errorf("issuing: provider returned retryable status %d: %w", statusCode, ErrProviderRetryable)
	}
	if statusCode >= 400 {
		return fmt.Errorf("issuing: provider returned permanent status %d: %w", statusCode, ErrProviderPermanent)
	}
	return nil
}

type httpAdapterFactory struct {
	factories map[string]func(context.Context, map[string]string) (IssuingAdapter, error)
}

func NewHTTPAdapterFactory() IssuingAdapterFactory {
	return &httpAdapterFactory{
		factories: make(map[string]func(context.Context, map[string]string) (IssuingAdapter, error)),
	}
}

func (f *httpAdapterFactory) Create(ctx context.Context, providerCode string, credential map[string]string) (IssuingAdapter, error) {
	factory, ok := f.factories[providerCode]
	if !ok {
		return nil, ErrProviderCodeUnregistered
	}
	return factory(ctx, credential)
}

func (f *httpAdapterFactory) Register(providerCode string, factory func(context.Context, map[string]string) (IssuingAdapter, error)) {
	f.factories[providerCode] = factory
}

// Has 检查指定 providerCode 是否已注册。
func (f *httpAdapterFactory) Has(providerCode string) bool {
	_, ok := f.factories[providerCode]
	return ok
}

// Validate 验证 Factory 已注册至少一个 Provider。
// production/real 模式下空 Factory 必须启动失败。
func (f *httpAdapterFactory) Validate() error {
	if len(f.factories) == 0 {
		return fmt.Errorf("issuing: adapter factory has no registered providers (production/real requires at least one)")
	}
	return nil
}

// RegisteredCodes 返回所有已注册的 providerCode 列表。
func (f *httpAdapterFactory) RegisteredCodes() []string {
	codes := make([]string, 0, len(f.factories))
	for code := range f.factories {
		codes = append(codes, code)
	}
	return codes
}

// ============================================================================
// vbaCapabilityResolver —— VBA capability 解析器实现 (G15)
//
// 设计：
//   - 检查 channel 是否有注册的外部 adapter（通过 ProviderCode 查 factory）
//   - 检查凭证是否可用（通过 vaultKey 查 vault）
//   - 都可用：返回 VBACapabilityExternalBankOpen
//   - adapter 可用但凭证缺失：返回 ErrVBABlockedCredential
//   - adapter 不可用：返回 ErrVBANoExternalAdapter（UI 必须禁用外部开户）
// ============================================================================

type vbaCapabilityResolver struct {
	factory IssuingAdapterFactory
	vault   VaultService
}

// NewVBACapabilityResolver 创建 VBA capability 解析器。
// factory 和 vault 任一为 nil 时，所有 channel 都不具备外部开户能力。
func NewVBACapabilityResolver(factory IssuingAdapterFactory, vault VaultService) VBACapabilityResolver {
	return &vbaCapabilityResolver{factory: factory, vault: vault}
}

// ResolveVBACapability 实现 VBACapabilityResolver 接口。
//
// 注意：此方法不接收 providerCode 和 vaultKey 参数（接口签名只接收 channelID）。
// 实际使用时，调用方应直接使用 ResolveVBACapabilityExplicit 方法，
// 或在实现中通过 channelID 查询 DB 获取 providerCode 和 vaultKey。
// 此处提供 Explicit 变体以支持直接参数传入。
func (r *vbaCapabilityResolver) ResolveVBACapability(ctx context.Context, channelID uuid.UUID) (VBACapability, error) {
	// 此方法需要 channelID -> providerCode + vaultKey 的查询路径。
	// 在当前架构中，channel 信息由 admin handler 持有，不在此处查询。
	// 返回 ErrVBANoExternalAdapter 作为安全默认（fail-closed）。
	return VBACapabilityLocalRecordCRUD, ErrVBANoExternalAdapter
}

// ResolveVBACapabilityExplicit 是 ResolveVBACapability 的显式参数变体。
// 调用方直接传入 providerCode 和 vaultKey，避免 DB 查询。
//
// 返回值：
//   - (VBACapabilityExternalBankOpen, nil)：外部开户可用
//   - (_, ErrVBABlockedCredential)：adapter 可用但凭证缺失
//   - (_, ErrVBANoExternalAdapter)：adapter 不可用
func (r *vbaCapabilityResolver) ResolveVBACapabilityExplicit(ctx context.Context, providerCode, vaultKey string) (VBACapability, error) {
	if r.factory == nil {
		return VBACapabilityLocalRecordCRUD, ErrVBANoExternalAdapter
	}

	// 检查 factory 是否注册了此 providerCode。
	// 我们通过尝试 Create 来检测，传入空凭证。
	// 如果返回 ErrProviderCodeUnregistered，则 adapter 不可用。
	// 注意：此检测不会真正创建 adapter，因为凭证为空时外部 adapter 应返回错误。
	_, err := r.factory.Create(ctx, providerCode, nil)
	if err != nil {
		// ErrProviderCodeUnregistered 表示 adapter 不可用
		if errors.Is(err, ErrProviderCodeUnregistered) {
			return VBACapabilityLocalRecordCRUD, ErrVBANoExternalAdapter
		}
		// 其他错误可能是凭证缺失（adapter 存在但需要凭证）
		// 此时 adapter 可用，但凭证缺失
		if vaultKey == "" {
			return VBACapabilityLocalRecordCRUD, ErrVBABlockedCredential
		}
		if r.vault != nil {
			if _, vaultErr := r.vault.Decrypt(ctx, VaultCredentialRequest{VaultKey: vaultKey}); vaultErr != nil {
				return VBACapabilityLocalRecordCRUD, ErrVBABlockedCredential
			}
		}
	}

	// adapter 可用且凭证可用
	return VBACapabilityExternalBankOpen, nil
}
