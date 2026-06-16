package acquirer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/acquirer/registry"
	"github.com/nexusglobal/mamr/internal/acquirer/repository"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

type Service struct {
	repo      *repository.Repository
	registry  *registry.ChannelRegistry
	encryptor *crypto.Encryptor
	logger    *slog.Logger
}

func NewService(repo *repository.Repository, reg *registry.ChannelRegistry, enc *crypto.Encryptor, log *slog.Logger) *Service {
	return &Service{
		repo:      repo,
		registry:  reg,
		encryptor: enc,
		logger:    log,
	}
}

func (s *Service) ListProviders(ctx context.Context) ([]domain.PaymentProvider, error) {
	return s.repo.ListAllPaymentProviders(ctx)
}

func (s *Service) CreateProvider(ctx context.Context, req *domain.CreateProviderRequest) (*domain.PaymentProvider, error) {
	provider := &domain.PaymentProvider{
		ID:           uuid.New(),
		ProviderCode: req.ProviderCode,
		ProviderName: req.Name,
		ProviderType: domain.ProviderType(req.ProviderType),
		IsActive:     true,
	}

	if err := s.repo.CreatePaymentProvider(ctx, provider); err != nil {
		return nil, err
	}

	return provider, nil
}

func (s *Service) ListChannels(ctx context.Context) ([]domain.TenantAcquirerChannel, error) {
	return s.repo.ListAllChannels(ctx)
}

func (s *Service) CreateChannel(ctx context.Context, req *domain.CreateChannelRequest) (*domain.TenantAcquirerChannel, error) {
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tenant_id: %v", domain.ErrInvalidInput, err)
	}

	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid merchant_id: %v", domain.ErrInvalidInput, err)
	}

	providerID, err := uuid.Parse(req.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid provider_id: %v", domain.ErrInvalidInput, err)
	}

	channel := &domain.TenantAcquirerChannel{
		ID:            uuid.New(),
		TenantID:      tenantID,
		MerchantID:    merchantID,
		ProviderID:    providerID,
		ChannelCode:   req.ChannelCode,
		ChannelName:   req.ChannelName,
		Currencies:    req.Currencies,
		PaymentMethod: req.PaymentMethod,
		IsActive:      req.IsActive,
		Version:       1,
	}

	if err := s.repo.CreateChannel(ctx, channel); err != nil {
		return nil, err
	}

	if s.registry != nil {
		_ = s.registry.PublishConfigUpdate(ctx, channel.ID, channel.Version)
	}

	return channel, nil
}

func (s *Service) UpdateChannel(ctx context.Context, id uuid.UUID, req *domain.UpdateChannelRequest) error {
	channel, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return err
	}

	if req.ChannelName != nil {
		channel.ChannelName = *req.ChannelName
	}
	if req.Currencies != nil {
		channel.Currencies = req.Currencies
	}
	if req.IsActive != nil {
		channel.IsActive = *req.IsActive
	}

	if err := s.repo.UpdateChannel(ctx, channel); err != nil {
		return err
	}

	channel.Version++
	if err := s.repo.UpdateChannelVersion(ctx, channel.ID, channel.Version); err != nil {
		return err
	}

	if s.registry != nil {
		_ = s.registry.PublishConfigUpdate(ctx, channel.ID, channel.Version)
	}

	return nil
}

func (s *Service) EnableChannel(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.EnableChannel(ctx, id); err != nil {
		return err
	}

	channel, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return err
	}

	channel.Version++
	if err := s.repo.UpdateChannelVersion(ctx, channel.ID, channel.Version); err != nil {
		return err
	}

	if s.registry != nil {
		_ = s.registry.PublishConfigUpdate(ctx, channel.ID, channel.Version)
	}

	return nil
}

func (s *Service) DisableChannel(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.DisableChannel(ctx, id); err != nil {
		return err
	}

	channel, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return err
	}

	channel.Version++
	if err := s.repo.UpdateChannelVersion(ctx, channel.ID, channel.Version); err != nil {
		return err
	}

	if s.registry != nil {
		_ = s.registry.PublishConfigUpdate(ctx, channel.ID, channel.Version)
	}

	return nil
}

func (s *Service) RotateSecret(ctx context.Context, channelID uuid.UUID, credType domain.CredentialType) error {
	channel, err := s.repo.GetChannelByID(ctx, channelID)
	if err != nil {
		return err
	}

	newSecret := uuid.New().String()

	var ciphertext []byte
	if s.encryptor != nil {
		ciphertext, err = s.encryptor.Encrypt(ctx, []byte(newSecret), crypto.Meta{
			TenantID:  channel.TenantID.String(),
			RecordID:  channel.ID.String(),
			FieldName: string(credType),
		})
		if err != nil {
			return err
		}
	} else {
		ciphertext = []byte(newSecret)
	}

	cred := &domain.ProviderCredentialVault{
		ID:             uuid.New(),
		ProviderID:     channel.ProviderID,
		ChannelID:      &channel.ID,
		CredentialType: credType,
		Ciphertext:     ciphertext,
		KeyVersion:     1,
	}

	if err := s.repo.UpsertCredential(ctx, cred); err != nil {
		return err
	}

	channel.Version++
	if err := s.repo.UpdateChannelVersion(ctx, channel.ID, channel.Version); err != nil {
		return err
	}

	if s.registry != nil {
		_ = s.registry.PublishConfigUpdate(ctx, channel.ID, channel.Version)
	}

	return nil
}

func (s *Service) ReloadChannel(ctx context.Context, id uuid.UUID) error {
	channel, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return err
	}

	channel.Version++
	if err := s.repo.UpdateChannelVersion(ctx, channel.ID, channel.Version); err != nil {
		return err
	}

	if s.registry != nil {
		_ = s.registry.PublishConfigUpdate(ctx, channel.ID, channel.Version)
	}

	return nil
}

func (s *Service) SelectChannelByTier(ctx context.Context, entityID uuid.UUID, tier domain.ProviderTier) (*domain.AcquirerChannel, error) {
	if err := tier.Validate(); err != nil {
		return nil, err
	}

	channels, err := s.registry.SelectByTier(ctx, entityID, tier)
	if err != nil {
		return nil, err
	}

	if len(channels) == 0 {
		return nil, domain.ErrChannelNotFound
	}

	return &channels[0], nil
}

func (s *Service) CheckChannelBurnMode(ctx context.Context, channelID uuid.UUID) error {
	status, ok := s.registry.GetChannelStatus(channelID)
	if !ok {
		return domain.ErrChannelNotFound
	}

	if !status.IsActive {
		return domain.ErrChannelDisabled
	}

	if status.IsBurnMode {
		return domain.ErrChannelBurnMode
	}

	return nil
}
