package domain

import (
	"time"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type BeneficiaryType string

const (
	BeneficiarySameName              BeneficiaryType = "SAME_NAME"
	BeneficiaryThirdParty            BeneficiaryType = "THIRD_PARTY"
	BeneficiaryThirdPartyCrossBorder BeneficiaryType = "THIRD_PARTY_CROSS_BORDER"
	BeneficiaryUnknown               BeneficiaryType = "UNKNOWN"
)

type SweepMode string

const (
	SweepModeTieredScheduled SweepMode = "TIERED_SCHEDULED"
)

type BankingCalendar struct {
	ID                    string
	CalendarName          string
	Jurisdiction          string
	Timezone              string
	BusinessDays          []string
	Holidays              []time.Time
	EmergencyClosureDates []time.Time
	CutOffMatrix          map[string]string
}

type SweepRule struct {
	ID                     string
	SourceBankAccountID    string
	TargetBankAccountID    string
	Currency               string
	Rail                   string
	BeneficiaryType        BeneficiaryType
	SameNameVerified       bool
	TriggerThresholdMinor  int64
	SweepRatioBps          int32
	MaxSingleSweepMinor    int64
	MinResidualMinor       int64
	TimeWindowMatrix       map[string][][2]string
	MaxDailySweepCount     int
	DelayQueueEnabled      bool
	RequireFourEyes        bool
	ApprovalThresholdMinor int64
}

func WithinBankingWindow(calendar *BankingCalendar, rail string, now time.Time) bool {
	loc, err := time.LoadLocation(calendar.Timezone)
	if err != nil {
		loc = time.UTC
	}
	localNow := now.In(loc)

	dayName := localNow.Weekday().String()
	isBusinessDay := false
	for _, bd := range calendar.BusinessDays {
		if bd == dayName {
			isBusinessDay = true
			break
		}
	}
	if !isBusinessDay {
		return false
	}

	dateOnly := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	for _, h := range calendar.Holidays {
		hLocal := h.In(loc)
		hDate := time.Date(hLocal.Year(), hLocal.Month(), hLocal.Day(), 0, 0, 0, 0, loc)
		if dateOnly.Equal(hDate) {
			return false
		}
	}

	for _, ec := range calendar.EmergencyClosureDates {
		ecLocal := ec.In(loc)
		ecDate := time.Date(ecLocal.Year(), ecLocal.Month(), ecLocal.Day(), 0, 0, 0, 0, loc)
		if dateOnly.Equal(ecDate) {
			return false
		}
	}

	cutOff, ok := calendar.CutOffMatrix[rail]
	if !ok {
		return false
	}

	cutOffTime, err := time.ParseInLocation("15:04", cutOff, loc)
	if err != nil {
		return false
	}
	cutOffDateTime := time.Date(localNow.Year(), localNow.Month(), localNow.Day(),
		cutOffTime.Hour(), cutOffTime.Minute(), 0, 0, loc)

	return localNow.Before(cutOffDateTime)
}

type BeneficiaryResult struct {
	Allowed          bool
	RequiresApproval bool
	RejectReason     string
}

func EvaluateBeneficiary(rule *SweepRule, amountMinor int64) BeneficiaryResult {
	switch rule.BeneficiaryType {
	case BeneficiaryUnknown:
		return BeneficiaryResult{
			Allowed:      false,
			RejectReason: "UNKNOWN beneficiary type is not allowed",
		}

	case BeneficiarySameName:
		if !rule.SameNameVerified {
			return BeneficiaryResult{
				Allowed:          true,
				RequiresApproval: true,
			}
		}
		return BeneficiaryResult{Allowed: true}

	case BeneficiaryThirdParty:
		if rule.ApprovalThresholdMinor > 0 && amountMinor >= rule.ApprovalThresholdMinor {
			return BeneficiaryResult{
				Allowed:          true,
				RequiresApproval: true,
			}
		}
		return BeneficiaryResult{Allowed: true}

	case BeneficiaryThirdPartyCrossBorder:
		return BeneficiaryResult{
			Allowed:          true,
			RequiresApproval: true,
		}

	default:
		return BeneficiaryResult{
			Allowed:      false,
			RejectReason: "unsupported beneficiary type",
		}
	}
}

type SweepDecision string

const (
	SweepSubmit     SweepDecision = "SUBMIT"
	SweepAccumulate SweepDecision = "ACCUMULATE"
	SweepDelayed    SweepDecision = "DELAYED"
	SweepApproval   SweepDecision = "APPROVAL_REQUIRED"
	SweepReject     SweepDecision = "REJECT"
)

type SweepTriggerInput struct {
	AccumulatedMinor int64
	DailySweepCount  int
	Rule             *SweepRule
	WithinWindow     bool
	RequiresApproval bool
}

func EvaluateSweepTrigger(input SweepTriggerInput) SweepDecision {
	if !input.WithinWindow {
		if input.Rule.DelayQueueEnabled {
			return SweepDelayed
		}
		return SweepAccumulate
	}

	if input.AccumulatedMinor < input.Rule.TriggerThresholdMinor {
		return SweepAccumulate
	}

	if input.DailySweepCount >= input.Rule.MaxDailySweepCount {
		return SweepAccumulate
	}

	if input.RequiresApproval {
		return SweepApproval
	}

	return SweepSubmit
}

var ErrOutsideBankingWindow = apperr.New("OUTSIDE_BANKING_WINDOW", 422)
var ErrBeneficiaryNotAllowed = apperr.New("BENEFICIARY_NOT_ALLOWED", 422)
var ErrApprovalRequired = apperr.New("APPROVAL_REQUIRED", 422)
