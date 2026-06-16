package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// CorporateEntity 法人实体
type CorporateEntity struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Name         string
	LegalName    string
	TaxID        string
	Country      string
	Jurisdiction string
	Status       EntityStatus
	Metadata     map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// EntityStatus 实体状态
type EntityStatus string

const (
	EntityStatusActive    EntityStatus = "ACTIVE"
	EntityStatusInactive  EntityStatus = "INACTIVE"
	EntityStatusSuspended EntityStatus = "SUSPENDED"
)

// EntityRiskProfile 实体风险配置
type EntityRiskProfile struct {
	ID                         uuid.UUID
	CorporateEntityID          uuid.UUID
	MaxDailyTransactionAmount  decimal.Decimal
	MaxSingleTransactionAmount decimal.Decimal
	RiskScore                  int
	AllowedCountries           []string
	BlockedCountries           []string
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

// EntityAssetLimit 实体资产限额
type EntityAssetLimit struct {
	ID                uuid.UUID
	CorporateEntityID uuid.UUID
	AssetCode         string
	MaxBalance        decimal.Decimal
	MaxDailyOutflow   decimal.Decimal
	MaxDailyInflow    decimal.Decimal
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// EntityError 实体错误
var (
	ErrEntityNotFound     = errors.New("entity: corporate entity not found")
	ErrEntityNotActive    = errors.New("entity: corporate entity not active")
	ErrEntityAlreadyExist = errors.New("entity: corporate entity already exists")
	ErrInsufficientLimit  = errors.New("entity: insufficient limit")
)
