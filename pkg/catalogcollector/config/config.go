// Package config provides the configuration loader for the
// flightctl-catalog-collector. It is part of the public extension foundation:
// external repositories can import it to build custom collector distributions
// that register additional source, processor, destination, or extension
// components.
//
// The loader parses a YAML file into named sources, processors, destinations,
// and pipelines. Each component map key is a component identifier in the
// format type[/name]. Provider-specific configuration is captured as opaque
// JSON for later decoding by the component factory layer.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"sigs.k8s.io/yaml"
)

// MetricsConfig holds configuration for the internal Prometheus metrics
// endpoint. When Endpoint is empty, the default localhost:8888 is used.
// Metrics are disabled only when the entire service.metrics block is absent
// (i.e. the pointer in ServiceConfig is nil).
type MetricsConfig struct {
	// Endpoint is the host:port for the Prometheus metrics HTTP server.
	// An empty value defaults to localhost:8888.
	Endpoint string
}

// ServiceConfig holds service-level settings that are applied before any
// component is constructed. All fields are optional.
type ServiceConfig struct {
	// LogLevel overrides the base logger's level for the lifetime of the
	// collector. Accepted values: debug, info, warn, error. Absent means
	// the caller's logger level is preserved unchanged.
	LogLevel *string

	// Metrics configures the internal Prometheus metrics endpoint.
	// A nil pointer (absent service.metrics block) disables metrics.
	// A non-nil pointer with an empty Endpoint defaults to localhost:8888.
	Metrics *MetricsConfig
}

// Config is the fully parsed and validated collector configuration.
// It is not a serialization target; only the intermediate rawConfig and
// the typed pipeline/component structs carry JSON tags.
type Config struct {
	Service      ServiceConfig
	Sources      map[string]ComponentConfig
	Processors   map[string]ComponentConfig
	Destinations map[string]ComponentConfig
	Extensions   map[string]ComponentConfig
	Pipelines    map[string]PipelineConfig
}

// ComponentConfig holds the parsed component identifier and the opaque
// provider-specific configuration for a source, processor, destination,
// or extension.
//
// Config contains the raw JSON body. It is nil when the component has no
// provider-specific fields.
type ComponentConfig struct {
	ID     catalogcollector.ComponentID
	Config json.RawMessage
}

// PipelineConfig references a source, an optional ordered list of processors,
// and a destination by their component identifiers.
type PipelineConfig struct {
	Source      string   `json:"source"`
	Processors  []string `json:"processors,omitempty"`
	Destination string   `json:"destination"`
}

// rawMetricsConfig mirrors MetricsConfig for YAML deserialization through
// rawConfig. Unknown fields are rejected by yaml.UnmarshalStrict.
type rawMetricsConfig struct {
	Endpoint string `json:"endpoint,omitempty"`
}

// rawServiceConfig is the intermediate representation for the service block.
// Unknown fields are rejected by yaml.UnmarshalStrict through rawConfig.
type rawServiceConfig struct {
	LogLevel *string           `json:"logLevel,omitempty"`
	Metrics  *rawMetricsConfig `json:"metrics,omitempty"`
}

// rawConfig is the intermediate representation for two-pass YAML decoding.
// Source, processor, destination, and extension entries are captured as raw
// JSON so that environment-variable resolution can run before
// provider-specific decoding.
type rawConfig struct {
	Service      rawServiceConfig           `json:"service,omitempty"`
	Sources      map[string]json.RawMessage `json:"sources"`
	Processors   map[string]json.RawMessage `json:"processors,omitempty"`
	Destinations map[string]json.RawMessage `json:"destinations"`
	Extensions   map[string]json.RawMessage `json:"extensions,omitempty"`
	Pipelines    map[string]PipelineConfig  `json:"pipelines"`
}

// Load reads a YAML configuration file and returns the parsed Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	return Parse(data)
}

