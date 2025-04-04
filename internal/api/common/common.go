package common

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

// ComposeSpec represents a Docker Compose specification.
type ComposeSpec struct {
	Services map[string]ComposeService `json:"services"`
}

// ComposeService represents a service in a Docker Compose specification.
type ComposeService struct {
	Image         string `json:"image"`
	ContainerName string `json:"container_name"`
}

// ParseComposeSpec parses YAML data into a ComposeSpec
func ParseComposeSpec(data []byte) (*ComposeSpec, error) {
	var spec ComposeSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("invalid compose YAML: %w", err)
	}
	return &spec, nil
}
