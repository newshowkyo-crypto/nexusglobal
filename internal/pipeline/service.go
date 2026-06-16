package pipeline

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Repository 管道隔离组仓储接口
type Repository interface {
	GetGroupByID(ctx context.Context, id uuid.UUID) (*IsolationGroup, error)
	GetGroupByName(ctx context.Context, name string) (*IsolationGroup, error)
	CreateGroup(ctx context.Context, group *IsolationGroup) error
	UpdateGroup(ctx context.Context, group *IsolationGroup) error
	FreezeGroup(ctx context.Context, id uuid.UUID, reason, operator string) error
	UnfreezeGroup(ctx context.Context, id uuid.UUID, operator string) error
	LogOverride(ctx context.Context, log *AdminOverrideAuditLog) error
}

// Service 管道隔离服务
type Service struct {
	repo  Repository
	clock func() time.Time
}

// NewService 创建服务实例
func NewService(repo Repository, clock func() time.Time) *Service {
	return &Service{
		repo:  repo,
		clock: clock,
	}
}

// CreateGroup 创建隔离组
func (s *Service) CreateGroup(ctx context.Context, name, description string) (*IsolationGroup, error) {
	group := &IsolationGroup{
		ID:          uuid.New(),
		Name:        name,
		Description: description,
		Status:      IsolationGroupStatusActive,
		CreatedAt:   s.clock(),
		UpdatedAt:   s.clock(),
	}

	err := s.repo.CreateGroup(ctx, group)
	if err != nil {
		return nil, err
	}

	return group, nil
}

// FreezeGroup 冻结隔离组
func (s *Service) FreezeGroup(ctx context.Context, id uuid.UUID, reason, operator string) error {
	if reason == "" {
		return ErrInvalidOverride
	}

	// 记录审计日志
	err := s.repo.LogOverride(ctx, &AdminOverrideAuditLog{
		ID:           uuid.New(),
		EntityType:   "pipeline_isolation_groups",
		EntityID:     id,
		OverrideType: OverrideTypeFrozen,
		Reason:       reason,
		Operator:     operator,
		CreatedAt:    s.clock(),
	})
	if err != nil {
		return err
	}

	return s.repo.FreezeGroup(ctx, id, reason, operator)
}

// UnfreezeGroup 解冻隔离组
func (s *Service) UnfreezeGroup(ctx context.Context, id uuid.UUID, operator string) error {
	return s.repo.UnfreezeGroup(ctx, id, operator)
}

// ValidateCrossGroupOperation 验证跨组操作
func (s *Service) ValidateCrossGroupOperation(ctx context.Context, sourceGroupID, targetGroupID *uuid.UUID) error {
	// 如果任一为 nil，允许操作
	if sourceGroupID == nil || targetGroupID == nil {
		return nil
	}

	// 同组操作允许
	if sourceGroupID == targetGroupID || *sourceGroupID == *targetGroupID {
		return nil
	}

	// 跨组操作 fail-closed
	return ErrCrossGroupOperation
}

// CheckGroupStatus 检查组状态
func (s *Service) CheckGroupStatus(ctx context.Context, groupID *uuid.UUID) error {
	if groupID == nil {
		return nil
	}

	group, err := s.repo.GetGroupByID(ctx, *groupID)
	if err != nil {
		return err
	}

	switch group.Status {
	case IsolationGroupStatusFrozen:
		return ErrGroupFrozen
	case IsolationGroupStatusRiskHold:
		return ErrGroupRiskHold
	}

	return nil
}
