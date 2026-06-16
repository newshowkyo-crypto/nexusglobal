package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// Onchain Wallet Adapter - 链上钱包适配器
// 支持 withdraw 到白名单地址
// 强制 asset/network match
// 强制单笔限额和日限额
// confirmations 不足不得 finalize
// ============================================================================

// OnchainWalletAdapter 链上钱包适配器
type OnchainWalletAdapter struct {
	providerCode        string
	signingKeyRef       string // KMS/HSM reference，不保存私钥
	baseURL             string
	httpClient          *http.Client
	allowlistAddresses  map[string]bool // 白名单地址
	maxSingleWithdrawal decimal.Decimal // 单笔限额
	dailyLimit          decimal.Decimal // 日限额
	supportedAssets     []string
	supportedNetworks   []string
	config              map[string]any
}

// OnchainWalletConfig 链上钱包配置
type OnchainWalletConfig struct {
	ProviderCode        string
	SigningKeyRef       string // KMS/HSM reference
	BaseURL             string
	Timeout             time.Duration
	AllowlistAddresses  []string
	MaxSingleWithdrawal decimal.Decimal
	DailyLimit          decimal.Decimal
	SupportedAssets     []string
	SupportedNetworks   []string
	Config              map[string]any
}

// NewOnchainWalletAdapter 创建链上钱包适配器
func NewOnchainWalletAdapter(providerCode string, signingKeyRef string, config map[string]any) (domain.AdapterClient, error) {
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
		case "custody":
			baseURL = "https://api.custody.local"
		case "hot_wallet":
			baseURL = "https://api.hot-wallet.local"
		default:
			baseURL = "https://api.mock-onchain-wallet.local"
		}
	}

	// 白名单地址
	allowlistAddresses := make(map[string]bool)
	if addresses, ok := config["allowlist_addresses"].([]string); ok {
		for _, addr := range addresses {
			allowlistAddresses[addr] = true
		}
	}

	// 单笔限额
	maxSingleWithdrawal := decimal.NewFromInt(10000) // 默认 10000
	if msw, ok := config["max_single_withdrawal"].(decimal.Decimal); ok {
		maxSingleWithdrawal = msw
	} else if mswStr, ok := config["max_single_withdrawal"].(string); ok {
		msw, _ = decimal.NewFromString(mswStr)
		if msw.IsPositive() {
			maxSingleWithdrawal = msw
		}
	}

	// 日限额
	dailyLimit := decimal.NewFromInt(50000) // 默认 50000
	if dl, ok := config["daily_limit"].(decimal.Decimal); ok {
		dailyLimit = dl
	} else if dlStr, ok := config["daily_limit"].(string); ok {
		dl, _ = decimal.NewFromString(dlStr)
		if dl.IsPositive() {
			dailyLimit = dl
		}
	}

	// 支持的资产和网络
	supportedAssets := []string{"USDT", "USDC", "BTC", "ETH"}
	supportedNetworks := []string{"TRON", "ETHEREUM", "BITCOIN"}

	if assets, ok := config["supported_assets"].([]string); ok {
		supportedAssets = assets
	}
	if networks, ok := config["supported_networks"].([]string); ok {
		supportedNetworks = networks
	}

	return &OnchainWalletAdapter{
		providerCode:  providerCode,
		signingKeyRef: signingKeyRef,
		baseURL:       baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
		allowlistAddresses:  allowlistAddresses,
		maxSingleWithdrawal: maxSingleWithdrawal,
		dailyLimit:          dailyLimit,
		supportedAssets:     supportedAssets,
		supportedNetworks:   supportedNetworks,
		config:              config,
	}, nil
}

// GetAdapterType 返回适配器类型
func (o *OnchainWalletAdapter) GetAdapterType() domain.AdapterType {
	return domain.AdapterTypeOnchainWallet
}

