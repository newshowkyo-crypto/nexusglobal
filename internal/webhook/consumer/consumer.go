package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/idempotency"
	"github.com/nexusglobal/mamr/internal/platform/stream"
	webhookrepo "github.com/nexusglobal/mamr/internal/webhook/repository"
)

type PayoutConfirmer interface {
	Confirm(ctx context.Context, payoutID uuid.UUID, providerRef string) error
}

type Consumer struct {
	stream      *stream.Consumer
	webhookRepo *webhookrepo.Repository
	payoutRepo  *payoutrepo.Repository
	ledgerRepo  *payoutrepo.LedgerRepo
	encryptor   *crypto.Encryptor
	confirmer   PayoutConfirmer
	rdb         *redis.Client
	logger      *slog.Logger
}

type Config struct {
	RDB           *redis.Client
	WebhookRepo   *webhookrepo.Repository
	PayoutRepo    *payoutrepo.Repository
	LedgerRepo    *payoutrepo.LedgerRepo
	Encryptor     *crypto.Encryptor
	Confirmer     PayoutConfirmer
	Logger        *slog.Logger
	StreamName    string
	GroupName     string
	ConsumerID    string
	MaxDeliveries int
	MinIdle       *time.Duration
}

func New(cfg Config) *Consumer {
	c := &Consumer{
		webhookRepo: cfg.WebhookRepo,
		payoutRepo:  cfg.PayoutRepo,
		ledgerRepo:  cfg.LedgerRepo,
		encryptor:   cfg.Encryptor,
		confirmer:   cfg.Confirmer,
		rdb:         cfg.RDB,
		logger:      cfg.Logger,
	}

	handler := func(ctx context.Context, msg redis.XMessage) error {
		return c.handleMessage(ctx, msg)
	}

	maxDeliveries := cfg.MaxDeliveries
	if maxDeliveries == 0 {
		maxDeliveries = 5
	}

	c.stream = stream.NewConsumer(stream.ConsumerConfig{
		RDB:           cfg.RDB,
		Stream:        cfg.StreamName,
		Group:         cfg.GroupName,
		ConsumerID:    cfg.ConsumerID,
		MaxDeliveries: maxDeliveries,
		Handler:       handler,
		Logger:        cfg.Logger,
		DLQStream:     "stream:dlq",
		MinIdle:       cfg.MinIdle,
	})

	return c
}

// NewLPConsumer creates a consumer for LP webhook stream
func NewLPConsumer(
	rdb *redis.Client,
	webhookRepo *webhookrepo.Repository,
	payoutRepo *payoutrepo.Repository,
	ledgerRepo *payoutrepo.LedgerRepo,
	encryptor *crypto.Encryptor,
	confirmer PayoutConfirmer,
	logger *slog.Logger,
) *Consumer {
	return New(Config{
		RDB:           rdb,
		WebhookRepo:   webhookRepo,
		PayoutRepo:    payoutRepo,
		LedgerRepo:    ledgerRepo,
		Encryptor:     encryptor,
		Confirmer:     confirmer,
		Logger:        logger,
		StreamName:    "stream:webhook:lp",
		GroupName:     "cg-core-lp",
		ConsumerID:    "consumer-lp-1",
		MaxDeliveries: 5,
	})
}

// NewMAEConsumer creates a consumer for MAE webhook stream
func NewMAEConsumer(
	rdb *redis.Client,
	webhookRepo *webhookrepo.Repository,
	payoutRepo *payoutrepo.Repository,
	ledgerRepo *payoutrepo.LedgerRepo,
	encryptor *crypto.Encryptor,
	logger *slog.Logger,
) *Consumer {
	return New(Config{
		RDB:           rdb,
		WebhookRepo:   webhookRepo,
		PayoutRepo:    payoutRepo,
		LedgerRepo:    ledgerRepo,
		Encryptor:     encryptor,
		Logger:        logger,
		StreamName:    "stream:webhook:mae",
		GroupName:     "cg-core-mae",
		ConsumerID:    "consumer-mae-1",
		MaxDeliveries: 5,
	})
}

// NewBankConsumer creates a consumer for Bank webhook stream
func NewBankConsumer(
	rdb *redis.Client,
	webhookRepo *webhookrepo.Repository,
	payoutRepo *payoutrepo.Repository,
	ledgerRepo *payoutrepo.LedgerRepo,
	encryptor *crypto.Encryptor,
	logger *slog.Logger,
) *Consumer {
	return New(Config{
		RDB:           rdb,
		WebhookRepo:   webhookRepo,
		PayoutRepo:    payoutRepo,
		LedgerRepo:    ledgerRepo,
		Encryptor:     encryptor,
		Logger:        logger,
		StreamName:    "stream:webhook:bank",
		GroupName:     "cg-core-bank",
		ConsumerID:    "consumer-bank-1",
		MaxDeliveries: 5,
	})
}

