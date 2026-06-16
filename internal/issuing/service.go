package issuing

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	registry     ProviderRegistry
	factory      IssuingAdapterFactory
	fraudGuard   FraudGuard
	vaultService VaultService
	cardRepo     CardRepository
	providerRepo ProviderRepository
}

type VaultService interface {
	Decrypt(ctx context.Context, vaultKey string) (map[string]string, error)
}

type CardRepository interface {
	Create(ctx context.Context, card VirtualCard) error
	GetByID(ctx context.Context, cardID uuid.UUID) (*VirtualCard, error)
	UpdateStatus(ctx context.Context, cardID uuid.UUID, status VirtualCardStatus, reason string) error
	GetByIdempotencyKey(ctx context.Context, merchantID uuid.UUID, idempotencyKey string) (*VirtualCard, error)
}

type ServiceConfig struct {
	MaxDeclines    int
	WindowDuration time.Duration
	MaxFraudBps    int
}

func NewService(registry ProviderRegistry, factory IssuingAdapterFactory, fraudGuard FraudGuard,
	vaultService VaultService, cardRepo CardRepository, providerRepo ProviderRepository) *Service {
	return &Service{
		registry:     registry,
		factory:      factory,
		fraudGuard:   fraudGuard,
		vaultService: vaultService,
		cardRepo:     cardRepo,
		providerRepo: providerRepo,
	}
}

func (s *Service) CreateCard(ctx context.Context, req CreateCardRequest) (*CreateCardResult, error) {
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = generateIdempotencyKey()
	}

	existingCard, err := s.cardRepo.GetByIdempotencyKey(ctx, req.MerchantID, req.IdempotencyKey)
	if err == nil && existingCard != nil {
		return &CreateCardResult{
			CardID:          existingCard.ID,
			ProviderCardRef: existingCard.ProviderCardRef,
			Last4:           existingCard.Last4,
			Status:          existingCard.Status,
			CreatedAt:       existingCard.CreatedAt,
		}, nil
	}

	if err := s.fraudGuard.CheckMerchantIssuingAllowed(ctx, req.MerchantID); err != nil {
		return nil, err
	}

	if err := s.fraudGuard.CheckMerchantFraudRate(ctx, req.MerchantID); err != nil {
		return nil, err
	}

	provider, err := s.registry.GetProvider(ctx, req.ProviderID)
	if err != nil {
		return nil, err
	}

	if provider.Status != "ACTIVE" {
		return nil, ErrProviderNotActive
	}

	if provider.CredentialVaultKey == "" {
		return nil, ErrCredentialVaultKeyEmpty
	}

	if err := provider.IssuingTier.Validate(); err != nil {
		return nil, err
	}

	if err := provider.FundingMode.Validate(); err != nil {
		return nil, err
	}

	credential, err := s.vaultService.Decrypt(ctx, provider.CredentialVaultKey)
	if err != nil {
		return nil, ErrCredentialDecryptFailed
	}

	adapter, err := s.factory.Create(ctx, provider.ProviderCode, credential)
	if err != nil {
		return nil, ErrProviderCodeUnregistered
	}

	result, err := adapter.CreateCard(ctx, req)
	if err != nil {
		if IsTimeout(err) {
			card := VirtualCard{
				ID:                         req.MerchantID,
				MerchantID:                 req.MerchantID,
				ProviderID:                 req.ProviderID,
				ProviderCardRef:            result.ProviderCardRef,
				Last4:                      result.Last4,
				Status:                     CardStatusUnknownExternalState,
				Currency:                   req.Currency,
				SpendingLimitMinor:         req.SpendingLimitMinor,
				DeclinedCountSlidingWindow: 0,
				FraudRateBps:               0,
				IdempotencyKey:             req.IdempotencyKey,
				PayloadHash:                req.PayloadHash,
				CreatedBy:                  req.CreatedBy,
				CreatedAt:                  time.Now(),
				UpdatedAt:                  time.Now(),
			}
			_ = s.cardRepo.Create(ctx, card)
			return nil, ErrUnknownExternalState
		}
		return nil, err
	}

	card := VirtualCard{
		ID:                         result.CardID,
		MerchantID:                 req.MerchantID,
		ProviderID:                 req.ProviderID,
		ProviderCardRef:            result.ProviderCardRef,
		Last4:                      result.Last4,
		Status:                     result.Status,
		Currency:                   req.Currency,
		SpendingLimitMinor:         req.SpendingLimitMinor,
		DeclinedCountSlidingWindow: 0,
		FraudRateBps:               0,
		IdempotencyKey:             req.IdempotencyKey,
		PayloadHash:                req.PayloadHash,
		CreatedBy:                  req.CreatedBy,
		CreatedAt:                  result.CreatedAt,
		UpdatedAt:                  result.CreatedAt,
	}

	if err := s.cardRepo.Create(ctx, card); err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Service) FreezeCard(ctx context.Context, cardID uuid.UUID, reason string) error {
	card, err := s.cardRepo.GetByID(ctx, cardID)
	if err != nil {
		return err
	}

	if card.Status == CardStatusTerminated {
		return ErrCardNotFound
	}

	provider, err := s.registry.GetProvider(ctx, card.ProviderID)
	if err != nil {
		return err
	}

	if provider.Status != "ACTIVE" {
		return ErrProviderNotActive
	}

	credential, err := s.vaultService.Decrypt(ctx, provider.CredentialVaultKey)
	if err != nil {
		return ErrCredentialDecryptFailed
	}

	adapter, err := s.factory.Create(ctx, provider.ProviderCode, credential)
	if err != nil {
		return ErrProviderCodeUnregistered
	}

	req := FreezeCardRequest{
		CardID:       cardID,
		ProviderID:   card.ProviderID,
		FrozenReason: reason,
	}

	if err := adapter.FreezeCard(ctx, req); err != nil {
		if IsTimeout(err) {
			_ = s.cardRepo.UpdateStatus(ctx, cardID, CardStatusUnknownExternalState, "provider timeout")
			return ErrUnknownExternalState
		}
		return err
	}

	return s.cardRepo.UpdateStatus(ctx, cardID, CardStatusFrozen, reason)
}

