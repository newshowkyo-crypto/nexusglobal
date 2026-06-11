package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCanTransition_Valid(t *testing.T) {
	tests := []struct {
		from, to MAEStatus
	}{
		{StatusDraft, StatusActive},
		{StatusActive, StatusCooldown},
		{StatusActive, StatusSuspended},
		{StatusCooldown, StatusTesting},
		{StatusTesting, StatusActive},
		{StatusTesting, StatusCooldown},
		{StatusTesting, StatusSuspended},
		{StatusSuspended, StatusTesting},
		{StatusSuspended, StatusDisabled},
	}
	for _, tt := range tests {
		assert.True(t, CanTransition(tt.from, tt.to), "%s -> %s should be valid", tt.from, tt.to)
	}
}

func TestCanTransition_Invalid(t *testing.T) {
	tests := []struct {
		from, to MAEStatus
	}{
		{StatusCooldown, StatusActive},
		{StatusDraft, StatusCooldown},
		{StatusDraft, StatusSuspended},
		{StatusActive, StatusDraft},
		{StatusActive, StatusTesting},
		{StatusActive, StatusDisabled},
		{StatusCooldown, StatusDraft},
		{StatusCooldown, StatusSuspended},
		{StatusCooldown, StatusDisabled},
		{StatusTesting, StatusDraft},
		{StatusTesting, StatusDisabled},
		{StatusDisabled, StatusDraft},
		{StatusDisabled, StatusActive},
		{StatusDisabled, StatusCooldown},
		{StatusDisabled, StatusTesting},
		{StatusDisabled, StatusSuspended},
		{StatusSuspended, StatusActive},
		{StatusSuspended, StatusCooldown},
		{StatusSuspended, StatusDraft},
	}
	for _, tt := range tests {
		assert.False(t, CanTransition(tt.from, tt.to), "%s -> %s should be invalid", tt.from, tt.to)
	}
}

func TestCooldownToActive_Invalid(t *testing.T) {
	assert.False(t, CanTransition(StatusCooldown, StatusActive),
		"Cooldown -> Active must be invalid (RED-08)")
}

func TestTransition_Valid(t *testing.T) {
	newStatus, err := Transition(StatusDraft, StatusActive)
	assert.NoError(t, err)
	assert.Equal(t, StatusActive, newStatus)
}

func TestTransition_Invalid(t *testing.T) {
	_, err := Transition(StatusCooldown, StatusActive)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ILLEGAL_STATE_TRANSITION")
}

func TestEvaluateCircuitBreaker_ExposureThreshold(t *testing.T) {
	input := CircuitBreakerInput{
		DailyVmaxMinor:     100000000,
		GrossExposureMinor: 80000000,
	}
	result := EvaluateCircuitBreaker(input)
	assert.True(t, result.ShouldTrip)
	assert.Equal(t, "EXPOSURE_THRESHOLD", result.Reason)
}

func TestEvaluateCircuitBreaker_ExposureBelowThreshold(t *testing.T) {
	input := CircuitBreakerInput{
		DailyVmaxMinor:     100000000,
		GrossExposureMinor: 79999999,
	}
	result := EvaluateCircuitBreaker(input)
	assert.False(t, result.ShouldTrip)
}

func TestEvaluateCircuitBreaker_AbnormalRate(t *testing.T) {
	input := CircuitBreakerInput{
		DailyVmaxMinor:  100000000,
		CmaxBps:         300,
		AbnormalRateBps: 301,
	}
	result := EvaluateCircuitBreaker(input)
	assert.True(t, result.ShouldTrip)
	assert.Equal(t, "ABNORMAL_RATE", result.Reason)
}

func TestEvaluateCircuitBreaker_CardTesting(t *testing.T) {
	input := CircuitBreakerInput{
		DailyVmaxMinor:   100000000,
		CardTestingLimit: 50,
		CardTestingCount: 51,
	}
	result := EvaluateCircuitBreaker(input)
	assert.True(t, result.ShouldTrip)
	assert.Equal(t, "CARD_TESTING", result.Reason)
}

