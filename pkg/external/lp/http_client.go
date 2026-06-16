package lp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
}

func NewHTTPClient(baseURL string, apiKey string, timeout time.Duration) *HTTPClient {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &HTTPClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Transport: httpx.DefaultTransport,
			Timeout:   timeout,
		},
		apiKey: apiKey,
	}
}

type fxLockRequest struct {
	PayoutRequestID string `json:"payout_request_id"`
	Amount          string `json:"amount"`
	FiatCurrency    string `json:"fiat_currency"`
	TargetAsset     string `json:"target_asset"`
	TargetNetwork   string `json:"target_network"`
}

type fxLockResponse struct {
	LockID    string `json:"lock_id"`
	Rate      string `json:"rate"`
	ExpiresAt string `json:"expires_at"`
	Error     string `json:"error,omitempty"`
}

func (c *HTTPClient) FXLock(ctx context.Context, req FXLockRequest) (FXLockResult, error) {
	body := fxLockRequest{
		PayoutRequestID: req.PayoutRequestID.String(),
		Amount:          req.Amount.String(),
		FiatCurrency:    req.FiatCurrency,
		TargetAsset:     req.TargetAsset,
		TargetNetwork:   req.TargetNetwork,
	}

	var resp fxLockResponse
	if err := c.doRequest(ctx, "POST", "/api/v1/fx-lock", body, &resp); err != nil {
		return FXLockResult{}, err
	}

	if resp.Error != "" {
		return FXLockResult{}, classifyError(resp.Error)
	}

	rate, _ := decimal.NewFromString(resp.Rate)
	expiresAt, _ := time.Parse(time.RFC3339, resp.ExpiresAt)

	return FXLockResult{
		LockID:    resp.LockID,
		Rate:      rate,
		ExpiresAt: expiresAt,
	}, nil
}

type payoutRequest struct {
	PayoutRequestID string `json:"payout_request_id"`
	LockID          string `json:"lock_id"`
	Amount          string `json:"amount"`
	FiatCurrency    string `json:"fiat_currency"`
	TargetAsset     string `json:"target_asset"`
	TargetAddress   string `json:"target_address"`
	TargetNetwork   string `json:"target_network"`
}

type payoutResponse struct {
	ProviderRef string `json:"provider_ref"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

func (c *HTTPClient) Payout(ctx context.Context, req PayoutRequest) (PayoutResult, error) {
	body := payoutRequest{
		PayoutRequestID: req.PayoutRequestID.String(),
		LockID:          req.LockID,
		Amount:          req.Amount.String(),
		FiatCurrency:    req.FiatCurrency,
		TargetAsset:     req.TargetAsset,
		TargetAddress:   req.TargetAddress,
		TargetNetwork:   req.TargetNetwork,
	}

	var resp payoutResponse
	if err := c.doRequest(ctx, "POST", "/api/v1/payout", body, &resp); err != nil {
		return PayoutResult{}, err
	}

	if resp.Error != "" {
		return PayoutResult{}, classifyError(resp.Error)
	}

	return PayoutResult{
		ProviderRef: resp.ProviderRef,
		Status:      resp.Status,
	}, nil
}

type statusResponse struct {
	ProviderRef string `json:"provider_ref"`
	Status      string `json:"status"`
	Amount      string `json:"amount"`
	Error       string `json:"error,omitempty"`
}

func (c *HTTPClient) QueryStatus(ctx context.Context, providerRef string) (StatusResult, error) {
	var resp statusResponse
	if err := c.doRequest(ctx, "GET", "/api/v1/status/"+providerRef, nil, &resp); err != nil {
		return StatusResult{}, err
	}

	if resp.Error != "" {
		return StatusResult{}, classifyError(resp.Error)
	}

	amount, _ := decimal.NewFromString(resp.Amount)

	return StatusResult{
		ProviderRef: resp.ProviderRef,
		Status:      resp.Status,
		Amount:      amount,
	}, nil
}

func (c *HTTPClient) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var reqBody *bytes.Buffer
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("lp: marshal request: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("lp: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ErrTimeout
		}
		return fmt.Errorf("lp: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusGatewayTimeout || resp.StatusCode == http.StatusRequestTimeout {
		return ErrTimeout
	}

	if resp.StatusCode >= 500 {
		return ErrExplicitFailure
	}

	if resp.StatusCode >= 400 {
		return ErrExplicitFailure
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("lp: decode response: %w", err)
		}
	}

	return nil
}

func classifyError(msg string) error {
	if strings.Contains(strings.ToLower(msg), "timeout") {
		return ErrTimeout
	}
	return ErrExplicitFailure
}
