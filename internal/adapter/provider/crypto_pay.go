package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// Crypto Pay Adapter - 加密货币支付适配器
// 支持 deposit_address、memo/tag、confirmations threshold
// 非白名单 asset/network 拒绝
// ============================================================================

// CryptoPayAdapter 加密货币支付适配器
type CryptoPayAdapter struct {
	providerCode      string
	rpcToken          string
	webhookSecret     string
	baseURL           string
	httpClient        *http.Client
	supportedAssets   []string
	supportedNetworks []string
	allowlistAssets   map[string]bool // 白名单资产
	allowlistNetworks map[string]bool // 白名单网络
	config            map[string]any
}

// CryptoPayConfig 加密货币支付配置
type CryptoPayConfig struct {
	ProviderCode      string
	RPCToken          string
	WebhookSecret     string
	BaseURL           string
	Timeout           time.Duration
	SupportedAssets   []string
	SupportedNetworks []string
	AllowlistAssets   []string
	AllowlistNetworks []string
	Config            map[string]any
}

// NewCryptoPayAdapter 创建加密货币支付适配器
func NewCryptoPayAdapter(providerCode string, rpcToken string, webhookSecret string, config map[string]any) (domain.AdapterClient, error) {
	timeout := 30 * time.Second
	if t, ok := config["timeout"].(time.Duration); ok {
		timeout = t
	}

	baseURL := ""
	if u, ok := config["base_url"].(string); ok {
		baseURL = u
	}

	// 根据不同 provider 设置默认 base URL
	if baseURL == "" {
		switch strings.ToLower(providerCode) {
		case "gateway":
			baseURL = "https://api.gateway.crypto"
		case "custody":
			baseURL = "https://api.custody.crypto"
		default:
			baseURL = "https://api.mock-crypto-pay.local"
		}
	}

	// 默认支持的资产和网络
	supportedAssets := []string{"USDT", "USDC", "BTC", "ETH"}
	supportedNetworks := []string{"TRON", "ETHEREUM", "BITCOIN"}

	if assets, ok := config["supported_assets"].([]string); ok {
		supportedAssets = assets
	}
	if networks, ok := config["supported_networks"].([]string); ok {
		supportedNetworks = networks
	}

	// 白名单资产和网络
	allowlistAssets := make(map[string]bool)
	allowlistNetworks := make(map[string]bool)

	for _, a := range supportedAssets {
		allowlistAssets[a] = true
	}
	for _, n := range supportedNetworks {
		allowlistNetworks[n] = true
	}

	// 从配置中读取额外白名单
	if assets, ok := config["allowlist_assets"].([]string); ok {
		for _, a := range assets {
			allowlistAssets[a] = true
		}
	}
	if networks, ok := config["allowlist_networks"].([]string); ok {
		for _, n := range networks {
			allowlistNetworks[n] = true
		}
	}

	return &CryptoPayAdapter{
		providerCode:  providerCode,
		rpcToken:      rpcToken,
		webhookSecret: webhookSecret,
		baseURL:       baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
		supportedAssets:   supportedAssets,
		supportedNetworks: supportedNetworks,
		allowlistAssets:   allowlistAssets,
		allowlistNetworks: allowlistNetworks,
		config:            config,
	}, nil
}

// GetAdapterType 返回适配器类型
func (c *CryptoPayAdapter) GetAdapterType() domain.AdapterType {
	return domain.AdapterTypeCryptoPay
}

// SupportsAsset 检查是否支持指定资产
func (c *CryptoPayAdapter) SupportsAsset(assetCode string) bool {
	return c.allowlistAssets[assetCode]
}

// SupportsNetwork 检查是否支持指定网络
func (c *CryptoPayAdapter) SupportsNetwork(network string) bool {
	return c.allowlistNetworks[network]
}

// ============================================================================
// InboundAdapter 接口实现
// ============================================================================

