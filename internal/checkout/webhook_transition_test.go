// webhook_transition_test.go — SEC-008/009 纯逻辑证据测试（无 DB 依赖）。
package checkout

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWebhookTransitionAllowed_Monotonic(t *testing.T) {
	tests := []struct {
		name    string
		from    string
		to      string
		allowed bool
	}{
		// 正常前进路径
		{"processing->captured", "PROCESSING", "CAPTURED", true},
		{"processing->authorized", "PROCESSING", "AUTHORIZED", true},
		{"processing->failed", "PROCESSING", "FAILED", true},
		{"requires_action->processing", "REQUIRES_ACTION", "PROCESSING", true},
		{"authorized->captured", "AUTHORIZED", "CAPTURED", true},
		{"captured->settled", "CAPTURED", "SETTLED", true},
		{"unknown->captured", "UNKNOWN_EXTERNAL_STATE", "CAPTURED", true},
		{"unknown->failed", "UNKNOWN_EXTERNAL_STATE", "FAILED", true},

		// 状态回退（必须拒绝）
		{"captured->processing (regress)", "CAPTURED", "PROCESSING", false},
		{"captured->authorized (regress)", "CAPTURED", "AUTHORIZED", false},
		{"settled->processing (regress)", "SETTLED", "PROCESSING", false},
		{"failed->processing (regress)", "FAILED", "PROCESSING", false},
		{"captured->requires_action (regress)", "CAPTURED", "REQUIRES_ACTION", false},

		// 终态拒绝一切变更
		{"settled->captured (terminal)", "SETTLED", "CAPTURED", false},
		{"failed->captured (terminal)", "FAILED", "CAPTURED", false},
		{"canceled->captured (terminal)", "CANCELED", "CAPTURED", false},
		{"refunded->settled (terminal)", "REFUNDED", "SETTLED", false},
		{"charged_back->captured (terminal)", "CHARGED_BACK", "CAPTURED", false},

		// 同状态重放：no-op
		{"captured->captured (replay)", "CAPTURED", "CAPTURED", false},
		{"processing->processing (replay)", "PROCESSING", "PROCESSING", false},

		// 未知来源状态
		{"bogus->captured", "BOGUS", "CAPTURED", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.allowed, WebhookTransitionAllowed(tt.from, tt.to))
		})
	}
}

func TestMapProviderStatusToAttemptStatus(t *testing.T) {
	valid := map[string]AttemptStatus{
		"authorized":      AttemptAuthorized,
		"captured":        AttemptCaptured,
		"settled":         AttemptSettled,
		"failed":          AttemptFailed,
		"requires_action": AttemptRequiresAction,
		"processing":      AttemptProcessing,
		"unknown":         AttemptUnknownExternalState,
		"timeout":         AttemptUnknownExternalState,
	}
	for in, want := range valid {
		got, ok := mapProviderStatusToAttemptStatus(in)
		assert.True(t, ok, "status %q should map", in)
		assert.Equal(t, want, got)
	}

	for _, bogus := range []string{"", "CAPTURED", "succeeded", "weird"} {
		_, ok := mapProviderStatusToAttemptStatus(bogus)
		assert.False(t, ok, "status %q must not map", bogus)
	}
}
