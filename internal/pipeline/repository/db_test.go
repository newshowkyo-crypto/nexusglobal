package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/nexusglobal/mamr/internal/pipeline"
)

// TestNoopRepository_FreezeGroupIsNoop 展示 G05 问题：
// NoopRepository.FreezeGroup 是空操作，冻结后 GetGroupByID 仍返回 ACTIVE。
// 这证明 NoopRepository 不能用于生产——冻结隔离组在 Noop 下仍放行。
//
// 修复：生产/local 装配必须使用 DBRepository（见 db.go），它真正更新 DB 状态。
func TestNoopRepository_FreezeGroupIsNoop(t *testing.T) {
	repo := NewNoopRepository()
	ctx := context.Background()
	groupID := uuid.New()

	// 冻结组
	err := repo.FreezeGroup(ctx, groupID, "suspicious activity", "admin")
	assert.NoError(t, err)

	// NoopRepository 的 GetGroupByID 永远返回 ACTIVE（这是问题所在）
	group, err := repo.GetGroupByID(ctx, groupID)
	assert.NoError(t, err)
	assert.Equal(t, pipeline.IsolationGroupStatusActive, group.Status,
		"BUG REPRODUCTION: NoopRepository returns ACTIVE even after FreezeGroup — "+
			"this is why NoopRepository must NOT be used in production (G05)")

	// 验证 pipeline.Service 在 Noop 下不会阻断
	svc := pipeline.NewService(repo, nil)
	err = svc.CheckGroupStatus(ctx, &groupID)
	assert.NoError(t, err,
		"BUG REPRODUCTION: CheckGroupStatus passes on NoopRepository even after freeze — "+
			"frozen group does not block (G05 fail-closed violation)")
}

// TestDBRepository_NilPoolPanics 验证 DBRepository 在 nil pool 时 panic。
// 这是 fail-closed 设计：不允许用 nil pool 构造（避免 silently 使用空实现）。
func TestDBRepository_NilPoolPanics(t *testing.T) {
	assert.Panics(t, func() {
		NewDBRepository(nil)
	}, "NewDBRepository(nil) must panic — fail-closed design")
}

// TestDBRepository_ImplementsInterface 编译期断言：DBRepository 实现 pipeline.Repository。
// 已在 db.go 中声明 var _ pipeline.Repository = (*DBRepository)(nil)，
// 此测试作为运行时补充确认。
func TestDBRepository_ImplementsInterface(t *testing.T) {
	var _ pipeline.Repository = (*DBRepository)(nil)
	var _ pipeline.Repository = (*NoopRepository)(nil)
}
