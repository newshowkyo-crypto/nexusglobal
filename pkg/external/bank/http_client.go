package bank

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

type submitTransferRequest struct {
	SweepTransferID string `json:"sweep_transfer_id"`
	SourceAccountID string `json:"source_account_id"`
	TargetAccountID string `json:"target_account_id"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	Rail            string `json:"rail"`
	Reference       string `json:"reference"`
}

type submitTransferResponse struct {
	BankRef string `json:"bank_ref"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

func (c *HTTPClient) SubmitTransfer(ctx context.Context, req TransferRequest) (TransferResult, error) {
	body := submitTransferRequest{
		SweepTransferID: req.SweepTransferID.String(),
		SourceAccountID: req.SourceAccountID.String(),
		TargetAccountID: req.TargetAccountID.String(),
		Amount:          req.Amount.StringFixed(4),
		Currency:        req.Currency,
		Rail:            req.Rail,
		Reference:       req.Reference,
	}

	var resp submitTransferResponse
	if err := c.doRequest(ctx, "POST", "/api/v1/transfers", body, &resp); err != nil {
		return TransferResult{}, err
	}

	if resp.Error != "" {
		return TransferResult{}, classifyError(resp.Error)
	}

	return TransferResult{
		BankRef: resp.BankRef,
		Status:  resp.Status,
	}, nil
}

type queryTransferResponse struct {
	BankRef string `json:"bank_ref"`
	Status  string `json:"status"`
	Amount  string `json:"amount"`
	Error   string `json:"error,omitempty"`
}

func (c *HTTPClient) QueryTransfer(ctx context.Context, bankRef string) (TransferStatus, error) {
	var resp queryTransferResponse
	if err := c.doRequest(ctx, "GET", "/api/v1/transfers/"+bankRef, nil, &resp); err != nil {
		return TransferStatus{}, err
	}

	if resp.Error != "" {
		return TransferStatus{}, classifyError(resp.Error)
	}

	return TransferStatus{
		BankRef: resp.BankRef,
		Status:  resp.Status,
	}, nil
}

func (c *HTTPClient) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var reqBody *bytes.Buffer
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("bank: marshal request: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("bank: create request: %w", err)
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
		return fmt.Errorf("bank: http request: %w", err)
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
			return fmt.Errorf("bank: decode response: %w", err)
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

func decimalFromStr(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}
