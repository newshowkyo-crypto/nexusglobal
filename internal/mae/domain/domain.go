package domain

import (
	"time"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type MAEStatus string

const (
	StatusDraft     MAEStatus = "Draft"
	StatusActive    MAEStatus = "Active"
	StatusCooldown  MAEStatus = "Cooldown"
	StatusTesting   MAEStatus = "Testing"
	StatusSuspended MAEStatus = "Suspended"
	StatusDisabled  MAEStatus = "Disabled"
)

var legalTransitions = map[MAEStatus][]MAEStatus{
	StatusDraft:     {StatusActive},
	StatusActive:    {StatusCooldown, StatusSuspended},
	StatusCooldown:  {StatusTesting},
	StatusTesting:   {StatusActive, StatusCooldown, StatusSuspended},
	StatusSuspended: {StatusTesting, StatusDisabled},
	StatusDisabled:  {},
}

func CanTransition(from, to MAEStatus) bool {
	allowed, ok := legalTransitions[from]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}

func Transition(from, to MAEStatus) (MAEStatus, error) {
	if !CanTransition(from, to) {
		return from, apperr.ErrIllegalTransition.WithMessage(
			"cannot transition from " + string(from) + " to " + string(to))
	}
	return to, nil
}

type CircuitBreakerInput struct {
	DailyVmaxMinor     int64
	GrossExposureMinor int64
	AbnormalRateBps    int32
	CmaxBps            int32
	CardTestingCount   int32
	CardTestingLimit   int32
}

type CircuitBreakerResult struct {
	ShouldTrip bool
	Reason     string
}

func EvaluateCircuitBreaker(input CircuitBreakerInput) CircuitBreakerResult {
	threshold := input.DailyVmaxMinor * 80 / 100
	if input.GrossExposureMinor >= threshold {
		return CircuitBreakerResult{ShouldTrip: true, Reason: "EXPOSURE_THRESHOLD"}
	}

	if input.CmaxBps > 0 && input.AbnormalRateBps > input.CmaxBps {
		return CircuitBreakerResult{ShouldTrip: true, Reason: "ABNORMAL_RATE"}
	}

	if input.CardTestingLimit > 0 && input.CardTestingCount > input.CardTestingLimit {
		return CircuitBreakerResult{ShouldTrip: true, Reason: "CARD_TESTING"}
	}

	return CircuitBreakerResult{ShouldTrip: false}
}

type TestingInput struct {
	TxnCount              int32
	TestingMinTxnCount    int32
	SuccessRateBps        int32
	TestingMinSuccessRate int32
	AbnormalRateBps       int32
	CmaxBps               int32
	P95LatencyMs          int32
	TestingMaxP95Latency  int32
	CardTestingCount      int32
	TestingFailCount      int32
}

type TestingDecision string

const (
	TestingPromote TestingDecision = "PROMOTE"
	TestingHold    TestingDecision = "HOLD"
	TestingSuspend TestingDecision = "SUSPEND"
)

type TestingResult struct {
	Decision     TestingDecision
	TargetStatus MAEStatus
	Reason       string
}

func EvaluateTesting(input TestingInput) TestingResult {
	passed := true

	if input.TxnCount < input.TestingMinTxnCount {
		passed = false
	}
	if input.SuccessRateBps < input.TestingMinSuccessRate {
		passed = false
	}
	if input.CmaxBps > 0 && input.AbnormalRateBps >= input.CmaxBps/2 {
		passed = false
	}
	if input.TestingMaxP95Latency > 0 && input.P95LatencyMs > input.TestingMaxP95Latency {
		passed = false
	}
	if input.CardTestingCount > 0 {
		passed = false
	}

	if passed {
		return TestingResult{
			Decision:     TestingPromote,
			TargetStatus: StatusActive,
			Reason:       "all criteria met",
		}
	}

	failCount := input.TestingFailCount + 1
	if failCount >= 3 {
		return TestingResult{
			Decision:     TestingSuspend,
			TargetStatus: StatusSuspended,
			Reason:       "consecutive failures >= 3",
		}
	}

	return TestingResult{
		Decision:     TestingHold,
		TargetStatus: StatusCooldown,
		Reason:       "criteria not met",
	}
}

func CanRecoverToTesting(now time.Time, cooldownMinutes int32, businessDayLockUntil *time.Time, lastTransitionAt time.Time) bool {
	if businessDayLockUntil != nil && !now.After(*businessDayLockUntil) {
		return false
	}

	cooldownEnd := lastTransitionAt.Add(time.Duration(cooldownMinutes) * time.Minute)
	return now.After(cooldownEnd)
}
