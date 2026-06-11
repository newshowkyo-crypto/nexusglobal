package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/mae/domain"
	"github.com/nexusglobal/mamr/internal/mae/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

type Repository interface {
	InsertConfig(ctx context.Context, params db.InsertMAEConfigParams) (*db.MaeConfiguration, error)
	GetConfig(ctx context.Context, id uuid.UUID) (*db.MaeConfiguration, error)
	GetConfigByMAEID(ctx context.Context, maeID string) (*db.MaeConfiguration, error)
	ListConfigs(ctx context.Context) ([]db.MaeConfiguration, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status, reason string, version int32) error
	RotateSecret(ctx context.Context, id uuid.UUID, apiKeyCiphertext, webhookSecretCiphertext []byte, version int32) error
}

type Service struct {
	repo      Repository
	encryptor *crypto.Encryptor
	pool      *pgxpool.Pool
}

func New(repo Repository, encryptor *crypto.Encryptor, pool *pgxpool.Pool) *Service {
	return &Service{repo: repo, encryptor: encryptor, pool: pool}
}

type CreateMAECmd struct {
	MAEID                    string
	DisplayName              string
	MAEType                  string
	TenantScope              []byte
	Jurisdiction             string
	SupportedCurrencies      []byte
	APIBaseURL               string
	APIKey                   string
	WebhookSecret            string
	WebhookProxyURL          string
	RiskLabel                string
	SettlementBankAccountID  uuid.UUID
	DailyVmaxMinor           int64
	MonthlyVmaxMinor         int64
	CmaxBps                  int32
	CardTestingLimit         int32
	RoutingWeight            int32
	CooldownMinutes          int32
	TestingTrafficBps        int32
	TestingMinTxnCount       int32
	TestingMinSuccessRateBps int32
	TestingMaxP95LatencyMs   int32
}

func (s *Service) Create(ctx context.Context, cmd CreateMAECmd) (*db.MaeConfiguration, error) {
	apiKeyEnc, err := s.encryptor.Encrypt(ctx, []byte(cmd.APIKey), crypto.Meta{
		TenantID:  cmd.MAEID,
		RecordID:  "mae",
		FieldName: "api_key",
	})
	if err != nil {
		return nil, err
	}

	webhookSecretEnc, err := s.encryptor.Encrypt(ctx, []byte(cmd.WebhookSecret), crypto.Meta{
		TenantID:  cmd.MAEID,
		RecordID:  "mae",
		FieldName: "webhook_secret",
	})
	if err != nil {
		return nil, err
	}

	return s.repo.InsertConfig(ctx, db.InsertMAEConfigParams{
		MaeID:                    cmd.MAEID,
		DisplayName:              cmd.DisplayName,
		MaeType:                  cmd.MAEType,
		TenantScope:              cmd.TenantScope,
		Jurisdiction:             cmd.Jurisdiction,
		SupportedCurrencies:      cmd.SupportedCurrencies,
		ApiBaseUrl:               cmd.APIBaseURL,
		ApiKeyCiphertext:         apiKeyEnc,
		WebhookSecretCiphertext:  webhookSecretEnc,
		WebhookProxyUrl:          cmd.WebhookProxyURL,
		RiskLabel:                cmd.RiskLabel,
		SettlementBankAccountID:  cmd.SettlementBankAccountID,
		DailyVmaxMinor:           cmd.DailyVmaxMinor,
		MonthlyVmaxMinor:         cmd.MonthlyVmaxMinor,
		CmaxBps:                  cmd.CmaxBps,
		CardTestingLimit:         cmd.CardTestingLimit,
		RoutingWeight:            cmd.RoutingWeight,
		CooldownMinutes:          cmd.CooldownMinutes,
		TestingTrafficBps:        cmd.TestingTrafficBps,
		TestingMinTxnCount:       cmd.TestingMinTxnCount,
		TestingMinSuccessRateBps: cmd.TestingMinSuccessRateBps,
		TestingMaxP95LatencyMs:   cmd.TestingMaxP95LatencyMs,
	})
}

type ChangeStatusCmd struct {
	MAEID        string
	TargetStatus domain.MAEStatus
	Reason       string
	Version      int32
}

func (s *Service) ChangeStatus(ctx context.Context, cmd ChangeStatusCmd) error {
	config, err := s.repo.GetConfigByMAEID(ctx, cmd.MAEID)
	if err != nil {
		return err
	}
	if config == nil {
		return apperr.ErrNotFound.WithMessage("MAE not found: " + cmd.MAEID)
	}

	currentStatus := domain.MAEStatus(config.Status)
	if !domain.CanTransition(currentStatus, cmd.TargetStatus) {
		return apperr.ErrIllegalTransition.WithMessage(
			"cannot transition from " + string(currentStatus) + " to " + string(cmd.TargetStatus))
	}

	return s.repo.UpdateStatus(ctx, config.ID, string(cmd.TargetStatus), cmd.Reason, config.Version)
}

func (s *Service) GetConfig(ctx context.Context, maeID string) (*db.MaeConfiguration, error) {
	return s.repo.GetConfigByMAEID(ctx, maeID)
}

func (s *Service) ListConfigs(ctx context.Context) ([]db.MaeConfiguration, error) {
	return s.repo.ListConfigs(ctx)
}
