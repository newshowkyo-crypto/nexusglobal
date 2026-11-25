package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
)

// RiskDecision 风控决策结果
type RiskDecision string

const (
	DecisionAllow     RiskDecision = "ALLOW"
	DecisionBlock     RiskDecision = "BLOCK"
	DecisionChallenge RiskDecision = "CHALLENGE"
	DecisionCooldown  RiskDecision = "COOLDOWN"
)

// WindowType 时间窗口类型
type WindowType string

const (
	Window60s WindowType = "60s"
	Window5m  WindowType = "5m"
	Window1h  WindowType = "1h"
)

// DimensionType 风控维度
type DimensionType string

const (
	DimensionBuyerIP    DimensionType = "buyer_ip"
	DimensionAppID      DimensionType = "app_id"
	DimensionTenantID   DimensionType = "tenant_id"
	DimensionMerchantID DimensionType = "merchant_id"
	DimensionCardBIN    DimensionType = "card_bin_hash"
	DimensionDevice     DimensionType = "device_hash"
	// P6: 池化维度
	DimensionChannel DimensionType = "channel_id"
	DimensionSite    DimensionType = "site_id"
)

// ChannelStrategyMode 通道风控策略模式
type ChannelStrategyMode string

const (
	ChannelStrategyModeDefault      ChannelStrategyMode = "DEFAULT"       // 标准阈值
	ChannelStrategyModeStrict       ChannelStrategyMode = "STRICT"        // 更严格阈值
	ChannelStrategyModeManualReview ChannelStrategyMode = "MANUAL_REVIEW" // 高风险订单转人工
)

// CheckRequest 风控检查请求
type CheckRequest struct {
	TenantID     uuid.UUID
	MerchantID   uuid.UUID
	AppID        string
	BuyerIP      string              // 原始 IP，内部会 hash
	CardBIN      string              // 原始 BIN（6-8位），内部会 hash
	DeviceID     string              // 设备指纹，内部会 hash
	ChannelID    uuid.UUID           // P6: 收单通道（池化维度）
	SiteID       string              // P6: 站点（池化维度）
	StrategyMode ChannelStrategyMode // 通道策略模式
}

// CheckResult 风控检查结果
type CheckResult struct {
	Decision             RiskDecision
	Reason               string
	WindowType           WindowType
	Threshold            int
	CurrentCount         int
	BlockedBy            DimensionType
	RequiresManualReview bool // 是否需要人工审核（MANUAL_REVIEW模式）
}

// RiskEvent 审计日志
type RiskEvent struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	MerchantID   uuid.UUID
	AppID        string
	Decision     RiskDecision
	Reason       string
	BuyerIPHash  string
	CardBINHash  string
	DeviceHash   string
	ChannelID    uuid.UUID // P6: 收单通道
	SiteID       string    // P6: 站点
	WindowType   WindowType
	ThresholdHit int
	CurrentCount int
	CreatedAt    time.Time
}

// ThresholdConfig 阈值配置
type ThresholdConfig struct {
	WindowType WindowType
	Threshold  int
	Dimensions []DimensionConfig
}

// DimensionConfig 维度配置
type DimensionConfig struct {
	Dimension DimensionType
	Threshold int
	Window    WindowType
}

// RuleScope 规则作用域。
type RuleScope string

const (
	ScopeGlobal   RuleScope = "GLOBAL"
	ScopeMerchant RuleScope = "MERCHANT"
	ScopeSite     RuleScope = "SITE"
	ScopeChannel  RuleScope = "CHANNEL"
)

// Rule 表化风控规则（P6）。
type Rule struct {
	ID          uuid.UUID     `json:"id"`
	TenantID    *uuid.UUID    `json:"tenant_id,omitempty"`
	MerchantID  *uuid.UUID    `json:"merchant_id,omitempty"`
	Scope       RuleScope     `json:"scope"`
	ScopeID     string        `json:"scope_id"`
	Dimension   DimensionType `json:"dimension"`
	Window      WindowType    `json:"window_type"`
	Threshold   int           `json:"threshold"`
	FailureRate float64       `json:"failure_rate"`
	MinSample   int           `json:"min_sample"`
	Priority    int           `json:"priority"`
	Status      string        `json:"status"`
	Version     int           `json:"version"`
}

