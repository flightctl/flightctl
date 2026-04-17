// Package quadlet provides Quadlet/systemd-specific implementations of the infra providers.
package quadlet

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"sigs.k8s.io/yaml"
)

// SectionMapping describes how one top-level section in the rendered per-service
// config maps back to service-config.yaml. RenderedKey is the key in the
// per-service config (e.g. imageBuilderWorker); ServiceConfigKey is the key in
// service-config.yaml (e.g. imagebuilderWorker). If Transform is non-nil, it
// converts the rendered subtree into service-config shape; otherwise the subtree
// is copied as-is (use when structure is 1:1, with key normalization).
type SectionMapping struct {
	RenderedKey      string
	ServiceConfigKey string
	Transform        func(renderedSubtree interface{}) (interface{}, error)
}

// serviceConfigSectionMappings defines, per service, how to map rendered
// per-service config back into service-config.yaml. Only services with entries
// here will have SetServiceConfig write to service-config.yaml; others write
// to the per-service config file.
var serviceConfigSectionMappings = map[infra.ServiceName][]SectionMapping{
	infra.ServiceImageBuilderWorker: {
		{
			RenderedKey:      "imageBuilderWorker",
			ServiceConfigKey: "imagebuilderWorker",
			Transform:        nil, // 1:1 copy, key name normalized above
		},
	},
	infra.ServiceTelemetryGateway: {
		{
			RenderedKey:      "telemetryGateway",
			ServiceConfigKey: "telemetryGateway",
			Transform:        nil,
		},
	},
}

// ServicesWithServiceConfigMappings returns service names that have a section
// mapping (SetServiceConfig writes to service-config.yaml for these).
func ServicesWithServiceConfigMappings() []infra.ServiceName {
	names := make([]infra.ServiceName, 0, len(serviceConfigSectionMappings))
	for name := range serviceConfigSectionMappings {
		names = append(names, name)
	}
	return names
}

// getServiceConfigMappings returns the section mappings for the service, or nil if none.
func getServiceConfigMappings(service infra.ServiceName) []SectionMapping {
	return serviceConfigSectionMappings[service]
}

// applyServiceConfigMappings parses the per-service config content (YAML), applies
// the section mappings for the service, and returns a map of service-config key ->
// value to merge into service-config.yaml. Returns nil, nil if service has no mappings.
func applyServiceConfigMappings(service infra.ServiceName, perServiceContent string) (map[string]interface{}, error) {
	mappings := getServiceConfigMappings(service)
	if len(mappings) == 0 {
		return nil, nil
	}

	var perService map[string]interface{}
	if err := yaml.Unmarshal([]byte(perServiceContent), &perService); err != nil {
		return nil, fmt.Errorf("parse per-service config: %w", err)
	}

	updates := make(map[string]interface{})
	for _, m := range mappings {
		subtree := getSubtreeWithKeyNormalization(perService, m.RenderedKey)
		if subtree == nil {
			continue
		}
		var value interface{}
		if m.Transform != nil {
			var err error
			value, err = m.Transform(subtree)
			if err != nil {
				return nil, fmt.Errorf("transform %s -> %s: %w", m.RenderedKey, m.ServiceConfigKey, err)
			}
		} else {
			value = deepCopyMap(subtree)
		}
		updates[m.ServiceConfigKey] = value
	}
	return updates, nil
}

// getSubtreeWithKeyNormalization returns perService[key] or perService[altKey] for
// common casing variants (e.g. imageBuilderWorker vs imagebuilderWorker).
func getSubtreeWithKeyNormalization(perService map[string]interface{}, key string) interface{} {
	if v, ok := perService[key]; ok {
		return v
	}
	if v, ok := perService[lowerFirst(key)]; ok {
		return v
	}
	// Try first uppercase letter lowercased (e.g. imageBuilderWorker -> imagebuilderWorker)
	if v, ok := perService[firstUpperToLower(key)]; ok {
		return v
	}
	return nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// firstUpperToLower returns the key with the first uppercase rune lowercased,
// e.g. imageBuilderWorker -> imagebuilderWorker (so we can match service-config key style).
func firstUpperToLower(s string) string {
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			return s[:i] + strings.ToLower(string(r)) + s[i+1:]
		}
	}
	return s
}

// deepCopyMap returns a copy of v so that merging into service-config does not
// mutate the original parsed per-service map. Only handles map[string]interface{}
// and []interface{}; other types are returned as-is.
func deepCopyMap(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = deepCopyMap(val)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if sk, ok := k.(string); ok {
				out[sk] = deepCopyMap(val)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = deepCopyMap(val)
		}
		return out
	default:
		return v
	}
}
