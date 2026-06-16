package pipeline

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// IsolationGroupStatus 隔离组状态
type IsolationGroupStatus string

const (
	IsolationGroupStatusActive   IsolationGroupStatus = "ACTIVE"
	IsolationGroupStatusFrozen   IsolationGroupStatus = "FROZEN"
	IsolationGroupStatusRiskHold IsolationGroupStatus = "RISK_HOLD"
)

// IsolationGroup 管道隔离组
type IsolationGroup struct {
	ID           uuid.UUID
	Name         string
	Description  string
	Status       IsolationGroupStatus
	FrozenReason string
	FrozenBy     string
	FrozenAt     *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// OverrideType 覆盖类型
type OverrideType string

const (
	OverrideTypeRiskHold     OverrideType = "RISK_HOLD"
	OverrideTypeFrozen       OverrideType = "FROZEN"
	OverrideTypeStrategyMode OverrideType = "STRATEGY_MODE"
)

// AdminOverrideAuditLog 管理员覆盖审计日志
type AdminOverrideAuditLog struct {
	ID           uuid.UUID
	EntityType   string
	EntityID     uuid.UUID
	OverrideType OverrideType
	Reason       string
	Operator     string
	ExpiresAt    *time.Time
	OldValue     []byte
	NewValue     []byte
	CreatedAt    time.Time
}

// 错误定义
var (
	ErrGroupNotFound       = errors.New("isolation group not found")
	ErrCrossGroupOperation = errors.New("cross-group operation not allowed - fail-closed")
	ErrGroupFrozen         = errors.New("isolation group is frozen")
	ErrGroupRiskHold       = errors.New("isolation group is in risk hold")
	ErrInvalidOverride     = errors.New("invalid override - reason and expires_at required")
)
