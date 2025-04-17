package ansiblewrapper

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// parseJSON parses the JSON callback output from Ansible into a map.
func parseJSON(raw string) (map[string]interface{}, error) {
	var parsed map[string]interface{}
	err := json.Unmarshal([]byte(raw), &parsed)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ansible output as JSON: %v", err)
	}
	return parsed, nil
}

// Extract parses the JSON output and extracts values based on the provided path.
func Extract(output interface{}, path string) []interface{} {
	raw, err := json.Marshal(output)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal result: %v", err))
	}
	result := gjson.GetBytes(raw, path)
	if !result.Exists() {
		return nil
	}

	var extracted []interface{}
	if result.IsArray() {
		_ = json.Unmarshal([]byte(result.Raw), &extracted)
	} else {
		extracted = []interface{}{result.Value()}
	}
	return extracted
}

// ExtractOne extracts a single value from the output based on the provided path.
func ExtractOne(output interface{}, path string) map[string]interface{} {
	values := Extract(output, path)
	if len(values) != 1 {
		panic(fmt.Sprintf("expected one result, got %d", len(values)))
	}
	return values[0].(map[string]interface{})
}

// NestedValue retrieves a nested value from a map using a dot-separated path.
func NestedValue(data map[string]interface{}, path string) interface{} {
	parts := strings.Split(path, ".")
	var current interface{} = data
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = m[part]
	}
	return current
}
