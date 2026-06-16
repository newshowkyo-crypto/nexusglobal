package riskprofile

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// RiskProfileStatus 风险配置状态
type RiskProfileStatus string

const (
	RiskProfileStatusActive   RiskProfileStatus = "ACTIVE"
	RiskProfileStatusInactive RiskProfileStatus = "INACTIVE"
	RiskProfileStatusRiskHold RiskProfileStatus = "RISK_HOLD" // 风险冻结状态，禁止释放准备金
)

// MerchantRiskProfile 商户风险配置
type MerchantRiskProfile struct {
	ID                    uuid.UUID
	MerchantID            uuid.UUID
	RollingReserveRateBps int
	DisputeReserveRateBps int
	ReserveReleaseDays    int
	Status                RiskProfileStatus
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ReserveType 准备金类型
type ReserveType string

const (
	ReserveTypeRolling ReserveType = "ROLLING_RESERVE"
	ReserveTypeDispute ReserveType = "DISPUTE_RESERVE"
)

// ReserveStatus 准备金状态
type ReserveStatus string

const (
	ReserveStatusHeld      ReserveStatus = "HELD"
	ReserveStatusReleased  ReserveStatus = "RELEASED"
	ReserveStatusForfeited ReserveStatus = "FORFEITED"
)

// RollingReserveEntry 滚动准备金条目
type RollingReserveEntry struct {
	ID            uuid.UUID
	MerchantID    uuid.UUID
	TransactionID uuid.UUID
	Amount        int64
	ReserveType   ReserveType
	ReleaseDate   time.Time
	Status        ReserveStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// 错误定义
var (
	ErrRiskProfileNotFound    = errors.New("risk profile not found")
	ErrReserveNotFound        = errors.New("reserve entry not found")
	ErrReserveNotReady        = errors.New("reserve not ready for release")
	ErrReserveInRiskHold      = errors.New("merchant is in risk hold, cannot release reserve")
	ErrReserveAlreadyReleased = errors.New("reserve already released")
)
