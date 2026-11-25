package vcc_funding

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	issuing "github.com/nexusglobal/mamr/internal/issuing"
)

type Worker interface {
	Start(ctx context.Context) error
	Stop() error
}

type worker struct {
	outboxRepo            OutboxRepository
	cryptoBridge          CryptoBridgeService
	ledgerService         LedgerService
	fundingRouter         FundingRouter
	issuingFundingHandler IssuingFundingHandler
	logger                Logger
	pollInterval          time.Duration
	shutdown              chan struct{}
}

type OutboxRepository interface {
	ClaimPendingEvents(ctx context.Context, batchSize int) ([]OutboxEvent, error)
	MarkEventProcessed(ctx context.Context, eventID uuid.UUID, processedAt time.Time) error
	MarkEventFailed(ctx context.Context, eventID uuid.UUID, errorMsg string, retryCount int) error
	MarkEventUnknown(ctx context.Context, eventID uuid.UUID) error
}

type OutboxEvent struct {
	ID          uuid.UUID
	EventType   string
	Payload     []byte
	Status      string
	RetryCount  int
	CreatedAt   time.Time
	ProcessedAt *time.Time
}

type CryptoBridgeService interface {
	Convert(ctx context.Context, req ConversionRequest) (*ConversionResult, error)
}

type ConversionRequest struct {
	BridgeBatchID string
	MerchantID    uuid.UUID
	AmountMinor   int64
	Currency      string
	SourceTier    string
}

type ConversionResult struct {
	BridgeBatchID     string
	LPCConversionRef  string
	TargetAmountMinor int64
	Status            string
	SlippageBps       int
}

type LedgerService interface {
	CreateCryptoPoolLedgerEntry(ctx context.Context, entry LedgerEntry) error
	TriggerVCCFundingEvent(ctx context.Context, event VCCFundingEvent) error
}

// BridgeFinalizer 是可选接口（P0-A4）：在同一数据库事务中完成
// Ledger Entry 与 Funding Outbox 写入。LocalLedgerService 实现此接口；
// 测试 double 可以只实现 LedgerService，worker 会回退到两个独立调用。
type BridgeFinalizer interface {
	FinalizeCryptoBridge(ctx context.Context, entry LedgerEntry, event VCCFundingEvent) error
}

type LedgerEntry struct {
	ID               uuid.UUID
	BridgeBatchID    string
	MerchantID       uuid.UUID
	AmountMinor      int64
	Currency         string
	EntryType        string
	SourceTier       string
	LPCConversionRef string
	Provenance       map[string]string
	CreatedAt        time.Time
}

type VCCFundingEvent struct {
	BridgeBatchID    string
	MerchantID       uuid.UUID
	TargetProviderID uuid.UUID
	AmountMinor      int64
	Currency         string
	Provenance       map[string]string
}

type FundingRouter interface {
	TriggerVCCFunding(ctx context.Context, req FundingTriggerRequest) error
}

type FundingTriggerRequest struct {
	MerchantID       uuid.UUID
	TargetProviderID uuid.UUID
	AmountMinor      int64
	Currency         string
	Provenance       map[string]string
}

// IssuingFundingHandler 处理 VCC_ISSUING_FUNDING_REQUESTED 事件。
// 调用外部 issuing Provider 为目标卡池入金。
type IssuingFundingHandler interface {
	FundIssuingProvider(ctx context.Context, req IssuingFundingRequest) (*IssuingFundingResult, error)
}

type IssuingFundingRequest struct {
	BridgeBatchID          string
	MerchantID             uuid.UUID
	TargetProviderID       uuid.UUID
	AmountMinor            int64
	Currency               string
	ProviderIdempotencyKey string
}

type IssuingFundingResult struct {
	ProviderRef string
	Status      string // "SUCCESS" | "FAILED"
}

type Logger interface {
	Info(ctx context.Context, message string, fields ...interface{})
	Error(ctx context.Context, message string, err error, fields ...interface{})
}

type WorkerConfig struct {
	PollInterval   time.Duration
	BatchSize      int
	MaxRetries     int
	MaxSlippageBps int
}

// WorkerOption 是 NewWorker 的可选配置函数。
type WorkerOption func(*worker)

