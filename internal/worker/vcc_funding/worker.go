package vcc_funding

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

type Worker interface {
	Start(ctx context.Context) error
	Stop() error
}

type worker struct {
	outboxRepo    OutboxRepository
	cryptoBridge  CryptoBridgeService
	ledgerService LedgerService
	fundingRouter FundingRouter
	logger        Logger
	pollInterval  time.Duration
	shutdown      chan struct{}
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

func NewWorker(outboxRepo OutboxRepository, cryptoBridge CryptoBridgeService,
	ledgerService LedgerService, logger Logger, config WorkerConfig) Worker {
	return &worker{
		outboxRepo:    outboxRepo,
		cryptoBridge:  cryptoBridge,
		ledgerService: ledgerService,
		logger:        logger,
		pollInterval:  config.PollInterval,
		shutdown:      make(chan struct{}),
	}
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
	w.logger.Info(ctx, "Processing crypto bridge event")

	if event.EventType != "VCC_CRYPTO_BRIDGE_REQUESTED" {
		return w.outboxRepo.MarkEventProcessed(ctx, event.ID, time.Now())
	}

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

	if err := w.ledgerService.CreateCryptoPoolLedgerEntry(ctx, LedgerEntry{
		ID:               uuid.New(),
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
	}); err != nil {
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, err.Error(), event.RetryCount+1)
	}

	if err := w.ledgerService.TriggerVCCFundingEvent(ctx, VCCFundingEvent{
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
	}); err != nil {
		return w.outboxRepo.MarkEventFailed(ctx, event.ID, err.Error(), event.RetryCount+1)
	}

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
