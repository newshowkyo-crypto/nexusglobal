package issuing

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPAdapterFreezeCard_UsesProviderRefAndIdempotencyHeader(t *testing.T) {
	var receivedPath string
	var receivedIdempotencyKey string
	var receivedBody map[string]interface{}

	// 模拟 Provider HTTP 服务
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedIdempotencyKey = r.Header.Get("Idempotency-Key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewHTTPAdapter(server.URL, "test-api-key")

	req := FreezeCardRequest{
		LocalCardID:     uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		ProviderID:      uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		ProviderCardRef: "ext-card-ref-abc123",
		IdempotencyKey:  "stable-idem-key-001",
		FrozenReason:    "fraud_detected",
	}

	err := adapter.FreezeCard(context.Background(), req)
	require.NoError(t, err)

	// 验证路径使用 ProviderCardRef 而非本地 UUID
	assert.True(t, strings.Contains(receivedPath, "ext-card-ref-abc123"),
		"HTTP path should contain ProviderCardRef, got: %s", receivedPath)
	assert.False(t, strings.Contains(receivedPath, "11111111-1111-1111-1111-111111111111"),
		"HTTP path should NOT contain local card UUID")

	// 验证 Idempotency-Key Header
	assert.Equal(t, "stable-idem-key-001", receivedIdempotencyKey,
		"Idempotency-Key header must match request")

	// 验证请求体包含 idempotency_key
	assert.Equal(t, "stable-idem-key-001", receivedBody["idempotency_key"])
	assert.Equal(t, "fraud_detected", receivedBody["frozen_reason"])
}

func TestHTTPAdapterFreezeCard_RejectsEmptyProviderCardRef(t *testing.T) {
	adapter := NewHTTPAdapter("http://localhost", "key")
	req := FreezeCardRequest{
		ProviderCardRef: "",
		IdempotencyKey:  "idem",
	}
	err := adapter.FreezeCard(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ProviderCardRef")
}

func TestHTTPAdapterFreezeCard_RejectsEmptyIdempotencyKey(t *testing.T) {
	adapter := NewHTTPAdapter("http://localhost", "key")
	req := FreezeCardRequest{
		ProviderCardRef: "ref",
		IdempotencyKey:  "",
	}
	err := adapter.FreezeCard(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IdempotencyKey")
}

func TestHTTPAdapterFreezeCard_TimeoutReturnsErrProviderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()

	adapter := NewHTTPAdapter(server.URL, "key")
	req := FreezeCardRequest{
		ProviderCardRef: "ref",
		IdempotencyKey:  "idem",
	}
	err := adapter.FreezeCard(context.Background(), req)
	require.Error(t, err)
	assert.True(t, IsTimeout(err), "504 should map to ErrProviderTimeout")
}

func TestHTTPAdapterFreezeCard_Accepts202And204(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))

		adapter := NewHTTPAdapter(server.URL, "key")
		req := FreezeCardRequest{
			ProviderCardRef: "ref",
			IdempotencyKey:  "idem",
		}
		err := adapter.FreezeCard(context.Background(), req)
		assert.NoError(t, err, "status %d should be accepted as success", status)

		server.Close()
	}
}

func TestAdapterFactory_HasAndValidate(t *testing.T) {
	factory := NewHTTPAdapterFactory()
	assert.False(t, factory.Has("test"), "should not have unregistered code")
	assert.Error(t, factory.Validate(), "empty factory should fail validation")

	factory.Register("test", func(_ context.Context, _ map[string]string) (IssuingAdapter, error) {
		return nil, nil
	})
	assert.True(t, factory.Has("test"), "should have registered code")
	assert.NoError(t, factory.Validate(), "non-empty factory should pass validation")
	assert.Len(t, factory.RegisteredCodes(), 1)
}

// ── P0-7 第二轮 P0-6: 网络超时分类测试 ──

