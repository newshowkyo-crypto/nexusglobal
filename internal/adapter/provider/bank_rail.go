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
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// Bank Rail Adapter - 银行通道适配器
// 支持 wire/SEPA/ACH/FPS 等银行转账
// ============================================================================

// BankRailAdapter 银行通道适配器
type BankRailAdapter struct {
	providerCode    string
	apiKey          string
	apiSecret       string
	baseURL         string
	httpClient      *http.Client
	supportedAssets []string
	supportedRails  []string // ACH, WIRE, SEPA, FPS
	config          map[string]any
}

// BankRailConfig 银行通道配置
type BankRailConfig struct {
	ProviderCode    string
	APIKey          string
	APISecret       string
	BaseURL         string
	Timeout         time.Duration
	SupportedAssets []string
	SupportedRails  []string
	Config          map[string]any
}

// NewBankRailAdapter 创建银行通道适配器
func NewBankRailAdapter(providerCode string, apiKey string, apiSecret string, config map[string]any) (domain.AdapterClient, error) {
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
		case "airwallex":
			baseURL = "https://api.airwallex.com"
		case "payoneer":
			baseURL = "https://api.payoneer.com"
		default:
			baseURL = "https://api.mock-bank-rail.local"
		}
	}

	// 支持的资产和通道
	supportedAssets := []string{"USD", "EUR", "GBP", "CNY"}
	supportedRails := []string{"ACH", "WIRE", "SEPA", "FPS"}

	if assets, ok := config["supported_assets"].([]string); ok {
		supportedAssets = assets
	}
	if rails, ok := config["supported_rails"].([]string); ok {
		supportedRails = rails
	}

	return &BankRailAdapter{
		providerCode: providerCode,
		apiKey:       apiKey,
		apiSecret:    apiSecret,
		baseURL:      baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
		supportedAssets: supportedAssets,
		supportedRails:  supportedRails,
		config:          config,
	}, nil
}

// GetAdapterType 返回适配器类型
func (b *BankRailAdapter) GetAdapterType() domain.AdapterType {
	return domain.AdapterTypeBankRail
}

// SupportsAsset 检查是否支持指定资产
func (b *BankRailAdapter) SupportsAsset(assetCode string) bool {
	for _, a := range b.supportedAssets {
		if a == assetCode {
			return true
		}
	}
	return false
}

// SupportsNetwork 检查是否支持指定网络（银行通道不使用网络概念）
func (b *BankRailAdapter) SupportsNetwork(network string) bool {
	return false
}

// SupportsRail 检查是否支持指定通道类型
func (b *BankRailAdapter) SupportsRail(rail string) bool {
	for _, r := range b.supportedRails {
		if r == rail {
			return true
		}
	}
	return false
}

// ============================================================================
// BridgeAdapter 接口实现
// ============================================================================

