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
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
	"github.com/nexusglobal/mamr/internal/ledger/fee"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// LP Adapter - 流动性提供商适配器
// 支持 quote、accept quote
// quote 有 TTL，过期必须拒绝
// max_allowed_slippage = 0.002 (20 bps)
// ============================================================================

// LPAdapter 流动性提供商适配器
type LPAdapter struct {
	providerCode       string
	apiKey             string
	apiSecret          string
	baseURL            string
	httpClient         *http.Client
	maxAllowedSlippage decimal.Decimal
	quoteTTLSeconds    int // 报价 TTL
	supportedAssets    []string
	supportedNetworks  []string
	config             map[string]any
	feeService         fee.FeeServiceInterface // 费用摊销服务
}

// LPConfig LP 配置
type LPConfig struct {
	ProviderCode       string
	APIKey             string
	APISecret          string
	BaseURL            string
	Timeout            time.Duration
	MaxAllowedSlippage decimal.Decimal
	QuoteTTLSeconds    int
	SupportedAssets    []string
	SupportedNetworks  []string
	Config             map[string]any
}

// NewLPAdapter 创建 LP 适配器
func NewLPAdapter(providerCode string, apiKey string, apiSecret string, feeService fee.FeeServiceInterface, config map[string]any) (domain.AdapterClient, error) {
	if apiKey == "" {
		return nil, domain.ErrCredentialMissing
	}

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
		case "lp_institutional":
			baseURL = "https://api.lp-institutional.local"
		default:
			baseURL = "https://api.mock-lp.local"
		}
	}

	// 最大滑点
	maxSlippage := decimal.NewFromFloat(DefaultMaxSlippage)
	if ms, ok := config["max_allowed_slippage"].(decimal.Decimal); ok {
		maxSlippage = ms
	} else if msStr, ok := config["max_allowed_slippage"].(string); ok {
		ms, _ = decimal.NewFromString(msStr)
		if ms.IsPositive() {
			maxSlippage = ms
		}
	}

	// 报价 TTL（默认 60 秒）
	quoteTTLSeconds := 60
	if ttl, ok := config["quote_ttl_seconds"].(int); ok {
		quoteTTLSeconds = ttl
	}

	// 支持的资产和网络
	supportedAssets := []string{"USD", "EUR", "USDT", "USDC"}
	supportedNetworks := []string{"TRON", "ETHEREUM"}

	if assets, ok := config["supported_assets"].([]string); ok {
		supportedAssets = assets
	}
	if networks, ok := config["supported_networks"].([]string); ok {
		supportedNetworks = networks
	}

	return &LPAdapter{
		providerCode: providerCode,
		apiKey:       apiKey,
		apiSecret:    apiSecret,
		baseURL:      baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
		maxAllowedSlippage: maxSlippage,
		quoteTTLSeconds:    quoteTTLSeconds,
		supportedAssets:    supportedAssets,
		supportedNetworks:  supportedNetworks,
		config:             config,
		feeService:         feeService,
	}, nil
}

// GetAdapterType 返回适配器类型
func (l *LPAdapter) GetAdapterType() domain.AdapterType {
	return domain.AdapterTypeLP
}

// SupportsAsset 检查是否支持指定资产
func (l *LPAdapter) SupportsAsset(assetCode string) bool {
	for _, a := range l.supportedAssets {
		if a == assetCode {
			return true
		}
	}
	return false
}

// SupportsNetwork 检查是否支持指定网络
func (l *LPAdapter) SupportsNetwork(network string) bool {
	for _, n := range l.supportedNetworks {
		if n == network {
			return true
		}
	}
	return false
}

// ============================================================================
// LPAdapter 接口实现
// ============================================================================