// CreateInboundIntent 创建入金意图（分配存款地址）
func (c *CryptoPayAdapter) CreateInboundIntent(ctx context.Context, req domain.InboundIntentRequest) (*domain.InboundIntentResponse, error) {
	// 验证资产白名单
	if !c.SupportsAsset(req.AssetCode) {
		return nil, domain.ErrAssetNotSupported
	}

	// 验证网络白名单
	if req.Network != "" && !c.SupportsNetwork(req.Network) {
		return nil, domain.ErrNetworkNotSupported
	}

	// 构建请求
	intentReq := map[string]any{
		"order_id":        req.OrderID,
		"asset":           req.AssetCode,
		"network":         req.Network,
		"amount":          req.Amount.String(),
		"memo_tag":        req.MemoTag,
		"idempotency_key": req.IdempotencyKey,
	}

	// confirmations_required 配置
	confirmationsRequired := 3 // 默认确认数
	if cr, ok := c.config["confirmations_required"].(int); ok {
		confirmationsRequired = cr
	}
	intentReq["confirmations_required"] = confirmationsRequired

	// 调用外部 API（不在 DB transaction 内）
	respBody, statusCode, err := c.doRequest(ctx, "POST", "/api/v1/deposit-addresses", intentReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		IntentID              string `json:"intent_id"`
		ProviderRef           string `json:"provider_ref"`
		Status                string `json:"status"`
		Address               string `json:"address"`
		MemoTag               string `json:"memo_tag"`
		Network               string `json:"network"`
		ConfirmationsRequired int    `json:"confirmations_required"`
		ExpiresAt             string `json:"expires_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("crypto pay: failed to parse response: %w", err)
	}

	// 检查状态码
	if statusCode >= 400 {
		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Code != "" {
			if errResp.Error.Code == "ASSET_NOT_ALLOWED" {
				return nil, domain.ErrAssetNotSupported
			}
			if errResp.Error.Code == "NETWORK_NOT_ALLOWED" {
				return nil, domain.ErrNetworkNotSupported
			}
			return nil, fmt.Errorf("crypto pay: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("crypto pay: request failed with status %d", statusCode)
	}

	intentID := uuid.New()
	if resp.IntentID != "" {
		intentID, _ = uuid.Parse(resp.IntentID)
	}

	// 转换状态
	status := domain.InboundStatusCreated
	switch resp.Status {
	case "WAITING_FUNDS":
		status = domain.InboundStatusWaitingFunds
	case "CREATED":
		status = domain.InboundStatusCreated
	}

	// 构建存款地址详情
	depositAddress := &domain.DepositAddressDetails{
		Address:               resp.Address,
		MemoTag:               resp.MemoTag,
		Network:               resp.Network,
		ConfirmationsRequired: resp.ConfirmationsRequired,
	}

	var expiresAt *time.Time
	if resp.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, resp.ExpiresAt)
		if err == nil {
			expiresAt = &t
		}
	}

	return &domain.InboundIntentResponse{
		IntentID:       intentID,
		ProviderRef:    resp.ProviderRef,
		Status:         status,
		DepositAddress: depositAddress,
		ExpiresAt:      expiresAt,
		CreatedAt:      time.Now(),
	}, nil
}

// QueryInboundIntent 查询入金意图状态
func (c *CryptoPayAdapter) QueryInboundIntent(ctx context.Context, req domain.QueryInboundIntentRequest) (*domain.InboundIntentStatusResponse, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("crypto pay: provider_ref required")
	}

	// 调用外部 API
	respBody, statusCode, err := c.doRequest(ctx, "GET", fmt.Sprintf("/api/v1/deposit-addresses/%s", req.ProviderRef), nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		IntentID        string `json:"intent_id"`
		ProviderRef     string `json:"provider_ref"`
		Status          string `json:"status"`
		ReceivedAmount  string `json:"received_amount"`
		ConfirmedAmount string `json:"confirmed_amount"`
		Confirmations   int    `json:"confirmations"`
		TransactionRef  string `json:"transaction_ref"`
		TxHash          string `json:"tx_hash"`
		UpdatedAt       string `json:"updated_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("crypto pay: failed to parse response: %w", err)
	}

	// 检查状态码
	if statusCode >= 400 {
		return nil, fmt.Errorf("crypto pay: query failed with status %d", statusCode)
	}

	// 转换状态
	status := domain.InboundStatusCreated
	switch resp.Status {
	case "WAITING_FUNDS":
		status = domain.InboundStatusWaitingFunds
	case "PARTIALLY_CONFIRMED":
		status = domain.InboundStatusPartiallyMatched // 确认数不足
	case "CONFIRMED":
		status = domain.InboundStatusConfirmed
	case "RECONCILE_MISMATCH":
		status = domain.InboundStatusReconcileMismatch // wrong network / amount mismatch / memo mismatch
	case "EXPIRED":
		status = domain.InboundStatusExpired
	case "FAILED":
		status = domain.InboundStatusFailed
	}

	receivedAmount, _ := decimal.NewFromString(resp.ReceivedAmount)
	confirmedAmount, _ := decimal.NewFromString(resp.ConfirmedAmount)

	return &domain.InboundIntentStatusResponse{
		IntentID:        req.IntentID,
		ProviderRef:     resp.ProviderRef,
		Status:          status,
		ReceivedAmount:  receivedAmount,
		ConfirmedAmount: confirmedAmount,
		Confirmations:   resp.Confirmations,
		TransactionRef:  resp.TxHash,
		UpdatedAt:       time.Now(),
	}, nil
}