func (s *Service) TerminateCard(ctx context.Context, cardID uuid.UUID, reason string) error {
	card, err := s.cardRepo.GetByID(ctx, cardID)
	if err != nil {
		return err
	}

	if card.Status == CardStatusTerminated {
		return nil
	}

	provider, err := s.registry.GetProvider(ctx, card.ProviderID)
	if err != nil {
		return err
	}

	if provider.Status != "ACTIVE" {
		return ErrProviderNotActive
	}

	credential, err := s.vaultService.Decrypt(ctx, provider.CredentialVaultKey)
	if err != nil {
		return ErrCredentialDecryptFailed
	}

	adapter, err := s.factory.Create(ctx, provider.ProviderCode, credential)
	if err != nil {
		return ErrProviderCodeUnregistered
	}

	req := TerminateCardRequest{
		CardID:           cardID,
		ProviderID:       card.ProviderID,
		TerminatedReason: reason,
	}

	if err := adapter.TerminateCard(ctx, req); err != nil {
		if IsTimeout(err) {
			_ = s.cardRepo.UpdateStatus(ctx, cardID, CardStatusUnknownExternalState, "provider timeout")
			return ErrUnknownExternalState
		}
		return err
	}

	return s.cardRepo.UpdateStatus(ctx, cardID, CardStatusTerminated, reason)
}

func (s *Service) ProcessAuthWebhook(ctx context.Context, req ProcessAuthWebhookRequest) (*CardAuthEvent, error) {
	provider, err := s.registry.GetProvider(ctx, req.ProviderID)
	if err != nil {
		return nil, err
	}

	if provider.Status != "ACTIVE" {
		return nil, ErrProviderNotActive
	}

	credential, err := s.vaultService.Decrypt(ctx, provider.CredentialVaultKey)
	if err != nil {
		return nil, ErrCredentialDecryptFailed
	}

	adapter, err := s.factory.Create(ctx, provider.ProviderCode, credential)
	if err != nil {
		return nil, ErrProviderCodeUnregistered
	}

	event, err := adapter.ProcessAuthWebhook(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := s.fraudGuard.RecordAuthEvent(ctx, *event); err != nil {
		return nil, err
	}

	if event.IsDeclined {
		if err := s.fraudGuard.CheckCardDeclineThreshold(ctx, event.CardID); err != nil {
			_ = s.cardRepo.UpdateStatus(ctx, event.CardID, CardStatusFrozen, "decline threshold exceeded")
			return event, err
		}
	}

	return event, nil
}

func (s *Service) SelectProviderByTierAndFundingMode(ctx context.Context, entityID *uuid.UUID, tier IssuingTier, fundingMode IssuingFundingMode) (*IssuingProvider, error) {
	providers, err := s.registry.SelectByTierAndFundingMode(ctx, entityID, tier, fundingMode)
	if err != nil {
		return nil, err
	}

	if len(providers) == 0 {
		return nil, ErrProviderNotFound
	}

	return &providers[0], nil
}

func generateIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
