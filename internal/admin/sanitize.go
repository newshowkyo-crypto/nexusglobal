package admin

import (
	"encoding/json"
	"strings"
)

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
			out[key] = "***"
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

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{"credential", "secret", "token", "api_key", "apikey", "private_key", "kms_key", "password"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
