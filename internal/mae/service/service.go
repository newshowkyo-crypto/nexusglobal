package service

import (
	"context"
	"fmt"
	"time"

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
	UpdateStatus(ctx context.Context, id uuid.UUID, status, reason string, version int32, lockUntil *time.Time) error
	RotateSecret(ctx context.Context, id uuid.UUID, apiKeyCiphertext, webhookSecretCiphertext []byte, version int32) error
	UpdateConfig(ctx context.Context, params db.UpdateMAEConfigParams) error
}

// TestingFailCounter 持久化 Testing 态连续灰度失败次数（Redis）。
// T1-1: 纯函数判定依赖真实失败计数，必须由此读取，不能传零值。
type TestingFailCounter interface {
	GetTestingFailCount(ctx context.Context, maeID string) (int32, error)
	IncrTestingFailCount(ctx context.Context, maeID string) (int32, error)
	ResetTestingFailCount(ctx context.Context, maeID string) error
}

type Service struct {
	repo        Repository
	encryptor   *crypto.Encryptor
	pool        *pgxpool.Pool
	failCounter TestingFailCounter
}

func New(repo Repository, encryptor *crypto.Encryptor, pool *pgxpool.Pool) *Service {
	return &Service{repo: repo, encryptor: encryptor, pool: pool}
}

// WithFailCounter 注入 Testing 失败计数器（Redis 实现）。
func (s *Service) WithFailCounter(fc TestingFailCounter) *Service {
	s.failCounter = fc
	return s
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

	return s.repo.UpdateStatus(ctx, config.ID, string(cmd.TargetStatus), cmd.Reason, config.Version, nil)
}

func (s *Service) GetConfig(ctx context.Context, maeID string) (*db.MaeConfiguration, error) {
	return s.repo.GetConfigByMAEID(ctx, maeID)
}

type UpdateMAECmd struct {
	MAEID                    string
	DisplayName              string
	MAEType                  string
	TenantScope              []byte
	Jurisdiction             string
	SupportedCurrencies      []byte
	APIBaseURL               string
	WebhookProxyURL          string
	RiskLabel                string
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
	Version                  int32
}

func (s *Service) Update(ctx context.Context, cmd UpdateMAECmd) error {
	config, err := s.repo.GetConfigByMAEID(ctx, cmd.MAEID)
	if err != nil {
		return err
	}
	if config == nil {
		return apperr.ErrNotFound.WithMessage("MAE not found: " + cmd.MAEID)
	}

	if config.Version != cmd.Version {
		return apperr.ErrVersionConflict.WithMessage("version conflict")
	}

	return s.repo.UpdateConfig(ctx, db.UpdateMAEConfigParams{
		ID:                       config.ID,
		DisplayName:              cmd.DisplayName,
		MaeType:                  cmd.MAEType,
		TenantScope:              cmd.TenantScope,
		Jurisdiction:             cmd.Jurisdiction,
		SupportedCurrencies:      cmd.SupportedCurrencies,
		ApiBaseUrl:               cmd.APIBaseURL,
		WebhookProxyUrl:          cmd.WebhookProxyURL,
		RiskLabel:                cmd.RiskLabel,
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
		Version:                  cmd.Version,
	})
}

type RotateSecretCmd struct {
	MAEID         string
	APIKey        string
	WebhookSecret string
	Version       int32
}

func (s *Service) RotateSecret(ctx context.Context, cmd RotateSecretCmd) error {
	config, err := s.repo.GetConfigByMAEID(ctx, cmd.MAEID)
	if err != nil {
		return err
	}
	if config == nil {
		return apperr.ErrNotFound.WithMessage("MAE not found: " + cmd.MAEID)
	}

	if config.Version != cmd.Version {
		return apperr.ErrVersionConflict.WithMessage("version conflict")
	}

	apiKeyEnc, err := s.encryptor.Encrypt(ctx, []byte(cmd.APIKey), crypto.Meta{
		TenantID:  cmd.MAEID,
		RecordID:  "mae",
		FieldName: "api_key",
	})
	if err != nil {
		return err
	}

	webhookSecretEnc, err := s.encryptor.Encrypt(ctx, []byte(cmd.WebhookSecret), crypto.Meta{
		TenantID:  cmd.MAEID,
		RecordID:  "mae",
		FieldName: "webhook_secret",
	})
	if err != nil {
		return err
	}

	return s.repo.RotateSecret(ctx, config.ID, apiKeyEnc, webhookSecretEnc, cmd.Version)
}

func (s *Service) ListConfigs(ctx context.Context) ([]db.MaeConfiguration, error) {
	return s.repo.ListConfigs(ctx)
}

type MetricReader interface {
	GetExposure(ctx context.Context, maeID, businessDate string) (int64, error)
}

type EvaluateCircuitBreakerCmd struct {
	MAEID            string
	BusinessDate     string
	AbnormalRateBps  int32
	CardTestingCount int32
}

