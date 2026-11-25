// Package domain 定义支付池的通道、策略、水位与租约模型。
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrNoPolicy 表示站点/业务线没有配置可用策略（fail-closed）。
	ErrNoPolicy = errors.New("pool: no policy for merchant/site/business_line")
	// ErrNoCandidate 表示硬过滤后没有可用通道（fail-closed）。
	ErrNoCandidate = errors.New("pool: no eligible channel")
	// ErrLimitExceeded 表示所有候选通道的限额/水位已满。
	ErrLimitExceeded = errors.New("pool: all candidates exhausted")
	// ErrLeaseNotFound 表示 attempt 没有可切换的备用通道。
	ErrLeaseNotFound = errors.New("pool: no standby lease")
)

// Mode 支付池运行模式。
type Mode string

const (
	ModeOff     Mode = "off"     // 不参与选路
	ModeShadow  Mode = "shadow"  // 只记录决策，不改变实际通道
	ModeEnforce Mode = "enforce" // 决策生效
)

func ParseMode(s string) Mode {
	switch Mode(s) {
	case ModeShadow, ModeEnforce:
		return Mode(s)
	default:
		return ModeOff
	}
}

// Channel 池成员：依附于既有 tenant_acquirer_channel。
type Channel struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	MerchantID        uuid.UUID
	AcquirerChannelID uuid.UUID
	EntityID          *uuid.UUID
	PSPCode           string
	ExternalMID       string
	MCC               string
	BusinessLines     []string
	Currencies        []string
	CardBrands        []string
	MinAmountMinor    int64
	MaxAmountMinor    int64
	DailyLimitMinor   int64
	MonthlyLimitMinor int64
	CostBps           int
	SettlementDays    int
	Weight            int
	HealthScore       int
	Status            string
	MonthUsedMinor    int64
	Watermark         Watermark
}

// Available 返回扣减在途后的可用额度；limit<=0 表示不限。
func (c Channel) RemainingDaily() (int64, bool) {
	if c.DailyLimitMinor <= 0 {
		return 0, false
	}
	remaining := c.DailyLimitMinor - c.Watermark.UsedAmountMinor - c.Watermark.ReservedAmountMinor
	return remaining, true
}

// Policy 站点/业务线到通道集合的绑定。
type Policy struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	MerchantID        uuid.UUID
	SiteID            string
	BusinessLine      string
	AllowedChannelIDs []uuid.UUID
	Strategy          string
	Weights           map[string]float64
	Status            string
	Version           int
}

// Watermark 当日用量。
type Watermark struct {
	UsedAmountMinor     int64
	ReservedAmountMinor int64
	UsedCount           int64
}

// SelectionRequest 选路入参。
type SelectionRequest struct {
	TenantID     uuid.UUID
	MerchantID   uuid.UUID
	SiteID       string
	BusinessLine string
	Currency     string
	CardBrand    string
	AmountMinor  int64
	AttemptID    uuid.UUID
	// Nil means no site restriction. A non-nil set is a hard allowlist before reservation.
	AllowedAcquirerChannels map[uuid.UUID]bool
}

// Candidate 打分结果。
type Candidate struct {
	Channel Channel
	Score   float64
	Weight  float64
}

// Decision 选路结果。
type Decision struct {
	ChannelID         uuid.UUID
	AcquirerChannelID uuid.UUID
	Score             float64
	Mode              Mode
	Reason            string
	BusinessLine      string
}

// Lease 状态。
const (
	LeaseStandby   = "STANDBY"
	LeaseHeld      = "HELD"
	LeaseConfirmed = "CONFIRMED"
	LeaseReleased  = "RELEASED"
	LeaseExpired   = "EXPIRED"
)

// Lease 按笔预占记录。
type Lease struct {
	ID          uuid.UUID
	ChannelID   uuid.UUID
	AttemptID   uuid.UUID
	AmountMinor int64
	Rank        int
	State       string
	ExpiresAt   time.Time
}
