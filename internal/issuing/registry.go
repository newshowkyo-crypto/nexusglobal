package issuing

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type ProviderRegistry interface {
	RegisterProvider(provider IssuingProvider) error
	DeregisterProvider(providerID uuid.UUID) error
	GetProvider(ctx context.Context, providerID uuid.UUID) (*IssuingProvider, error)
	SelectByTierAndFundingMode(ctx context.Context, entityID *uuid.UUID, tier IssuingTier, fundingMode IssuingFundingMode) ([]IssuingProvider, error)
	GetProviderStatus(providerID uuid.UUID) (ProviderStatus, bool)
	Refresh(ctx context.Context) error
	ListAllProviders(ctx context.Context) ([]IssuingProvider, error)
}

type ChannelRegistry struct {
	mu        sync.RWMutex
	providers map[uuid.UUID]IssuingProvider
	statusMap map[uuid.UUID]ProviderStatus
	repo      ProviderRepository
}

type ProviderRepository interface {
	ListProviders(ctx context.Context, entityID *uuid.UUID) ([]IssuingProvider, error)
	GetProviderByID(ctx context.Context, providerID uuid.UUID) (*IssuingProvider, error)
	ListProvidersByTierAndFundingMode(ctx context.Context, entityID *uuid.UUID, tier IssuingTier, fundingMode IssuingFundingMode) ([]IssuingProvider, error)
}

func NewChannelRegistry(repo ProviderRepository) *ChannelRegistry {
	return &ChannelRegistry{
		providers: make(map[uuid.UUID]IssuingProvider),
		statusMap: make(map[uuid.UUID]ProviderStatus),
		repo:      repo,
	}
}

func (r *ChannelRegistry) RegisterProvider(provider IssuingProvider) error {
	if err := provider.IssuingTier.Validate(); err != nil {
		return err
	}
	if err := provider.FundingMode.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.providers[provider.ID] = provider
	r.statusMap[provider.ID] = ProviderStatus{
		ProviderID:    provider.ID,
		IsActive:      provider.Status == "ACTIVE",
		FundingMode:   provider.FundingMode,
		Version:       provider.Version,
		LastUpdatedAt: provider.UpdatedAt,
	}

	return nil
}

func (r *ChannelRegistry) DeregisterProvider(providerID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.providers, providerID)
	delete(r.statusMap, providerID)

	return nil
}

func (r *ChannelRegistry) GetProvider(ctx context.Context, providerID uuid.UUID) (*IssuingProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providers[providerID]
	if !ok {
		return nil, ErrProviderNotFound
	}

	return &provider, nil
}

func (r *ChannelRegistry) SelectByTierAndFundingMode(ctx context.Context, entityID *uuid.UUID, tier IssuingTier, fundingMode IssuingFundingMode) ([]IssuingProvider, error) {
	if err := tier.Validate(); err != nil {
		return nil, err
	}
	if err := fundingMode.Validate(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var activeProviders []IssuingProvider
	for _, provider := range r.providers {
		if provider.Status != "ACTIVE" {
			continue
		}
		if provider.IssuingTier != tier {
			continue
		}
		if provider.FundingMode != fundingMode {
			continue
		}
		if provider.CredentialVaultKey == "" {
			continue
		}
		if entityID != nil && provider.EntityID != nil && *provider.EntityID != *entityID {
			continue
		}
		activeProviders = append(activeProviders, provider)
	}

	return activeProviders, nil
}

func (r *ChannelRegistry) GetProviderStatus(providerID uuid.UUID) (ProviderStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	status, ok := r.statusMap[providerID]
	return status, ok
}

func (r *ChannelRegistry) Refresh(ctx context.Context) error {
	providers, err := r.repo.ListProviders(ctx, nil)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, provider := range providers {
		r.providers[provider.ID] = provider
		r.statusMap[provider.ID] = ProviderStatus{
			ProviderID:    provider.ID,
			IsActive:      provider.Status == "ACTIVE",
			FundingMode:   provider.FundingMode,
			Version:       provider.Version,
			LastUpdatedAt: provider.UpdatedAt,
		}
	}

	return nil
}

func (r *ChannelRegistry) ListAllProviders(ctx context.Context) ([]IssuingProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providers := make([]IssuingProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		providers = append(providers, provider)
	}

	return providers, nil
}
