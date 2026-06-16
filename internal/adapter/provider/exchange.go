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
// Exchange Adapter - 交易所适配器
// 支持 order book、limit order、market order（默认禁止）
// max_allowed_slippage = 0.002 (20 bps)
// ============================================================================

const (
	// DefaultMaxSlippage 默认最大滑点 20 bps
	DefaultMaxSlippage = 0.002 // 0.2%
)

// ExchangeAdapter 交易所适配器
type ExchangeAdapter struct {
	providerCode       string
	apiKey             string
	apiSecret          string
	withdrawKeyRef     string // KMS/HSM reference，不保存私钥
	baseURL            string
	httpClient         *http.Client
	maxAllowedSlippage decimal.Decimal
	marketOrderAllowed bool // 默认禁止市价单
	supportedAssets    []string
	supportedNetworks  []string
	config             map[string]any
	feeService         fee.FeeServiceInterface // 费用摊销服务
}

// ExchangeConfig 交易所配置
type ExchangeConfig struct {
	ProviderCode       string
	APIKey             string
	APISecret          string
	WithdrawKeyRef     string // KMS/HSM reference
	BaseURL            string
	Timeout            time.Duration
	MaxAllowedSlippage decimal.Decimal
	MarketOrderAllowed bool
	SupportedAssets    []string
	SupportedNetworks  []string
	Config             map[string]any
}

// NewExchangeAdapter 创建交易所适配器
func NewExchangeAdapter(providerCode string, apiKey string, apiSecret string, withdrawKeyRef string, feeService fee.FeeServiceInterface, config map[string]any) (domain.AdapterClient, error) {
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
		case "binance":
			baseURL = "https://api.binance.com"
		case "kraken":
			baseURL = "https://api.kraken.com"
		case "coinbase":
			baseURL = "https://api.coinbase.com"
		default:
			baseURL = "https://api.mock-exchange.local"
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

	// 市价单是否允许（默认禁止）
	marketOrderAllowed := false
	if ma, ok := config["market_order_allowed"].(bool); ok {
		marketOrderAllowed = ma
	}

	// 支持的资产和网络
	supportedAssets := []string{"USD", "USDT", "USDC", "BTC", "ETH"}
	supportedNetworks := []string{"TRON", "ETHEREUM", "BITCOIN"}

	if assets, ok := config["supported_assets"].([]string); ok {
		supportedAssets = assets
	}
	if networks, ok := config["supported_networks"].([]string); ok {
		supportedNetworks = networks
	}

	return &ExchangeAdapter{
		providerCode:   providerCode,
		apiKey:         apiKey,
		apiSecret:      apiSecret,
		withdrawKeyRef: withdrawKeyRef,
		baseURL:        baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
		maxAllowedSlippage: maxSlippage,
		marketOrderAllowed: marketOrderAllowed,
		supportedAssets:    supportedAssets,
		supportedNetworks:  supportedNetworks,
		config:             config,
		feeService:         feeService,
	}, nil
}

// GetAdapterType 返回适配器类型
func (e *ExchangeAdapter) GetAdapterType() domain.AdapterType {
	return domain.AdapterTypeExchange
}

// SupportsAsset 检查是否支持指定资产
func (e *ExchangeAdapter) SupportsAsset(assetCode string) bool {
	for _, a := range e.supportedAssets {
		if a == assetCode {
			return true
		}
	}
	return false
}

// SupportsNetwork 检查是否支持指定网络
func (e *ExchangeAdapter) SupportsNetwork(network string) bool {
	for _, n := range e.supportedNetworks {
		if n == network {
			return true
		}
	}
	return false
}

// ============================================================================
// ExchangeAdapter 接口实现
// ============================================================================

