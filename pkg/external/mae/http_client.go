package mae

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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

type healthResponse struct {
	Status  string `json:"status"`
	Latency int    `json:"latency_ms"`
	Error   string `json:"error,omitempty"`
}

func (c *HTTPClient) HealthCheck(ctx context.Context, maeID string) (Health, error) {
	var resp healthResponse
	if err := c.doRequest(ctx, "GET", "/api/v1/mae/"+maeID+"/health", nil, &resp); err != nil {
		return Health{}, err
	}

	if resp.Error != "" {
		return Health{}, classifyError(resp.Error)
	}

	return Health{
		Status:  resp.Status,
		Latency: resp.Latency,
	}, nil
}

func (c *HTTPClient) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var req *http.Request
	var err error

	if body != nil {
		jsonBody, _ := json.Marshal(body)
		req, err = http.NewRequestWithContext(ctx, method, c.baseURL+path, strings.NewReader(string(jsonBody)))
	} else {
		req, err = http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	}
	if err != nil {
		return fmt.Errorf("mae: create request: %w", err)
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
		return fmt.Errorf("mae: http request: %w", err)
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
			return fmt.Errorf("mae: decode response: %w", err)
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
