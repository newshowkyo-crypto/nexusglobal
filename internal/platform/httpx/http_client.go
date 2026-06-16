package httpx

import (
	"net/http"
	"sync"
	"time"
)

// 优化的 HTTP Transport 配置
const (
	// MaxIdleConns 全局最大空闲连接数
	MaxIdleConns = 1000
	// MaxIdleConnsPerHost 每个 host 最大空闲连接数
	MaxIdleConnsPerHost = 100
	// IdleConnTimeout 空闲连接超时
	IdleConnTimeout = 90 * time.Second
	// TLSHandshakeTimeout TLS 握手超时
	TLSHandshakeTimeout = 10 * time.Second
	// ResponseHeaderTimeout 响应头超时（防御卡网抖动）
	ResponseHeaderTimeout = 30 * time.Second
)

// DefaultTransport 共享的默认 Transport
// 严禁使用 http.DefaultClient，所有外部请求必须使用此 Transport
var DefaultTransport http.RoundTripper = &http.Transport{
	MaxIdleConns:          MaxIdleConns,
	MaxIdleConnsPerHost:   MaxIdleConnsPerHost,
	IdleConnTimeout:       IdleConnTimeout,
	TLSHandshakeTimeout:   TLSHandshakeTimeout,
	ResponseHeaderTimeout: ResponseHeaderTimeout,
	DisableKeepAlives:     false,
	ForceAttemptHTTP2:     true,
}

// 全局 http.Client 实例池
var (
	defaultClient     *http.Client
	defaultClientOnce sync.Once
)

// DefaultClient 返回共享的高性能 http.Client
// 适用于所有外部 API 调用（Stripe/Nxtapay/LP/Bank等）
func DefaultClient() *http.Client {
	defaultClientOnce.Do(func() {
		defaultClient = &http.Client{
			Transport: DefaultTransport,
			// 不设置 Timeout，让 Transport 的各阶段超时控制
		}
	})
	return defaultClient
}

// NewOptimizedClient 创建自定义优化的 http.Client
// timeout: 请求总超时时间
func NewOptimizedClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:          MaxIdleConns,
			MaxIdleConnsPerHost:   MaxIdleConnsPerHost,
			IdleConnTimeout:       IdleConnTimeout,
			TLSHandshakeTimeout:   TLSHandshakeTimeout,
			ResponseHeaderTimeout: ResponseHeaderTimeout,
			DisableKeepAlives:     false,
			ForceAttemptHTTP2:     true,
		},
		Timeout: timeout,
	}
}