// GetOrderBook 获取订单簿
func (e *ExchangeAdapter) GetOrderBook(ctx context.Context, req domain.OrderBookRequest) (*domain.OrderBookResponse, error) {
	// 调用外部 API
	respBody, statusCode, err := e.doRequest(ctx, "GET", fmt.Sprintf("/api/v3/depth?symbol=%s&limit=%d", req.Symbol, req.Depth), nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	if statusCode >= 400 {
		return nil, fmt.Errorf("exchange: get order book failed with status %d", statusCode)
	}

	// 解析响应
	var resp struct {
		Symbol string     `json:"symbol"`
		Bids   [][]string `json:"bids"` // [price, amount]
		Asks   [][]string `json:"asks"` // [price, amount]
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("exchange: failed to parse order book: %w", err)
	}

	// 转换 bids/asks
	bids := make([]domain.OrderBookEntry, 0, len(resp.Bids))
	asks := make([]domain.OrderBookEntry, 0, len(resp.Asks))

	for _, b := range resp.Bids {
		price, _ := decimal.NewFromString(b[0])
		amount, _ := decimal.NewFromString(b[1])
		bids = append(bids, domain.OrderBookEntry{Price: price, Amount: amount})
	}

	for _, a := range resp.Asks {
		price, _ := decimal.NewFromString(a[0])
		amount, _ := decimal.NewFromString(a[1])
		asks = append(asks, domain.OrderBookEntry{Price: price, Amount: amount})
	}

	return &domain.OrderBookResponse{
		Symbol:    resp.Symbol,
		Bids:      bids,
		Asks:      asks,
		Timestamp: time.Now(),
	}, nil
}

// CalculateVWAP 计算 VWAP（Volume Weighted Average Price）
func (e *ExchangeAdapter) CalculateVWAP(orderBook *domain.OrderBookResponse, side domain.OrderSide, amount decimal.Decimal) (decimal.Decimal, decimal.Decimal, error) {
	entries := orderBook.Asks // BUY 时使用 asks
	if side == domain.OrderSideSell {
		entries = orderBook.Bids // SELL 时使用 bids
	}

	if len(entries) == 0 {
		return decimal.Zero, decimal.Zero, domain.ErrLiquidityInsufficient
	}

	// 计算 VWAP
	remaining := amount
	totalCost := decimal.Zero
	totalFilled := decimal.Zero

	for _, entry := range entries {
		if remaining.IsZero() || remaining.IsNegative() {
			break
		}

		fillAmount := decimal.Min(remaining, entry.Amount)
		totalCost = totalCost.Add(fillAmount.Mul(entry.Price))
		totalFilled = totalFilled.Add(fillAmount)
		remaining = remaining.Sub(fillAmount)
	}

	if !remaining.IsZero() {
		// 流动性不足
		return decimal.Zero, totalFilled, domain.ErrLiquidityInsufficient
	}

	vwap := totalCost.Div(totalFilled)
	return vwap, totalFilled, nil
}

// EstimateSlippage 估算滑点
func (e *ExchangeAdapter) EstimateSlippage(orderBook *domain.OrderBookResponse, side domain.OrderSide, amount decimal.Decimal) (decimal.Decimal, error) {
	entries := orderBook.Asks
	if side == domain.OrderSideSell {
		entries = orderBook.Bids
	}

	if len(entries) == 0 {
		return decimal.Zero, domain.ErrLiquidityInsufficient
	}

	// 最佳价格
	bestPrice := entries[0].Price

	// 计算 VWAP
	vwap, _, err := e.CalculateVWAP(orderBook, side, amount)
	if err != nil {
		return decimal.Zero, err
	}

	// 滑点 = (VWAP - bestPrice) / bestPrice
	slippage := vwap.Sub(bestPrice).Div(bestPrice)
	if side == domain.OrderSideSell {
		// SELL 时滑点为负，取绝对值
		slippage = slippage.Abs()
	}

	return slippage, nil
}

// PlaceLimitOrder 下限价单
func (e *ExchangeAdapter) PlaceLimitOrder(ctx context.Context, req domain.LimitOrderRequest) (*domain.OrderResponse, error) {
	// 先获取订单簿，计算滑点
	orderBook, err := e.GetOrderBook(ctx, domain.OrderBookRequest{
		Symbol: req.Symbol,
		Depth:  20,
	})
	if err != nil {
		return nil, err
	}

	// 估算滑点
	slippage, err := e.EstimateSlippage(orderBook, req.Side, req.Amount)
	if err != nil {
		return nil, err
	}

	// 检查滑点是否超限
	if slippage.GreaterThan(e.maxAllowedSlippage) {
		return nil, domain.ErrSlippageExceeded
	}

	// 构建请求
	orderReq := map[string]any{
		"symbol":          req.Symbol,
		"side":            string(req.Side),
		"type":            "LIMIT",
		"quantity":        req.Amount.String(),
		"price":           req.Price.String(),
		"timeInForce":     req.TimeInForce,
		"idempotency_key": req.IdempotencyKey,
	}

	// 调用外部 API（不在 DB transaction 内）
	respBody, statusCode, err := e.doRequest(ctx, "POST", "/api/v3/order", orderReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		OrderID             string `json:"orderId"`
		ProviderRef         string `json:"clientOrderId"`
		Symbol              string `json:"symbol"`
		Side                string `json:"side"`
		Type                string `json:"type"`
		OrigQty             string `json:"origQty"`
		Price               string `json:"price"`
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
		Status              string `json:"status"`
		TransactTime        int64  `json:"transactTime"`
		Fee                 string `json:"fee"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("exchange: failed to parse order response: %w", err)
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
			return nil, fmt.Errorf("exchange: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("exchange: order failed with status %d", statusCode)
	}

	// 转换状态
	status := domain.OrderStatusNew
	switch resp.Status {
	case "NEW":
		status = domain.OrderStatusNew
	case "PARTIALLY_FILLED":
		status = domain.OrderStatusPartiallyFilled
	case "FILLED":
		status = domain.OrderStatusFilled
	case "CANCELED":
		status = domain.OrderStatusCancelled
	case "REJECTED":
		status = domain.OrderStatusRejected
	case "EXPIRED":
		status = domain.OrderStatusExpired
	}

	origQty, _ := decimal.NewFromString(resp.OrigQty)
	price, _ := decimal.NewFromString(resp.Price)
	executedQty, _ := decimal.NewFromString(resp.ExecutedQty)
	cummulativeQuoteQty, _ := decimal.NewFromString(resp.CummulativeQuoteQty)
	feeAmount, _ := decimal.NewFromString(resp.Fee)

	filledPrice := decimal.Zero
	if executedQty.IsPositive() {
		filledPrice = cummulativeQuoteQty.Div(executedQty)
	}

	// 记录滑点费用（如果有费用且费用服务可用）
	if e.feeService != nil && feeAmount.IsPositive() {
		transactionID, _ := uuid.Parse(resp.OrderID)
		feeDetails := []fee.FeeDetail{
			{
				FeeType:    fee.FeeTypeFXSlippage,
				Amount:     int64(feeAmount.Mul(decimal.NewFromInt(100)).IntPart()), // 转换为最小单位
				SourceData: map[string]interface{}{"provider": e.providerCode, "order_id": resp.OrderID, "slippage_bps": slippage.Mul(decimal.NewFromInt(10000)).IntPart()},
			},
		}
		// 异步记录费用，不阻塞主流程
		go func() {
			if err := e.feeService.AllocateTransactionFees(context.Background(), transactionID, uuid.Nil, "USD", feeDetails); err != nil {
				fmt.Printf("failed to allocate FX slippage fee: %v\n", err)
			}
		}()
	}

	return &domain.OrderResponse{
		OrderID:      resp.OrderID,
		ProviderRef:  resp.ProviderRef,
		Symbol:       resp.Symbol,
		Side:         domain.OrderSide(resp.Side),
		Type:         "LIMIT",
		Amount:       origQty,
		Price:        price,
		FilledAmount: executedQty,
		FilledPrice:  filledPrice,
		Status:       status,
		CreatedAt:    time.UnixMilli(resp.TransactTime),
	}, nil
}

// PlaceMarketOrder 下市价单（默认禁止）
func (e *ExchangeAdapter) PlaceMarketOrder(ctx context.Context, req domain.MarketOrderRequest) (*domain.OrderResponse, error) {
	// 检查是否允许市价单
	if !e.marketOrderAllowed {
		return nil, errors.New("exchange: market order not allowed by policy")
	}

	// 先获取订单簿，计算滑点
	orderBook, err := e.GetOrderBook(ctx, domain.OrderBookRequest{
		Symbol: req.Symbol,
		Depth:  20,
	})
	if err != nil {
		return nil, err
	}

	// 估算滑点
	slippage, err := e.EstimateSlippage(orderBook, req.Side, req.Amount)
	if err != nil {
		return nil, err
	}

	// 检查滑点是否超限
	if slippage.GreaterThan(e.maxAllowedSlippage) {
		return nil, domain.ErrSlippageExceeded
	}

	// 构建请求
	orderReq := map[string]any{
		"symbol":          req.Symbol,
		"side":            string(req.Side),
		"type":            "MARKET",
		"quantity":        req.Amount.String(),
		"idempotency_key": req.IdempotencyKey,
	}

	// 调用外部 API
	respBody, statusCode, err := e.doRequest(ctx, "POST", "/api/v3/order", orderReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应（类似限价单）
	var resp struct {
		OrderID             string `json:"orderId"`
		ProviderRef         string `json:"clientOrderId"`
		Symbol              string `json:"symbol"`
		Side                string `json:"side"`
		Type                string `json:"type"`
		OrigQty             string `json:"origQty"`
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
		Status              string `json:"status"`
		TransactTime        int64  `json:"transactTime"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("exchange: failed to parse order response: %w", err)
	}

	if statusCode >= 400 {
		return nil, fmt.Errorf("exchange: market order failed with status %d", statusCode)
	}

	origQty, _ := decimal.NewFromString(resp.OrigQty)
	executedQty, _ := decimal.NewFromString(resp.ExecutedQty)
	cummulativeQuoteQty, _ := decimal.NewFromString(resp.CummulativeQuoteQty)

	filledPrice := decimal.Zero
	if executedQty.IsPositive() {
		filledPrice = cummulativeQuoteQty.Div(executedQty)
	}

	return &domain.OrderResponse{
		OrderID:      resp.OrderID,
		ProviderRef:  resp.ProviderRef,
		Symbol:       resp.Symbol,
		Side:         domain.OrderSide(resp.Side),
		Type:         "MARKET",
		Amount:       origQty,
		FilledAmount: executedQty,
		FilledPrice:  filledPrice,
		Status:       domain.OrderStatusFilled,
		CreatedAt:    time.UnixMilli(resp.TransactTime),
	}, nil
}

// QueryOrder 查询订单状态
func (e *ExchangeAdapter) QueryOrder(ctx context.Context, req domain.QueryOrderRequest) (*domain.OrderStatus, error) {
	if req.OrderID == "" && req.ProviderRef == "" {
		return nil, errors.New("exchange: order_id or provider_ref required")
	}

	// 调用外部 API
	path := fmt.Sprintf("/api/v3/order?symbol=%s", req.OrderID)
	if req.ProviderRef != "" {
		path = fmt.Sprintf("/api/v3/order?origClientOrderId=%s", req.ProviderRef)
	}

	respBody, statusCode, err := e.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	if statusCode >= 400 {
		return nil, fmt.Errorf("exchange: query order failed with status %d", statusCode)
	}

	// 解析响应
	var resp struct {
		OrderID             string `json:"orderId"`
		ProviderRef         string `json:"clientOrderId"`
		Symbol              string `json:"symbol"`
		Side                string `json:"side"`
		OrigQty             string `json:"origQty"`
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
		Status              string `json:"status"`
		Fee                 string `json:"fee"`
		UpdateTime          int64  `json:"updateTime"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("exchange: failed to parse order status: %w", err)
	}

	// 转换
	origQty, _ := decimal.NewFromString(resp.OrigQty)
	executedQty, _ := decimal.NewFromString(resp.ExecutedQty)
	cummulativeQuoteQty, _ := decimal.NewFromString(resp.CummulativeQuoteQty)
	fee, _ := decimal.NewFromString(resp.Fee)

	filledPrice := decimal.Zero
	if executedQty.IsPositive() {
		filledPrice = cummulativeQuoteQty.Div(executedQty)
	}

	status := domain.OrderStatusNew
	switch resp.Status {
	case "NEW":
		status = domain.OrderStatusNew
	case "PARTIALLY_FILLED":
		status = domain.OrderStatusPartiallyFilled
	case "FILLED":
		status = domain.OrderStatusFilled
	case "CANCELED":
		status = domain.OrderStatusCancelled
	case "REJECTED":
		status = domain.OrderStatusRejected
	case "EXPIRED":
		status = domain.OrderStatusExpired
	}

	return &domain.OrderStatus{
		OrderID:      resp.OrderID,
		ProviderRef:  resp.ProviderRef,
		Symbol:       resp.Symbol,
		Side:         domain.OrderSide(resp.Side),
		Amount:       origQty,
		FilledAmount: executedQty,
		FilledPrice:  filledPrice,
		Status:       status,
		Fee:          fee,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.UnixMilli(resp.UpdateTime),
	}, nil
}

// CancelOrder 取消订单
func (e *ExchangeAdapter) CancelOrder(ctx context.Context, req domain.CancelOrderRequest) error {
	if req.OrderID == "" && req.ProviderRef == "" {
		return errors.New("exchange: order_id or provider_ref required")
	}

	// 调用外部 API
	path := fmt.Sprintf("/api/v3/order?orderId=%s", req.OrderID)
	if req.ProviderRef != "" {
		path = fmt.Sprintf("/api/v3/order?origClientOrderId=%s", req.ProviderRef)
	}

	_, statusCode, err := e.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return domain.ErrUnknownExternalState
		}
		return err
	}

	if statusCode >= 400 {
		return fmt.Errorf("exchange: cancel order failed with status %d", statusCode)
	}

	return nil
}

// Withdraw 提现
func (e *ExchangeAdapter) Withdraw(ctx context.Context, req domain.WithdrawRequest) (*domain.WithdrawResponse, error) {
	// 验证网络支持
	if !e.SupportsNetwork(req.Network) {
		return nil, domain.ErrNetworkNotSupported
	}

	// 构建请求
	withdrawReq := map[string]any{
		"asset":           req.AssetCode,
		"network":         req.Network,
		"amount":          req.Amount.String(),
		"address":         req.DestinationAddress,
		"memoTag":         req.MemoTag,
		"idempotency_key": req.IdempotencyKey,
	}

	// 调用外部 API（不在 DB transaction 内）
	respBody, statusCode, err := e.doRequest(ctx, "POST", "/api/v3/withdraw", withdrawReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		WithdrawID    string `json:"id"`
		ProviderRef   string `json:"clientOrderId"`
		Status        string `json:"status"`
		TransactionID string `json:"transactionId"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("exchange: failed to parse withdraw response: %w", err)
	}

	if statusCode >= 400 {
		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Code != "" {
			return nil, fmt.Errorf("exchange: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("exchange: withdraw failed with status %d", statusCode)
	}

	// 转换状态
	status := domain.TransferStatusPending
	switch resp.Status {
	case "PENDING":
		status = domain.TransferStatusPending
	case "PROCESSING":
		status = domain.TransferStatusProcessing
	case "COMPLETED":
		status = domain.TransferStatusCompleted
	case "FAILED":
		status = domain.TransferStatusFailed
	}

	return &domain.WithdrawResponse{
		WithdrawID:    resp.WithdrawID,
		ProviderRef:   resp.ProviderRef,
		Status:        status,
		TransactionID: resp.TransactionID,
		CreatedAt:     time.Now(),
	}, nil
}

// ============================================================================
// BridgeAdapter 接口实现
// ============================================================================

// CreateTransfer 创建转账（Exchange 使用 Withdraw）
func (e *ExchangeAdapter) CreateTransfer(ctx context.Context, req domain.TransferRequest) (*domain.TransferResponse, error) {
	withdrawResp, err := e.Withdraw(ctx, domain.WithdrawRequest{
		AssetCode:          req.SourceAsset,
		Network:            req.Network,
		Amount:             req.Amount,
		DestinationAddress: req.Destination,
		MemoTag:            "",
		IdempotencyKey:     req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}

	return &domain.TransferResponse{
		TransferID:  uuid.New(),
		ProviderRef: withdrawResp.ProviderRef,
		Status:      withdrawResp.Status,
		CreatedAt:   withdrawResp.CreatedAt,
	}, nil
}

// QueryTransfer 查询转账状态
func (e *ExchangeAdapter) QueryTransfer(ctx context.Context, req domain.QueryTransferRequest) (*domain.TransferStatus, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("exchange: provider_ref required")
	}

	// 调用外部 API
	respBody, statusCode, err := e.doRequest(ctx, "GET", fmt.Sprintf("/api/v3/withdraw?clientOrderId=%s", req.ProviderRef), nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	if statusCode >= 400 {
		return nil, fmt.Errorf("exchange: query transfer failed with status %d", statusCode)
	}

	// 解析响应
	var resp struct {
		WithdrawID    string `json:"id"`
		ProviderRef   string `json:"clientOrderId"`
		Status        string `json:"status"`
		Amount        string `json:"amount"`
		Fee           string `json:"fee"`
		TransactionID string `json:"transactionId"`
		Confirmations int    `json:"confirmations"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("exchange: failed to parse transfer status: %w", err)
	}

	amount, _ := decimal.NewFromString(resp.Amount)
	fee, _ := decimal.NewFromString(resp.Fee)

	status := domain.TransferStatusPending
	switch resp.Status {
	case "PENDING":
		status = domain.TransferStatusPending
	case "PROCESSING":
		status = domain.TransferStatusProcessing
	case "COMPLETED":
		status = domain.TransferStatusCompleted
	case "FAILED":
		status = domain.TransferStatusFailed
	}

	return &domain.TransferStatus{
		TransferID:     req.TransferID,
		ProviderRef:    resp.ProviderRef,
		Status:         status,
		Amount:         amount,
		Fee:            fee,
		TransactionRef: resp.TransactionID,
		Confirmations:  resp.Confirmations,
		UpdatedAt:      time.Now(),
	}, nil
}

// CancelTransfer 取消转账（Exchange 通常不支持取消提现）
func (e *ExchangeAdapter) CancelTransfer(ctx context.Context, req domain.CancelTransferRequest) error {
	return errors.New("exchange: withdraw cannot be cancelled once submitted")
}

// ============================================================================
// 内部方法
// ============================================================================

// doRequest 执行 HTTP 请求
func (e *ExchangeAdapter) doRequest(ctx context.Context, method string, path string, body map[string]any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = strings.NewReader(string(jsonBody))
	}

	url := e.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MBX-APIKEY", e.apiKey)

	// 签名（如果需要）
	if e.apiSecret != "" && method != "GET" {
		// 添加 timestamp 和签名
		timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
		signature := e.signRequest(path, timestamp, body)
		req.URL.RawQuery += "&timestamp=" + timestamp + "&signature=" + signature
	}

	resp, err := e.httpClient.Do(req)
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
func (e *ExchangeAdapter) signRequest(path string, timestamp string, body map[string]any) string {
	// 构建查询字符串
	query := path
	if timestamp != "" {
		query += "&timestamp=" + timestamp
	}
	if body != nil {
		for k, v := range body {
			query += "&" + k + "=" + fmt.Sprintf("%v", v)
		}
	}

	// HMAC-SHA256 签名
	mac := hmac.New(sha256.New, []byte(e.apiSecret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
