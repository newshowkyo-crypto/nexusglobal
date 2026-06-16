package carding

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/risk/carding/domain"
)

// Service 卡风控服务
type Service struct {
	gateway *Gateway
	repo    *Repository
	pool    *pgxpool.Pool
	logger  *slog.Logger
}

// NewService 创建服务
func NewService(gateway *Gateway, repo *Repository, pool *pgxpool.Pool, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		gateway: gateway,
		repo:    repo,
		pool:    pool,
		logger:  logger,
	}
}

// Evaluate 风控评估
// 返回决策结果，并写入审计日志
func (s *Service) Evaluate(ctx context.Context, req domain.CheckRequest) (domain.CheckResult, error) {
	result := s.gateway.Check(ctx, req)

	// 写入审计日志
	event := &domain.RiskEvent{
		ID:           uuid.New(),
		TenantID:     req.TenantID,
		MerchantID:   req.MerchantID,
		AppID:        req.AppID,
		Decision:     result.Decision,
		Reason:       result.Reason,
		BuyerIPHash:  domain.HashIP(req.BuyerIP),
		CardBINHash:  domain.HashBIN(req.CardBIN),
		DeviceHash:   domain.HashDevice(req.DeviceID),
		WindowType:   result.WindowType,
		ThresholdHit: result.Threshold,
		CurrentCount: result.CurrentCount,
		CreatedAt:    time.Now(),
	}

	if err := s.repo.InsertRiskEvent(ctx, event); err != nil {
		s.logger.Error("insert risk event failed", "err", err, "decision", result.Decision)
		// 不影响决策结果，审计失败不阻止业务
	}

	s.logger.Info("risk evaluation completed",
		"decision", result.Decision,
		"reason", result.Reason,
		"merchant_id", req.MerchantID,
		"app_id", req.AppID,
		"blocked_by", result.BlockedBy)

	return result, nil
}

// RecordFailure 记录失败事件
func (s *Service) RecordFailure(ctx context.Context, req domain.CheckRequest) {
	s.gateway.RecordFailure(ctx, req)
}

// RecordSuccess 记录成功事件
func (s *Service) RecordSuccess(ctx context.Context, req domain.CheckRequest) {
	s.gateway.RecordSuccess(ctx, req)
}

// SetCooldown 设置商户冷却期
func (s *Service) SetCooldown(ctx context.Context, merchantID string) {
	s.gateway.SetCooldown(ctx, merchantID)
	s.logger.Warn("merchant cooldown set", "merchant_id", merchantID)
}

// ClearCooldown 清除商户冷却期
func (s *Service) ClearCooldown(ctx context.Context, merchantID string) {
	s.gateway.ClearCooldown(ctx, merchantID)
	s.logger.Info("merchant cooldown cleared", "merchant_id", merchantID)
}

// ListEvents 查询风控审计日志
func (s *Service) ListEvents(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.RiskEvent, error) {
	return s.repo.ListRiskEvents(ctx, tenantID, limit)
}

// GetEventByID 根据 ID 查询事件
func (s *Service) GetEventByID(ctx context.Context, id uuid.UUID) (*domain.RiskEvent, error) {
	return s.repo.GetRiskEventByID(ctx, id)
}

// IsBlocked 检查是否被拦截（便捷方法）
func (s *Service) IsBlocked(result domain.CheckResult) bool {
	return result.Decision == domain.DecisionBlock || result.Decision == domain.DecisionCooldown
}

// ShouldBlockAcquirer 是否应该阻止调用 acquirer
// 根据 mimo 提示词要求：命中阈值后不得调用后方 acquirer
func (s *Service) ShouldBlockAcquirer(result domain.CheckResult) bool {
	return s.IsBlocked(result)
}