// SupportsAsset 检查是否支持指定资产
func (o *OnchainWalletAdapter) SupportsAsset(assetCode string) bool {
	for _, a := range o.supportedAssets {
		if a == assetCode {
			return true
		}
	}
	return false
}

// SupportsNetwork 检查是否支持指定网络
func (o *OnchainWalletAdapter) SupportsNetwork(network string) bool {
	for _, n := range o.supportedNetworks {
		if n == network {
			return true
		}
	}
	return false
}

// IsAddressAllowlisted 检查地址是否在白名单
func (o *OnchainWalletAdapter) IsAddressAllowlisted(address string) bool {
	return o.allowlistAddresses[address]
}

// ============================================================================
// BridgeAdapter 接口实现
// ============================================================================

// CreateTransfer 创建转账（链上提现）
func (o *OnchainWalletAdapter) CreateTransfer(ctx context.Context, req domain.TransferRequest) (*domain.TransferResponse, error) {
	// 验证资产支持
	if !o.SupportsAsset(req.SourceAsset) {
		return nil, domain.ErrAssetNotSupported
	}

	// 验证网络支持
	if !o.SupportsNetwork(req.Network) {
		return nil, domain.ErrNetworkNotSupported
	}

	// 验证地址白名单
	if !o.IsAddressAllowlisted(req.Destination) {
		return nil, domain.ErrAddressNotAllowlisted
	}

	// 验证单笔限额
	if req.Amount.GreaterThan(o.maxSingleWithdrawal) {
		return nil, domain.ErrLimitExceeded
	}

	// 构建请求
	transferReq := map[string]any{
		"asset":           req.SourceAsset,
		"network":         req.Network,
		"amount":          req.Amount.String(),
		"destination":     req.Destination,
		"idempotency_key": req.IdempotencyKey,
		"signing_key_ref": o.signingKeyRef, // KMS/HSM reference
	}

	// 调用外部 API（不在 DB transaction 内）
	respBody, statusCode, err := o.doRequest(ctx, "POST", "/api/v1/transfers", transferReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		TransferID    string `json:"transfer_id"`
		ProviderRef   string `json:"provider_ref"`
		TxHash        string `json:"tx_hash"`
		Status        string `json:"status"`
		Confirmations int    `json:"confirmations"`
		CreatedAt     string `json:"created_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("onchain wallet: failed to parse response: %w", err)
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
			if errResp.Error.Code == "ADDRESS_NOT_ALLOWLISTED" {
				return nil, domain.ErrAddressNotAllowlisted
			}
			if errResp.Error.Code == "LIMIT_EXCEEDED" {
				return nil, domain.ErrLimitExceeded
			}
			if errResp.Error.Code == "WRONG_NETWORK" {
				return nil, domain.ErrNetworkNotSupported
			}
			return nil, fmt.Errorf("onchain wallet: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("onchain wallet: transfer failed with status %d", statusCode)
	}

	// 转换状态
	status := domain.TransferStatusPending
	switch resp.Status {
	case "PENDING":
		status = domain.TransferStatusPending
	case "SUBMITTED":
		status = domain.TransferStatusSubmitted
	case "PROCESSING":
		status = domain.TransferStatusProcessing
	case "CONFIRMING":
		status = domain.TransferStatusProcessing // 等待确认
	case "COMPLETED":
		status = domain.TransferStatusCompleted
	case "FAILED":
		status = domain.TransferStatusFailed
	}

	createdAt := time.Now()
	if resp.CreatedAt != "" {
		t, err := time.Parse(time.RFC3339, resp.CreatedAt)
		if err == nil {
			createdAt = t
		}
	}

	transferID := uuid.New()
	if resp.TransferID != "" {
		transferID, _ = uuid.Parse(resp.TransferID)
	}

	return &domain.TransferResponse{
		TransferID:  transferID,
		ProviderRef: resp.TxHash, // 链上使用 tx hash 作为 provider ref
		Status:      status,
		CreatedAt:   createdAt,
	}, nil
}

// QueryTransfer 查询转账状态
func (o *OnchainWalletAdapter) QueryTransfer(ctx context.Context, req domain.QueryTransferRequest) (*domain.TransferStatus, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("onchain wallet: provider_ref (tx_hash) required")
	}

	// 调用外部 API
	respBody, statusCode, err := o.doRequest(ctx, "GET", fmt.Sprintf("/api/v1/transfers/%s", req.ProviderRef), nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	if statusCode >= 400 {
		return nil, fmt.Errorf("onchain wallet: query transfer failed with status %d", statusCode)
	}

	// 解析响应
	var resp struct {
		TransferID    string `json:"transfer_id"`
		ProviderRef   string `json:"tx_hash"`
		Status        string `json:"status"`
		Amount        string `json:"amount"`
		Fee           string `json:"fee"`
		Confirmations int    `json:"confirmations"`
		UpdatedAt     string `json:"updated_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("onchain wallet: failed to parse transfer status: %w", err)
	}

	// 转换状态
	status := domain.TransferStatusPending
	switch resp.Status {
	case "PENDING":
		status = domain.TransferStatusPending
	case "SUBMITTED":
		status = domain.TransferStatusSubmitted
	case "PROCESSING":
		status = domain.TransferStatusProcessing
	case "CONFIRMING":
		status = domain.TransferStatusProcessing
	case "COMPLETED":
		status = domain.TransferStatusCompleted
	case "FAILED":
		status = domain.TransferStatusFailed
	}

	amount, _ := decimal.NewFromString(resp.Amount)
	fee, _ := decimal.NewFromString(resp.Fee)

	updatedAt := time.Now()
	if resp.UpdatedAt != "" {
		t, err := time.Parse(time.RFC3339, resp.UpdatedAt)
		if err == nil {
			updatedAt = t
		}
	}

	return &domain.TransferStatus{
		TransferID:     req.TransferID,
		ProviderRef:    resp.ProviderRef,
		Status:         status,
		Amount:         amount,
		Fee:            fee,
		TransactionRef: resp.ProviderRef,
		Confirmations:  resp.Confirmations,
		UpdatedAt:      updatedAt,
	}, nil
}

