package domain

import (
	"encoding/json"
	"strings"
	"time"
)

func normalizeTime(value time.Time, field string) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, InvalidRequestf("%s is required", field)
	}
	return value.UTC(), nil
}

func normalizeOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneStrings(input []string) []string {
	if len(input) == 0 {
		return []string{}
	}
	return append([]string(nil), input...)
}

func cloneJSONMap(input map[string]any, field string) (map[string]any, error) {
	if input == nil {
		return map[string]any{}, nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, InvalidRequestf("%s must contain JSON-compatible values: %v", field, err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, InvalidRequestf("%s must contain a JSON object: %v", field, err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func isWindowsAbsolutePath(value string) bool {
	if len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}
	return strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//")
}