// WithIssuingFundingHandler 注入 IssuingFundingHandler。
// 如果不调用此 option，issuingFundingHandler 为 nil，
// VCC_ISSUING_FUNDING_REQUESTED 事件将 fail-closed。
func WithIssuingFundingHandler(h IssuingFundingHandler) WorkerOption {
	return func(w *worker) {
		w.issuingFundingHandler = h
	}
}

func NewWorker(outboxRepo OutboxRepository, cryptoBridge CryptoBridgeService,
	ledgerService LedgerService, fundingRouter FundingRouter, logger Logger, config WorkerConfig, opts ...WorkerOption) Worker {
	w := &worker{
		outboxRepo:    outboxRepo,
		cryptoBridge:  cryptoBridge,
		ledgerService: ledgerService,
		fundingRouter: fundingRouter,
		logger:        logger,
		pollInterval:  config.PollInterval,
		shutdown:      make(chan struct{}),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

var (
	ErrEventProcessingFailed = errors.New("vcc_funding: event processing failed")
	ErrConversionTimeout     = errors.New("vcc_funding: crypto conversion timeout")
	ErrSlippageExceeded      = errors.New("vcc_funding: conversion slippage exceeded threshold")
	ErrUnknownExternalState  = errors.New("vcc_funding: unknown external state after LP call")
)

func (w *worker) Start(ctx context.Context) error {
	w.logger.Info(ctx, "VCC funding worker started")

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := w.processBatch(ctx); err != nil {
				w.logger.Error(ctx, "Failed to process batch", err)
			}
		case <-w.shutdown:
			w.logger.Info(ctx, "VCC funding worker stopping")
			return nil
		case <-ctx.Done():
			w.logger.Info(ctx, "VCC funding worker context cancelled")
			return ctx.Err()
		}
	}
}

func (w *worker) Stop() error {
	close(w.shutdown)
	return nil
}

func (w *worker) processBatch(ctx context.Context) error {
	events, err := w.outboxRepo.ClaimPendingEvents(ctx, 10)
	if err != nil {
		return err
	}

	if len(events) == 0 {
		return nil
	}

	for _, event := range events {
		if err := w.processEvent(ctx, event); err != nil {
			w.logger.Error(ctx, "Failed to process event", err, "event_id", event.ID)
		}
	}

	return nil
}

func (w *worker) processEvent(ctx context.Context, event OutboxEvent) error {
	w.logger.Info(ctx, "Processing VCC event", "event_type", event.EventType)

	switch event.EventType {
	case "VCC_CRYPTO_BRIDGE_REQUESTED":
		return w.processCryptoBridgeEvent(ctx, event)
	case "VCC_FUNDING_REQUESTED":
		return w.processFundingEvent(ctx, event)
	case "VCC_ISSUING_FUNDING_REQUESTED":
		return w.processIssuingFundingEvent(ctx, event)
	default:
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, "unknown event type for vcc_funding worker", event.RetryCount+1)
	}
}

