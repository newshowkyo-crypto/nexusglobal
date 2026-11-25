package admin

import (
	"encoding/json"
	"strings"
)

// SanitizeValue 递归脱敏任意值，用于审批 payload、before/after 快照、审计和日志。
// P0-5: 统一脱敏入口，键名大小写不敏感，处理嵌套 object、array 和组合键。
func SanitizeValue(v interface{}) interface{} {
	switch typed := v.(type) {
	case map[string]interface{}:
		return sanitizeConfigMap(typed)
	case []interface{}:
		return sanitizeConfigList(typed)
	default:
		return v
	}
}

// SanitizeMap 递归脱敏 map，用于审批 payload 和 before/after 快照。
func SanitizeMap(in map[string]interface{}) map[string]interface{} {
	return sanitizeConfigMap(in)
}

// SanitizeMapForExecution 构造"执行 payload"的脱敏版本（ADM-003：view 与 execution 分离）。
//
// 差异点：credential_vault_key 的值是 vault:// 引用（租户/记录/字段/版本），是 ID 而非密钥。
// 执行 payload 必须保留该引用，否则审批 apply 会把 "<redacted>" 写回数据库，
// 破坏通道凭证绑定。真正的密钥值（api_key 等）仍然一律脱敏。
func SanitizeMapForExecution(in map[string]interface{}) map[string]interface{} {
	return sanitizeExecutionMap(in)
}

func sanitizeExecutionMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		if isSensitiveKey(key) {
			if s, ok := value.(string); ok && strings.HasPrefix(s, "vault://") {
				out[key] = value
				continue
			}
			out[key] = "<redacted>"
			continue
		}

		switch typed := value.(type) {
		case map[string]interface{}:
			out[key] = sanitizeExecutionMap(typed)
		case []interface{}:
			out[key] = sanitizeConfigList(typed)
		default:
			out[key] = value
		}
	}
	return out
}

func sanitizeJSONText(raw string) map[string]interface{} {
	if raw == "" {
		return map[string]interface{}{}
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return map[string]interface{}{}
	}
	return sanitizeConfigMap(data)
}

func sanitizeConfigMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		if isSensitiveKey(key) {
			out[key] = "<redacted>"
			continue
		}

		switch typed := value.(type) {
		case map[string]interface{}:
			out[key] = sanitizeConfigMap(typed)
		case []interface{}:
			out[key] = sanitizeConfigList(typed)
		default:
			out[key] = value
		}
	}
	return out
}

func sanitizeConfigList(in []interface{}) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, value := range in {
		switch typed := value.(type) {
		case map[string]interface{}:
			out = append(out, sanitizeConfigMap(typed))
		case []interface{}:
			out = append(out, sanitizeConfigList(typed))
		default:
			out = append(out, value)
		}
	}
	return out
}

// isSensitiveKey 检查键名是否为敏感字段，大小写不敏感。
// P0-5: 至少覆盖 secret, token, api_key, private_key, password, credential, webhook_secret, vault_key。
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{
		"credential",
		"secret",
		"token",
		"api_key",
		"apikey",
		"private_key",
		"privatekey",
		"kms_key",
		"password",
		"webhook_secret",
		"vault_key",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