func (c *Consumer) Start(ctx context.Context) error {
	if err := c.stream.EnsureGroup(ctx); err != nil {
		return fmt.Errorf("ensure group: %w", err)
	}
	c.stream.Start(ctx)
	return nil
}

func (c *Consumer) ReclaimPending(ctx context.Context) (int64, error) {
	return c.stream.ReclaimPending(ctx)
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

	// T2-3: L1 幂等加速层 - Redis 快速否决
	if c.rdb != nil {
		l1Key := "idempotency:webhook:" + idempotencyKey
		exists, err := c.rdb.Exists(ctx, l1Key).Result()
		if err == nil && exists > 0 {
			c.logger.Info("L1 idempotency hit, skipping",
				"idempotency_key", idempotencyKey)
			return nil
		}
	}

	payload, err := c.loadPayload(ctx, ingressID)
	if err != nil {
		return fmt.Errorf("load payload: %w", err)
	}
	if payload == nil {
		c.logger.Warn("no payload found, skipping", "ingress_id", ingressID)
		return nil
	}

	var result error
	switch eventType {
	case "captured":
		result = c.handleCaptured(ctx, idempotencyKey, payload)
	case "settled":
		result = c.handleSettled(ctx, idempotencyKey, payload)
	case "refund":
		result = c.handleRefund(ctx, idempotencyKey, payload)
	case "dispute":
		result = c.handleDispute(ctx, idempotencyKey, payload)
	case "payout_confirmed", "lp_callback":
		result = c.handlePayoutConfirmed(ctx, payload)
	default:
		c.logger.Warn("unknown event type, skipping",
			"event_type", eventType, "msg_id", msg.ID)
		return nil
	}

	// T2-3: 处理成功后设置 L1 缓存
	if result == nil && c.rdb != nil {
		l1Key := "idempotency:webhook:" + idempotencyKey
		c.rdb.Set(ctx, l1Key, "1", 24*time.Hour)
	}

	return result
}

