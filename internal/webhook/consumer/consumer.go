package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	payoutrepo "github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/idempotency"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/platform/stream"
	"github.com/nexusglobal/mamr/internal/settlement/projection"
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

	payload, err := c.loadPayload(ctx, ingressID, eventType)
	if err != nil {
		// 加载/解密/解析/必填字段验证失败：不 ACK，交给重试；超过 MaxDeliveries 后进入 DLQ。
		c.logger.Warn("webhook payload load failed, will retry/dlq",
			"ingress_id", ingressID, "event_type", eventType, "err_category", categorizeError(err))
		return fmt.Errorf("load payload: %w", err)
	}
	if payload == nil {
		return fmt.Errorf("webhook payload nil for ingress %s", ingressID)
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
		// 未知事件类型：不能 ACK，进入重试/DLQ，避免静默丢失。
		c.logger.Warn("unknown event type, will retry/dlq",
			"event_type", eventType, "msg_id", msg.ID)
		return fmt.Errorf("unknown webhook event type %q for ingress %s", eventType, ingressID)
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

func (c *Consumer) loadPayload(ctx context.Context, ingressID string, eventType string) (*webhookPayload, error) {
	if c.webhookRepo == nil {
		return nil, fmt.Errorf("webhook repository not configured")
	}
	if ingressID == "" {
		return nil, fmt.Errorf("missing ingress_id in message")
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
		return nil, fmt.Errorf("ingress event %s not found", ingressID)
	}

	rawPayload := ingress.RawPayloadCiphertext
	if len(rawPayload) > 0 {
		if c.encryptor == nil {
			return nil, fmt.Errorf("ingress %s has ciphertext but encryptor is not configured", ingressID)
		}
		decrypted, decErr := c.encryptor.Decrypt(ctx, rawPayload, crypto.Meta{
			TenantID:  ingress.ProviderType,
			RecordID:  ingress.ProviderID,
			FieldName: "raw_payload",
		})
		if decErr != nil {
			return nil, fmt.Errorf("decrypt ingress payload: %w", decErr)
		}
		rawPayload = decrypted
	} else {
		return nil, fmt.Errorf("ingress %s has empty ciphertext", ingressID)
	}

	var payload webhookPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload for ingress %s: %w", ingressID, err)
	}

	// P0-6: 按事件类型验证必填字段
	if err := validatePayload(eventType, &payload); err != nil {
		return nil, fmt.Errorf("ingress %s: %w", ingressID, err)
	}

	return &payload, nil
}

// validatePayload 按事件类型验证必填字段，可在测试中独立调用。
func validatePayload(eventType string, payload *webhookPayload) error {
	if payload.MerchantID == "" {
		return fmt.Errorf("missing required field merchant_id")
	}
	switch eventType {
	case "captured", "settled", "refund", "dispute":
		if payload.Amount == "" {
			return fmt.Errorf("missing required field amount for %s event", eventType)
		}
		if payload.Currency == "" {
			return fmt.Errorf("missing required field currency for %s event", eventType)
		}
	case "payout_confirmed", "lp_callback":
		if payload.PayoutRequestID == "" {
			return fmt.Errorf("missing required field payout_request_id for %s event", eventType)
		}
	}
	return nil
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

	// SEC-005: 交易必须携带成对平衡分录（RED-13），禁止无分录入账。
	debitAccID, err := c.ledgerRepo.GetOrCreateAccount(ctx, tx, merchantID, debitAccountType, currency)
	if err != nil {
		return fmt.Errorf("get/create debit account %s: %w", debitAccountType, err)
	}
	creditAccID, err := c.ledgerRepo.GetOrCreateAccount(ctx, tx, merchantID, creditAccountType, currency)
	if err != nil {
		return fmt.Errorf("get/create credit account %s: %w", creditAccountType, err)
	}

	_, err = c.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
		IdempotencyKey:  idempotencyKey,
		TransactionType: ledgerdomain.TransactionType(eventType),
		AggregateType:   "merchant",
		AggregateID:     merchantID.String(),
		Description:     fmt.Sprintf("Webhook %s %s %s", eventType, amount.String(), currency),
		Amount:          amount,
		Entries: []ledgerdomain.Entry{
			{AccountID: debitAccID, Direction: ledgerdomain.DirectionDebit, Amount: amount},
			{AccountID: creditAccID, Direction: ledgerdomain.DirectionCredit, Amount: amount},
		},
	})
	if err != nil {
		return fmt.Errorf("ledger post: %w", err)
	}

	// P0 4.5：投影唯一写入口
	minor := money.ToMinor(amount).Int64()
	switch eventType {
	case "CAPTURED":
		err = projection.AddPendingTx(ctx, tx, merchantID, currency, minor)
	case "SETTLED":
		err = projection.MovePendingToSettledTx(ctx, tx, merchantID, currency, minor)
	case "REFUND":
		err = projection.RemoveSettledTx(ctx, tx, merchantID, currency, minor)
	case "DISPUTE":
		err = projection.MoveSettledToDisputeTx(ctx, tx, merchantID, currency, minor)
	default:
		err = fmt.Errorf("unknown financial event type %q", eventType)
	}
	if err != nil {
		return fmt.Errorf("update projection: %w", err)
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

// categorizeError 将错误映射为安全分类，避免日志泄露密文、明文 payload 或敏感细节。
func categorizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "encryptor is not configured") ||
		strings.Contains(msg, "repository not configured") ||
		strings.Contains(msg, "no confirmer configured"):
		return "config_error"
	case strings.Contains(msg, "not found") ||
		strings.Contains(msg, "missing ingress_id"):
		return "not_found"
	case strings.Contains(msg, "decrypt") ||
		strings.Contains(msg, "ciphertext"):
		return "decrypt_error"
	case strings.Contains(msg, "unmarshal") ||
		strings.Contains(msg, "invalid json") ||
		strings.Contains(msg, "JSON"):
		return "parse_error"
	case strings.Contains(msg, "missing required field") ||
		strings.Contains(msg, "invalid merchant_id") ||
		strings.Contains(msg, "invalid amount") ||
		strings.Contains(msg, "invalid payout_request_id") ||
		strings.Contains(msg, "amount must be positive"):
		return "validation_error"
	default:
		return "internal_error"
	}
}
