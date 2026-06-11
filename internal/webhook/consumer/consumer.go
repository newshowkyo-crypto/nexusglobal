package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/platform/idempotency"
	"github.com/nexusglobal/mamr/internal/platform/stream"
)

type LedgerService interface {
	Post(ctx context.Context, cmd LedgerPostCmd) (LedgerPostResult, error)
}

type LedgerPostCmd struct {
	IdempotencyKey  string
	TransactionType string
	AggregateType   string
	AggregateID     string
	Description     string
	Entries         []LedgerEntry
}

type LedgerEntry struct {
	AccountID string
	Direction string
	Amount    string
}

type LedgerPostResult struct {
	TransactionID string
	Replayed      bool
}

type Consumer struct {
	stream   *stream.Consumer
	ledger   LedgerService
	logger   *slog.Logger
}

func New(rdb *redis.Client, ledger LedgerService, logger *slog.Logger) *Consumer {
	c := &Consumer{
		ledger: ledger,
		logger: logger,
	}

	handler := func(ctx context.Context, msg redis.XMessage) error {
		return c.handleMessage(ctx, msg)
	}

	c.stream = stream.NewConsumer(stream.ConsumerConfig{
		RDB:           rdb,
		Stream:        "stream:webhook:lp",
		Group:         "cg-core-lp",
		ConsumerID:    "consumer-1",
		MaxDeliveries: 5,
		Handler:       handler,
		Logger:        logger,
		DLQStream:     "stream:dlq",
	})

	return c
}

func (c *Consumer) Start(ctx context.Context) error {
	if err := c.stream.EnsureGroup(ctx); err != nil {
		return fmt.Errorf("ensure group: %w", err)
	}
	c.stream.Start(ctx)
	return nil
}

func (c *Consumer) handleMessage(ctx context.Context, msg redis.XMessage) error {
	ingressID, _ := msg.Values["ingress_id"].(string)
	idempotencyKey, _ := msg.Values["idempotency_key"].(string)
	eventType, _ := msg.Values["event_type"].(string)

	c.logger.Info("processing webhook event",
		"ingress_id", ingressID,
		"idempotency_key", idempotencyKey,
		"event_type", eventType)

	if idempotencyKey == "" {
		return fmt.Errorf("missing idempotency_key in message %s", msg.ID)
	}

	payloadHash, _ := msg.Values["payload_hash"].(string)
	_ = payloadHash

	switch eventType {
	case "captured", "settled":
		return c.handleFinancialEvent(ctx, msg)
	case "refund", "dispute":
		return c.handleFinancialEvent(ctx, msg)
	case "payout_confirmed", "lp_callback":
		return c.handleFinancialEvent(ctx, msg)
	default:
		c.logger.Warn("unknown event type, skipping",
			"event_type", eventType, "msg_id", msg.ID)
		return nil
	}
}

func (c *Consumer) handleFinancialEvent(ctx context.Context, msg redis.XMessage) error {
	idempotencyKey, _ := msg.Values["idempotency_key"].(string)
	eventType, _ := msg.Values["event_type"].(string)
	providerType, _ := msg.Values["provider_type"].(string)

	cmd := LedgerPostCmd{
		IdempotencyKey:  idempotencyKey,
		TransactionType: eventType,
		AggregateType:   providerType,
		Description:     fmt.Sprintf("webhook %s", eventType),
	}

	result, err := c.ledger.Post(ctx, cmd)
	if err != nil {
		return fmt.Errorf("ledger post: %w", err)
	}

	if result.Replayed {
		c.logger.Info("idempotent replay, skipping",
			"idempotency_key", idempotencyKey)
		return nil
	}

	c.logger.Info("ledger entry created",
		"transaction_id", result.TransactionID,
		"event_type", eventType)

	return nil
}

func PayloadHash(data []byte) string {
	return idempotency.PayloadHash(data)
}

func WebhookIdempotencyKey(providerType, providerID, providerEventID, eventType string) string {
	return idempotency.WebhookIdempotencyKey(providerType, providerID, providerEventID, eventType)
}

func _() {
	var _ = json.Marshal
}