func TestEvaluateCircuitBreaker_NoTrip(t *testing.T) {
	input := CircuitBreakerInput{
		DailyVmaxMinor:     100000000,
		GrossExposureMinor: 10000000,
		CmaxBps:            300,
		AbnormalRateBps:    100,
		CardTestingLimit:   50,
		CardTestingCount:   5,
	}
	result := EvaluateCircuitBreaker(input)
	assert.False(t, result.ShouldTrip)
}

func TestEvaluateTesting_Promote(t *testing.T) {
	input := TestingInput{
		TxnCount:              100,
		TestingMinTxnCount:    100,
		SuccessRateBps:        9500,
		TestingMinSuccessRate: 9500,
		AbnormalRateBps:       100,
		CmaxBps:               300,
		P95LatencyMs:          500,
		TestingMaxP95Latency:  800,
		CardTestingCount:      0,
		TestingFailCount:      0,
	}
	result := EvaluateTesting(input)
	assert.Equal(t, TestingPromote, result.Decision)
	assert.Equal(t, StatusActive, result.TargetStatus)
}

func TestEvaluateTesting_Hold(t *testing.T) {
	input := TestingInput{
		TxnCount:              50,
		TestingMinTxnCount:    100,
		SuccessRateBps:        9500,
		TestingMinSuccessRate: 9500,
		TestingFailCount:      0,
	}
	result := EvaluateTesting(input)
	assert.Equal(t, TestingHold, result.Decision)
	assert.Equal(t, StatusCooldown, result.TargetStatus)
}

func TestEvaluateTesting_Suspend(t *testing.T) {
	input := TestingInput{
		TxnCount:           50,
		TestingMinTxnCount: 100,
		TestingFailCount:   2,
	}
	result := EvaluateTesting(input)
	assert.Equal(t, TestingSuspend, result.Decision)
	assert.Equal(t, StatusSuspended, result.TargetStatus)
}

func TestEvaluateTesting_CardTestingFails(t *testing.T) {
	input := TestingInput{
		TxnCount:              100,
		TestingMinTxnCount:    100,
		SuccessRateBps:        9500,
		TestingMinSuccessRate: 9500,
		P95LatencyMs:          500,
		TestingMaxP95Latency:  800,
		CardTestingCount:      1,
		TestingFailCount:      0,
	}
	result := EvaluateTesting(input)
	assert.Equal(t, TestingHold, result.Decision)
}

func TestCanRecoverToTesting_BeforeLock(t *testing.T) {
	lockUntil := time.Date(2026, 6, 12, 23, 59, 59, 0, time.UTC)
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	lastTransition := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	assert.False(t, CanRecoverToTesting(now, 30, &lockUntil, lastTransition))
}

func TestCanRecoverToTesting_AfterLock(t *testing.T) {
	lockUntil := time.Date(2026, 6, 12, 23, 59, 59, 0, time.UTC)
	now := time.Date(2026, 6, 13, 0, 0, 1, 0, time.UTC)
	lastTransition := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, CanRecoverToTesting(now, 30, &lockUntil, lastTransition))
}

func TestCanRecoverToTesting_CooldownNotExpired(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 15, 0, 0, time.UTC)
	lastTransition := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	assert.False(t, CanRecoverToTesting(now, 30, nil, lastTransition))
}

func TestCanRecoverToTesting_CooldownExpired(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 31, 0, 0, time.UTC)
	lastTransition := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, CanRecoverToTesting(now, 30, nil, lastTransition))
}

func TestMAEStatus_Constants(t *testing.T) {
	assert.Equal(t, MAEStatus("Draft"), StatusDraft)
	assert.Equal(t, MAEStatus("Active"), StatusActive)
	assert.Equal(t, MAEStatus("Cooldown"), StatusCooldown)
	assert.Equal(t, MAEStatus("Testing"), StatusTesting)
	assert.Equal(t, MAEStatus("Suspended"), StatusSuspended)
	assert.Equal(t, MAEStatus("Disabled"), StatusDisabled)
}
