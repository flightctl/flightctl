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
func Extract(output interface{}, path string) ([]interface{}, error) {
	raw, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}
	result := gjson.GetBytes(raw, path)
	if !result.Exists() {
		return nil, nil
	}
	var extracted []interface{}
	if result.IsArray() {
		if err := json.Unmarshal([]byte(result.Raw), &extracted); err != nil {
			return nil, fmt.Errorf("failed to unmarshal array result: %w", err)
		}
	} else {
		extracted = []interface{}{result.Value()}
	}
	return extracted, nil
}

// ExtractOne extracts a single value from the output based on the provided path.
func ExtractOne(output interface{}, path string) (map[string]interface{}, error) {
	values, err := Extract(output, path)
	if err != nil {
		return nil, err
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("expected one result, got %d", len(values))
	}

	mapValue, ok := values[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("expected map[string]interface{}, got %T", values[0])
	}
	return mapValue, nil
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

func buildArgs(args map[string]interface{}) string {
	var parts []string
	for k, v := range args {
		// Handle string values that might contain spaces or special characters
		switch vTyped := v.(type) {
		case string:
			parts = append(parts, fmt.Sprintf("%s='%s'", k, strings.ReplaceAll(vTyped, "'", "\\'")))
		default:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " ")
}