// TestHTTPAdapterFreezeCard_ContextDeadlineIsTimeout 验证 context.DeadlineExceeded 映射为 ErrProviderTimeout。
func TestHTTPAdapterFreezeCard_ContextDeadlineIsTimeout(t *testing.T) {
	// 创建一个会阻塞的服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewHTTPAdapter(server.URL, "key")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := FreezeCardRequest{
		ProviderCardRef: "ref",
		IdempotencyKey:  "idem",
	}
	err := adapter.FreezeCard(ctx, req)
	require.Error(t, err)
	assert.True(t, IsTimeout(err), "context.DeadlineExceeded should map to ErrProviderTimeout")
}

// TestHTTPAdapterFreezeCard_NetworkTimeoutIsTimeout 验证 net.Error.Timeout() 映射为 ErrProviderTimeout。
func TestHTTPAdapterFreezeCard_NetworkTimeoutIsTimeout(t *testing.T) {
	// 使用一个不可达的地址，触发网络超时
	adapter := &httpAdapter{
		client:  &http.Client{Timeout: 50 * time.Millisecond},
		baseURL: "http://192.0.2.1:9999", // TEST-NET-1 不可达地址
		apiKey:  "key",
	}

	req := FreezeCardRequest{
		ProviderCardRef: "ref",
		IdempotencyKey:  "idem",
	}
	err := adapter.FreezeCard(context.Background(), req)
	require.Error(t, err)
	assert.True(t, IsTimeout(err), "net.Error.Timeout() should map to ErrProviderTimeout")
}

// TestHTTPAdapterFreezeCard_UsesProviderRequestKey 验证 HTTP 请求使用正确的路径和幂等键。
func TestHTTPAdapterFreezeCard_UsesProviderRequestKey(t *testing.T) {
	var receivedPath string
	var receivedIdempotencyKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedIdempotencyKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewHTTPAdapter(server.URL, "key")
	req := FreezeCardRequest{
		ProviderCardRef: "ext-card-ref-xyz",
		IdempotencyKey:  "provider-request-key-001",
	}
	err := adapter.FreezeCard(context.Background(), req)
	require.NoError(t, err)

	assert.Contains(t, receivedPath, "ext-card-ref-xyz")
	assert.Equal(t, "provider-request-key-001", receivedIdempotencyKey)
}

// TestHTTPAdapterFreezeCard_ContextCanceledPreserved 验证 context.Canceled 保留取消语义。
func TestHTTPAdapterFreezeCard_ContextCanceledPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := NewHTTPAdapter(server.URL, "key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	req := FreezeCardRequest{
		ProviderCardRef: "ref",
		IdempotencyKey:  "idem",
	}
	err := adapter.FreezeCard(ctx, req)
	require.Error(t, err)
	// context.Canceled 不应映射为 ErrProviderTimeout
	assert.False(t, IsTimeout(err), "context.Canceled must NOT map to ErrProviderTimeout")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestHTTPAdapterFreezeCard_408IsTimeout 验证 HTTP 408 映射为 ErrProviderTimeout。
func TestHTTPAdapterFreezeCard_408IsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer server.Close()

	adapter := NewHTTPAdapter(server.URL, "key")
	req := FreezeCardRequest{
		ProviderCardRef: "ref",
		IdempotencyKey:  "idem",
	}
	err := adapter.FreezeCard(context.Background(), req)
	require.Error(t, err)
	assert.True(t, IsTimeout(err), "HTTP 408 should map to ErrProviderTimeout")
}

// fakeNetTimeout 合成 net.Error——不依赖真实网络黑洞地址（192.0.2.1 在
// NAT/防火墙环境下可能返回快速非超时错误，导致测试偶发失败）。
type fakeNetTimeout struct{}

func (fakeNetTimeout) Error() string   { return "i/o timeout" }
func (fakeNetTimeout) Timeout() bool   { return true }
func (fakeNetTimeout) Temporary() bool { return false }

var _ net.Error = fakeNetTimeout{}

// TestClassifyHTTPError_NetTimeout 验证 net.Error.Timeout() 被正确分类。
func TestClassifyHTTPError_NetTimeout(t *testing.T) {
	classified := classifyHTTPError(fakeNetTimeout{})
	assert.True(t, IsTimeout(classified), "net.Error.Timeout() should map to ErrProviderTimeout")
}
