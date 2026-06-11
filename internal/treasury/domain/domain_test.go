package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func testCalendar() *BankingCalendar {
	loc, _ := time.LoadLocation("America/New_York")
	return &BankingCalendar{
		CalendarName: "US_FED",
		Jurisdiction: "US",
		Timezone:     "America/New_York",
		BusinessDays: []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday"},
		Holidays: []time.Time{
			time.Date(2026, 7, 4, 0, 0, 0, 0, loc),
		},
		EmergencyClosureDates: []time.Time{},
		CutOffMatrix: map[string]string{
			"ACH":     "16:00",
			"FedWire": "17:00",
			"SWIFT":   "15:00",
		},
	}
}

func TestWithinBankingWindow_WeekdayBeforeCutoff(t *testing.T) {
	cal := testCalendar()
	loc, _ := time.LoadLocation("America/New_York")
	// Friday 2026-06-12 at 10:00 ET
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, loc)
	assert.True(t, WithinBankingWindow(cal, "ACH", now))
}

func TestWithinBankingWindow_WeekdayAfterCutoff(t *testing.T) {
	cal := testCalendar()
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 6, 12, 16, 30, 0, 0, loc)
	assert.False(t, WithinBankingWindow(cal, "ACH", now))
}

func TestWithinBankingWindow_Weekend(t *testing.T) {
	cal := testCalendar()
	loc, _ := time.LoadLocation("America/New_York")
	// Saturday 2026-06-13
	now := time.Date(2026, 6, 13, 10, 0, 0, 0, loc)
	assert.False(t, WithinBankingWindow(cal, "ACH", now))
}

func TestWithinBankingWindow_Holiday(t *testing.T) {
	cal := testCalendar()
	loc, _ := time.LoadLocation("America/New_York")
	// July 4 2026 is Saturday, but let's test with a weekday holiday
	cal.Holidays = []time.Time{time.Date(2026, 6, 12, 0, 0, 0, 0, loc)}
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, loc)
	assert.False(t, WithinBankingWindow(cal, "ACH", now))
}

func TestWithinBankingWindow_UnsupportedRail(t *testing.T) {
	cal := testCalendar()
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, loc)
	assert.False(t, WithinBankingWindow(cal, "UNKNOWN", now))
}

func TestWithinBankingWindow_DifferentRails(t *testing.T) {
	cal := testCalendar()
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 6, 12, 16, 30, 0, 0, loc)
	assert.False(t, WithinBankingWindow(cal, "ACH", now))    // 16:00 cutoff, 16:30 is after
	assert.True(t, WithinBankingWindow(cal, "FedWire", now)) // 17:00 cutoff, 16:30 is before
}

func TestEvaluateBeneficiary_SameName_Verified(t *testing.T) {
	rule := &SweepRule{BeneficiaryType: BeneficiarySameName, SameNameVerified: true}
	result := EvaluateBeneficiary(rule, 100000)
	assert.True(t, result.Allowed)
	assert.False(t, result.RequiresApproval)
}

func TestEvaluateBeneficiary_SameName_NotVerified(t *testing.T) {
	rule := &SweepRule{BeneficiaryType: BeneficiarySameName, SameNameVerified: false}
	result := EvaluateBeneficiary(rule, 100000)
	assert.True(t, result.Allowed)
	assert.True(t, result.RequiresApproval)
}

func TestEvaluateBeneficiary_ThirdParty_BelowThreshold(t *testing.T) {
	rule := &SweepRule{BeneficiaryType: BeneficiaryThirdParty, ApprovalThresholdMinor: 200000}
	result := EvaluateBeneficiary(rule, 100000)
	assert.True(t, result.Allowed)
	assert.False(t, result.RequiresApproval)
}

func TestEvaluateBeneficiary_ThirdParty_AboveThreshold(t *testing.T) {
	rule := &SweepRule{BeneficiaryType: BeneficiaryThirdParty, ApprovalThresholdMinor: 200000}
	result := EvaluateBeneficiary(rule, 300000)
	assert.True(t, result.Allowed)
	assert.True(t, result.RequiresApproval)
}

func TestEvaluateBeneficiary_CrossBorder(t *testing.T) {
	rule := &SweepRule{BeneficiaryType: BeneficiaryThirdPartyCrossBorder}
	result := EvaluateBeneficiary(rule, 100000)
	assert.True(t, result.Allowed)
	assert.True(t, result.RequiresApproval)
}

func TestEvaluateBeneficiary_Unknown(t *testing.T) {
	rule := &SweepRule{BeneficiaryType: BeneficiaryUnknown}
	result := EvaluateBeneficiary(rule, 100000)
	assert.False(t, result.Allowed)
	assert.NotEmpty(t, result.RejectReason)
}

func TestEvaluateSweepTrigger_Submit(t *testing.T) {
	input := SweepTriggerInput{
		AccumulatedMinor: 50000000,
		DailySweepCount:  0,
		Rule: &SweepRule{
			TriggerThresholdMinor: 50000000,
			MaxDailySweepCount:    2,
			DelayQueueEnabled:     true,
		},
		WithinWindow: true,
	}
	assert.Equal(t, SweepSubmit, EvaluateSweepTrigger(input))
}

func TestEvaluateSweepTrigger_Accumulate_BelowThreshold(t *testing.T) {
	input := SweepTriggerInput{
		AccumulatedMinor: 10000000,
		DailySweepCount:  0,
		Rule: &SweepRule{
			TriggerThresholdMinor: 50000000,
			MaxDailySweepCount:    2,
		},
		WithinWindow: true,
	}
	assert.Equal(t, SweepAccumulate, EvaluateSweepTrigger(input))
}

func TestEvaluateSweepTrigger_Accumulate_MaxCountReached(t *testing.T) {
	input := SweepTriggerInput{
		AccumulatedMinor: 50000000,
		DailySweepCount:  2,
		Rule: &SweepRule{
			TriggerThresholdMinor: 50000000,
			MaxDailySweepCount:    2,
		},
		WithinWindow: true,
	}
	assert.Equal(t, SweepAccumulate, EvaluateSweepTrigger(input))
}

func TestEvaluateSweepTrigger_Delayed_OutsideWindow(t *testing.T) {
	input := SweepTriggerInput{
		AccumulatedMinor: 50000000,
		Rule: &SweepRule{
			TriggerThresholdMinor: 50000000,
			DelayQueueEnabled:     true,
		},
		WithinWindow: false,
	}
	assert.Equal(t, SweepDelayed, EvaluateSweepTrigger(input))
}

func TestEvaluateSweepTrigger_ApprovalRequired(t *testing.T) {
	input := SweepTriggerInput{
		AccumulatedMinor: 50000000,
		DailySweepCount:  0,
		Rule: &SweepRule{
			TriggerThresholdMinor: 50000000,
			MaxDailySweepCount:    2,
		},
		WithinWindow:     true,
		RequiresApproval: true,
	}
	assert.Equal(t, SweepApproval, EvaluateSweepTrigger(input))
}

func TestBeneficiaryType_Constants(t *testing.T) {
	assert.Equal(t, BeneficiaryType("SAME_NAME"), BeneficiarySameName)
	assert.Equal(t, BeneficiaryType("UNKNOWN"), BeneficiaryUnknown)
}

func TestSweepDecision_Constants(t *testing.T) {
	assert.Equal(t, SweepDecision("SUBMIT"), SweepSubmit)
	assert.Equal(t, SweepDecision("ACCUMULATE"), SweepAccumulate)
	assert.Equal(t, SweepDecision("DELAYED"), SweepDelayed)
}