// processCryptoBridgeRequest 处理 VCC_CRYPTO_BRIDGE_REQUESTED 事件。
func (w *worker) processCryptoBridgeEvent(ctx context.Context, event OutboxEvent) error {
	var payload CryptoBridgePayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		w.logger.Error(ctx, "Failed to unmarshal crypto bridge payload", err)
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, "invalid payload", event.RetryCount+1)
	}

	result, err := w.cryptoBridge.Convert(ctx, ConversionRequest{
		BridgeBatchID: payload.BridgeBatchID,
		MerchantID:    payload.MerchantID,
		AmountMinor:   payload.AmountMinor,
		Currency:      payload.Currency,
		SourceTier:    payload.SourceTier,
	})
	if err != nil {
		w.logger.Error(ctx, "Crypto bridge conversion failed", err)
		if errors.Is(err, ErrConversionTimeout) {
			return w.outboxRepo.MarkEventUnknown(ctx, event.ID)
		}
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, err.Error(), event.RetryCount+1)
	}

	if result.SlippageBps > 50 {
		w.logger.Error(ctx, "Slippage exceeded threshold", ErrSlippageExceeded)
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, ErrSlippageExceeded.Error(), event.RetryCount+1)
	}

	w.logger.Info(ctx, "Crypto bridge conversion completed")

	// P0-A4: Ledger entry 主键由稳定业务键 (bridge_batch_id, entry_type) 派生，
	// 不再每次随机生成。重复消费同一 bridge batch 时派生出相同 ID，
	// 配合 DB 唯一约束实现幂等。
	ledgerEntry := LedgerEntry{
		ID:               uuid.NewSHA1(uuid.NameSpaceOID, []byte(result.BridgeBatchID+"|CRYPTO_BRIDGE_COMPLETED")),
		BridgeBatchID:    result.BridgeBatchID,
		MerchantID:       payload.MerchantID,
		AmountMinor:      result.TargetAmountMinor,
		Currency:         payload.Currency,
		EntryType:        "CRYPTO_BRIDGE_COMPLETED",
		SourceTier:       payload.SourceTier,
		LPCConversionRef: result.LPCConversionRef,
		Provenance: map[string]string{
			"source_tier":            payload.SourceTier,
			"crypto_bridge_batch_id": payload.BridgeBatchID,
			"lp_conversion_ref":      result.LPCConversionRef,
		},
		CreatedAt: time.Now(),
	}
	fundingEvent := VCCFundingEvent{
		BridgeBatchID:    result.BridgeBatchID,
		MerchantID:       payload.MerchantID,
		TargetProviderID: payload.TargetProviderID,
		AmountMinor:      result.TargetAmountMinor,
		Currency:         payload.Currency,
		Provenance: map[string]string{
			"source_tier":                payload.SourceTier,
			"crypto_bridge_batch_id":     payload.BridgeBatchID,
			"lp_conversion_ref":          result.LPCConversionRef,
			"target_issuing_provider_id": payload.TargetProviderID.String(),
		},
	}

	// P0-A4: 优先使用同事务 Finalize（Ledger Entry + Funding Outbox 原子写入）。
	// 未实现 BridgeFinalizer 的测试 double 回退到两个独立调用。
	if fin, ok := w.ledgerService.(BridgeFinalizer); ok {
		if err := fin.FinalizeCryptoBridge(ctx, ledgerEntry, fundingEvent); err != nil {
			return w.outboxRepo.MarkEventFailed(ctx, event.ID, err.Error(), event.RetryCount+1)
		}
	} else {
		if err := w.ledgerService.CreateCryptoPoolLedgerEntry(ctx, ledgerEntry); err != nil {
			return w.outboxRepo.MarkEventFailed(ctx, event.ID, err.Error(), event.RetryCount+1)
		}

		if err := w.ledgerService.TriggerVCCFundingEvent(ctx, fundingEvent); err != nil {
			return w.outboxRepo.MarkEventFailed(ctx, event.ID, err.Error(), event.RetryCount+1)
		}
	}

	return w.outboxRepo.MarkEventProcessed(ctx, event.ID, time.Now())
}

// processFundingEvent 处理 VCC_FUNDING_REQUESTED 事件。
// P0-5: VCC_FUNDING_REQUESTED 必须接通真实消费者，不能留空、ACK 或死信。
func (w *worker) processFundingEvent(ctx context.Context, event OutboxEvent) error {
	var payload VCCFundingPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		w.logger.Error(ctx, "Failed to unmarshal VCC funding payload", err)
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, "invalid payload", event.RetryCount+1)
	}

	// P0-8: VCC_FUNDING_REQUESTED 接通 FundingRouter/Issuing Service。
	// 如果 FundingRouter 未注入，返回错误（不 ACK），进入重试/死信。
	if w.fundingRouter == nil {
		w.logger.Error(ctx, "VCC_FUNDING_REQUESTED requires FundingRouter, but none injected", ErrUnsupportedCapability)
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, "funding router not configured", event.RetryCount+1)
	}

	if err := w.fundingRouter.TriggerVCCFunding(ctx, FundingTriggerRequest{
		MerchantID:       payload.MerchantID,
		TargetProviderID: payload.TargetProviderID,
		AmountMinor:      payload.AmountMinor,
		Currency:         payload.Currency,
		Provenance:       payload.Provenance,
	}); err != nil {
		w.logger.Error(ctx, "VCC funding trigger failed", err)
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, err.Error(), event.RetryCount+1)
	}

	w.logger.Info(ctx, "VCC funding triggered successfully")
	return w.outboxRepo.MarkEventProcessed(ctx, event.ID, time.Now())
}

