package common

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/util"
)

// HealthChecksConfig holds health check endpoint configuration.
type HealthChecksConfig struct {
	Enabled          bool          `json:"enabled,omitempty"`
	ReadinessPath    string        `json:"readinessPath,omitempty"`
	LivenessPath     string        `json:"livenessPath,omitempty"`
	ReadinessTimeout util.Duration `json:"readinessTimeout,omitempty"`
}

// NewDefaultHealthChecks returns a default health checks configuration.
func NewDefaultHealthChecks() *HealthChecksConfig {
	return &HealthChecksConfig{
		Enabled:          true,
		ReadinessPath:    "/readyz",
		LivenessPath:     "/healthz",
		ReadinessTimeout: util.Duration(2 * time.Second),
	}
}

// Validate validates the health checks configuration.
func (c *HealthChecksConfig) Validate(prefix string) error {
	if !c.Enabled {
		return nil
	}

	if strings.TrimSpace(c.ReadinessPath) == "" {
		return fmt.Errorf("%sreadinessPath must be non-empty", prefix)
	}
	if !strings.HasPrefix(c.ReadinessPath, "/") {
		return fmt.Errorf("%sreadinessPath must start with '/'", prefix)
	}
	if strings.TrimSpace(c.LivenessPath) == "" {
		return fmt.Errorf("%slivenessPath must be non-empty", prefix)
	}
	if !strings.HasPrefix(c.LivenessPath, "/") {
		return fmt.Errorf("%slivenessPath must start with '/'", prefix)
	}
	if c.ReadinessTimeout <= 0 {
		return fmt.Errorf("%sreadinessTimeout must be greater than 0", prefix)
	}
	if c.ReadinessPath == c.LivenessPath {
		return fmt.Errorf("%sreadinessPath and livenessPath must not be identical", prefix)
	}

	return nil
}
