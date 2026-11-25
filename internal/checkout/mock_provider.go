package checkout

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// Mock Provider — 本地可控 HTTP server
//
// 模拟成功、拒绝、3DS/redirect、timeout、晚到 webhook、重复 webhook、乱序 webhook
// 内部仍走真 DB/ledger/outbox
// ============================================================================

// MockProviderScenario mock provider 场景。
type MockProviderScenario string

const (
	MockScenarioSuccess           MockProviderScenario = "success"
	MockScenarioFailure           MockProviderScenario = "failure"
	MockScenario3DSRedirect       MockProviderScenario = "3ds_redirect"
	MockScenarioTimeout           MockProviderScenario = "timeout"
	MockScenarioLateWebhook       MockProviderScenario = "late_webhook"
	MockScenarioDuplicateWebhook  MockProviderScenario = "duplicate_webhook"
	MockScenarioOutOfOrderWebhook MockProviderScenario = "out_of_order_webhook"
)

// MockProvider 本地可控 mock provider HTTP server。
type MockProvider struct {
	server           *httptest.Server
	scenario         MockProviderScenario
	mu               sync.Mutex
	webhookURL       string
	webhookSecret    []byte
	callCount        int64
	webhookDelay     time.Duration
	lateWebhookDelay time.Duration
	timeoutDelay     time.Duration
	providerRef      string

	webhooksSent  int64
	webhookMu     sync.Mutex
	webhookEvents []WebhookEventPayload
}

// NewMockProvider 创建 mock provider。
func NewMockProvider(scenario MockProviderScenario, webhookURL string, webhookSecret []byte) *MockProvider {
	mp := &MockProvider{
		scenario:         scenario,
		webhookURL:       webhookURL,
		webhookSecret:    webhookSecret,
		webhookDelay:     100 * time.Millisecond,
		lateWebhookDelay: 2 * time.Second,
		timeoutDelay:     30 * time.Second,
	}

	// 使用自定义 server 配置，允许 Close 时强制关闭不等待正在处理的请求
	server := httptest.NewUnstartedServer(http.HandlerFunc(mp.handleCharge))
	server.Config.RegisterOnShutdown(func() {
		// 强制关闭，不等待正在处理的请求
	})
	mp.server = server
	server.Start()
	return mp
}

// Close 关闭 mock provider。
func (mp *MockProvider) Close() {
	mp.server.Close()
}

// URL 返回 mock provider URL。
func (mp *MockProvider) URL() string {
	return mp.server.URL
}

// SetScenario 设置场景。
func (mp *MockProvider) SetScenario(s MockProviderScenario) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.scenario = s
}

// SetWebhookDelay 设置 webhook 延迟。
func (mp *MockProvider) SetWebhookDelay(d time.Duration) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.webhookDelay = d
}

// SetLateWebhookDelay 设置晚到 webhook 延迟。
func (mp *MockProvider) SetLateWebhookDelay(d time.Duration) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.lateWebhookDelay = d
}

// SetTimeoutDelay 设置 timeout 场景的延迟。
func (mp *MockProvider) SetTimeoutDelay(d time.Duration) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.timeoutDelay = d
}

// CallCount 返回调用次数。
func (mp *MockProvider) CallCount() int64 {
	return atomic.LoadInt64(&mp.callCount)
}

// WebhooksSent 返回已发送 webhook 数。
func (mp *MockProvider) WebhooksSent() int64 {
	return atomic.LoadInt64(&mp.webhooksSent)
}

// GetWebhookEvents 返回已发送 webhook 事件。
func (mp *MockProvider) GetWebhookEvents() []WebhookEventPayload {
	mp.webhookMu.Lock()
	defer mp.webhookMu.Unlock()
	result := make([]WebhookEventPayload, len(mp.webhookEvents))
	copy(result, mp.webhookEvents)
	return result
}

// MockChargeRequest mock provider charge 请求。
type MockChargeRequest struct {
	OrderID                string `json:"order_id"`
	AttemptID              string `json:"attempt_id"`
	AmountMinor            int64  `json:"amount_minor"`
	Currency               string `json:"currency"`
	PaymentMethodToken     string `json:"payment_method_token"`
	ProviderIdempotencyKey string `json:"provider_idempotency_key"`
}

// MockChargeResponse mock provider charge 响应。
type MockChargeResponse struct {
	Status                string `json:"status"`
	ProviderRef           string `json:"provider_ref"`
	ProviderTransactionID string `json:"provider_transaction_id"`
	RedirectURL           string `json:"redirect_url,omitempty"`
	ActionType            string `json:"action_type,omitempty"`
	FailureCode           string `json:"failure_code,omitempty"`
	FailureReason         string `json:"failure_reason,omitempty"`
}