// Parse decodes YAML bytes into a validated Config.
//
// The decoding pipeline is:
//  1. Strict YAML decode into rawConfig, rejecting unknown top-level and
//     pipeline fields while capturing component entries as raw JSON.
//  2. Parse each component map key as a component identifier (type[/name]).
//  3. Store each raw JSON body as opaque provider-specific configuration.
//     Environment-variable references (${env:NAME}, ${NAME}) are preserved
//     as-is; resolution is deferred to [DecodeComponent].
//  4. Validate cross-component references.
//
// Provider type validation, environment expansion, and provider-specific
// configuration decoding are deferred to the component factory layer through
// [DecodeComponent].
func Parse(yamlBytes []byte) (*Config, error) {
	var raw rawConfig
	if err := yaml.UnmarshalStrict(yamlBytes, &raw); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	sources, err := parseComponents(raw.Sources, "sources")
	if err != nil {
		return nil, err
	}

	processors, err := parseComponents(raw.Processors, "processors")
	if err != nil {
		return nil, err
	}

	destinations, err := parseComponents(raw.Destinations, "destinations")
	if err != nil {
		return nil, err
	}

	extensions, err := parseComponents(raw.Extensions, "extensions")
	if err != nil {
		return nil, err
	}

	// Translate the raw metrics pointer into the public config.
	// nil raw pointer → nil public pointer (metrics disabled).
	// non-nil raw pointer → non-nil public pointer; empty endpoint
	// defaults to localhost:8888.
	var metricsPtr *MetricsConfig
	if raw.Service.Metrics != nil {
		endpoint := raw.Service.Metrics.Endpoint
		if endpoint == "" {
			endpoint = "localhost:8888"
		}
		metricsPtr = &MetricsConfig{
			Endpoint: endpoint,
		}
	}

	cfg := &Config{
		Service: ServiceConfig{
			LogLevel: raw.Service.LogLevel,
			Metrics:  metricsPtr,
		},
		Sources:      sources,
		Processors:   processors,
		Destinations: destinations,
		Extensions:   extensions,
		Pipelines:    raw.Pipelines,
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// parseComponents parses raw component entries, validating each map key as a
// component identifier. The raw JSON body is stored as-is; environment-variable
// references are resolved later by [DecodeComponent].
func parseComponents(
	raw map[string]json.RawMessage,
	kind string,
) (map[string]ComponentConfig, error) {
	result := make(map[string]ComponentConfig, len(raw))

	for key, rawMessage := range raw {
		id, err := catalogcollector.ParseComponentID(key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", kind, err)
		}

		result[key] = ComponentConfig{
			ID:     id,
			Config: normalizeBody(rawMessage),
		}
	}

	return result, nil
}

// normalizeBody returns nil for null or empty JSON bodies and returns the raw
// JSON otherwise. Components without provider-specific fields therefore
// receive nil Config rather than an empty or null JSON value.
func normalizeBody(data json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	if string(trimmed) == "null" {
		return nil
	}
	if string(trimmed) == "{}" {
		return nil
	}
	return data
}

var envRefPattern = regexp.MustCompile(
	`^\$\{(?:env:)?([A-Za-z_][A-Za-z0-9_]*)\}$`,
)

// resolveEnvVars walks a JSON value and replaces whole-string environment
// variable references with their resolved values.
//
// Accepted forms:
//
//	${env:NAME}
//	${NAME}
//
// A reference must occupy the entire string value. Inline interpolation is
// intentionally unsupported. Object keys are never expanded.
//
// An unset variable is a fatal error that identifies the variable and its
// configuration path but never its value.
func resolveEnvVars(
	data json.RawMessage,
	path string,
) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return data, nil
	}

	switch trimmed[0] {
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, fmt.Errorf("%s: decoding string value: %w", path, err)
		}

		varName, matched, err := parseEnvRef(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !matched {
			return data, nil
		}

		resolved, found := os.LookupEnv(varName)
		if !found {
			return nil, fmt.Errorf(
				"%s: environment variable %q is not set",
				path,
				varName,
			)
		}

		encoded, err := json.Marshal(resolved)
		if err != nil {
			return nil, fmt.Errorf(
				"%s: encoding resolved environment value: %w",
				path,
				err,
			)
		}
		return json.RawMessage(encoded), nil

	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return nil, fmt.Errorf("%s: decoding object: %w", path, err)
		}

		// Stable traversal makes errors deterministic when multiple fields
		// contain invalid or missing environment references.
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		for _, key := range keys {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}

			resolved, err := resolveEnvVars(object[key], childPath)
			if err != nil {
				return nil, err
			}
			object[key] = resolved
		}

		encoded, err := json.Marshal(object)
		if err != nil {
			return nil, fmt.Errorf(
				"%s: encoding resolved object: %w",
				path,
				err,
			)
		}
		return json.RawMessage(encoded), nil

	case '[':
		var array []json.RawMessage
		if err := json.Unmarshal(trimmed, &array); err != nil {
			return nil, fmt.Errorf("%s: decoding array: %w", path, err)
		}

		for index := range array {
			childPath := fmt.Sprintf("%s[%d]", path, index)

			resolved, err := resolveEnvVars(array[index], childPath)
			if err != nil {
				return nil, err
			}
			array[index] = resolved
		}

		encoded, err := json.Marshal(array)
		if err != nil {
			return nil, fmt.Errorf(
				"%s: encoding resolved array: %w",
				path,
				err,
			)
		}
		return json.RawMessage(encoded), nil

	default:
		return data, nil
	}
}