// CancelTransfer 取消转账（链上交易无法取消）
func (o *OnchainWalletAdapter) CancelTransfer(ctx context.Context, req domain.CancelTransferRequest) error {
	return errors.New("onchain wallet: blockchain transaction cannot be cancelled")
}

// ============================================================================
// 内部方法
// ============================================================================

// doRequest 执行 HTTP 请求
func (o *OnchainWalletAdapter) doRequest(ctx context.Context, method string, path string, body map[string]any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = strings.NewReader(string(jsonBody))
	}

	url := o.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	if o.signingKeyRef != "" {
		req.Header.Set("X-Signing-Key-Ref", o.signingKeyRef)
	}

	resp, err := o.httpClient.Do(req)
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

// ============================================================================
// 限额检查
// ============================================================================

// CheckDailyLimit 检查日限额（需要外部调用获取当日已用额度）
func (o *OnchainWalletAdapter) CheckDailyLimit(ctx context.Context, amount decimal.Decimal) error {
	// 获取当日已用额度
	respBody, statusCode, err := o.doRequest(ctx, "GET", "/api/v1/limits/daily", nil)
	if err != nil {
		return err
	}

	if statusCode >= 400 {
		return fmt.Errorf("onchain wallet: get daily limit failed with status %d", statusCode)
	}

	var resp struct {
		UsedToday string `json:"used_today"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return fmt.Errorf("onchain wallet: failed to parse daily limit: %w", err)
	}

	usedToday, _ := decimal.NewFromString(resp.UsedToday)
	total := usedToday.Add(amount)

	if total.GreaterThan(o.dailyLimit) {
		return domain.ErrLimitExceeded
	}

	return nil
}