// processIssuingFundingEvent 处理 VCC_ISSUING_FUNDING_REQUESTED 事件。
// 调用外部 issuing Provider 为目标卡池入金。
// - Provider idempotency key = bridge_batch_id + target_provider_id
// - 成功 → MarkEventProcessed + 写 VCC_ISSUING_FUNDING_COMPLETED 审计事件
// - timeout/网络结果未知 → MarkEventUnknown（不自动重试 Provider）
// - provider 明确失败 → MarkEventFailed（保留可对账的失败状态，不退回资金）
func (w *worker) processIssuingFundingEvent(ctx context.Context, event OutboxEvent) error {
	var payload VCCIssuingFundingPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		w.logger.Error(ctx, "Failed to unmarshal VCC issuing funding payload", err)
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, "invalid payload", event.RetryCount+1)
	}

	// Fail-closed：如果 IssuingFundingHandler 未注入，不 ACK。
	if w.issuingFundingHandler == nil {
		w.logger.Error(ctx, "VCC_ISSUING_FUNDING_REQUESTED requires IssuingFundingHandler, but none injected", ErrUnsupportedCapability)
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, "issuing funding handler not configured", event.RetryCount+1)
	}

	// P0-5: 使用 issuing 包的正确错误分类，不用 ErrConversionTimeout
	result, err := w.issuingFundingHandler.FundIssuingProvider(ctx, IssuingFundingRequest{
		BridgeBatchID:          payload.BridgeBatchID,
		MerchantID:             payload.MerchantID,
		TargetProviderID:       payload.TargetProviderID,
		AmountMinor:            payload.AmountMinor,
		Currency:               payload.Currency,
		ProviderIdempotencyKey: payload.BridgeBatchID + ":" + payload.TargetProviderID.String(),
	})
	if err != nil {
		w.logger.Error(ctx, "Issuing funding provider call failed", err)
		// P0-5: 用 issuing.IsTimeout / issuing.ErrUnknownExternalState 判断
		if issuing.IsTimeout(err) || errors.Is(err, issuing.ErrUnknownExternalState) {
			// timeout/网络结果未知 → MarkEventUnknown，不自动重试 Provider
			return w.outboxRepo.MarkEventUnknown(ctx, event.ID)
		}
		// 明确的本地可重试错误才 MarkEventFailed
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, err.Error(), event.RetryCount+1)
	}

	if result.Status == "FAILED" {
		// Provider 明确拒绝 → MarkEventProcessed（不重试 Provider）
		// funding balance 状态已由 DBIssuingFundingHandler 更新为 FAILED
		w.logger.Error(ctx, "Issuing funding provider returned FAILED", errors.New(result.ProviderRef))
		return w.outboxRepo.MarkEventProcessed(ctx, event.ID, time.Now())
	}

	// 成功 → funding balance 已由 DBIssuingFundingHandler 更新为 FUNDED + 写 Completed Outbox
	w.logger.Info(ctx, "Issuing funding completed successfully")
	return w.outboxRepo.MarkEventProcessed(ctx, event.ID, time.Now())
}

type CryptoBridgePayload struct {
	BridgeBatchID    string    `json:"bridge_batch_id"`
	MerchantID       uuid.UUID `json:"merchant_id"`
	AmountMinor      int64     `json:"amount_minor"`
	Currency         string    `json:"currency"`
	SourceTier       string    `json:"source_tier"`
	TargetProviderID uuid.UUID `json:"target_provider_id"`
}

// VCCFundingPayload 是 VCC_FUNDING_REQUESTED 事件的 Outbox payload 结构。
type VCCFundingPayload struct {
	MerchantID       uuid.UUID         `json:"merchant_id"`
	TargetProviderID uuid.UUID         `json:"target_provider_id"`
	AmountMinor      int64             `json:"amount_minor"`
	Currency         string            `json:"currency"`
	Provenance       map[string]string `json:"provenance"`
}

// VCCIssuingFundingPayload 是 VCC_ISSUING_FUNDING_REQUESTED 事件的 Outbox payload 结构。
type VCCIssuingFundingPayload struct {
	BridgeBatchID    string    `json:"bridge_batch_id"`
	MerchantID       uuid.UUID `json:"merchant_id"`
	TargetProviderID uuid.UUID `json:"target_provider_id"`
	AmountMinor      int64     `json:"amount_minor"`
	Currency         string    `json:"currency"`
}
