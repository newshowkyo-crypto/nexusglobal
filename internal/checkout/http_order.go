package checkout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type HTTPOrderProvider struct {
	client  *http.Client
	baseURL string
}

func NewHTTPOrderProvider(baseURL string) *HTTPOrderProvider {
	return &HTTPOrderProvider{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		baseURL: baseURL,
	}
}

func (p *HTTPOrderProvider) GetOrderDetails(ctx context.Context, orderID string) (*CheckoutData, error) {
	if orderID == "" {
		return nil, fmt.Errorf("order ID is required")
	}

	url := fmt.Sprintf("%s/orders/%s", p.baseURL, orderID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("order not found: %s", orderID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("order service returned status %d", resp.StatusCode)
	}

	var data CheckoutData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode order response: %w", err)
	}

	data.OrderID = orderID
	return &data, nil
}