// CreateTransfer 创建银行转账
func (b *BankRailAdapter) CreateTransfer(ctx context.Context, req domain.TransferRequest) (*domain.TransferResponse, error) {
	// 验证资产支持
	if !b.SupportsAsset(req.SourceAsset) {
		return nil, domain.ErrAssetNotSupported
	}

	// 获取通道类型（从 metadata 或 config）
	rail := "ACH" // 默认 ACH
	if r, ok := req.Metadata["rail"].(string); ok {
		rail = r
	}
	if !b.SupportsRail(rail) {
		return nil, errors.New("bank rail: unsupported rail type")
	}

	// 构建请求
	transferReq := map[string]any{
		"asset":           req.SourceAsset,
		"amount":          req.Amount.String(),
		"destination":     req.Destination,
		"rail":            rail,
		"idempotency_key": req.IdempotencyKey,
	}

	// 添加银行账户详情（从 metadata）
	if accountNumber, ok := req.Metadata["account_number"].(string); ok {
		transferReq["account_number"] = accountNumber
	}
	if routingNumber, ok := req.Metadata["routing_number"].(string); ok {
		transferReq["routing_number"] = routingNumber
	}
	if iban, ok := req.Metadata["iban"].(string); ok {
		transferReq["iban"] = iban
	}
	if beneficiaryName, ok := req.Metadata["beneficiary_name"].(string); ok {
		transferReq["beneficiary_name"] = beneficiaryName
	}

	// 调用外部 API（不在 DB transaction 内）
	respBody, statusCode, err := b.doRequest(ctx, "POST", "/api/v1/transfers", transferReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	// 解析响应
	var resp struct {
		TransferID  string `json:"transfer_id"`
		ProviderRef string `json:"provider_ref"`
		Status      string `json:"status"`
		CreatedAt   string `json:"created_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("bank rail: failed to parse response: %w", err)
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
			return nil, fmt.Errorf("bank rail: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("bank rail: transfer failed with status %d", statusCode)
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
		ProviderRef: resp.ProviderRef,
		Status:      status,
		CreatedAt:   createdAt,
	}, nil
}

// QueryTransfer 查询转账状态
func (b *BankRailAdapter) QueryTransfer(ctx context.Context, req domain.QueryTransferRequest) (*domain.TransferStatus, error) {
	if req.ProviderRef == "" {
		return nil, errors.New("bank rail: provider_ref required")
	}

	// 调用外部 API
	respBody, statusCode, err := b.doRequest(ctx, "GET", fmt.Sprintf("/api/v1/transfers/%s", req.ProviderRef), nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return nil, domain.ErrUnknownExternalState
		}
		return nil, err
	}

	if statusCode >= 400 {
		return nil, fmt.Errorf("bank rail: query transfer failed with status %d", statusCode)
	}

	// 解析响应
	var resp struct {
		TransferID     string `json:"transfer_id"`
		ProviderRef    string `json:"provider_ref"`
		Status         string `json:"status"`
		Amount         string `json:"amount"`
		Fee            string `json:"fee"`
		TransactionRef string `json:"transaction_ref"`
		UpdatedAt      string `json:"updated_at"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("bank rail: failed to parse transfer status: %w", err)
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
	case "COMPLETED":
		status = domain.TransferStatusCompleted
	case "FAILED":
		status = domain.TransferStatusFailed
	case "CANCELLED":
		status = domain.TransferStatusCancelled
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
		TransactionRef: resp.TransactionRef,
		UpdatedAt:      updatedAt,
	}, nil
}

// CancelTransfer 取消转账（银行转账可能支持取消）
func (b *BankRailAdapter) CancelTransfer(ctx context.Context, req domain.CancelTransferRequest) error {
	if req.ProviderRef == "" {
		return errors.New("bank rail: provider_ref required")
	}

	// 调用外部 API
	_, statusCode, err := b.doRequest(ctx, "DELETE", fmt.Sprintf("/api/v1/transfers/%s", req.ProviderRef), nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
			return domain.ErrUnknownExternalState
		}
		return err
	}

	if statusCode >= 400 {
		return fmt.Errorf("bank rail: cancel transfer failed with status %d", statusCode)
	}

	return nil
}

// ============================================================================
// 内部方法
// ============================================================================

// doRequest 执行 HTTP 请求
func (b *BankRailAdapter) doRequest(ctx context.Context, method string, path string, body map[string]any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = strings.NewReader(string(jsonBody))
	}

	url := b.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", b.apiKey)

	// 签名请求
	if b.apiSecret != "" {
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		signature := b.signRequest(method, path, timestamp, body)
		req.Header.Set("X-Timestamp", timestamp)
		req.Header.Set("X-Signature", signature)
	}

	resp, err := b.httpClient.Do(req)
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
func (b *BankRailAdapter) signRequest(method string, path string, timestamp string, body map[string]any) string {
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
	mac := hmac.New(sha256.New, []byte(b.apiSecret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