// handleCharge 处理 charge 请求。
func (mp *MockProvider) handleCharge(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&mp.callCount, 1)

	mp.mu.Lock()
	scenario := mp.scenario
	webhookDelay := mp.webhookDelay
	lateWebhookDelay := mp.lateWebhookDelay
	timeoutDelay := mp.timeoutDelay
	mp.mu.Unlock()

	var req MockChargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
		return
	}

	providerRef := fmt.Sprintf("ref_%s", uuid.New().String())
	mp.providerRef = providerRef

	switch scenario {
	case MockScenarioTimeout:
		// 模拟 timeout：不响应，等待客户端超时
		time.Sleep(timeoutDelay)
		w.WriteHeader(http.StatusGatewayTimeout)
		return

	case MockScenarioFailure:
		resp := MockChargeResponse{
			Status:        "failed",
			FailureCode:   "CARD_DECLINED",
			FailureReason: "card declined by issuer",
			ProviderRef:   providerRef,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		mp.sendWebhook(req, resp, webhookDelay)
		return

	case MockScenario3DSRedirect:
		resp := MockChargeResponse{
			Status:      "requires_action",
			RedirectURL: "https://3ds.example.com/challenge/" + req.AttemptID,
			ActionType:  "3DS_CHALLENGE",
			ProviderRef: providerRef,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		mp.sendWebhook(req, resp, webhookDelay)
		return

	case MockScenarioLateWebhook:
		resp := MockChargeResponse{
			Status:      "processing",
			ProviderRef: providerRef,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		successResp := MockChargeResponse{
			Status:                "captured",
			ProviderRef:           providerRef,
			ProviderTransactionID: "txn_" + req.OrderID,
		}
		mp.sendWebhook(req, successResp, lateWebhookDelay)
		return

	case MockScenarioDuplicateWebhook:
		resp := MockChargeResponse{
			Status:                "captured",
			ProviderRef:           providerRef,
			ProviderTransactionID: "txn_" + req.OrderID,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		// 发送两次相同 webhook
		mp.sendWebhook(req, resp, webhookDelay)
		mp.sendWebhook(req, resp, webhookDelay+100*time.Millisecond)
		return

	case MockScenarioOutOfOrderWebhook:
		resp := MockChargeResponse{
			Status:                "captured",
			ProviderRef:           providerRef,
			ProviderTransactionID: "txn_" + req.OrderID,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		// 先发送 captured（sequence=2）
		mp.sendWebhookWithSequence(req, resp, webhookDelay, 2)
		// 再发送 authorized（sequence=1，乱序）
		authResp := MockChargeResponse{
			Status:      "authorized",
			ProviderRef: providerRef,
		}
		mp.sendWebhookWithSequence(req, authResp, webhookDelay+100*time.Millisecond, 1)
		return

	default: // MockScenarioSuccess
		resp := MockChargeResponse{
			Status:                "captured",
			ProviderRef:           providerRef,
			ProviderTransactionID: "txn_" + req.OrderID,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		mp.sendWebhook(req, resp, webhookDelay)
		return
	}
}

// sendWebhook 发送 webhook 事件。
func (mp *MockProvider) sendWebhook(req MockChargeRequest, resp MockChargeResponse, delay time.Duration) {
	mp.sendWebhookWithSequence(req, resp, delay, 1)
}

// sendWebhookWithSequence 发送带 sequence 的 webhook 事件。
func (mp *MockProvider) sendWebhookWithSequence(req MockChargeRequest, resp MockChargeResponse, delay time.Duration, sequence int64) {
	if mp.webhookURL == "" {
		return
	}

	event := WebhookEventPayload{
		EventType:       "payment." + resp.Status,
		ProviderEventID: fmt.Sprintf("evt_%s_%d", req.AttemptID, atomic.LoadInt64(&mp.webhooksSent)+1),
		AttemptID:       req.AttemptID,
		Status:          resp.Status,
		ProviderRef:     resp.ProviderRef,
		RedirectURL:     resp.RedirectURL,
		ActionType:      resp.ActionType,
		FailureCode:     resp.FailureCode,
		FailureReason:   resp.FailureReason,
		Sequence:        sequence,
	}

	go func() {
		time.Sleep(delay)

		payload, _ := json.Marshal(event)
		signature := computeMockHMAC(payload, mp.webhookSecret)

		httpReq, err := http.NewRequest("POST", mp.webhookURL, bytes.NewReader(payload))
		if err != nil {
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("X-Signature", signature)
		httpReq.Header.Set("X-Provider-Event-Id", event.ProviderEventID)

		client := &http.Client{Timeout: 5 * time.Second}
		httpResp, err := client.Do(httpReq)
		if err != nil {
			return
		}
		defer httpResp.Body.Close()

		atomic.AddInt64(&mp.webhooksSent, 1)
		mp.webhookMu.Lock()
		mp.webhookEvents = append(mp.webhookEvents, event)
		mp.webhookMu.Unlock()
	}()
}

// computeMockHMAC 计算 HMAC-SHA256 签名（hex 编码）。
func computeMockHMAC(payload, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// Ensure context is used
var _ = context.Background
