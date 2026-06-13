package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type Event struct {
	IdempotencyKey string
	ProviderType   string
	ProviderID     string
	EventType      string
	PayloadHash    string
	IngressID      string
}

type Producer struct {
	rdb *redis.Client
}

func NewProducer(rdb *redis.Client) *Producer {
	return &Producer{rdb: rdb}
}

func (p *Producer) Publish(ctx context.Context, stream string, evt Event) (string, error) {
	values := map[string]any{
		"idempotency_key": evt.IdempotencyKey,
		"provider_type":   evt.ProviderType,
		"provider_id":     evt.ProviderID,
		"event_type":      evt.EventType,
		"payload_hash":    evt.PayloadHash,
		"ingress_id":      evt.IngressID,
	}

	id, err := p.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: values,
	}).Result()
	if err != nil {
		return "", fmt.Errorf("stream publish: %w", err)
	}

	return id, nil
}

type Consumer struct {
	rdb           *redis.Client
	stream        string
	group         string
	consumerID    string
	maxDeliveries int
	handler       func(ctx context.Context, msg redis.XMessage) error
	logger        *slog.Logger
	dlqStream     string
	minIdle       time.Duration
}

type ConsumerConfig struct {
	RDB           *redis.Client
	Stream        string
	Group         string
	ConsumerID    string
	MaxDeliveries int
	Handler       func(ctx context.Context, msg redis.XMessage) error
	Logger        *slog.Logger
	DLQStream     string
	MinIdle       *time.Duration
}

func NewConsumer(cfg ConsumerConfig) *Consumer {
	minIdle := 30 * time.Second
	if cfg.MinIdle != nil {
		minIdle = *cfg.MinIdle
	}
	return &Consumer{
		rdb:           cfg.RDB,
		stream:        cfg.Stream,
		group:         cfg.Group,
		consumerID:    cfg.ConsumerID,
		maxDeliveries: cfg.MaxDeliveries,
		handler:       cfg.Handler,
		logger:        cfg.Logger,
		dlqStream:     cfg.DLQStream,
		minIdle:       minIdle,
	}
}

func (c *Consumer) EnsureGroup(ctx context.Context) error {
	return c.rdb.XGroupCreateMkStream(ctx, c.stream, c.group, "0").Err()
}

func (c *Consumer) Start(ctx context.Context) {
	c.logger.Info("consumer starting", "stream", c.stream, "group", c.group)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("consumer stopping", "stream", c.stream)
			return
		default:
		}

		res, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.group,
			Consumer: c.consumerID,
			Streams:  []string{c.stream, ">"},
			Count:    16,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil {
				continue
			}
			c.logger.Error("xreadgroup failed", "err", err)
			time.Sleep(time.Second)
			continue
		}

		for _, stream := range res {
			for _, msg := range stream.Messages {
				c.processMessage(ctx, msg)
			}
		}
	}
}

func (c *Consumer) processMessage(ctx context.Context, msg redis.XMessage) {
	if err := c.handler(ctx, msg); err != nil {
		c.logger.Error("handler failed, leaving in PEL for retry",
			"msg_id", msg.ID, "err", err)
		// 不 XAck，让消息留在 PEL，由 ReclaimPending 回收重试
		return
	}

	c.rdb.XAck(ctx, c.stream, c.group, msg.ID)
}

func (c *Consumer) sendToDLQ(ctx context.Context, msg redis.XMessage) {
	payload, _ := json.Marshal(map[string]any{
		"original_stream": c.stream,
		"original_id":     msg.ID,
		"values":          msg.Values,
		"reason":          "max_deliveries_exceeded",
		"timestamp":       time.Now().UTC().Format(time.RFC3339),
	})

	c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.dlqStream,
		Values: map[string]any{
			"payload": string(payload),
		},
	})
}

func (c *Consumer) ReclaimPending(ctx context.Context) (int64, error) {
	var totalReclaimed int64
	start := "0-0"

	for {
		msgs, newStart, err := c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   c.stream,
			Group:    c.group,
			MinIdle:  c.minIdle,
			Start:    start,
			Count:    16,
			Consumer: c.consumerID,
		}).Result()
		if err != nil {
			return totalReclaimed, err
		}

		if len(msgs) == 0 {
			break
		}

		// 获取这些消息的 delivery count（不限 consumer）
		pendingExt, err := c.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: c.stream,
			Group:  c.group,
			Start:  msgs[0].ID,
			End:    msgs[len(msgs)-1].ID,
			Count:  int64(len(msgs)),
		}).Result()
		if err != nil {
			return totalReclaimed, err
		}

		// 构建 message ID -> delivery count 的映射
		deliveryCounts := make(map[string]int64)
		for _, pe := range pendingExt {
			deliveryCounts[pe.ID] = pe.RetryCount
		}

		for _, msg := range msgs {
			retryCount := deliveryCounts[msg.ID]
			if retryCount == 0 {
				retryCount = 1 // 至少 1 次
			}

			if int(retryCount) >= c.maxDeliveries {
				c.logger.Warn("max deliveries reached, sending to DLQ",
					"msg_id", msg.ID, "retry_count", retryCount)
				c.sendToDLQ(ctx, msg)
				c.rdb.XAck(ctx, c.stream, c.group, msg.ID)
			} else {
				c.processMessage(ctx, msg)
			}
			totalReclaimed++
		}

		start = newStart
		if start == "0-0" {
			break
		}
	}

	return totalReclaimed, nil
}
