package experimental

import (
	"os"

	"k8s.io/klog/v2"
)

const (
	// ExperimentalFeatureEnvKey is the environment variable key used to enable experimental features.
	ExperimentalFeatureEnvKey = "FLIGHTCTL_EXPERIMENTAL_FEATURES_ENABLED"
)

// NewFeatures creates a new experimental Features. The experimental features
// are enabled if the FLIGHTCTL_EXPERIMENTAL_FEATURES_ENABLED environment
// variable is set.
func NewFeatures() *Features {
	var enabled bool
	value, exists := os.LookupEnv(ExperimentalFeatureEnvKey)
	if exists && value != "" {
		klog.Warning("Experimental features enabled")
		enabled = true
	}

	return &Features{
		enabled: enabled,
	}
}

// Features represents the experimental features.
type Features struct {
	// Enabled is true if the experimental features are enabled.
	enabled bool
}

// IsEnabled returns true if the experimental features are enabled.
func (f *Features) IsEnabled() bool {
	return f.enabled
}