// VerifyInboundWebhook 验证入金 webhook
func (c *CryptoPayAdapter) VerifyInboundWebhook(ctx context.Context, payload []byte, signature string) (*domain.InboundWebhookEvent, error) {
	// 验证签名
	if c.webhookSecret != "" {
		if !c.verifySignature(payload, signature) {
			return nil, errors.New("crypto pay: webhook signature verification failed")
		}
	}

	// 解析事件
	var event struct {
		EventID       string `json:"event_id"`
		EventType     string `json:"event_type"`
		IntentID      string `json:"intent_id"`
		ProviderRef   string `json:"provider_ref"`
		Amount        string `json:"amount"`
		Asset         string `json:"asset"`
		Network       string `json:"network"`
		Status        string `json:"status"`
		Confirmations int    `json:"confirmations"`
		TxHash        string `json:"tx_hash"`
		Timestamp     string `json:"timestamp"`
	}

	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("crypto pay: failed to parse webhook: %w", err)
	}

	// 验证必填字段
	if event.EventID == "" || event.ProviderRef == "" || event.TxHash == "" {
		return nil, errors.New("crypto pay: webhook missing required fields")
	}

	// 验证资产白名单
	if !c.SupportsAsset(event.Asset) {
		return nil, domain.ErrAssetNotSupported
	}

	// 验证网络白名单
	if event.Network != "" && !c.SupportsNetwork(event.Network) {
		return nil, domain.ErrNetworkNotSupported
	}

	// 转换状态
	status := domain.InboundStatusCreated
	switch event.Status {
	case "CONFIRMED":
		status = domain.InboundStatusConfirmed
	case "PARTIALLY_CONFIRMED":
		status = domain.InboundStatusPartiallyMatched
	case "RECONCILE_MISMATCH":
		status = domain.InboundStatusReconcileMismatch
	case "FAILED":
		status = domain.InboundStatusFailed
	}

	amount, _ := decimal.NewFromString(event.Amount)

	timestamp := time.Now()
	if event.Timestamp != "" {
		t, err := time.Parse(time.RFC3339, event.Timestamp)
		if err == nil {
			timestamp = t
		}
	}

	intentID := uuid.Nil
	if event.IntentID != "" {
		intentID, _ = uuid.Parse(event.IntentID)
	}

	return &domain.InboundWebhookEvent{
		EventID:        event.EventID,
		EventType:      event.EventType,
		IntentID:       intentID,
		ProviderRef:    event.ProviderRef,
		Amount:         amount,
		AssetCode:      event.Asset,
		Status:         status,
		TransactionRef: event.TxHash,
		Timestamp:      timestamp,
		RawPayload:     payload,
	}, nil
}

// ============================================================================
// 内部方法
// ============================================================================

// doRequest 执行 HTTP 请求
// 不在 DB transaction 内调用
func (c *CryptoPayAdapter) doRequest(ctx context.Context, method string, path string, body map[string]any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = strings.NewReader(string(jsonBody))
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	if c.rpcToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.rpcToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	return respBody, resp.StatusCode, nil
}

// verifySignature 验证 webhook 签名
func (c *CryptoPayAdapter) verifySignature(payload []byte, signature string) bool {
	// 解析签名头
	// 格式: t=timestamp,v1=signature
	var timestamp string
	var sig string
	for _, part := range strings.Split(signature, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			sig = kv[1]
		}
	}

	if timestamp == "" || sig == "" {
		return false
	}

	// 计算期望签名
	message := timestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(c.webhookSecret))
	mac.Write([]byte(message))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(sig), []byte(expectedSig))
}

// ============================================================================
// 地址池管理（骨架）
// ============================================================================

// AddressPool 地址池管理
type AddressPool struct {
	addresses map[string]*PoolAddress // address -> PoolAddress
	mu        sync.RWMutex
}

// PoolAddress 池地址
type PoolAddress struct {
	Address     string
	Asset       string
	Network     string
	Status      string // AVAILABLE, LOCKED, USED
	LockedUntil *time.Time
	IntentID    *uuid.UUID
}

// NewAddressPool 创建地址池
func NewAddressPool() *AddressPool {
	return &AddressPool{
		addresses: make(map[string]*PoolAddress),
	}
}

// LockAddress 锁定地址
func (p *AddressPool) LockAddress(address string, intentID uuid.UUID, lockDuration time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	addr, ok := p.addresses[address]
	if !ok {
		return errors.New("address not found in pool")
	}

	if addr.Status != "AVAILABLE" {
		return errors.New("address not available")
	}

	addr.Status = "LOCKED"
	addr.IntentID = &intentID
	lockedUntil := time.Now().Add(lockDuration)
	addr.LockedUntil = &lockedUntil

	return nil
}

// ReleaseAddress 释放地址
func (p *AddressPool) ReleaseAddress(address string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	addr, ok := p.addresses[address]
	if !ok {
		return errors.New("address not found in pool")
	}

	addr.Status = "AVAILABLE"
	addr.IntentID = nil
	addr.LockedUntil = nil

	return nil
}

// AddAddress 添加地址到池
func (p *AddressPool) AddAddress(address string, asset string, network string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.addresses[address] = &PoolAddress{
		Address: address,
		Asset:   asset,
		Network: network,
		Status:  "AVAILABLE",
	}
}

// GetAvailableAddress 获取可用地址
func (p *AddressPool) GetAvailableAddress(asset string, network string) (*PoolAddress, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, addr := range p.addresses {
		if addr.Asset == asset && addr.Network == network && addr.Status == "AVAILABLE" {
			return addr, nil
		}
	}

	return nil, errors.New("no available address in pool")
}
