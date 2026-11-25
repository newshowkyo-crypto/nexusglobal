// Package repository 提供 pipeline.Repository 的真实 DB 实现与测试用 Noop 实现。
//
// db.go 是 G05 修复产物：原先 API 装配使用 NewNoopRepository，导致冻结隔离组
// 在 Noop 下仍放行（CheckGroupStatus 永远返回 ACTIVE）。本文件提供基于 pgxpool
// 的真实实现，不存在/DB 错误/未知状态一律 fail-closed。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/pipeline"
)

// DBRepository 是 pipeline.Repository 的 PostgreSQL 实现。
//
// 与 NoopRepository 的关键区别：
//   - GetGroupByID 在行不存在时返回 pipeline.ErrGroupNotFound（fail-closed），
//     而非伪造一个 ACTIVE 组。
//   - DB 错误原样返回，调用方按 fail-closed 处理。
//   - 未知 status 值不映射为 ACTIVE，而是返回错误，避免放行。
type DBRepository struct {
	pool *pgxpool.Pool
}

// NewDBRepository 创建真实 DB repository。pool 不能为 nil。
func NewDBRepository(pool *pgxpool.Pool) *DBRepository {
	if pool == nil {
		panic("pipeline repository: nil pool is not allowed (use NoopRepository only in tests)")
	}
	return &DBRepository{pool: pool}
}

// GetGroupByID 按 ID 读取隔离组。行不存在返回 ErrGroupNotFound。
func (r *DBRepository) GetGroupByID(ctx context.Context, id uuid.UUID) (*pipeline.IsolationGroup, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(description, ''), status,
		       COALESCE(frozen_reason, ''), COALESCE(frozen_by, ''), frozen_at,
		       created_at, updated_at
		FROM pipeline_isolation_groups WHERE id = $1`, id)

	g, err := scanGroup(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pipeline.ErrGroupNotFound
		}
		return nil, fmt.Errorf("pipeline repo GetGroupByID: %w", err)
	}
	return g, nil
}

// GetGroupByName 按 name 读取隔离组。行不存在返回 ErrGroupNotFound。
func (r *DBRepository) GetGroupByName(ctx context.Context, name string) (*pipeline.IsolationGroup, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(description, ''), status,
		       COALESCE(frozen_reason, ''), COALESCE(frozen_by, ''), frozen_at,
		       created_at, updated_at
		FROM pipeline_isolation_groups WHERE name = $1`, name)

	g, err := scanGroup(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pipeline.ErrGroupNotFound
		}
		return nil, fmt.Errorf("pipeline repo GetGroupByName: %w", err)
	}
	return g, nil
}

// CreateGroup 插入新隔离组。
func (r *DBRepository) CreateGroup(ctx context.Context, group *pipeline.IsolationGroup) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO pipeline_isolation_groups (id, name, description, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		group.ID, group.Name, group.Description, string(group.Status),
		group.CreatedAt, group.UpdatedAt)
	if err != nil {
		return fmt.Errorf("pipeline repo CreateGroup: %w", err)
	}
	return nil
}

// UpdateGroup 更新隔离组（全字段）。
func (r *DBRepository) UpdateGroup(ctx context.Context, group *pipeline.IsolationGroup) error {
	cmd, err := r.pool.Exec(ctx, `
		UPDATE pipeline_isolation_groups
		SET name = $2, description = $3, status = $4,
		    frozen_reason = $5, frozen_by = $6, frozen_at = $7, updated_at = $8
		WHERE id = $1`,
		group.ID, group.Name, group.Description, string(group.Status),
		group.FrozenReason, group.FrozenBy, group.FrozenAt, group.UpdatedAt)
	if err != nil {
		return fmt.Errorf("pipeline repo UpdateGroup: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return pipeline.ErrGroupNotFound
	}
	return nil
}

// FreezeGroup 将隔离组标记为 FROZEN 并记录原因/操作人。
func (r *DBRepository) FreezeGroup(ctx context.Context, id uuid.UUID, reason, operator string) error {
	cmd, err := r.pool.Exec(ctx, `
		UPDATE pipeline_isolation_groups
		SET status = 'FROZEN', frozen_reason = $2, frozen_by = $3,
		    frozen_at = now(), updated_at = now()
		WHERE id = $1`, id, reason, operator)
	if err != nil {
		return fmt.Errorf("pipeline repo FreezeGroup: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return pipeline.ErrGroupNotFound
	}
	return nil
}

// UnfreezeGroup 将隔离组恢复为 ACTIVE 并清空冻结字段。
func (r *DBRepository) UnfreezeGroup(ctx context.Context, id uuid.UUID, operator string) error {
	cmd, err := r.pool.Exec(ctx, `
		UPDATE pipeline_isolation_groups
		SET status = 'ACTIVE', frozen_reason = NULL, frozen_by = NULL,
		    frozen_at = NULL, updated_at = now()
		WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("pipeline repo UnfreezeGroup: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return pipeline.ErrGroupNotFound
	}
	return nil
}

// LogOverride 写入管理员覆盖审计日志。
func (r *DBRepository) LogOverride(ctx context.Context, log *pipeline.AdminOverrideAuditLog) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO admin_override_audit_log
		(id, entity_type, entity_id, override_type, reason, operator, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		log.ID, log.EntityType, log.EntityID, string(log.OverrideType),
		log.Reason, log.Operator, log.ExpiresAt, log.CreatedAt)
	if err != nil {
		return fmt.Errorf("pipeline repo LogOverride: %w", err)
	}
	return nil
}

// scanGroup 扫描单行到 IsolationGroup，并校验 status 为已知枚举。
// 未知 status 返回错误（fail-closed），避免放行。
func scanGroup(row pgx.Row) (*pipeline.IsolationGroup, error) {
	var g pipeline.IsolationGroup
	var status string
	err := row.Scan(
		&g.ID, &g.Name, &g.Description, &status,
		&g.FrozenReason, &g.FrozenBy, &g.FrozenAt,
		&g.CreatedAt, &g.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// 校验 status 为已知枚举，未知值 fail-closed
	switch pipeline.IsolationGroupStatus(status) {
	case pipeline.IsolationGroupStatusActive,
		pipeline.IsolationGroupStatusFrozen,
		pipeline.IsolationGroupStatusRiskHold:
		g.Status = pipeline.IsolationGroupStatus(status)
	default:
		return nil, fmt.Errorf("pipeline repo: unknown isolation group status %q (fail-closed)", status)
	}
	return &g, nil
}

// 编译期断言：DBRepository 实现 pipeline.Repository 接口。
var _ pipeline.Repository = (*DBRepository)(nil)

// now 返回当前时间，供内部使用（与 service.go 的 clock 注入对应）。
var now = func() time.Time { return time.Now() }
