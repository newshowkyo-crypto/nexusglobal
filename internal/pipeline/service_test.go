package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mockRepository 用于测试的mock仓储
type mockRepository struct {
	groups []*IsolationGroup
	logs   []*AdminOverrideAuditLog
}

func (m *mockRepository) GetGroupByID(ctx context.Context, id uuid.UUID) (*IsolationGroup, error) {
	for _, g := range m.groups {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, ErrGroupNotFound
}

func (m *mockRepository) GetGroupByName(ctx context.Context, name string) (*IsolationGroup, error) {
	for _, g := range m.groups {
		if g.Name == name {
			return g, nil
		}
	}
	return nil, ErrGroupNotFound
}

func (m *mockRepository) CreateGroup(ctx context.Context, group *IsolationGroup) error {
	m.groups = append(m.groups, group)
	return nil
}

func (m *mockRepository) UpdateGroup(ctx context.Context, group *IsolationGroup) error {
	for i, g := range m.groups {
		if g.ID == group.ID {
			m.groups[i] = group
			return nil
		}
	}
	return ErrGroupNotFound
}

func (m *mockRepository) FreezeGroup(ctx context.Context, id uuid.UUID, reason, operator string) error {
	for _, g := range m.groups {
		if g.ID == id {
			g.Status = IsolationGroupStatusFrozen
			g.FrozenReason = reason
			g.FrozenBy = operator
			now := time.Now()
			g.FrozenAt = &now
			return nil
		}
	}
	return ErrGroupNotFound
}

func (m *mockRepository) UnfreezeGroup(ctx context.Context, id uuid.UUID, operator string) error {
	for _, g := range m.groups {
		if g.ID == id {
			g.Status = IsolationGroupStatusActive
			g.FrozenReason = ""
			g.FrozenBy = ""
			g.FrozenAt = nil
			return nil
		}
	}
	return ErrGroupNotFound
}

func (m *mockRepository) LogOverride(ctx context.Context, log *AdminOverrideAuditLog) error {
	m.logs = append(m.logs, log)
	return nil
}

func TestService_CreateGroup(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	group, err := service.CreateGroup(context.Background(), "test-group", "test description")
	if err != nil {
		t.Fatalf("CreateGroup failed: %v", err)
	}

	if group.ID == uuid.Nil {
		t.Error("expected non-nil ID")
	}

	if group.Status != IsolationGroupStatusActive {
		t.Errorf("expected status ACTIVE, got %v", group.Status)
	}
}

func TestService_FreezeGroup(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	group, _ := service.CreateGroup(context.Background(), "test-group", "test")

	// 测试冻结需要原因
	err := service.FreezeGroup(context.Background(), group.ID, "", "admin")
	if err != ErrInvalidOverride {
		t.Errorf("expected ErrInvalidOverride, got %v", err)
	}

	// 测试成功冻结
	err = service.FreezeGroup(context.Background(), group.ID, "risk reason", "admin")
	if err != nil {
		t.Fatalf("FreezeGroup failed: %v", err)
	}

	if group.Status != IsolationGroupStatusFrozen {
		t.Errorf("expected status FROZEN, got %v", group.Status)
	}

	// 验证审计日志
	if len(mockRepo.logs) != 1 {
		t.Errorf("expected 1 audit log, got %d", len(mockRepo.logs))
	}
}

func TestService_ValidateCrossGroupOperation(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	group1 := uuid.New()
	group2 := uuid.New()

	// 同组操作应允许
	err := service.ValidateCrossGroupOperation(context.Background(), &group1, &group1)
	if err != nil {
		t.Errorf("expected no error for same group, got %v", err)
	}

	// 跨组操作应 fail-closed
	err = service.ValidateCrossGroupOperation(context.Background(), &group1, &group2)
	if err != ErrCrossGroupOperation {
		t.Errorf("expected ErrCrossGroupOperation, got %v", err)
	}

	// nil group 应允许
	err = service.ValidateCrossGroupOperation(context.Background(), nil, &group1)
	if err != nil {
		t.Errorf("expected no error for nil source group, got %v", err)
	}
}