func (s *Service) EvaluateCircuitBreaker(ctx context.Context, metrics MetricReader, cmd EvaluateCircuitBreakerCmd) error {
	config, err := s.repo.GetConfigByMAEID(ctx, cmd.MAEID)
	if err != nil {
		return err
	}
	if config == nil {
		return apperr.ErrNotFound.WithMessage("MAE not found: " + cmd.MAEID)
	}

	if domain.MAEStatus(config.Status) != domain.StatusActive {
		return nil
	}

	exposure, err := metrics.GetExposure(ctx, cmd.MAEID, cmd.BusinessDate)
	if err != nil {
		return err
	}

	result := domain.EvaluateCircuitBreaker(domain.CircuitBreakerInput{
		DailyVmaxMinor:     config.DailyVmaxMinor,
		GrossExposureMinor: exposure,
		AbnormalRateBps:    cmd.AbnormalRateBps,
		CmaxBps:            config.CmaxBps,
		CardTestingCount:   cmd.CardTestingCount,
		CardTestingLimit:   config.CardTestingLimit,
	})

	if !result.ShouldTrip {
		return nil
	}

	var lockUntil *time.Time
	if result.Reason == "EXPOSURE_THRESHOLD" {
		endOfDay := time.Date(
			time.Now().Year(), time.Now().Month(), time.Now().Day(),
			23, 59, 59, 0, time.UTC,
		)
		lockUntil = &endOfDay
	}

	return s.repo.UpdateStatus(ctx, config.ID, string(domain.StatusCooldown), result.Reason, config.Version, lockUntil)
}

type EvaluateTestingCmd struct {
	MAEID            string
	TxnCount         int32
	SuccessRateBps   int32
	AbnormalRateBps  int32
	P95LatencyMs     int32
	CardTestingCount int32
}

func (s *Service) EvaluateTesting(ctx context.Context, cmd EvaluateTestingCmd) (domain.TestingResult, error) {
	config, err := s.repo.GetConfigByMAEID(ctx, cmd.MAEID)
	if err != nil {
		return domain.TestingResult{}, err
	}
	if config == nil {
		return domain.TestingResult{}, apperr.ErrNotFound.WithMessage("MAE not found: " + cmd.MAEID)
	}

	if domain.MAEStatus(config.Status) != domain.StatusTesting {
		return domain.TestingResult{}, apperr.ErrIllegalTransition.WithMessage("MAE not in Testing status")
	}

	// T1-1: 读取真实连续失败计数，传入纯函数（否则 failCount 恒为 0，永不触发 Suspend）。
	var priorFailCount int32
	if s.failCounter != nil {
		c, err := s.failCounter.GetTestingFailCount(ctx, cmd.MAEID)
		if err != nil {
			return domain.TestingResult{}, fmt.Errorf("get testing fail count: %w", err)
		}
		priorFailCount = c
	}

	result := domain.EvaluateTesting(domain.TestingInput{
		TxnCount:              cmd.TxnCount,
		TestingMinTxnCount:    config.TestingMinTxnCount,
		SuccessRateBps:        cmd.SuccessRateBps,
		TestingMinSuccessRate: config.TestingMinSuccessRateBps,
		AbnormalRateBps:       cmd.AbnormalRateBps,
		CmaxBps:               config.CmaxBps,
		P95LatencyMs:          cmd.P95LatencyMs,
		TestingMaxP95Latency:  config.TestingMaxP95LatencyMs,
		CardTestingCount:      cmd.CardTestingCount,
		TestingFailCount:      priorFailCount,
	})

	if err := s.repo.UpdateStatus(ctx, config.ID, string(result.TargetStatus), result.Reason, config.Version, nil); err != nil {
		return domain.TestingResult{}, err
	}

	// T1-1: 维护失败计数。达标晋级或被 Suspend 都清零；判定为 HOLD（未达标但未到阈值）则递增。
	if s.failCounter != nil {
		switch result.Decision {
		case domain.TestingHold:
			if _, err := s.failCounter.IncrTestingFailCount(ctx, cmd.MAEID); err != nil {
				return domain.TestingResult{}, fmt.Errorf("incr testing fail count: %w", err)
			}
		case domain.TestingPromote, domain.TestingSuspend:
			if err := s.failCounter.ResetTestingFailCount(ctx, cmd.MAEID); err != nil {
				return domain.TestingResult{}, fmt.Errorf("reset testing fail count: %w", err)
			}
		}
	}

	return result, nil
}

// EvaluateTestingTimeout T1-1: 探测窗口超时巡检。
// Testing 态停留超过 cooldown_minutes 仍未晋级 Active，判定探测窗口超时 → 回 Cooldown，
// 同时清零失败计数（重新进入下一轮探测窗口）。返回是否发生超时迁移。
func (s *Service) EvaluateTestingTimeout(ctx context.Context, maeID string, now time.Time) (bool, error) {
	config, err := s.repo.GetConfigByMAEID(ctx, maeID)
	if err != nil {
		return false, err
	}
	if config == nil {
		return false, apperr.ErrNotFound.WithMessage("MAE not found: " + maeID)
	}

	if domain.MAEStatus(config.Status) != domain.StatusTesting {
		return false, nil
	}

	// cooldown_minutes <= 0 视为不启用超时巡检，避免误判。
	if config.CooldownMinutes <= 0 {
		return false, nil
	}

	// UpdatedAt 在状态切入 Testing 时被刷新，作为进入 Testing 的时间基准。
	if !config.UpdatedAt.Valid {
		return false, nil
	}
	enteredAt := config.UpdatedAt.Time
	deadline := enteredAt.Add(time.Duration(config.CooldownMinutes) * time.Minute)
	if !now.After(deadline) {
		return false, nil
	}

	if !domain.CanTransition(domain.StatusTesting, domain.StatusCooldown) {
		return false, apperr.ErrIllegalTransition.WithMessage("cannot transition Testing to Cooldown")
	}

	if err := s.repo.UpdateStatus(ctx, config.ID, string(domain.StatusCooldown),
		"TESTING_WINDOW_TIMEOUT", config.Version, nil); err != nil {
		return false, err
	}

	if s.failCounter != nil {
		if err := s.failCounter.ResetTestingFailCount(ctx, maeID); err != nil {
			return false, fmt.Errorf("reset testing fail count on timeout: %w", err)
		}
	}

	return true, nil
}