// GetQuote 获取报价
func (l *LPAdapter) GetQuote(ctx context.Context, req domain.QuoteRequest) (*domain.QuoteResponse, error) {
	// 验证资产支持
	if !l.SupportsAsset(req.SourceAsset) || !l.SupportsAsset(req.TargetAsset) {
		return nil, domain.ErrAssetNotSupported
	}

	// 构建请求
	quoteReq := map[string]any{
		"source_asset": req.SourceAsset,
		"target_asset": req.TargetAsset,
		"amount":       req.Amount.String(),
		"direction":    req.Direction,
	}

	// 调用外部 API（不在 DB transaction 内）
	respBody, statusCode, err := l.doRequest(ctx, "POST", "/api/v1/quotes", quoteReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		QuoteID      string `json:"quote_id"`
		SourceAsset  string `json:"source_asset"`
		TargetAsset  string `json:"target_asset"`
		SourceAmount string `json:"source_amount"`
		TargetAmount string `json:"target_amount"`
		Rate         string `json:"rate"`
		SpreadBps    int    `json:"spread_bps"`
		ExpiresAt    string `json:"expires_at"`
		CreatedAt    string `json:"created_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("lp: failed to parse quote response: %w", err)
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
			if errResp.Error.Code == "LIQUIDITY_INSUFFICIENT" {
				return nil, domain.ErrLiquidityInsufficient
			}
			return nil, fmt.Errorf("lp: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("lp: get quote failed with status %d", statusCode)
	}

	// 检查滑点
	sourceAmount, _ := decimal.NewFromString(resp.SourceAmount)
	targetAmount, _ := decimal.NewFromString(resp.TargetAmount)
	rate, _ := decimal.NewFromString(resp.Rate)

	// 计算实际滑点（spread）
	spread := decimal.NewFromInt32(int32(resp.SpreadBps)).Div(decimal.NewFromInt(10000))
	if spread.GreaterThan(l.maxAllowedSlippage) {
		return nil, domain.ErrSlippageExceeded
	}

	// 解析过期时间
	expiresAt := time.Now().Add(time.Duration(l.quoteTTLSeconds) * time.Second)
	if resp.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, resp.ExpiresAt)
		if err == nil {
			expiresAt = t
		}
	}

	createdAt := time.Now()
	if resp.CreatedAt != "" {
		t, err := time.Parse(time.RFC3339, resp.CreatedAt)
		if err == nil {
			createdAt = t
		}
	}

	return &domain.QuoteResponse{
		QuoteID:      resp.QuoteID,
		SourceAsset:  resp.SourceAsset,
		TargetAsset:  resp.TargetAsset,
		SourceAmount: sourceAmount,
		TargetAmount: targetAmount,
		Rate:         rate,
		SpreadBps:    resp.SpreadBps,
		ExpiresAt:    expiresAt,
		CreatedAt:    createdAt,
	}, nil
}

// AcceptQuote 接受报价
func (l *LPAdapter) AcceptQuote(ctx context.Context, req domain.AcceptQuoteRequest) (*domain.QuoteAcceptResponse, error) {
	if req.QuoteID == "" {
		return nil, errors.New("lp: quote_id required")
	}

	// 构建请求
	acceptReq := map[string]any{
		"quote_id":        req.QuoteID,
		"idempotency_key": req.IdempotencyKey,
	}

	// 调用外部 API（不在 DB transaction 内）
	respBody, statusCode, err := l.doRequest(ctx, "POST", fmt.Sprintf("/api/v1/quotes/%s/accept", req.QuoteID), acceptReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		QuoteID       string `json:"quote_id"`
		ProviderRef   string `json:"transaction_id"`
		Status        string `json:"status"`
		TransactionID string `json:"external_transaction_id"`
		FeeAmount     string `json:"fee_amount"`
		FeeCurrency   string `json:"fee_currency"`
		CreatedAt     string `json:"created_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("lp: failed to parse accept response: %w", err)
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
			if errResp.Error.Code == "QUOTE_EXPIRED" {
				return nil, domain.ErrQuoteExpired
			}
			if errResp.Error.Code == "LIQUIDITY_INSUFFICIENT" {
				return nil, domain.ErrLiquidityInsufficient
			}
			return nil, fmt.Errorf("lp: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("lp: accept quote failed with status %d", statusCode)
	}

	createdAt := time.Now()
	if resp.CreatedAt != "" {
		t, err := time.Parse(time.RFC3339, resp.CreatedAt)
		if err == nil {
			createdAt = t
		}
	}

	// 记录 LP 费用（如果有费用且费用服务可用）
	if l.feeService != nil && resp.FeeAmount != "" {
		feeAmount, _ := decimal.NewFromString(resp.FeeAmount)
		if feeAmount.IsPositive() {
			transactionID, _ := uuid.Parse(resp.TransactionID)
			feeCurrency := resp.FeeCurrency
			if feeCurrency == "" {
				feeCurrency = "USD"
			}
			feeDetails := []fee.FeeDetail{
				{
					FeeType:    fee.FeeTypeLP,
					Amount:     int64(feeAmount.Mul(decimal.NewFromInt(100)).IntPart()), // 转换为最小单位
					SourceData: map[string]interface{}{"provider": l.providerCode, "quote_id": req.QuoteID, "transaction_id": resp.TransactionID},
				},
			}
			// 异步记录费用，不阻塞主流程
			go func() {
				if err := l.feeService.AllocateTransactionFees(context.Background(), transactionID, uuid.Nil, feeCurrency, feeDetails); err != nil {
					fmt.Printf("failed to allocate LP fee: %v\n", err)
				}
			}()
		}
	}

	return &domain.QuoteAcceptResponse{
		QuoteID:       resp.QuoteID,
		ProviderRef:   resp.ProviderRef,
		Status:        resp.Status,
		TransactionID: resp.TransactionID,
		CreatedAt:     createdAt,
	}, nil
}

// QueryQuote 查询报价状态
func (l *LPAdapter) QueryQuote(ctx context.Context, req domain.QueryQuoteRequest) (*domain.QuoteStatus, error) {
	if req.QuoteID == "" && req.ProviderRef == "" {
		return nil, errors.New("lp: quote_id or provider_ref required")
	}

	// 调用外部 API
	path := fmt.Sprintf("/api/v1/quotes/%s", req.QuoteID)
	if req.ProviderRef != "" {
		path = fmt.Sprintf("/api/v1/transactions/%s", req.ProviderRef)
	}

	respBody, statusCode, err := l.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	if statusCode >= 400 {
		return nil, fmt.Errorf("lp: query quote failed with status %d", statusCode)
	}

	// 解析响应
	var resp struct {
		QuoteID       string `json:"quote_id"`
		ProviderRef   string `json:"transaction_id"`
		SourceAsset   string `json:"source_asset"`
		TargetAsset   string `json:"target_asset"`
		SourceAmount  string `json:"source_amount"`
		TargetAmount  string `json:"target_amount"`
		Status        string `json:"status"`
		TransactionID string `json:"external_transaction_id"`
		UpdatedAt     string `json:"updated_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("lp: failed to parse quote status: %w", err)
	}

	sourceAmount, _ := decimal.NewFromString(resp.SourceAmount)
	targetAmount, _ := decimal.NewFromString(resp.TargetAmount)

	updatedAt := time.Now()
	if resp.UpdatedAt != "" {
		t, err := time.Parse(time.RFC3339, resp.UpdatedAt)
		if err == nil {
			updatedAt = t
		}
	}

	return &domain.QuoteStatus{
		QuoteID:       resp.QuoteID,
		ProviderRef:   resp.ProviderRef,
		SourceAsset:   resp.SourceAsset,
		TargetAsset:   resp.TargetAsset,
		SourceAmount:  sourceAmount,
		TargetAmount:  targetAmount,
		Status:        resp.Status,
		TransactionID: resp.TransactionID,
		UpdatedAt:     updatedAt,
	}, nil
}

// ============================================================================
// BridgeAdapter 接口实现
// ============================================================================

// CreateTransfer 创建转账（LP 使用 AcceptQuote）
func (l *LPAdapter) CreateTransfer(ctx context.Context, req domain.TransferRequest) (*domain.TransferResponse, error) {
	// 先获取报价
	quoteResp, err := l.GetQuote(ctx, domain.QuoteRequest{
		SourceAsset: req.SourceAsset,
		TargetAsset: req.TargetAsset,
		Amount:      req.Amount,
		Direction:   "SELL", // 卖出源资产
	})
	if err != nil {
		return nil, err
	}

	// 接受报价
	acceptResp, err := l.AcceptQuote(ctx, domain.AcceptQuoteRequest{
		QuoteID:        quoteResp.QuoteID,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}

	// 转换状态
	status := domain.TransferStatusPending
	switch acceptResp.Status {
	case "COMPLETED":
		status = domain.TransferStatusCompleted
	case "PROCESSING":
		status = domain.TransferStatusProcessing
	case "FAILED":
		status = domain.TransferStatusFailed
	}

	return &domain.TransferResponse{
		TransferID:  uuid.New(),
		ProviderRef: acceptResp.ProviderRef,
		Status:      status,
		CreatedAt:   acceptResp.CreatedAt,
	}, nil
}

// QueryTransfer 查询转账状态
func (l *LPAdapter) QueryTransfer(ctx context.Context, req domain.QueryTransferRequest) (*domain.TransferStatus, error) {
	quoteStatus, err := l.QueryQuote(ctx, domain.QueryQuoteRequest{
		ProviderRef: req.ProviderRef,
	})
	if err != nil {
		return nil, err
	}

	// 转换状态
	status := domain.TransferStatusPending
	switch quoteStatus.Status {
	case "COMPLETED":
		status = domain.TransferStatusCompleted
	case "PROCESSING":
		status = domain.TransferStatusProcessing
	case "FAILED":
		status = domain.TransferStatusFailed
	}

	return &domain.TransferStatus{
		TransferID:     req.TransferID,
		ProviderRef:    quoteStatus.ProviderRef,
		Status:         status,
		Amount:         quoteStatus.TargetAmount,
		TransactionRef: quoteStatus.TransactionID,
		UpdatedAt:      quoteStatus.UpdatedAt,
	}, nil
}

// CancelTransfer 取消转账（LP 通常不支持取消）
func (l *LPAdapter) CancelTransfer(ctx context.Context, req domain.CancelTransferRequest) error {
	return errors.New("lp: transfer cannot be cancelled once accepted")
}

// ============================================================================
// 内部方法
// ============================================================================

// doRequest 执行 HTTP 请求
func (l *LPAdapter) doRequest(ctx context.Context, method string, path string, body map[string]any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = strings.NewReader(string(jsonBody))
	}

	url := l.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", l.apiKey)

	// 签名请求
	if l.apiSecret != "" {
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		signature := l.signRequest(method, path, timestamp, body)
		req.Header.Set("X-Timestamp", timestamp)
		req.Header.Set("X-Signature", signature)
	}

	resp, err := l.httpClient.Do(req)
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

// signRequest 签名请求
func (l *LPAdapter) signRequest(method string, path string, timestamp string, body map[string]any) string {
	// 计算请求体 hash
	var bodyHash string
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		h := sha256.Sum256(jsonBody)
		bodyHash = hex.EncodeToString(h[:])
	} else {
		bodyHash = ""
	}

	// 构建签名消息
	message := strings.Join([]string{method, path, timestamp, bodyHash}, "")

	// HMAC-SHA256 签名
	mac := hmac.New(sha256.New, []byte(l.apiSecret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