type webhookPayload struct {
	MerchantID      string `json:"merchant_id"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	ProviderEventID string `json:"provider_event_id"`
	PayoutRequestID string `json:"payout_request_id"`
}

func (c *Consumer) loadPayload(ctx context.Context, ingressID string) (*webhookPayload, error) {
	if c.webhookRepo == nil || ingressID == "" {
		return nil, nil
	}

	id, err := uuid.Parse(ingressID)
	if err != nil {
		return nil, fmt.Errorf("parse ingress id: %w", err)
	}

	ingress, err := c.webhookRepo.GetIngressEvent(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get ingress event: %w", err)
	}
	if ingress == nil {
		return nil, nil
	}

	rawPayload := ingress.RawPayloadCiphertext
	if c.encryptor != nil && len(rawPayload) > 0 {
		decrypted, decErr := c.encryptor.Decrypt(ctx, rawPayload, crypto.Meta{
			TenantID:  ingress.ProviderType,
			RecordID:  ingress.ProviderID,
			FieldName: "raw_payload",
		})
		if decErr == nil {
			rawPayload = decrypted
		}
	}

	var payload webhookPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return nil, nil
	}
	return &payload, nil
}

func (c *Consumer) handleCaptured(ctx context.Context, idempotencyKey string, payload *webhookPayload) error {
	merchantID, err := uuid.Parse(payload.MerchantID)
	if err != nil {
		return fmt.Errorf("invalid merchant_id: %w", err)
	}
	amount, err := decimal.NewFromString(payload.Amount)
	if err != nil {
		return fmt.Errorf("invalid amount: %w", err)
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return fmt.Errorf("amount must be positive")
	}

	return c.postFinancialEntry(ctx, merchantID, amount, payload.Currency,
		idempotencyKey, "PENDING", "CLEARING", "CAPTURED")
}

func (c *Consumer) handleSettled(ctx context.Context, idempotencyKey string, payload *webhookPayload) error {
	merchantID, err := uuid.Parse(payload.MerchantID)
	if err != nil {
		return fmt.Errorf("invalid merchant_id: %w", err)
	}
	amount, err := decimal.NewFromString(payload.Amount)
	if err != nil {
		return fmt.Errorf("invalid amount: %w", err)
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return fmt.Errorf("amount must be positive")
	}

	return c.postFinancialEntry(ctx, merchantID, amount, payload.Currency,
		idempotencyKey, "SETTLED", "PENDING", "SETTLED")
}

func (c *Consumer) handleRefund(ctx context.Context, idempotencyKey string, payload *webhookPayload) error {
	merchantID, err := uuid.Parse(payload.MerchantID)
	if err != nil {
		return fmt.Errorf("invalid merchant_id: %w", err)
	}
	amount, err := decimal.NewFromString(payload.Amount)
	if err != nil {
		return fmt.Errorf("invalid amount: %w", err)
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return fmt.Errorf("amount must be positive")
	}

	return c.postFinancialEntry(ctx, merchantID, amount, payload.Currency,
		idempotencyKey, "SETTLED", "DISPUTE_HOLD", "REFUND")
}

func (c *Consumer) handleDispute(ctx context.Context, idempotencyKey string, payload *webhookPayload) error {
	merchantID, err := uuid.Parse(payload.MerchantID)
	if err != nil {
		return fmt.Errorf("invalid merchant_id: %w", err)
	}
	amount, err := decimal.NewFromString(payload.Amount)
	if err != nil {
		return fmt.Errorf("invalid amount: %w", err)
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return fmt.Errorf("amount must be positive")
	}

	return c.postFinancialEntry(ctx, merchantID, amount, payload.Currency,
		idempotencyKey, "DISPUTE_HOLD", "SETTLED", "DISPUTE")
}

func (c *Consumer) handlePayoutConfirmed(ctx context.Context, payload *webhookPayload) error {
	if payload.PayoutRequestID == "" {
		return fmt.Errorf("missing payout_request_id for payout_confirmed event")
	}
	payoutID, err := uuid.Parse(payload.PayoutRequestID)
	if err != nil {
		return fmt.Errorf("invalid payout_request_id: %w", err)
	}
	if c.confirmer == nil {
		return fmt.Errorf("no confirmer configured")
	}
	return c.confirmer.Confirm(ctx, payoutID, payload.ProviderEventID)
}

func (c *Consumer) postFinancialEntry(
	ctx context.Context,
	merchantID uuid.UUID,
	amount decimal.Decimal,
	currency string,
	idempotencyKey string,
	debitAccountType string,
	creditAccountType string,
	eventType string,
) error {
	existing, err := c.ledgerRepo.GetTransactionByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return fmt.Errorf("idempotency check: %w", err)
	}
	if existing != nil {
		c.logger.Info("idempotent replay, skipping", "idempotency_key", idempotencyKey)
		return nil
	}

	pool := c.payoutRepo.Pool()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	bal, err := c.payoutRepo.LockBalanceForUpdate(ctx, tx, merchantID, currency)
	if err != nil {
		return fmt.Errorf("lock balance: %w", err)
	}
	if bal == nil {
		return fmt.Errorf("no treasury ledger for merchant %s currency %s", merchantID, currency)
	}

	debitAccID, err := c.ledgerRepo.GetOrCreateAccount(ctx, tx, merchantID, debitAccountType, currency)
	if err != nil {
		return fmt.Errorf("get debit account: %w", err)
	}
	creditAccID, err := c.ledgerRepo.GetOrCreateAccount(ctx, tx, merchantID, creditAccountType, currency)
	if err != nil {
		return fmt.Errorf("get credit account: %w", err)
	}

	_, err = c.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
		IdempotencyKey:  idempotencyKey,
		TransactionType: ledgerdomain.TransactionType(eventType),
		AggregateType:   "merchant",
		AggregateID:     merchantID.String(),
		Description:     fmt.Sprintf("Webhook %s %s %s", eventType, amount.String(), currency),
		Entries: []ledgerdomain.Entry{
			{AccountID: debitAccID.String(), Direction: ledgerdomain.DirectionDebit, Amount: amount},
			{AccountID: creditAccID.String(), Direction: ledgerdomain.DirectionCredit, Amount: amount},
		},
	})
	if err != nil {
		return fmt.Errorf("ledger post: %w", err)
	}

	switch eventType {
	case "CAPTURED":
		bal.PendingBalance = bal.PendingBalance.Add(amount)
	case "SETTLED":
		bal.PendingBalance = bal.PendingBalance.Sub(amount)
		bal.SettledBalance = bal.SettledBalance.Add(amount)
	case "REFUND":
		bal.SettledBalance = bal.SettledBalance.Sub(amount)
	case "DISPUTE":
		bal.SettledBalance = bal.SettledBalance.Sub(amount)
		bal.DisputeHoldBalance = bal.DisputeHoldBalance.Add(amount)
	}

	bal.AvailablePayoutBalance = bal.SettledBalance.
		Sub(bal.ReserveBalance).
		Sub(bal.FrozenPayoutBalance).
		Sub(bal.DisputeHoldBalance)

	if err := c.payoutRepo.UpdateBalance(ctx, tx, bal); err != nil {
		return fmt.Errorf("update balance: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	c.logger.Info("accounting entry created",
		"event_type", eventType,
		"merchant_id", merchantID,
		"amount", amount.String(),
		"currency", currency)

	return nil
}

func PayloadHash(data []byte) string {
	return idempotency.PayloadHash(data)
}

func WebhookIdempotencyKey(providerType, providerID, providerEventID, eventType string) string {
	return idempotency.WebhookIdempotencyKey(providerType, providerID, providerEventID, eventType)
}