// EffectiveRules 合并作用域覆盖后的有效阈值（P6）。
type EffectiveRules struct {
	IPThreshold      int
	IPWindow         WindowType
	BINThreshold     int
	BINWindow        WindowType
	DeviceThreshold  int
	DeviceWindow     WindowType
	ChannelThreshold int
	ChannelWindow    WindowType
	SiteThreshold    int
	SiteWindow       WindowType
	AppFailureRate   float64
	AppMinSample     int
	AppWindow        WindowType
}

// DefaultEffectiveRules 由 DefaultRuleConfig 生成默认有效规则。
func DefaultEffectiveRules() EffectiveRules {
	return EffectiveRules{
		IPThreshold:    DefaultRuleConfig.IPFailureThreshold,
		IPWindow:       DefaultRuleConfig.IPWindow,
		BINThreshold:   DefaultRuleConfig.BINFailureThreshold,
		BINWindow:      DefaultRuleConfig.BINWindow,
		AppFailureRate: DefaultRuleConfig.AppFailureRateThreshold,
		AppMinSample:   DefaultRuleConfig.AppMinSampleSize,
		AppWindow:      DefaultRuleConfig.AppWindow,
	}
}

// RuleConfig 规则配置
type RuleConfig struct {
	IPFailureThreshold      int // IP 失败阈值
	IPWindow                WindowType
	BINFailureThreshold     int // BIN 失败阈值
	BINWindow               WindowType
	AppFailureRateThreshold float64 // App 失败率阈值（百分比）
	AppMinSampleSize        int     // App 最小样本量
	AppWindow               WindowType
	CooldownDuration        time.Duration // 冷却期时长
}

// DefaultRuleConfig 默认规则配置
var DefaultRuleConfig = RuleConfig{
	IPFailureThreshold:      5,
	IPWindow:                Window60s,
	BINFailureThreshold:     3,
	BINWindow:               Window5m,
	AppFailureRateThreshold: 30.0,
	AppMinSampleSize:        10,
	AppWindow:               Window1h,
	CooldownDuration:        5 * time.Minute,
}

var (
	ErrBlocked          = errors.New("carding: request blocked by risk gateway")
	ErrCooldown         = errors.New("carding: merchant in cooldown period")
	ErrRedisUnavailable = errors.New("carding: redis unavailable")
)

// HashIP 对 IP 进行 SHA-256 哈希
func HashIP(ip string) string {
	if ip == "" {
		return ""
	}
	h := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(h[:])
}

// HashBIN 对卡 BIN 进行 SHA-256 哈希
// 注意：只接收 BIN（6-8位），不接收完整卡号
func HashBIN(bin string) string {
	if bin == "" {
		return ""
	}
	// 只取前 8 位，确保不会意外接收完整卡号
	if len(bin) > 8 {
		bin = bin[:8]
	}
	h := sha256.Sum256([]byte(bin))
	return hex.EncodeToString(h[:])
}

// HashDevice 对设备指纹进行哈希
func HashDevice(deviceID string) string {
	if deviceID == "" {
		return ""
	}
	h := sha256.Sum256([]byte(deviceID))
	return hex.EncodeToString(h[:])
}

// WindowToDuration 将窗口类型转换为时长
func WindowToDuration(w WindowType) time.Duration {
	switch w {
	case Window60s:
		return 60 * time.Second
	case Window5m:
		return 5 * time.Minute
	case Window1h:
		return 1 * time.Hour
	default:
		return 60 * time.Second
	}
}

// WindowToSeconds 将窗口类型转换为秒数
func WindowToSeconds(w WindowType) int64 {
	switch w {
	case Window60s:
		return 60
	case Window5m:
		return 300
	case Window1h:
		return 3600
	default:
		return 60
	}
}
