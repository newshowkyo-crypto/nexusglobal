package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Service 执行单次投递。
type Service struct {
	repo   *Repository
	client *http.Client
	log    *slog.Logger
}

func NewService(repo *Repository, log *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
	}
}

// Deliver 签名并 POST 一条投递。返回 HTTP 状态码与错误。
func (s *Service) Deliver(ctx context.Context, d Delivery) (int, error) {
	secret, url, err := s.repo.endpointSecret(ctx, d.EndpointID, d.MerchantID)
	if err != nil {
		return 0, fmt.Errorf("merchant webhook: resolve endpoint: %w", err)
	}

	ts := time.Now().Unix()
	signature := Sign([]byte(secret), ts, d.Payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(d.Payload))
	if err != nil {
		return 0, fmt.Errorf("merchant webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "NexusGlobal-Webhook/1.0")
	req.Header.Set("X-Nexus-Event", d.EventType)
	req.Header.Set("X-Nexus-Delivery", d.ID.String())
	req.Header.Set("X-Nexus-Timestamp", fmt.Sprintf("%d", ts))
	req.Header.Set("X-Nexus-Signature", "v1="+signature)

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("merchant webhook: post: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("merchant webhook: non-2xx status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// Worker 轮询到期投递并执行。
type Worker struct {
	repo     *Repository
	svc      *Service
	log      *slog.Logger
	interval time.Duration
	batch    int
}

func NewWorker(repo *Repository, log *slog.Logger) *Worker {
	return &Worker{
		repo:     repo,
		svc:      NewService(repo, log),
		log:      log,
		interval: 5 * time.Second,
		batch:    20,
	}
}

// Start 启动后台投递循环，ctx 取消时退出。
func (w *Worker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

// ProcessOnce 执行一轮（便于测试）。
func (w *Worker) ProcessOnce(ctx context.Context) {
	w.processBatch(ctx)
}

func (w *Worker) processBatch(ctx context.Context) {
	deliveries, err := w.repo.ClaimDue(ctx, w.batch)
	if err != nil {
		w.log.Error("merchant webhook: claim due failed", "err", err)
		return
	}
	for _, d := range deliveries {
		statusCode, derr := w.svc.Deliver(ctx, d)
		if derr == nil {
			if err := w.repo.MarkSucceeded(ctx, d.ID, statusCode); err != nil {
				w.log.Error("merchant webhook: mark succeeded failed", "delivery_id", d.ID, "err", err)
			}
			continue
		}

		w.log.Warn("merchant webhook: delivery failed",
			"delivery_id", d.ID, "attempt", d.AttemptCount, "status", statusCode, "err", derr)
		if err := w.repo.MarkFailed(ctx, d.ID, d.AttemptCount, statusCode, derr.Error()); err != nil {
			w.log.Error("merchant webhook: mark failed failed", "delivery_id", d.ID, "err", err)
		}
	}
}
