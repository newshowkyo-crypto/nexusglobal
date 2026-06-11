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
}

func NewConsumer(cfg ConsumerConfig) *Consumer {
	return &Consumer{
		rdb:           cfg.RDB,
		stream:        cfg.Stream,
		group:         cfg.Group,
		consumerID:    cfg.ConsumerID,
		maxDeliveries: cfg.MaxDeliveries,
		handler:       cfg.Handler,
		logger:        cfg.Logger,
		dlqStream:     cfg.DLQStream,
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
	deliveries := c.getDeliveryCount(msg)

	if deliveries >= c.maxDeliveries {
		c.logger.Warn("max deliveries reached, sending to DLQ",
			"msg_id", msg.ID, "deliveries", deliveries)
		c.sendToDLQ(ctx, msg)
		c.rdb.XAck(ctx, c.stream, c.group, msg.ID)
		return
	}

	if err := c.handler(ctx, msg); err != nil {
		c.logger.Error("handler failed", "msg_id", msg.ID, "err", err)
		return
	}

	c.rdb.XAck(ctx, c.stream, c.group, msg.ID)
}

func (c *Consumer) getDeliveryCount(msg redis.XMessage) int {
	if v, ok := msg.Values["delivery_count"]; ok {
		if n, ok := v.(int64); ok {
			return int(n)
		}
	}
	return 1
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
	msgs, _, err := c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   c.stream,
		Group:    c.group,
		MinIdle:  30 * time.Second,
		Start:    "0-0",
		Count:    16,
		Consumer: c.consumerID,
	}).Result()
	if err != nil {
		return 0, err
	}

	for _, msg := range msgs {
		c.processMessage(ctx, msg)
	}

	return int64(len(msgs)), nil
}
