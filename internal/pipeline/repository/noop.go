package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/pipeline"
)

// NoopRepository 是 pipeline.Repository 的空实现。
//
// ⚠️ 仅供测试使用。生产/local 装配必须使用 DBRepository（见 db.go）。
// 使用 NoopRepository 会导致冻结隔离组仍放行（CheckGroupStatus 永远返回 ACTIVE），
// 违反 G05 fail-closed 要求。
type NoopRepository struct{}

func NewNoopRepository() *NoopRepository {
	return &NoopRepository{}
}

func (n *NoopRepository) GetGroupByID(ctx context.Context, id uuid.UUID) (*pipeline.IsolationGroup, error) {
	return &pipeline.IsolationGroup{
		ID:        id,
		Status:    pipeline.IsolationGroupStatusActive,
		CreatedAt: time.Time{},
		UpdatedAt: time.Time{},
	}, nil
}

func (n *NoopRepository) GetGroupByName(ctx context.Context, name string) (*pipeline.IsolationGroup, error) {
	return &pipeline.IsolationGroup{
		Name:      name,
		Status:    pipeline.IsolationGroupStatusActive,
		CreatedAt: time.Time{},
		UpdatedAt: time.Time{},
	}, nil
}

func (n *NoopRepository) CreateGroup(ctx context.Context, group *pipeline.IsolationGroup) error {
	return nil
}

func (n *NoopRepository) UpdateGroup(ctx context.Context, group *pipeline.IsolationGroup) error {
	return nil
}

func (n *NoopRepository) FreezeGroup(ctx context.Context, id uuid.UUID, reason, operator string) error {
	return nil
}

func (n *NoopRepository) UnfreezeGroup(ctx context.Context, id uuid.UUID, operator string) error {
	return nil
}

func (n *NoopRepository) LogOverride(ctx context.Context, log *pipeline.AdminOverrideAuditLog) error {
	return nil
}