// parseEnvRef recognizes a whole-string environment-variable reference.
//
// matched is false when value is an ordinary literal. A value surrounded by
// ${...} but not conforming to one of the supported forms is reported as an
// error instead of being silently treated as a literal.
func parseEnvRef(value string) (name string, matched bool, err error) {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return "", false, nil
	}

	matches := envRefPattern.FindStringSubmatch(value)
	if matches == nil {
		return "", true, fmt.Errorf(
			"invalid environment variable reference; expected ${env:NAME} or ${NAME}",
		)
	}

	return matches[1], true, nil
}

// Validator is an optional interface that component configuration structs may
// implement. If the typed config returned by CreateDefaultConfig implements
// Validator, [DecodeComponent] calls Validate after decoding.
type Validator interface {
	Validate() error
}

// DecodeComponent resolves environment-variable references in the raw
// component body and strict-decodes the result into defaults. defaults must be
// a pointer to the component's configuration struct, typically obtained from
// Factory.CreateDefaultConfig. Fields absent from the raw body retain their
// default values.
//
// The raw body is never modified. The expanded intermediate value is discarded
// after decoding so resolved values are not retained in the generic collector
// configuration.
//
// componentPath identifies the configuration location in errors, for example
// "destinations.flightctl/local".
func DecodeComponent(
	raw json.RawMessage,
	defaults any,
	componentPath string,
) error {
	if raw == nil {
		if validator, ok := defaults.(Validator); ok {
			return validator.Validate()
		}
		return nil
	}

	expanded, err := resolveEnvVars(raw, componentPath)
	if err != nil {
		return err
	}

	if err := yaml.UnmarshalStrict(expanded, defaults); err != nil {
		return fmt.Errorf("%s: %w", componentPath, err)
	}

	if validator, ok := defaults.(Validator); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("%s: %w", componentPath, err)
		}
	}

	return nil
}

var validLogLevels = map[string]struct{}{
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
}

func (c *Config) validate() error {
	if c.Service.LogLevel != nil {
		level := *c.Service.LogLevel
		if level == "" {
			return fmt.Errorf(
				"service.logLevel: explicit empty string is not allowed; omit the field to preserve the current level",
			)
		}
		if _, ok := validLogLevels[level]; !ok {
			return fmt.Errorf(
				"service.logLevel %q is not supported; expected one of: debug, info, warn, error",
				level,
			)
		}
	}

	if c.Service.Metrics != nil {
		if endpoint := c.Service.Metrics.Endpoint; endpoint != "" {
			host, port, err := net.SplitHostPort(endpoint)
			if err != nil {
				return fmt.Errorf(
					"service.metrics.endpoint %q is not a valid host:port: %w",
					endpoint,
					err,
				)
			}
			if host == "" {
				return fmt.Errorf(
					"service.metrics.endpoint %q has an empty host; both host and port are required",
					endpoint,
				)
			}
			if port == "" {
				return fmt.Errorf(
					"service.metrics.endpoint %q has an empty port; both host and port are required",
					endpoint,
				)
			}
		}
	}

	if len(c.Pipelines) == 0 {
		return fmt.Errorf("at least one pipeline must be defined")
	}

	pipelineNames := make([]string, 0, len(c.Pipelines))
	for name := range c.Pipelines {
		pipelineNames = append(pipelineNames, name)
	}
	sort.Strings(pipelineNames)

	for _, name := range pipelineNames {
		pipeline := c.Pipelines[name]

		if pipeline.Source == "" {
			return fmt.Errorf(
				"pipelines.%s: missing required field \"source\"",
				name,
			)
		}
		if pipeline.Destination == "" {
			return fmt.Errorf(
				"pipelines.%s: missing required field \"destination\"",
				name,
			)
		}
		if _, ok := c.Sources[pipeline.Source]; !ok {
			return fmt.Errorf(
				"pipelines.%s: source %q is not defined",
				name,
				pipeline.Source,
			)
		}
		if _, ok := c.Destinations[pipeline.Destination]; !ok {
			return fmt.Errorf(
				"pipelines.%s: destination %q is not defined",
				name,
				pipeline.Destination,
			)
		}
		for _, processorName := range pipeline.Processors {
			if _, ok := c.Processors[processorName]; !ok {
				return fmt.Errorf(
					"pipelines.%s: processor %q is not defined",
					name,
					processorName,
				)
			}
		}
	}

	return nil
}
