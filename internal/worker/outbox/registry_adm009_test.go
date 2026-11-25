package outbox

import (
	"testing"

	"github.com/nexusglobal/mamr/internal/admin"
)

// TestADM009_ApprovalEventTypesKnownByRegistry 双向集合测试（ADM-009）：
//  1. 审批服务可能产生的每个 event_type 都必须被 Worker registry 认识
//     （否则事件进入重试/死信，配置变更永远不生效）；
//  2. registry 中全部小写点分风格的配置变更事件都必须在审批服务清单中
//     （防止 registry 膨胀出无人生产的事件）。
func TestADM009_ApprovalEventTypesKnownByRegistry(t *testing.T) {
	// 方向 1：producer → consumer
	for _, eventType := range admin.ApprovalOutboxEventTypes() {
		if !IsKnownEventType(eventType) {
			t.Errorf("ADM-009 VIOLATION: approval event %q is NOT known by worker registry (would be dead-lettered)", eventType)
		}
	}

	// 方向 2：consumer → producer（只校验配置变更类的小写点分事件）
	known := KnownEventTypes()
	for _, eventType := range known {
		if !isDottedConfigEvent(eventType) {
			continue
		}
		if !containsString(admin.ApprovalOutboxEventTypes(), eventType) {
			t.Errorf("ADM-009 WARNING: registry event %q (dotted config style) has no producer in approval service", eventType)
		}
	}
}

// isDottedConfigEvent 判断是否为配置变更类的小写点分事件。
// 大写下划线事件（PAYMENT_DISPATCH_REQUESTED 等）由其他 producer 产生，不在本测试范围。
func isDottedConfigEvent(eventType string) bool {
	if len(eventType) == 0 {
		return false
	}
	for _, r := range eventType {
		if r == '.' || (r >= 'a' && r <= 'z') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
