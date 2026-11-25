package admin

import "testing"

func TestPresentOverrideAuditLogHumanText(t *testing.T) {
	tests := []struct {
		name         string
		overrideType string
		operator     string
		reason       string
		expected     string
	}{
		{
			name:         "RISK_OVERRIDE",
			overrideType: "RISK_OVERRIDE",
			operator:     "operator_a",
			reason:       "测试风控",
			expected:     "操作员 operator_a 对通道风控参数执行了人工覆盖，原因：测试风控",
		},
		{
			name:         "FREEZE",
			overrideType: "FREEZE",
			operator:     "admin_user",
			reason:       "可疑交易",
			expected:     "操作员 admin_user 冻结了隔离组，原因：可疑交易",
		},
		{
			name:         "UNFREEZE",
			overrideType: "UNFREEZE",
			operator:     "operator_b",
			reason:       "核实无误",
			expected:     "操作员 operator_b 解除了隔离组冻结，原因：核实无误",
		},
		{
			name:         "SYSTEM_LOCKDOWN",
			overrideType: "SYSTEM_LOCKDOWN",
			operator:     "security_team",
			reason:       "检测到大规模攻击",
			expected:     "操作员 security_team 启动了系统全局 Fail-Closed 锁死大闸，原因：检测到大规模攻击",
		},
		{
			name:         "RFI_APPROVE",
			overrideType: "RFI_APPROVE",
			operator:     "compliance_a",
			reason:       "资料齐全",
			expected:     "操作员 compliance_a 通过了 RFI 资料包，原因：资料齐全",
		},
		{
			name:         "RFI_REJECT",
			overrideType: "RFI_REJECT",
			operator:     "compliance_b",
			reason:       "资料不一致",
			expected:     "操作员 compliance_b 拒绝了 RFI 资料包，原因：资料不一致",
		},
		{
			name:         "unknown fallback",
			overrideType: "CUSTOM_ACTION",
			operator:     "test_op",
			reason:       "测试原因",
			expected:     "操作员 test_op 执行了 CUSTOM_ACTION 操作，原因：测试原因",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := PresentOverrideAuditLogHumanText(tc.overrideType, tc.operator, tc.reason)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}
